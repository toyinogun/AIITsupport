package graph_test

import (
	"errors"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/toyinogun/AIITsupport/internal/graph"
	"github.com/toyinogun/AIITsupport/internal/graph/graphtest"
)

func fakeClient(t *testing.T, f *graphtest.Fake) *graph.Client {
	t.Helper()
	c, err := graph.New(graph.Options{
		BaseURL: f.URL(), Token: graph.StaticToken(graphtest.Token),
		Timeout: 5 * time.Second, ClientIDSetting: "GRAPH_CLIENT_ID",
	})
	if err != nil {
		t.Fatalf("graph.New: %v", err)
	}
	return c
}

func authCalls(f *graphtest.Fake) []string {
	return slices.DeleteFunc(f.Requests(), func(r string) bool { return !strings.Contains(r, "/authentication") })
}

// covers: AC-10. Only a marked user passes the fence; the tenant admin and an
// unknown name fail before any auth method call.
func TestUserByUsernameFence(t *testing.T) {
	f := graphtest.New(t)
	f.AddUser(map[string]any{"mailNickname": "eva.peters", "employeeType": graph.ManagedMarker, "userPrincipalName": "eva.peters@t.test"})
	f.AddUser(map[string]any{"mailNickname": "tenantadmin", "userPrincipalName": "tenantadmin@t.test"})
	f.AddUser(map[string]any{"mailNickname": "someone_example.com#EXT#", "userPrincipalName": "someone_example.com#EXT#@t.test"})
	c := fakeClient(t, f)

	u, err := c.UserByUsername(t.Context(), "eva.peters")
	if err != nil {
		t.Fatalf("UserByUsername(eva.peters): %v", err)
	}
	if u.User().MailNickname != "eva.peters" || u.ID() == "" {
		t.Errorf("got %+v", u.User())
	}

	if _, err := c.UserByUsername(t.Context(), "tenantadmin"); !errors.Is(err, graph.ErrNotManaged) {
		t.Errorf("tenantadmin: %v, want ErrNotManaged", err)
	}
	if _, err := c.UserByUsername(t.Context(), "someone_example.com#EXT#"); !errors.Is(err, graph.ErrNotManaged) {
		t.Errorf("guest admin: %v, want ErrNotManaged", err)
	}
	if _, err := c.UserByUsername(t.Context(), "made.up"); !errors.Is(err, graph.ErrNotFound) {
		t.Errorf("made.up: %v, want ErrNotFound", err)
	}
	if calls := authCalls(f); len(calls) != 0 {
		t.Errorf("auth method calls before the fence passed: %v", calls)
	}
}

func TestUserByUsernameRefusesOddNames(t *testing.T) {
	f := graphtest.New(t)
	c := fakeClient(t, f)
	for _, name := range []string{"", "a' or 1 eq 1 or 'x", "a b", strings.Repeat("a", 80)} {
		if _, err := c.UserByUsername(t.Context(), name); err == nil {
			t.Errorf("UserByUsername(%q) = nil error", name)
		}
	}
	if len(f.Requests()) != 0 {
		t.Errorf("requests sent for refused names: %v", f.Requests())
	}
}

func TestManagedRejectsAnUnmarkedUser(t *testing.T) {
	if _, err := graph.Managed(graph.User{ID: "x", MailNickname: "admin"}); !errors.Is(err, graph.ErrNotManaged) {
		t.Errorf("Managed(unmarked) = %v, want ErrNotManaged", err)
	}
	var zero graph.ManagedUser
	f := graphtest.New(t)
	if _, err := fakeClient(t, f).AuthMethods(t.Context(), zero); !errors.Is(err, graph.ErrNotManaged) {
		t.Errorf("AuthMethods(zero) = %v, want ErrNotManaged", err)
	}
}

// covers: AC-9. Methods are listed by type, and a phone method deletes.
func TestAuthMethodsAndDelete(t *testing.T) {
	f := graphtest.New(t)
	id := f.AddUser(map[string]any{"mailNickname": "eva.peters", "employeeType": graph.ManagedMarker})
	f.SetPhones(id, []graphtest.Phone{{ID: graphtest.PhoneID("mobile"), PhoneNumber: "+1 2025550114", PhoneType: "mobile"}})
	c := fakeClient(t, f)
	u, err := c.UserByUsername(t.Context(), "eva.peters")
	if err != nil {
		t.Fatal(err)
	}

	methods, err := c.AuthMethods(t.Context(), u)
	if err != nil {
		t.Fatalf("AuthMethods: %v", err)
	}
	var types []string
	for _, m := range methods {
		types = append(types, m.Type)
	}
	if !slices.Equal(types, []string{"password", "phone"}) {
		t.Errorf("types = %v, want [password phone]", types)
	}

	if err := c.DeletePhoneMethod(t.Context(), u, graphtest.PhoneID("mobile")); err != nil {
		t.Fatalf("DeletePhoneMethod: %v", err)
	}
	phones, err := c.PhoneMethods(t.Context(), u)
	if err != nil || len(phones) != 0 {
		t.Errorf("PhoneMethods = %v, %v; want none left", phones, err)
	}
}

// covers: AC-9, AC-15. Masking never shows more than the last 2 digits.
func TestMaskPhone(t *testing.T) {
	cases := map[string]string{
		"+1 2025550114": "+* ********14",
		"+1 2025550167": "+* ********67",
		"12":            "12",
		"":              "",
	}
	for in, want := range cases {
		if got := graph.MaskPhone(in); got != want {
			t.Errorf("MaskPhone(%q) = %q, want %q", in, got, want)
		}
	}
	visible := regexp.MustCompile(`\d`)
	for _, n := range []string{"+1 2025550198", "+1 2025550123", "+12 025550197"} {
		if got := len(visible.FindAllString(graph.MaskPhone(n), -1)); got > 2 {
			t.Errorf("MaskPhone(%q) shows %d digits", n, got)
		}
	}
}
