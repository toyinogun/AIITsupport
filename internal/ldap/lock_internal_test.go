package ldap

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	goldap "github.com/go-ldap/ldap/v3"
)

// fakeDirectory answers base object searches from entries keyed by DN, the
// way ReadOne asks. A DN with no entry is "no such object". Only Search is
// implemented; any other call panics on the nil embedded Client.
type fakeDirectory struct {
	goldap.Client
	entries map[string][]*goldap.Entry
	err     error
	// partial is returned along with err, as go-ldap does on a size limit.
	partial *goldap.SearchResult
	asked   []string              // attributes of the last search
	last    *goldap.SearchRequest // the last search
}

func (f *fakeDirectory) Search(req *goldap.SearchRequest) (*goldap.SearchResult, error) {
	f.asked, f.last = req.Attributes, req
	if f.err != nil {
		return f.partial, f.err
	}
	entries, ok := f.entries[req.BaseDN]
	if !ok {
		return nil, goldap.NewError(goldap.LDAPResultNoSuchObject, errors.New("no such object"))
	}
	return &goldap.SearchResult{Entries: entries}, nil
}

func oneEntry(dn string, attrs map[string][]string) *fakeDirectory {
	return &fakeDirectory{entries: map[string][]*goldap.Entry{dn: {goldap.NewEntry(dn, attrs)}}}
}

const evaDN = "CN=Eva Peters,OU=Sales,OU=Users,OU=Noord,DC=ad,DC=noord,DC=example"

func TestFileTime(t *testing.T) {
	for raw, want := range map[string]time.Time{
		"":                   {},
		"0":                  {},
		"116444736000000000": time.Unix(0, 0).UTC(),
		"134046144001234567": time.Date(2025, time.October, 11, 0, 0, 0, 123456700, time.UTC),
	} {
		got, err := fileTime(raw)
		if err != nil || !got.Equal(want) {
			t.Errorf("fileTime(%q) = %s, %v; want %s", raw, got, err, want)
		}
	}
	if _, err := fileTime("soon"); err == nil {
		t.Error("fileTime accepted a non number")
	}
}

// covers: AC-6. Locked comes from the computed flag only: lockoutTime stays
// set after a lock expires, so it must not decide the state.
func TestStateReadsTheComputedLockFlag(t *testing.T) {
	lockedAt := time.Date(2025, time.October, 11, 0, 0, 0, 0, time.UTC)
	const lockoutTime = "134046144000000000" // lockedAt as a FILETIME

	for _, tc := range []struct {
		name     string
		computed string
		want     bool
	}{
		{"the lockout bit is set", "16", true},
		{"the lockout bit is set with other bits", "8388624", true}, // 0x800010: password expired too
		{"the lock expired but lockoutTime is still set", "0", false},
		{"other bits without the lockout bit", "8388608", false}, // 0x800000: password expired only
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := oneEntry(evaDN, map[string][]string{
				attrComputedUAC: {tc.computed}, attrLockoutTime: {lockoutTime}, attrBadPwdCount: {"5"},
			})

			got, err := State(dir, evaDN)
			if err != nil {
				t.Fatal(err)
			}
			if got.Locked != tc.want || got.BadPwdCount != 5 || !got.LockedAt.Equal(lockedAt) {
				t.Errorf("State = %+v; want Locked %v, BadPwdCount 5, LockedAt %s", got, tc.want, lockedAt)
			}
		})
	}
}

// The computed attribute is constructed: AD returns it only when asked for by
// name, so State must name it.
func TestStateAsksForTheComputedAttributeByName(t *testing.T) {
	dir := oneEntry(evaDN, map[string][]string{attrComputedUAC: {"0"}})

	if _, err := State(dir, evaDN); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(dir.asked, attrComputedUAC) {
		t.Errorf("State asked for %v, want %s among them", dir.asked, attrComputedUAC)
	}
}

func TestStateTreatsAbsentCountersAsZero(t *testing.T) {
	dir := oneEntry(evaDN, map[string][]string{attrComputedUAC: {"0"}})

	got, err := State(dir, evaDN)
	if err != nil || got != (LockState{}) {
		t.Errorf("State = %+v, %v; want the zero state", got, err)
	}
}

// A lock state that cannot be read is an error, never "not locked".
func TestStateFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name string
		dir  *fakeDirectory
		want string
	}{
		{"the user does not exist", &fakeDirectory{entries: map[string][]*goldap.Entry{}}, "not found"},
		{"the computed flag is missing", oneEntry(evaDN, map[string][]string{attrLockoutTime: {"0"}}), "lock flag"},
		{"the computed flag is not a number", oneEntry(evaDN, map[string][]string{attrComputedUAC: {"locked"}}), "lock flag"},
		{"lockoutTime is not a FILETIME", oneEntry(evaDN, map[string][]string{attrComputedUAC: {"16"}, attrLockoutTime: {"soon"}}), "lockoutTime"},
		{"the search fails", &fakeDirectory{err: goldap.NewError(goldap.LDAPResultInsufficientAccessRights, errors.New("denied"))}, "read " + evaDN},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := State(tc.dir, evaDN)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("State = %+v, %v; want an error mentioning %q", got, err, tc.want)
			}
		})
	}
}

// covers: AC-6. lockoutDuration is a negative count of 100ns intervals; 0 and
// the smallest int64 mean the lock lasts until an admin clears it.
func TestLockoutDuration(t *testing.T) {
	const base = "DC=ad,DC=noord,DC=example"
	for _, tc := range []struct {
		raw     string
		want    time.Duration
		forever bool
	}{
		{"-9000000000", 15 * time.Minute, false},
		{"-18000000000", 30 * time.Minute, false},
		{"0", 0, true},
		{"-9223372036854775808", 0, true},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			dir := oneEntry(base, map[string][]string{"lockoutDuration": {tc.raw}})

			got, forever, err := LockoutDuration(dir, base)
			if err != nil || got != tc.want || forever != tc.forever {
				t.Errorf("LockoutDuration = %s, %v, %v; want %s, %v", got, forever, err, tc.want, tc.forever)
			}
		})
	}
}

func TestLockoutDurationRefusesWhatItCannotRead(t *testing.T) {
	const base = "DC=ad,DC=noord,DC=example"
	for name, dir := range map[string]*fakeDirectory{
		"a positive interval":  oneEntry(base, map[string][]string{"lockoutDuration": {"9000000000"}}),
		"a non number":         oneEntry(base, map[string][]string{"lockoutDuration": {"fifteen minutes"}}),
		"no attribute":         oneEntry(base, map[string][]string{}),
		"no domain root entry": {entries: map[string][]*goldap.Entry{}},
	} {
		t.Run(name, func(t *testing.T) {
			if d, forever, err := LockoutDuration(dir, base); err == nil {
				t.Errorf("LockoutDuration = %s, %v, nil; want an error", d, forever)
			}
		})
	}
}
