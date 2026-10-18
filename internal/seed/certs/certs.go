// Package certs makes the dev CA and the Samba server certificate that LDAPS
// is verified against (spec 0004, AC-12). Locally the files live in
// .dev/certs; on k3s cert-manager makes the same set (#17).
package certs

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"slices"
	"time"
)

// File names inside the cert directory. The Samba entrypoint expects these.
const (
	CAFile         = "ca.pem"
	CAKeyFile      = "ca.key"
	ServerFile     = "server.pem"
	ServerKeyFile  = "server.key"
	caValidity     = 5 * 365 * 24 * time.Hour
	serverValidity = 2 * 365 * 24 * time.Hour
)

// DefaultHosts are the server certificate's SANs: the host port, the Compose
// service name, and the k3s service names (AC-12).
var DefaultHosts = []string{
	"localhost",
	"127.0.0.1",
	"samba",
	"samba.noord-directory.svc",
	"samba.noord-directory.svc.cluster.local",
}

// Result says which files Generate wrote and which it left alone.
type Result struct {
	Written []string
	Skipped []string
}

// Generate makes the CA and the server certificate in dir. Files that already
// exist are kept, so a rerun never replaces a CA clients already trust. The
// server certificate covers DefaultHosts plus any extra hosts.
func Generate(dir string, extraHosts []string) (Result, error) {
	var res Result
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return res, fmt.Errorf("make cert dir %s: %w", dir, err)
	}

	caCert, caKey, wroteCA, err := loadOrCreateCA(dir)
	if err != nil {
		return res, err
	}
	if wroteCA {
		res.Written = append(res.Written, CAFile, CAKeyFile)
	} else {
		res.Skipped = append(res.Skipped, CAFile, CAKeyFile)
	}

	serverPath := filepath.Join(dir, ServerFile)
	if exists(serverPath) && exists(filepath.Join(dir, ServerKeyFile)) {
		res.Skipped = append(res.Skipped, ServerFile, ServerKeyFile)
		return res, nil
	}
	hosts := slices.Compact(slices.Sorted(slices.Values(append(slices.Clone(DefaultHosts), extraHosts...))))
	if err := writeServer(dir, caCert, caKey, hosts); err != nil {
		return res, err
	}
	res.Written = append(res.Written, ServerFile, ServerKeyFile)
	return res, nil
}

func loadOrCreateCA(dir string) (*x509.Certificate, *ecdsa.PrivateKey, bool, error) {
	certPath, keyPath := filepath.Join(dir, CAFile), filepath.Join(dir, CAKeyFile)
	if exists(certPath) && exists(keyPath) {
		cert, key, err := loadCA(certPath, keyPath)
		return cert, key, false, err
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, false, fmt.Errorf("generate CA key: %w", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial(),
		Subject:               pkix.Name{CommonName: "Noord dev CA", Organization: []string{"Noord Logistics (fictional)"}},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(caValidity),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLenZero:        true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, false, fmt.Errorf("sign CA certificate: %w", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, nil, false, fmt.Errorf("parse CA certificate: %w", err)
	}
	if err := writePair(certPath, keyPath, der, key); err != nil {
		return nil, nil, false, err
	}
	return cert, key, true, nil
}

func loadCA(certPath, keyPath string) (*x509.Certificate, *ecdsa.PrivateKey, error) {
	certPEM, err := os.ReadFile(certPath) //nolint:gosec // G304: the path is the operator's own cert dir
	if err != nil {
		return nil, nil, fmt.Errorf("read CA certificate: %w", err)
	}
	keyPEM, err := os.ReadFile(keyPath) //nolint:gosec // G304: the path is the operator's own cert dir
	if err != nil {
		return nil, nil, fmt.Errorf("read CA key: %w", err)
	}
	certBlock, _ := pem.Decode(certPEM)
	keyBlock, _ := pem.Decode(keyPEM)
	if certBlock == nil || keyBlock == nil {
		return nil, nil, fmt.Errorf("CA files in %s are not PEM; delete them and run make certs", filepath.Dir(certPath))
	}
	cert, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil {
		return nil, nil, fmt.Errorf("parse CA certificate: %w", err)
	}
	key, err := x509.ParseECPrivateKey(keyBlock.Bytes)
	if err != nil {
		return nil, nil, fmt.Errorf("parse CA key: %w", err)
	}
	return cert, key, nil
}

func writeServer(dir string, caCert *x509.Certificate, caKey *ecdsa.PrivateKey, hosts []string) error {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return fmt.Errorf("generate server key: %w", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial(),
		Subject:      pkix.Name{CommonName: "samba", Organization: []string{"Noord Logistics (fictional)"}},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(serverValidity),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	for _, h := range hosts {
		if ip := net.ParseIP(h); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		} else {
			tmpl.DNSNames = append(tmpl.DNSNames, h)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, caCert, &key.PublicKey, caKey)
	if err != nil {
		return fmt.Errorf("sign server certificate: %w", err)
	}
	return writePair(filepath.Join(dir, ServerFile), filepath.Join(dir, ServerKeyFile), der, key)
}

func writePair(certPath, keyPath string, der []byte, key *ecdsa.PrivateKey) error {
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return fmt.Errorf("encode key: %w", err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", keyPath, err)
	}
	// The certificate is public; 0644 lets the Samba container and clients read it.
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil { //nolint:gosec // G306: a certificate is public
		return fmt.Errorf("write %s: %w", certPath, err)
	}
	return nil
}

// Pool reads a CA file into a pool for verifying the server.
func Pool(caPath string) (*x509.CertPool, error) {
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

func serial() *big.Int {
	n, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		// crypto/rand does not fail on supported platforms.
		panic(fmt.Sprintf("read random serial: %v", err))
	}
	return n
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return !errors.Is(err, fs.ErrNotExist)
}
