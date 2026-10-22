package graph_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"testing"

	"github.com/toyinogun/AIITsupport/internal/config"
	"github.com/toyinogun/AIITsupport/internal/graph"
)

// TestLiveAuthenticatorProbe is spec 0012's build step 1 probe (AC-20),
// against the real tenant with the tool server's own app (GRAPH_*). It runs
// only with TEST_ENTRA=1, the .env settings loaded, and
// TEST_ENTRA_MFA_USER set to a synced demo user (mailNickname) who has
// registered the Microsoft Authenticator app on a phone. It prints what it
// finds for the journal and the Entra runbook:
//
//   - the app can list the user's Authenticator methods,
//   - whether it can read signInPreferences, and the preferred method,
//   - that POST .../temporaryAccessPassMethods is still 403.
//
// With TEST_ENTRA_DELETE=1 it also deletes the user's methods in the order
// the clear uses and reports which delete Graph refused, if any; the user
// then has to register again. Never in CI. Register the Authenticator on two
// phones first to see which registration Graph treats as the default (the
// clear defers a refused Authenticator delete behind the others, AC-11).
func TestLiveAuthenticatorProbe(t *testing.T) {
	if os.Getenv("TEST_ENTRA") == "" {
		t.Skip("set TEST_ENTRA=1 and TEST_ENTRA_MFA_USER (with .env loaded) to run the Authenticator probe")
	}
	username := os.Getenv("TEST_ENTRA_MFA_USER")
	if username == "" {
		t.Fatal("TEST_ENTRA is set but TEST_ENTRA_MFA_USER is not")
	}
	cfg, err := config.LoadGraph(os.LookupEnv)
	if err != nil {
		t.Fatalf("LoadGraph: %v", err)
	}
	c, err := graph.NewWithCredentials(
		graph.Credentials{TenantID: cfg.GraphTenantID, ClientID: cfg.GraphClientID, ClientSecret: cfg.GraphClientSecret, CertFile: cfg.GraphClientCert},
		graph.Options{MaxRetries: cfg.GraphMaxRetries, Timeout: cfg.GraphTimeout, ClientIDSetting: "GRAPH_CLIENT_ID"})
	if err != nil {
		t.Fatal(err)
	}
	u, err := c.UserByUsername(t.Context(), username)
	if err != nil {
		t.Fatalf("UserByUsername: %v", err)
	}

	auth, err := c.AuthenticatorMethods(t.Context(), u)
	if err != nil {
		t.Fatalf("AuthenticatorMethods (is UserAuthMethod-MicrosoftAuthApp.ReadWrite.All granted with admin consent?): %v", err)
	}
	t.Logf("authenticator methods: %d", len(auth))
	methods, err := c.AuthMethods(t.Context(), u)
	if err != nil {
		t.Fatalf("AuthMethods: %v", err)
	}
	for _, m := range methods {
		t.Logf("method type %s (phone type %q)", m.Type, m.PhoneType)
	}

	pref, err := c.SignInPreference(t.Context(), u)
	switch {
	case graph.IsForbidden(err):
		t.Log("signInPreferences: 403, the app cannot read it; the clear uses the fixed order")
	case err != nil:
		t.Logf("signInPreferences: error status %d", graph.Status(err))
	default:
		t.Logf("signInPreferences: readable, preferred %q", pref)
	}

	// This POST must be refused. If it ever succeeds (the app was given the
	// wider UserAuthenticationMethod.ReadWrite.All fallback), it has issued a
	// real one time sign in code, so the probe deletes that pass at once and
	// fails loudly rather than leave it on the demo user.
	tapPath := graph.UserPath(u.ID()) + "/authentication/temporaryAccessPassMethods"
	var tap struct {
		ID string `json:"id"`
	}
	err = c.Do(t.Context(), graph.Request{
		Method: http.MethodPost, Path: tapPath,
		Body: map[string]any{"lifetimeInMinutes": 60, "isUsableOnce": true}, Out: &tap,
	})
	switch {
	case err == nil:
		delErr := c.Do(context.WithoutCancel(t.Context()), graph.Request{Method: http.MethodDelete, Path: tapPath + "/" + tap.ID})
		t.Errorf("Temporary Access Pass was ISSUED for %s (the tool server app must never be able to); deleting it: %s. "+
			"Check the user in the Entra admin center and remove the wider permission.", username, describe(delErr))
	case !graph.IsForbidden(err):
		t.Errorf("Temporary Access Pass: %s, want 403 (the tool server app must never issue one)", describe(err))
	}

	if os.Getenv("TEST_ENTRA_DELETE") == "" {
		t.Log("set TEST_ENTRA_DELETE=1 to also probe the delete order (the user must register again afterwards)")
		return
	}
	// Authenticator first (the fixed order), then the phones, then the
	// Authenticator again if Graph refused it while other methods remained.
	refused := false
	for _, a := range auth {
		err := c.DeleteAuthenticatorMethod(t.Context(), u, a.ID)
		t.Logf("delete Authenticator first: %s", describe(err))
		refused = refused || err != nil
	}
	for _, m := range methods {
		if m.Type == graph.MethodPhone {
			err := c.DeletePhoneMethod(t.Context(), u, m.ID)
			t.Logf("delete %s phone: %s", m.PhoneType, describe(err))
		}
	}
	if refused {
		for _, a := range auth {
			err := c.DeleteAuthenticatorMethod(t.Context(), u, a.ID)
			t.Logf("delete Authenticator again, after the phones: %s", describe(err))
		}
	}
}

// describe is a delete's result as Graph reported it: status, code, message.
func describe(err error) string {
	var gerr *graph.Error
	switch {
	case err == nil:
		return "ok"
	case errors.As(err, &gerr):
		return fmt.Sprintf("status %d, code %q, message %q", gerr.Status, gerr.Code, gerr.Message)
	}
	return "error with no Graph status (network or timeout)"
}
