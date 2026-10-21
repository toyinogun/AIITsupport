package directory

import (
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf16"
)

// smallSeed is a valid seed file the validation tests break one rule at a time.
const smallSeed = `version: 1
mail_domain: noord.example
departments: [Finance, IT, Management]
users:
  - {username: boss.top, given_name: Boss, surname: Top, department: Management, title: Director, manager: null}
  - {username: ann.fin, given_name: Ann, surname: Fin, department: Finance, title: Clerk, manager: boss.top}
  - {username: ian.it, given_name: Ian, surname: It, department: IT, title: IT, manager: boss.top, synced: false}
  - {username: lee.gone, given_name: Lee, surname: Gone, department: Finance, title: Clerk (temp), manager: ann.fin, description: contract ended, left: 2026-06-30, locked: true}
admins:
  - {username: adm.ian.it, person: ian.it}
groups:
  - {name: SG-Finance, kind: department, owner: ann.fin, members: [ann.fin, lee.gone]}
  - {name: APP-ERP, kind: app, owner: null, members: [SG-Finance]}
  - {name: allstaff, kind: distribution, owner: boss.top, members: [SG-Finance, boss.top]}
  - {name: SG-IT-Admins, kind: privileged, owner: null, members: [adm.ian.it], nested_into: [Domain Admins]}
`

func mustParse(t *testing.T, seed string) Model {
	t.Helper()
	m, err := Parse([]byte(seed), nil)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return m
}

func TestParseValidSeed(t *testing.T) {
	m := mustParse(t, smallSeed)

	lee, ok := m.User("lee.gone")
	if !ok {
		t.Fatal("lee.gone missing")
	}
	if lee.Left == nil || lee.Left.Format("2006-01-02") != "2026-06-30" {
		t.Errorf("lee.gone Left = %v, want 2026-06-30", lee.Left)
	}
	if !lee.Locked || !lee.Synced {
		t.Errorf("lee.gone locked=%t synced=%t, want true and the default true", lee.Locked, lee.Synced)
	}
	if lee.Mail != "lee.gone@noord.example" {
		t.Errorf("lee.gone mail = %q", lee.Mail)
	}

	sync, ok := m.Group(SyncGroup)
	if !ok {
		t.Fatalf("%s was not generated", SyncGroup)
	}
	if strings.Join(sync.Members, ",") != "boss.top,ann.fin,lee.gone" {
		t.Errorf("%s members = %v, want every synced user", SyncGroup, sync.Members)
	}
	if !strings.HasPrefix(m.Marker, "seed:sha256:") || !strings.HasSuffix(m.Marker, ":none") {
		t.Errorf("marker = %q, want seed:sha256:<hex>:none", m.Marker)
	}
}

// covers: AC-14
func TestParseRejects(t *testing.T) {
	cases := []struct {
		name string
		edit func(string) string
		want []string
	}{
		{
			"unknown fields, all of them",
			func(s string) string {
				s = strings.Replace(s, "title: Director,", "title: Director, shoe_size: 44,", 1)
				return strings.Replace(s, "kind: app,", "kind: app, colour: red,", 1)
			},
			[]string{"shoe_size", "colour"},
		},
		{
			"username used twice ignoring case",
			func(s string) string {
				return strings.Replace(s, "username: ian.it,", "username: ann.fin,", 1)
			},
			[]string{`"ann.fin"`},
		},
		{
			"department tag not a department",
			func(s string) string {
				return strings.Replace(s, "kind: department, owner: ann.fin,", "kind: department, departments: [Finance, Sales], owner: ann.fin,", 1)
			},
			[]string{`department "Sales" is not one of the departments`},
		},
		{
			"department tag listed twice",
			func(s string) string {
				return strings.Replace(s, "kind: department, owner: ann.fin,", "kind: department, departments: [Finance, Finance], owner: ann.fin,", 1)
			},
			[]string{`department "Finance" is listed twice`},
		},
		{
			"department tag on a distribution list and a privileged group",
			func(s string) string {
				s = strings.Replace(s, "kind: distribution, owner: boss.top,", "kind: distribution, departments: [Finance], owner: boss.top,", 1)
				return strings.Replace(s, "kind: privileged, owner: null,", "kind: privileged, departments: [IT], owner: null,", 1)
			},
			[]string{"a distribution group may not have departments", "a privileged group may not have departments"},
		},
		{
			"group name clashes with a user",
			func(s string) string { return strings.Replace(s, "name: APP-ERP,", "name: Boss.Top,", 1) },
			[]string{`"Boss.Top"`},
		},
		{
			"admin name clashes with a reserved account",
			func(s string) string {
				s = strings.Replace(s, "username: adm.ian.it,", "username: administrator,", 1)
				return strings.Replace(s, "members: [adm.ian.it]", "members: [administrator]", 1)
			},
			[]string{"administrator", "built in"},
		},
		{
			"username charset",
			func(s string) string {
				s = strings.ReplaceAll(s, "boss.top", "Boss_Top")
				return s
			},
			[]string{`"Boss_Top" must match`},
		},
		{
			"username too long",
			func(s string) string { return strings.ReplaceAll(s, "ann.fin", "ann.finance.department") },
			[]string{"22 characters"},
		},
		{
			"admin username too long",
			func(s string) string { return strings.ReplaceAll(s, "adm.ian.it", "adm.ian.it.is.too.long") },
			[]string{"admin username", "characters"},
		},
		{
			"display name clash in one OU",
			func(s string) string {
				return strings.Replace(s, "given_name: Lee, surname: Gone", "given_name: Ann, surname: Fin", 1)
			},
			[]string{`share the display name "Ann Fin"`},
		},
		{
			"missing manager, owner, and member",
			func(s string) string {
				s = strings.Replace(s, "manager: boss.top, synced", "manager: ghost.one, synced", 1)
				s = strings.Replace(s, "owner: ann.fin", "owner: ghost.two", 1)
				return strings.Replace(s, "members: [SG-Finance, boss.top]", "members: [SG-Finance, ghost.three]", 1)
			},
			[]string{`manager "ghost.one"`, `owner "ghost.two"`, `member "ghost.three"`},
		},
		{
			"manager cycle",
			func(s string) string {
				return strings.Replace(s, "department: Finance, title: Clerk, manager: boss.top", "department: Finance, title: Clerk, manager: lee.gone", 1)
			},
			[]string{"manager cycle: ann.fin -> lee.gone -> ann.fin"},
		},
		{
			"two users without a manager",
			func(s string) string {
				return strings.Replace(s, "title: IT, manager: boss.top", "title: IT, manager: null", 1)
			},
			[]string{"exactly one user may have no manager, found 2"},
		},
		{
			"group inside itself",
			func(s string) string {
				return strings.Replace(s, "members: [SG-Finance]}", "members: [APP-ERP]}", 1)
			},
			[]string{"APP-ERP: contains itself"},
		},
		{
			"group membership cycle",
			func(s string) string {
				return strings.Replace(s, "members: [ann.fin, lee.gone]", "members: [ann.fin, allstaff]", 1)
			},
			[]string{"group membership cycle"},
		},
		{
			"non privileged group nested into a privileged one",
			func(s string) string {
				return strings.Replace(s, "members: [adm.ian.it]", "members: [adm.ian.it, SG-Finance]", 1)
			},
			[]string{"non privileged group SG-Finance"},
		},
		{
			"nested_into on a non privileged group",
			func(s string) string {
				return strings.Replace(s, "members: [SG-Finance]}", "members: [SG-Finance], nested_into: [Domain Admins]}", 1)
			},
			[]string{"only privileged groups may use nested_into"},
		},
		{
			"unknown department",
			func(s string) string { return strings.Replace(s, "department: IT,", "department: Marketing,", 1) },
			[]string{`unknown department "Marketing"`},
		},
		{
			"unknown kind",
			func(s string) string { return strings.Replace(s, "kind: app", "kind: misc", 1) },
			[]string{`unknown kind "misc"`},
		},
		{
			"sync group name taken",
			func(s string) string { return strings.Replace(s, "name: APP-ERP,", "name: SYNC-Entra-Users,", 1) },
			[]string{`"SYNC-Entra-Users"`},
		},
		{
			"bad leave date",
			func(s string) string { return strings.Replace(s, "left: 2026-06-30", "left: last summer", 1) },
			[]string{`left "last summer"`},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Parse([]byte(c.edit(smallSeed)), nil)
			if err == nil {
				t.Fatal("Parse accepted a broken seed")
			}
			for _, w := range c.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("error does not mention %q:\n%v", w, err)
				}
			}
		})
	}
}

// covers: AC-14. Every problem comes back in one error, not just the first.
func TestParseReportsEveryProblemAtOnce(t *testing.T) {
	broken := strings.Replace(smallSeed, "department: IT,", "department: Marketing,", 1)
	broken = strings.Replace(broken, "kind: app", "kind: misc", 1)
	broken = strings.Replace(broken, "owner: ann.fin", "owner: ghost.two", 1)

	_, err := Parse([]byte(broken), nil)
	if err == nil {
		t.Fatal("Parse accepted a broken seed")
	}
	for _, w := range []string{"Marketing", "misc", "ghost.two"} {
		if !strings.Contains(err.Error(), w) {
			t.Errorf("error does not mention %q:\n%v", w, err)
		}
	}
}

// covers: AC-15
func TestOverlay(t *testing.T) {
	t.Run("replaces mail and changes the marker", func(t *testing.T) {
		plain := mustParse(t, smallSeed)
		m, err := Parse([]byte(smallSeed), []byte("users:\n  - {username: ann.fin, mail: ann@slack-test.example}\n"))
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}
		ann, _ := m.User("ann.fin")
		if ann.Mail != "ann@slack-test.example" {
			t.Errorf("ann.fin mail = %q, want the overlay value", ann.Mail)
		}
		boss, _ := m.User("boss.top")
		if boss.Mail != "boss.top@noord.example" {
			t.Errorf("boss.top mail = %q, want the default", boss.Mail)
		}
		if m.Marker == plain.Marker || strings.HasSuffix(m.Marker, ":none") {
			t.Errorf("marker %q does not reflect the overlay", m.Marker)
		}
	})

	t.Run("unknown user", func(t *testing.T) {
		_, err := Parse([]byte(smallSeed), []byte("users:\n  - {username: nobody.here, mail: x@y.example}\n"))
		if err == nil || !strings.Contains(err.Error(), `overlay: unknown user "nobody.here"`) {
			t.Errorf("err = %v, want an unknown overlay user", err)
		}
	})

	t.Run("unknown field", func(t *testing.T) {
		_, err := Parse([]byte(smallSeed), []byte("users:\n  - {username: ann.fin, mail: a@b.example, phone: 1}\n"))
		if err == nil || !strings.Contains(err.Error(), "phone") {
			t.Errorf("err = %v, want the unknown field named", err)
		}
	})

	t.Run("missing file is fine", func(t *testing.T) {
		dir := t.TempDir()
		seedPath := filepath.Join(dir, "seed.yaml")
		if err := os.WriteFile(seedPath, []byte(smallSeed), 0o600); err != nil {
			t.Fatal(err)
		}
		m, err := Load(seedPath, filepath.Join(dir, "overlay.local.yaml"))
		if err != nil {
			t.Fatalf("Load without an overlay: %v", err)
		}
		if !strings.HasSuffix(m.Marker, ":none") {
			t.Errorf("marker = %q, want :none with no overlay", m.Marker)
		}
	})

	t.Run("example overlay matches the real seed", func(t *testing.T) {
		if _, err := Load("../../../seed/directory/noord.yaml", "../../../seed/directory/overlay.example.yaml"); err != nil {
			t.Errorf("overlay.example.yaml does not load against noord.yaml: %v", err)
		}
	})
}

// covers: AC-14. Values that go into a DN are escaped.
func TestDNsAreEscaped(t *testing.T) {
	seed := strings.Replace(smallSeed, "departments: [Finance, IT, Management]", "departments: [Finance, IT, Management, \"R+D, Lab\"]", 1)
	seed = strings.Replace(seed, "given_name: Ian, surname: It, department: IT", `given_name: Ian, surname: "O'Neil, Jr", department: "R+D, Lab"`, 1)
	m := mustParse(t, seed)

	dn, ok := m.UserDN("DC=ad,DC=noord,DC=example", "ian.it")
	if !ok {
		t.Fatal("ian.it has no DN")
	}
	want := `CN=Ian O'Neil\, Jr,OU=R\+D\, Lab,OU=Users,OU=Noord,DC=ad,DC=noord,DC=example`
	if dn != want {
		t.Errorf("UserDN = %s\nwant      %s", dn, want)
	}
	if dn, _ := m.GroupDN("DC=x", "SG-IT-Admins"); dn != "CN=SG-IT-Admins,OU=Admin Accounts,OU=Noord,DC=x" {
		t.Errorf("privileged group DN = %s, want it under OU=Admin Accounts", dn)
	}
	if dn, _ := m.UserDN("DC=x", "adm.ian.it"); dn != "CN=adm.ian.it,OU=Admin Accounts,OU=Noord,DC=x" {
		t.Errorf("admin DN = %s, want it under OU=Admin Accounts", dn)
	}
}

func TestEncodePassword(t *testing.T) {
	got := encodePassword("Ab1")
	units := utf16.Encode([]rune(`"Ab1"`))
	if len(got) != 2*len(units) {
		t.Fatalf("length = %d, want %d", len(got), 2*len(units))
	}
	for i, u := range units {
		if binary.LittleEndian.Uint16([]byte(got[2*i:2*i+2])) != u {
			t.Fatalf("byte pair %d is not UTF-16LE of %q", i, rune(u))
		}
	}
}

func TestLoadNamesTheFile(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "missing.yaml"), "")
	if err == nil || !errors.Is(err, os.ErrNotExist) {
		t.Errorf("err = %v, want a not exist error", err)
	}
}

// covers: spec 0013 AC-3. A group's department tags load from the seed and
// become the info value the tool layer parses back.
func TestDepartmentTags(t *testing.T) {
	seed := strings.Replace(smallSeed, "kind: app, owner: null,", "kind: app, departments: [Finance, Management], owner: null,", 1)
	m := mustParse(t, seed)
	g, _ := m.Group("APP-ERP")
	if got := DepartmentsInfo(g.Departments); got != "noord-departments: Finance, Management" {
		t.Errorf("info = %q", got)
	}
	if untagged, _ := m.Group("SG-Finance"); len(untagged.Departments) != 0 {
		t.Errorf("SG-Finance departments = %v", untagged.Departments)
	}
}
