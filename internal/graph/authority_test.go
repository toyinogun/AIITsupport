package graph_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/toyinogun/AIITsupport/internal/graph"
	"github.com/toyinogun/AIITsupport/internal/graph/graphtest"
)

const (
	fakeClientID = "00000000-0000-0000-0000-00000000c11e"
	fakeSecret   = "fake-secret-for-the-eval-harness"
	childEnv     = "GRAPH_AUTHORITY_TEST_URL"
)

// covers: AC-20 (spec 0018). The real azidentity credential signs in at an
// overridden authority host (instance discovery off) and reads a user from a
// fake Graph over TLS that the process trusts through SSL_CERT_FILE. The
// reading half runs in a child process, because Go loads the system roots once
// per process and the test binary may already have done so.
func TestCredentialUsesTheOverriddenAuthority(t *testing.T) {
	if os.Getenv(childEnv) != "" {
		runAuthorityChild(t)
		return
	}
	fake := graphtest.StartTLS(fakeClientID, fakeSecret)
	t.Cleanup(fake.Close)
	fake.AddUser(map[string]any{
		"mailNickname": "anna.devries", "userPrincipalName": "anna.devries@noord.example",
		"employeeType": graph.ManagedMarker,
	})
	ca := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(ca, fake.CAPEM(), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestCredentialUsesTheOverriddenAuthority$", "-test.v") //nolint:gosec // re-runs this test binary with fixed arguments
	cmd.Env = []string{childEnv + "=" + fake.URL(), "SSL_CERT_FILE=" + ca}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("child: %v\n%s", err, out)
	}
}

func runAuthorityChild(t *testing.T) {
	base := os.Getenv(childEnv)
	creds := graph.Credentials{TenantID: "tenant", ClientID: fakeClientID, ClientSecret: fakeSecret, AuthorityHost: base}
	c, err := graph.NewWithCredentials(creds, graph.Options{BaseURL: base, Timeout: 10 * time.Second, ClientIDSetting: "GRAPH_CLIENT_ID"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	u, err := c.UserByUsername(ctx, "anna.devries")
	if err != nil {
		t.Fatalf("read a user: %v", err)
	}
	if u.User().MailNickname != "anna.devries" {
		t.Errorf("user = %q", u.User().MailNickname)
	}
	bad := graph.Credentials{TenantID: "tenant", ClientID: fakeClientID, ClientSecret: "wrong", AuthorityHost: base}
	b, err := graph.NewWithCredentials(bad, graph.Options{BaseURL: base, Timeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.UserByUsername(ctx, "anna.devries"); err == nil {
		t.Error("a wrong secret read a user")
	}
}
