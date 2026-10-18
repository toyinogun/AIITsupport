package ldap

import (
	"errors"
	"testing"

	goldap "github.com/go-ldap/ldap/v3"
)

func TestReadOne(t *testing.T) {
	t.Run("a missing object is nil, not an error", func(t *testing.T) {
		e, err := ReadOne(&fakeDirectory{entries: map[string][]*goldap.Entry{}}, evaDN, "mail")
		if e != nil || err != nil {
			t.Errorf("ReadOne = %v, %v; want nil, nil", e, err)
		}
	})

	t.Run("returns the one entry", func(t *testing.T) {
		dir := oneEntry(evaDN, map[string][]string{"mail": {"eva.peters@noord.example"}})

		e, err := ReadOne(dir, evaDN, "mail")
		if err != nil || e.GetAttributeValue("mail") != "eva.peters@noord.example" {
			t.Errorf("ReadOne = %v, %v", e, err)
		}
	})

	t.Run("more than one entry is an error", func(t *testing.T) {
		dir := &fakeDirectory{entries: map[string][]*goldap.Entry{evaDN: {
			goldap.NewEntry(evaDN, nil), goldap.NewEntry(evaDN, nil),
		}}}

		if _, err := ReadOne(dir, evaDN); err == nil {
			t.Error("ReadOne accepted two entries for one DN")
		}
	})

	t.Run("a search error is wrapped with the DN", func(t *testing.T) {
		cause := goldap.NewError(goldap.LDAPResultBusy, errors.New("busy"))

		_, err := ReadOne(&fakeDirectory{err: cause}, evaDN)
		if !errors.Is(err, cause) {
			t.Errorf("ReadOne = %v; want it to wrap %v", err, cause)
		}
	})
}

func TestBaseDN(t *testing.T) {
	t.Run("reads defaultNamingContext from the rootDSE", func(t *testing.T) {
		dir := oneEntry("", map[string][]string{"defaultNamingContext": {"DC=ad,DC=noord,DC=example"}})

		got, err := BaseDN(dir)
		if err != nil || got != "DC=ad,DC=noord,DC=example" {
			t.Errorf("BaseDN = %q, %v", got, err)
		}
	})

	for name, dir := range map[string]*fakeDirectory{
		"the rootDSE has no naming context": oneEntry("", map[string][]string{}),
		"the rootDSE is empty":              {entries: map[string][]*goldap.Entry{"": {}}},
		"the search fails":                  {err: errors.New("connection reset")},
	} {
		t.Run(name, func(t *testing.T) {
			if got, err := BaseDN(dir); err == nil {
				t.Errorf("BaseDN = %q, nil; want an error", got)
			}
		})
	}
}

// WaitAndBind retries only while the directory is still coming up; a wrong
// password or a refusal must fail at once.
func TestRetryable(t *testing.T) {
	for _, tc := range []struct {
		code uint16
		want bool
	}{
		{goldap.ErrorNetwork, true},
		{goldap.LDAPResultUnavailable, true},
		{goldap.LDAPResultBusy, true},
		{goldap.LDAPResultInvalidCredentials, false},
		{goldap.LDAPResultInsufficientAccessRights, false},
	} {
		err := goldap.NewError(tc.code, errors.New("x"))
		if got := retryable(err); got != tc.want {
			t.Errorf("retryable(code %d) = %v, want %v", tc.code, got, tc.want)
		}
	}
}
