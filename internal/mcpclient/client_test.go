package mcpclient

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNewChecksTheEndpointAndToken(t *testing.T) {
	for _, tc := range []struct{ url, token string }{
		{"", "t"}, {"ftp://toolserver/mcp", "t"}, {"http:///mcp", "t"}, {"http://toolserver:8080/mcp", ""},
	} {
		if _, err := New(tc.url, tc.token); err == nil {
			t.Errorf("New(%q, %q) succeeded", tc.url, tc.token)
		}
	}
	if _, err := New("http://toolserver:8080/mcp", "t"); err != nil {
		t.Errorf("New with a valid endpoint: %v", err)
	}
}

// covers: AC-17. The token rides on every request and is never sent on to a
// redirect target.
func TestTokenIsSentAndRedirectsAreNotFollowed(t *testing.T) {
	var gotAuth string
	redirected := false
	elsewhere := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { redirected = true }))
	defer elsewhere.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		http.Redirect(w, r, elsewhere.URL, http.StatusTemporaryRedirect)
	}))
	defer srv.Close()

	c, err := New(srv.URL+"/mcp", "secret-token")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Call(t.Context(), Requester{SlackID: "U01", Email: "e@noord.example", ChannelID: "C01", ThreadTS: "1760000000.000100"}, "user_lookup", nil); err == nil {
		t.Error("a call to a redirecting server succeeded")
	}
	if gotAuth != "Bearer secret-token" {
		t.Errorf("Authorization = %q", gotAuth)
	}
	if redirected {
		t.Error("the client followed a redirect")
	}
}

func TestRequesterMeta(t *testing.T) {
	m, err := Requester{SlackID: "U01", Email: "e@noord.example", ChannelID: "C01", ThreadTS: "1760000000.000100", TicketID: 7}.meta()
	if err != nil || m[MetaTicketID] != int64(7) || m[MetaEmail] != "e@noord.example" || len(m) != 5 {
		t.Fatalf("meta = %v, %v", m, err)
	}
	m, _ = Requester{SlackID: "U01", Email: "e@noord.example", ChannelID: "C01", ThreadTS: "1760000000.000100"}.meta()
	if _, ok := m[MetaTicketID]; ok {
		t.Error("a requester with no ticket sent a ticket id")
	}
}
