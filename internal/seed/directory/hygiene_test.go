package directory

import (
	"os"
	"strings"
	"testing"
)

const noordSeed = "../../../seed/directory/noord.yaml"

// covers: AC-16. The public repo holds no real data and no passwords.
func TestNoordSeedHygiene(t *testing.T) {
	raw, err := os.ReadFile(noordSeed)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.ToLower(string(raw)), "password") {
		t.Error("noord.yaml mentions a password; passwords come from .env only")
	}

	m, err := Load(noordSeed, "")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if m.MailDomain != "noord.example" {
		t.Errorf("mail_domain = %q, want noord.example", m.MailDomain)
	}
	for _, u := range m.Users {
		if !strings.HasSuffix(u.Mail, "@noord.example") {
			t.Errorf("user %s mail %q does not end in @noord.example", u.Username, u.Mail)
		}
	}
}

// covers: AC-3 to AC-7, AC-10. The seed file holds the shape the spec promises.
func TestNoordSeedShape(t *testing.T) {
	m, err := Load(noordSeed, "")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	var current, leavers, locked, unsynced int
	for _, u := range m.Users {
		if u.Left != nil {
			leavers++
		} else {
			current++
		}
		if u.Locked {
			locked++
		}
		if !u.Synced {
			unsynced++
		}
	}
	if current != 80 || leavers != 5 || locked != 3 {
		t.Errorf("current=%d leavers=%d locked=%d, want 80, 5, 3", current, leavers, locked)
	}
	if unsynced < 4 || unsynced > 8 {
		t.Errorf("unsynced = %d, want about 6", unsynced)
	}

	for _, pair := range [][2]string{{"jan.devries", "jan.devries2"}, {"sanne.jansen", "sanne.janssen"}} {
		a, okA := m.User(pair[0])
		b, okB := m.User(pair[1])
		if !okA || !okB {
			t.Errorf("look alike pair %v is missing", pair)
		}
		if pair[0] == "jan.devries" && (a.DisplayName() != b.DisplayName() || a.Department == b.Department || a.Manager == b.Manager || a.Title == b.Title) {
			t.Errorf("the two Jan de Vries must share a name but differ in department, manager, and title")
		}
	}

	var unowned int
	for _, name := range []string{"SG-Finance", "Finance_Users", "fin-readonly"} {
		if _, ok := m.Group(name); !ok {
			t.Errorf("group %s is missing", name)
		}
	}
	for _, g := range m.Groups {
		if g.Owner == "" {
			unowned++
		}
	}
	if unowned < 4 {
		t.Errorf("%d unowned groups, want at least 4", unowned)
	}
	if len(m.Groups) < 18 || len(m.Groups) > 24 {
		t.Errorf("%d groups, want about 20", len(m.Groups))
	}

	admins, ok := m.Group("SG-IT-Admins")
	if !ok || admins.Kind != KindPrivileged || len(admins.NestedInto) != 1 || admins.NestedInto[0] != "Domain Admins" {
		t.Errorf("SG-IT-Admins = %+v, want privileged and nested into Domain Admins", admins)
	}
}
