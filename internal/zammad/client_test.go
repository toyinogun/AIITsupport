package zammad_test

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/toyinogun/AIITsupport/internal/zammad"
	"github.com/toyinogun/AIITsupport/internal/zammad/zammadtest"
)

func TestNewRejectsBadOptions(t *testing.T) {
	cases := map[string]zammad.Options{
		"no auth":    {BaseURL: "http://127.0.0.1:58080", Timeout: time.Second},
		"no timeout": {BaseURL: "http://127.0.0.1:58080", Auth: zammad.TokenAuth("t")},
		"bad url":    {BaseURL: "127.0.0.1:58080", Auth: zammad.TokenAuth("t"), Timeout: time.Second},
		"ftp":        {BaseURL: "ftp://host", Auth: zammad.TokenAuth("t"), Timeout: time.Second},
	}
	for name, opts := range cases {
		if _, err := zammad.New(opts); err == nil {
			t.Errorf("%s: New accepted %+v", name, opts)
		}
	}
}

// covers: AC-14. A REST error names the method, path, and status, never the
// token or the query string.
func TestErrorNamesCallNotSecrets(t *testing.T) {
	f := zammadtest.New(t)
	c, err := zammad.New(zammad.Options{BaseURL: f.URL(), Auth: zammad.TokenAuth("wrong-token-value-secret-0123456789"), Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.SearchUsers(t.Context(), "eva.peters", 5)
	if !zammad.IsUnauthorized(err) {
		t.Fatalf("err = %v, want 401", err)
	}
	msg := err.Error()
	for _, want := range []string{"GET", "/api/v1/users/search", "401"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q lacks %q", msg, want)
		}
	}
	for _, leak := range []string{"wrong-token-value", "eva.peters", "query="} {
		if strings.Contains(msg, leak) {
			t.Errorf("error %q leaks %q", msg, leak)
		}
	}
}

// covers: AC-6. The assistant token gets 403 on settings and on any KB write.
func TestAssistantForbidden(t *testing.T) {
	f := zammadtest.New(t)
	c := tokenClient(t, f)
	if err := c.Do(t.Context(), zammad.Request{Method: http.MethodGet, Path: "/api/v1/settings"}); !zammad.IsForbidden(err) {
		t.Errorf("settings: %v, want 403", err)
	}
	if _, err := c.CreateCategory(t.Context(), 1, zammad.NewCategory{Title: "x", Icon: "f023", KBLocaleID: 1}); !zammad.IsForbidden(err) {
		t.Errorf("category: %v, want 403", err)
	}
}

func TestTicketFlowSendsFromAndTags(t *testing.T) {
	f := zammadtest.New(t)
	customer := f.AddUser("eva.peters")
	c := adminClient(t, f)
	tk, err := c.CreateTicket(t.Context(), zammad.NewTicket{
		Title: "Locked out", Group: "IT Support", CustomerID: customer, Tags: []string{"t-01", "lockout"},
		Article: zammad.NewArticle{Body: "help", ContentType: "text/plain", Type: zammad.ArticleWeb, Sender: zammad.SenderCustomer},
	}, "eva.peters")
	if err != nil {
		t.Fatalf("CreateTicket: %v", err)
	}
	if tk.ID == 0 || len(tk.ArticleIDs) != 1 {
		t.Fatalf("ticket = %+v", tk)
	}
	pending := time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC)
	if _, err := c.UpdateTicket(t.Context(), tk.ID, zammad.TicketUpdate{State: "pending reminder", OwnerID: 7, PendingTime: &pending}, "ruben.mulder"); err != nil {
		t.Fatalf("UpdateTicket: %v", err)
	}
	reqs := f.Requests()
	if reqs[0].From != "eva.peters" || reqs[0].Body["tags"] != "t-01,lockout" {
		t.Errorf("create sent %+v", reqs[0])
	}
	if reqs[1].From != "ruben.mulder" || reqs[1].Body["pending_time"] != "2026-11-17T09:00:00Z" || reqs[1].Body["owner_id"] != float64(7) {
		t.Errorf("update sent %+v", reqs[1])
	}
	if _, ok := reqs[0].Body["customer"]; ok {
		t.Errorf("create sent a customer object; only customer_id is allowed: %+v", reqs[0].Body)
	}
}

func TestNoRedirects(t *testing.T) {
	f := zammadtest.New(t)
	f.Intercept = func(*http.Request) (zammadtest.Fault, bool) {
		return zammadtest.Fault{Status: http.StatusFound}, true
	}
	c := tokenClient(t, f)
	if _, err := c.Me(t.Context()); zammad.Status(err) != http.StatusFound {
		t.Errorf("err = %v, want the 302 itself", err)
	}
}
