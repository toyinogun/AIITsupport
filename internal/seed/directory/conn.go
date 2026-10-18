package directory

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/url"
	"time"

	"github.com/go-ldap/ldap/v3"
)

const (
	dialTimeout    = 10 * time.Second
	firstBackoff   = 500 * time.Millisecond
	maxBackoff     = 5 * time.Second
	requestTimeout = 30 * time.Second
)

// Dialer opens LDAPS connections verified against our dev CA. There is no
// option to skip verification (AC-12).
type Dialer struct {
	URL     string
	RootCAs *x509.CertPool
}

// Dial opens one connection without binding.
func (d Dialer) Dial() (*ldap.Conn, error) {
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
	conn, err := ldap.DialURL(d.URL, ldap.DialWithDialer(&net.Dialer{Timeout: dialTimeout}), ldap.DialWithTLSConfig(tlsConfig))
	if err != nil {
		return nil, fmt.Errorf("connect to %s (verifying as %s): %w", d.URL, u.Hostname(), err)
	}
	conn.SetTimeout(requestTimeout)
	return conn, nil
}

// Bind opens a connection and binds as dn.
func (d Dialer) Bind(dn, password string) (*ldap.Conn, error) {
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
func (d Dialer) WaitAndBind(ctx context.Context, dn, password string, wait time.Duration) (*ldap.Conn, error) {
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
	return ldap.IsErrorAnyOf(err, ldap.ErrorNetwork, ldap.LDAPResultUnavailable, ldap.LDAPResultBusy)
}

// BaseDN reads the domain's naming context from the rootDSE.
func BaseDN(conn ldap.Client) (string, error) {
	res, err := conn.Search(ldap.NewSearchRequest("", ldap.ScopeBaseObject, ldap.NeverDerefAliases,
		0, 0, false, "(objectClass=*)", []string{"defaultNamingContext"}, nil))
	if err != nil {
		return "", fmt.Errorf("read rootDSE: %w", err)
	}
	if len(res.Entries) != 1 || res.Entries[0].GetAttributeValue("defaultNamingContext") == "" {
		return "", errors.New("rootDSE has no defaultNamingContext")
	}
	return res.Entries[0].GetAttributeValue("defaultNamingContext"), nil
}

// readOne reads one object's attributes by DN. A missing object returns nil.
func readOne(conn ldap.Client, dn string, attrs ...string) (*ldap.Entry, error) {
	res, err := conn.Search(ldap.NewSearchRequest(dn, ldap.ScopeBaseObject, ldap.NeverDerefAliases,
		0, 0, false, "(objectClass=*)", attrs, nil))
	if ldap.IsErrorWithCode(err, ldap.LDAPResultNoSuchObject) {
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
