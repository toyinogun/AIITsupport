package entra

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/go-ldap/ldap/v3"
)

const demoExample = "../../../seed/entra/demo.example.yaml"

// covers: AC-8. The demo file's rules, each reported without the number.
func TestParseDemo(t *testing.T) {
	m := loadNoord(t)
	cases := []struct {
		name, yaml, wantErr string
	}{
		{"valid", `users: [{username: eva.peters, mfa_phone: "+31 612345678"}]`, ""},
		{"empty file", ``, ""},
		{"unknown user", `users: [{username: no.one, mfa_phone: "+31 612345678"}]`, "not a user in the seed file"},
		{"not synced", `users: [{username: piotr.nowak, mfa_phone: "+31 612345678"}]`, "synced: false"},
		{"three users", `users: [{username: eva.peters, mfa_phone: "+31 612345671"}, {username: bas.kramer, mfa_phone: "+31 612345672"}, {username: ilse.smit, mfa_phone: "+31 612345673"}]`, "at most 2"},
		{"shared number", `users: [{username: eva.peters, mfa_phone: "+31 612345678"}, {username: bas.kramer, mfa_phone: "+31 612345678"}]`, "share an mfa_phone"},
		{"bad pattern", `users: [{username: eva.peters, mfa_phone: "0612345678"}]`, "6 to 12 digits"},
		{"listed twice", `users: [{username: eva.peters, mfa_phone: "+31 612345671"}, {username: eva.peters, mfa_phone: "+31 612345672"}]`, "listed twice"},
		{"unknown field", `users: [{username: eva.peters, mfa_phone: "+31 612345678", mail: x}]`, "decode"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := ParseDemo([]byte(c.yaml), m)
			if c.wantErr == "" {
				if err != nil {
					t.Fatalf("ParseDemo: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("ParseDemo = %v, want %q", err, c.wantErr)
			}
			if strings.Contains(err.Error(), "612345") {
				t.Errorf("error %q echoes a phone number", err)
			}
		})
	}
}

func TestLoadDemoMissingFileMeansNone(t *testing.T) {
	d, err := LoadDemo(filepath.Join(t.TempDir(), "absent.yaml"), loadNoord(t))
	if err != nil || len(d.Users) != 0 {
		t.Errorf("LoadDemo(missing) = %+v, %v; want no demo users", d, err)
	}
}

// covers: AC-8. The demo password follows AD's complexity rule.
func TestCheckDemoPassword(t *testing.T) {
	m := loadNoord(t)
	cases := map[string]bool{
		"":                  false,
		"Short1!":           false,
		"alllowercase123":   false,
		"Kq7!vXz2-Wm9#":     true,
		"Peters-Kq7!vXz2":   false, // holds a staff surname
		"xKRAMer9!qq-Wvv77": false, // "kramer" in another case
		"ZZ9-qq8-WW7-xx6":   true,
	}
	for pw, ok := range cases {
		err := CheckDemoPassword(pw, m)
		if (err == nil) != ok {
			t.Errorf("CheckDemoPassword(%q) = %v, want ok %t", pw, err, ok)
		}
		if err != nil && pw != "" && strings.Contains(err.Error(), pw) {
			t.Errorf("error %q echoes the password", err)
		}
	}
}

// covers: AC-8. 10,000 generated passwords all hold upper, lower, and digit.
func TestRandomPassword(t *testing.T) {
	seen := map[string]bool{}
	allowed := regexp.MustCompile(`^[A-Za-z0-9_!-]{32}$`)
	for range 10000 {
		pw := RandomPassword()
		if !allowed.MatchString(pw) || !strings.ContainsAny(pw, "ABCDEFGHIJKLMNOPQRSTUVWXYZ") ||
			!strings.ContainsAny(pw, "abcdefghijklmnopqrstuvwxyz") || !strings.ContainsAny(pw, "0123456789") {
			t.Fatalf("RandomPassword() = %q breaks the rule", pw)
		}
		if seen[pw] {
			t.Fatal("RandomPassword repeated a password")
		}
		seen[pw] = true
	}
}

// covers: AC-5, AC-15. Generated numbers are fictional, staff only, and
// follow the bytewise sorted username order; demo numbers win.
func TestExpectedPhones(t *testing.T) {
	m := loadNoord(t)
	phones := ExpectedPhones(m, Demo{Users: []DemoUser{{Username: "eva.peters", MFAPhone: "+31 612345678"}}})
	if len(phones) != len(m.Users) {
		t.Errorf("%d numbers for %d staff", len(phones), len(m.Users))
	}
	if _, ok := phones["adm.ruben.mulder"]; ok {
		t.Error("an admin account got a number")
	}
	names := make([]string, 0, len(m.Users))
	for _, u := range m.Users {
		names = append(names, u.Username)
	}
	slices.Sort(names)
	if phones[names[0]] != "+31 600000001" {
		t.Errorf("first sorted user %s = %s, want +31 600000001", names[0], phones[names[0]])
	}
	generated := regexp.MustCompile(`^\+31 60000\d{4}$`)
	for name, n := range phones {
		if name != "eva.peters" && !generated.MatchString(n) {
			t.Errorf("%s = %s, outside +31 60000NNNN", name, n)
		}
	}
	if phones["eva.peters"] != "+31 612345678" {
		t.Error("the demo number did not win")
	}
}

// covers: AC-15. noord.yaml never gains a phone; the tracked demo example
// holds only fictional numbers and parses against the seed.
func TestEntraHygiene(t *testing.T) {
	raw, err := os.ReadFile(noordSeed)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.ToLower(string(raw)), "phone") {
		t.Error("noord.yaml mentions a phone; MFA numbers come from the generator or the gitignored demo file")
	}
	example, err := os.ReadFile(demoExample)
	if err != nil {
		t.Fatal(err)
	}
	d, err := ParseDemo(example, loadNoord(t))
	if err != nil {
		t.Fatalf("demo.example.yaml: %v", err)
	}
	fictional := regexp.MustCompile(`^\+31 60000\d{4}$`)
	for _, u := range d.Users {
		if !fictional.MatchString(u.MFAPhone) {
			t.Errorf("demo.example.yaml: %s has a number outside +31 60000NNNN", u.Username)
		}
	}
}

// covers: AC-3. Members outside OU=Users are flagged; managers and the
// disabled bit come from AD.
func TestBuildScope(t *testing.T) {
	users := "OU=Users,OU=Noord," + testBase
	eva := "CN=Eva Peters,OU=Sales," + users
	daan := "CN=Daan Bos,OU=Sales," + users
	adm := "CN=adm.ruben.mulder,OU=Admin Accounts,OU=Noord," + testBase
	group := "CN=Some Group,OU=Groups,OU=Noord," + testBase
	entries := []*ldap.Entry{
		ldap.NewEntry(eva, map[string][]string{"sAMAccountName": {"eva.peters"}, "displayName": {"Eva Peters"}, "manager": {daan}, "userAccountControl": {"512"}}),
		ldap.NewEntry(daan, map[string][]string{"sAMAccountName": {"daan.bos"}, "displayName": {"Daan Bos"}, "userAccountControl": {"514"}}),
		ldap.NewEntry(adm, map[string][]string{"sAMAccountName": {"adm.ruben.mulder"}, "userAccountControl": {"66048"}}),
	}
	scope := BuildScope(testBase, []string{eva, daan, adm, group}, entries)

	byName := map[string]ADUser{}
	for _, u := range scope {
		byName[u.Username+"|"+u.DN] = u
	}
	if u := byName["eva.peters|"+eva]; u.Outside || u.Manager != "daan.bos" || u.Disabled {
		t.Errorf("eva.peters = %+v", u)
	}
	if u := byName["daan.bos|"+daan]; !u.Disabled {
		t.Errorf("daan.bos = %+v, want disabled", u)
	}
	if u := byName["adm.ruben.mulder|"+adm]; !u.Outside {
		t.Errorf("adm.ruben.mulder = %+v, want outside", u)
	}
	if u := byName["|"+group]; !u.Outside {
		t.Errorf("the group member = %+v, want outside", u)
	}
}
