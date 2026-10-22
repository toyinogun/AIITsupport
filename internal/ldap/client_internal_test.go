package ldap

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	goldap "github.com/go-ldap/ldap/v3"

	"github.com/toyinogun/AIITsupport/internal/cause"
)

func TestToUser(t *testing.T) {
	attrs := func(uac ...string) map[string][]string {
		m := map[string][]string{
			"sAMAccountName": {"eva.peters"}, "displayName": {"Eva Peters"},
			"department": {"Sales"}, "mail": {"eva.peters@noord.example"},
		}
		if len(uac) > 0 {
			m["userAccountControl"] = uac
		}
		return m
	}
	for _, tc := range []struct {
		name    string
		attrs   map[string][]string
		enabled bool
	}{
		{"a normal account is enabled", attrs("512"), true},
		{"the disabled bit marks it disabled", attrs("514"), false},
		{"no userAccountControl reads as enabled", attrs(), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := toUser(goldap.NewEntry(evaDN, tc.attrs))

			want := User{DN: evaDN, Username: "eva.peters", DisplayName: "Eva Peters", Department: "Sales", Mail: "eva.peters@noord.example", Enabled: tc.enabled}
			if got != want {
				t.Errorf("toUser = %+v, want %+v", got, want)
			}
		})
	}
}

// covers: AC-4. The identity search escapes the mail, skips disabled
// accounts in the filter, and caps the result.
func TestUsersByMailSearch(t *testing.T) {
	const base = "DC=ad,DC=noord,DC=example"
	dir := oneEntry(base, map[string][]string{"sAMAccountName": {"eva.peters"}, "mail": {"eva.peters@noord.example"}, "userAccountControl": {"512"}})

	users, err := usersByMail(dir, base, "eva*)(mail=*@noord.example")
	if err != nil || len(users) != 1 || users[0].Username != "eva.peters" {
		t.Fatalf("usersByMail = %+v, %v", users, err)
	}
	if want := `(mail=eva\2a\29\28mail=\2a@noord.example)`; !strings.Contains(dir.last.Filter, want) {
		t.Errorf("filter %s does not hold the escaped mail %s", dir.last.Filter, want)
	}
	if !strings.Contains(dir.last.Filter, "(!(userAccountControl:1.2.840.113556.1.4.803:=2))") {
		t.Errorf("filter %s does not skip disabled accounts", dir.last.Filter)
	}
	if dir.last.SizeLimit != maxMailMatches || dir.last.Scope != goldap.ScopeWholeSubtree {
		t.Errorf("search size limit %d, scope %d; want %d over the subtree", dir.last.SizeLimit, dir.last.Scope, maxMailMatches)
	}
}

// covers: AC-4. Hitting the size limit is ErrTooManyMatches and returns no
// users, even when go-ldap hands back the partial result with the error.
func TestUsersByMailSizeLimitIsTooManyMatches(t *testing.T) {
	dir := &fakeDirectory{
		err:     goldap.NewError(goldap.LDAPResultSizeLimitExceeded, errors.New("size limit exceeded")),
		partial: &goldap.SearchResult{Entries: []*goldap.Entry{goldap.NewEntry(evaDN, map[string][]string{"sAMAccountName": {"eva.peters"}})}},
	}

	users, err := usersByMail(dir, "DC=ad,DC=noord,DC=example", "shared@noord.example")
	if !errors.Is(err, ErrTooManyMatches) || users != nil {
		t.Errorf("usersByMail = %+v, %v; want no users and ErrTooManyMatches", users, err)
	}
}

func TestUsersByMailWrapsOtherErrors(t *testing.T) {
	cause := goldap.NewError(goldap.LDAPResultBusy, errors.New("busy"))

	_, err := usersByMail(&fakeDirectory{err: cause}, "DC=ad,DC=noord,DC=example", "eva.peters@noord.example")
	if !errors.Is(err, cause) || errors.Is(err, ErrTooManyMatches) {
		t.Errorf("usersByMail = %v; want it to wrap %v", err, cause)
	}
}

// covers: AC-16 (spec 0015). An LDAP failure in Ping logs as
// ldap_<result code>, never its text; other failures keep their own token.
func TestPingErrorCause(t *testing.T) {
	refused := pingError{err: &goldap.Error{ResultCode: goldap.LDAPResultInvalidCredentials, Err: errors.New("detail")}, code: goldap.LDAPResultInvalidCredentials}
	if got := cause.Of(fmt.Errorf("check: %w", refused)); got != "ldap_49" {
		t.Errorf("cause.Of = %q, want ldap_49", got)
	}
	if strings.Contains(cause.Of(refused), "detail") {
		t.Error("the cause token carries the error text")
	}
}
