// Package ldap is Noord's one LDAPS layer (spec 0007): the dialer verified
// against our CA, the DN helpers, lock state, and the reads and the one write
// the tool server makes. cmd/seed uses the same dialer, DN helpers, and lock
// state; only the tool server uses Client.
package ldap

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"time"

	goldap "github.com/go-ldap/ldap/v3"
)

const (
	defaultDialTimeout    = 10 * time.Second
	defaultRequestTimeout = 30 * time.Second
	firstBackoff          = 500 * time.Millisecond
	maxBackoff            = 5 * time.Second
)

// Dialer opens LDAPS connections verified against our CA. There is no option
// to skip verification (spec 0004, AC-12).
type Dialer struct {
	URL     string
	RootCAs *x509.CertPool
	// Timeout bounds the dial and each operation (LDAP_TIMEOUT). Zero keeps
	// the seeder's defaults: 10s to dial, 30s per operation.
	Timeout time.Duration
}

// Dial opens one connection without binding.
func (d Dialer) Dial() (*goldap.Conn, error) {
	return d.dial(d.dialTimeout(), d.requestTimeout())
}

// DialContext opens one connection bounded by ctx as well as Timeout: the
// connection closes when ctx ends, so no operation outlives the call. Call
// the returned func when done; it closes the connection.
func (d Dialer) DialContext(ctx context.Context) (*goldap.Conn, func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, fmt.Errorf("connect to %s: %w", d.URL, err)
	}
	dialTimeout, requestTimeout := d.dialTimeout(), d.requestTimeout()
	if deadline, ok := ctx.Deadline(); ok {
		left := time.Until(deadline)
		dialTimeout, requestTimeout = min(dialTimeout, left), min(requestTimeout, left)
	}
	conn, err := d.dial(dialTimeout, requestTimeout)
	if err != nil {
		return nil, nil, err
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	return conn, func() {
		stop()
		_ = conn.Close()
	}, nil
}

func (d Dialer) dial(dialTimeout, requestTimeout time.Duration) (*goldap.Conn, error) {
	u, err := url.Parse(d.URL)
	if err != nil {
		return nil, fmt.Errorf("parse LDAP URL: %w", err)
	}
	if u.Scheme != "ldaps" {
		return nil, fmt.Errorf("LDAP URL %s must use ldaps://", d.URL)
	}
	if d.RootCAs == nil {
		return nil, errors.New("no CA to verify the directory against")
	}
	tlsConfig := &tls.Config{RootCAs: d.RootCAs, ServerName: u.Hostname(), MinVersion: tls.VersionTLS12}
	conn, err := goldap.DialURL(d.URL, goldap.DialWithDialer(&net.Dialer{Timeout: dialTimeout}), goldap.DialWithTLSConfig(tlsConfig))
	if err != nil {
		return nil, fmt.Errorf("connect to %s (verifying as %s): %w", d.URL, u.Hostname(), err)
	}
	conn.SetTimeout(requestTimeout)
	return conn, nil
}

func (d Dialer) dialTimeout() time.Duration {
	if d.Timeout > 0 {
		return d.Timeout
	}
	return defaultDialTimeout
}

func (d Dialer) requestTimeout() time.Duration {
	if d.Timeout > 0 {
		return d.Timeout
	}
	return defaultRequestTimeout
}

// Bind opens a connection and binds as dn.
func (d Dialer) Bind(dn, password string) (*goldap.Conn, error) {
	conn, err := d.Dial()
	if err != nil {
		return nil, err
	}
	if err := conn.Bind(dn, password); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("bind as %s: %w", dn, err)
	}
	return conn, nil
}

// WaitAndBind retries Bind with backoff while the directory is still coming
// up, until wait has passed. A wrong password is not retried.
func (d Dialer) WaitAndBind(ctx context.Context, dn, password string, wait time.Duration) (*goldap.Conn, error) {
	ctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()

	backoff := firstBackoff
	for {
		conn, err := d.Bind(dn, password)
		if err == nil {
			return conn, nil
		}
		if !retryable(err) {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("directory at %s did not answer within %s: %w", d.URL, wait, err)
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, maxBackoff)
	}
}

func retryable(err error) bool {
	return goldap.IsErrorAnyOf(err, goldap.ErrorNetwork, goldap.LDAPResultUnavailable, goldap.LDAPResultBusy)
}

// BaseDN reads the domain's naming context from the rootDSE.
func BaseDN(conn goldap.Client) (string, error) {
	res, err := conn.Search(goldap.NewSearchRequest("", goldap.ScopeBaseObject, goldap.NeverDerefAliases,
		0, 0, false, "(objectClass=*)", []string{"defaultNamingContext"}, nil))
	if err != nil {
		return "", fmt.Errorf("read rootDSE: %w", err)
	}
	if len(res.Entries) != 1 || res.Entries[0].GetAttributeValue("defaultNamingContext") == "" {
		return "", errors.New("rootDSE has no defaultNamingContext")
	}
	return res.Entries[0].GetAttributeValue("defaultNamingContext"), nil
}

// ReadOne reads one object's attributes by DN. A missing object returns nil.
func ReadOne(conn goldap.Client, dn string, attrs ...string) (*goldap.Entry, error) {
	res, err := conn.Search(goldap.NewSearchRequest(dn, goldap.ScopeBaseObject, goldap.NeverDerefAliases,
		0, 0, false, "(objectClass=*)", attrs, nil))
	if goldap.IsErrorWithCode(err, goldap.LDAPResultNoSuchObject) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", dn, err)
	}
	if len(res.Entries) != 1 {
		return nil, fmt.Errorf("read %s: got %d entries", dn, len(res.Entries))
	}
	return res.Entries[0], nil
}

// LoadCA reads the PEM CA the directory's certificate must chain to
// (LDAP_CA_CERT, SEED_CA_CERT).
func LoadCA(caPath string) (*x509.CertPool, error) {
	pemBytes, err := os.ReadFile(caPath) //nolint:gosec // G304: the path comes from config
	if err != nil {
		return nil, fmt.Errorf("read CA %s (run make certs): %w", caPath, err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pemBytes) {
		return nil, fmt.Errorf("CA %s holds no PEM certificate", caPath)
	}
	return pool, nil
}
