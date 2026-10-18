package zammad_test

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/toyinogun/AIITsupport/internal/zammad"
	"github.com/toyinogun/AIITsupport/internal/zammad/zammadtest"
)

func adminClient(t *testing.T, f *zammadtest.Fake) *zammad.Client {
	t.Helper()
	c, err := zammad.New(zammad.Options{
		BaseURL: f.URL(), Auth: zammad.BasicAuth{Login: zammadtest.AdminLogin, Password: zammadtest.AdminPassword},
		Timeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("zammad.New: %v", err)
	}
	return c
}

func tokenClient(t *testing.T, f *zammadtest.Fake) *zammad.Client {
	t.Helper()
	c, err := zammad.New(zammad.Options{BaseURL: f.URL(), Auth: zammad.TokenAuth(zammadtest.Token), Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("zammad.New: %v", err)
	}
	return c
}

// everyMethod calls each exported Client method once. TestEveryMethodIsCovered
// fails when a method is added without an entry here, so the guard test
// below always covers the whole client.
var everyMethod = map[string]func(ctx context.Context, c *zammad.Client, customer int) error{
	"Me": func(ctx context.Context, c *zammad.Client, _ int) error {
		_, err := c.Me(ctx)
		return err
	},
	"SearchUsers": func(ctx context.Context, c *zammad.Client, _ int) error {
		_, err := c.SearchUsers(ctx, "eva", 10)
		return err
	},
	"UsersByLogin": func(ctx context.Context, c *zammad.Client, _ int) error {
		_, err := c.UsersByLogin(ctx, "eva.peters")
		return err
	},
	"TicketStates": func(ctx context.Context, c *zammad.Client, _ int) error {
		_, err := c.TicketStates(ctx)
		return err
	},
	"CreateTicket": func(ctx context.Context, c *zammad.Client, customer int) error {
		_, err := c.CreateTicket(ctx, zammad.NewTicket{
			Title: "Locked out", Group: "IT Support", CustomerID: customer, Tags: []string{"t-01"},
			Article: zammad.NewArticle{Body: "help", ContentType: "text/plain", Type: zammad.ArticleWeb, Sender: zammad.SenderCustomer},
		}, "eva.peters")
		return err
	},
	"UpdateTicket": func(ctx context.Context, c *zammad.Client, customer int) error {
		tk, err := c.CreateTicket(ctx, zammad.NewTicket{Title: "x", Group: "IT Support", CustomerID: customer, Article: zammad.NewArticle{Body: "b", ContentType: "text/plain", Type: zammad.ArticleWeb, Sender: zammad.SenderCustomer}}, "")
		if err != nil {
			return err
		}
		_, err = c.UpdateTicket(ctx, tk.ID, zammad.TicketUpdate{
			State: "closed", OwnerID: customer,
			Article: &zammad.NewArticle{Body: "done", ContentType: "text/plain", Type: zammad.ArticleNote, Sender: zammad.SenderAgent, Internal: true},
		}, "ruben.mulder")
		return err
	},
	"GetTicket": func(ctx context.Context, c *zammad.Client, customer int) error {
		tk, err := c.CreateTicket(ctx, zammad.NewTicket{Title: "x", Group: "IT Support", CustomerID: customer, Article: zammad.NewArticle{Body: "b", ContentType: "text/plain", Type: zammad.ArticleWeb, Sender: zammad.SenderCustomer}}, "")
		if err != nil {
			return err
		}
		_, err = c.GetTicket(ctx, tk.ID)
		return err
	},
	"CreateArticle": func(ctx context.Context, c *zammad.Client, customer int) error {
		tk, err := c.CreateTicket(ctx, zammad.NewTicket{Title: "x", Group: "IT Support", CustomerID: customer, Article: zammad.NewArticle{Body: "b", ContentType: "text/plain", Type: zammad.ArticleWeb, Sender: zammad.SenderCustomer}}, "")
		if err != nil {
			return err
		}
		_, err = c.CreateArticle(ctx, zammad.NewArticle{TicketID: tk.ID, Body: "reply", ContentType: "text/plain", Type: zammad.ArticleNote, Sender: zammad.SenderAgent}, "ruben.mulder")
		return err
	},
	"CreateCategory": func(ctx context.Context, c *zammad.Client, _ int) error {
		_, err := c.CreateCategory(ctx, 1, zammad.NewCategory{Title: "Accounts", Icon: "f023", KBLocaleID: 1})
		return err
	},
	"CreateAnswer": func(ctx context.Context, c *zammad.Client, _ int) error {
		_, err := c.CreateAnswer(ctx, 1, zammad.NewAnswer{CategoryID: 1, Title: "Reset", Body: "<p>x</p>", KBLocaleID: 1})
		return err
	},
	"PublishAnswer": func(ctx context.Context, c *zammad.Client, _ int) error {
		id, err := c.CreateAnswer(ctx, 1, zammad.NewAnswer{CategoryID: 1, Title: "Reset", Body: "<p>x</p>", KBLocaleID: 1})
		if err != nil {
			return err
		}
		return c.PublishAnswer(ctx, 1, id)
	},
	"GetAnswer": func(ctx context.Context, c *zammad.Client, _ int) error {
		id, err := c.CreateAnswer(ctx, 1, zammad.NewAnswer{CategoryID: 1, Title: "Reset", Body: "<p>x</p>", KBLocaleID: 1})
		if err != nil {
			return err
		}
		_, err = c.GetAnswer(ctx, 1, id)
		return err
	},
	"Do": func(ctx context.Context, c *zammad.Client, _ int) error {
		return c.Do(ctx, zammad.Request{Method: http.MethodGet, Path: "/api/v1/users/me"})
	},
}

func TestEveryMethodIsCovered(t *testing.T) {
	typ := reflect.TypeFor[*zammad.Client]()
	var got []string
	for i := range typ.NumMethod() {
		got = append(got, typ.Method(i).Name)
	}
	var want []string
	for name := range everyMethod {
		want = append(want, name)
	}
	sort.Strings(got)
	sort.Strings(want)
	if !slices.Equal(got, want) {
		t.Errorf("Client methods %v, guard test covers %v: add the new method to everyMethod", got, want)
	}
}

// covers: AC-6. No exported client method sends POST, PUT, PATCH, or DELETE
// to a /api/v1/users path (or organizations, or user tokens).
func TestNoMethodWritesUsers(t *testing.T) {
	f := zammadtest.New(t)
	customer := f.AddUser("eva.peters")
	c := adminClient(t, f)
	for name, call := range everyMethod {
		if err := call(t.Context(), c, customer); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	for _, r := range f.Requests() {
		if r.Method != http.MethodGet && zammad.Guarded(r.Path) {
			t.Errorf("%s %s reached Zammad", r.Method, r.Path)
		}
	}
}

// covers: AC-6. The transport refuses a user write even through Do, before
// anything reaches Zammad, whatever the spelling of the path.
func TestGuardRefusesUserWrites(t *testing.T) {
	f := zammadtest.New(t)
	c := tokenClient(t, f)
	paths := []string{
		"/api/v1/users", "/api/v1/users/", "/api/v1/Users/5", "/api/v1/users.json",
		"/api/v1/users/5?expand=true", "/api/v1//users", "/api/v1/tickets/../users",
		"/api/v1/organizations/1", "/api/v1/user_access_token", "/api/v1/user_access_token/3",
	}
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		for _, p := range paths {
			err := c.Do(t.Context(), zammad.Request{Method: method, Path: p, Body: map[string]any{"login": "x"}})
			if !errors.Is(err, zammad.ErrUserWrite) {
				t.Errorf("%s %s: err = %v, want ErrUserWrite", method, p, err)
			}
		}
	}
	if n := len(f.Requests()); n != 0 {
		t.Errorf("%d requests reached Zammad: %+v", n, f.Requests())
	}
}

func TestGuarded(t *testing.T) {
	tests := map[string]bool{
		"/api/v1/users":             true,
		"/api/v1/users/search":      true,
		"/API/V1/USERS/1/":          true,
		"/api/v1/organizations":     true,
		"/api/v1/user_access_token": true,
		"/api/v1/tickets":           false,
		"/api/v1/ticket_articles":   false,
		"/api/v1/knowledge_bases/1": false,
		"/api/v1/usersettings":      false,
		"":                          false,
	}
	for p, want := range tests {
		if got := zammad.Guarded(p); got != want {
			t.Errorf("Guarded(%q) = %t, want %t", p, got, want)
		}
	}
}

// GET to a user path still works: #9 reads users.
func TestGuardAllowsUserReads(t *testing.T) {
	f := zammadtest.New(t)
	c := tokenClient(t, f)
	me, err := c.Me(t.Context())
	if err != nil {
		t.Fatalf("Me: %v", err)
	}
	if me.Login != "noord-assistant" {
		t.Errorf("Me = %+v", me)
	}
	if !strings.Contains(f.Requests()[0].Path, "/users/me") {
		t.Errorf("requests %+v", f.Requests())
	}
}
