package graph

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeCertPEM writes a self signed certificate and its RSA key into one
// PEM file, the shape GRAPH_CLIENT_CERT points at.
func writeCertPEM(t *testing.T) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "noord-toolserver"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	raw := append(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})...)
	path := filepath.Join(t.TempDir(), "graph-client.pem")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// covers: AC-24 (spec 0015). A certificate file builds a client without a
// secret; a missing or unreadable one fails before any request.
func TestNewWithCredentialsCertificate(t *testing.T) {
	opts := Options{Timeout: time.Second, ClientIDSetting: "GRAPH_CLIENT_ID"}
	creds := Credentials{TenantID: "00000000-0000-0000-0000-000000000001", ClientID: "00000000-0000-0000-0000-000000000002", CertFile: writeCertPEM(t)}
	if _, err := NewWithCredentials(creds, opts); err != nil {
		t.Fatalf("NewWithCredentials with a certificate: %v", err)
	}

	creds.CertFile = filepath.Join(t.TempDir(), "absent.pem")
	if _, err := NewWithCredentials(creds, opts); err == nil || !strings.Contains(err.Error(), "GRAPH_CLIENT_ID") {
		t.Errorf("a missing certificate: err = %v, want one naming GRAPH_CLIENT_ID", err)
	}

	bad := filepath.Join(t.TempDir(), "bad.pem")
	if err := os.WriteFile(bad, []byte("not a pem"), 0o600); err != nil {
		t.Fatal(err)
	}
	creds.CertFile = bad
	if _, err := NewWithCredentials(creds, opts); err == nil {
		t.Error("a file with no certificate built a client")
	}
}
