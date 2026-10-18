package helpdesk

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/toyinogun/AIITsupport/internal/seed/directory"
)

const (
	repoRoot    = "../../.."
	helpdeskDir = repoRoot + "/seed/helpdesk"
	noordSeed   = repoRoot + "/seed/directory/noord.yaml"
)

func loadNoord(t *testing.T) (Content, directory.Model) {
	t.Helper()
	m, err := directory.Load(noordSeed, "")
	if err != nil {
		t.Fatalf("directory.Load: %v", err)
	}
	c, err := Load(helpdeskDir, m)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return c, m
}

// covers: AC-7, AC-10. The real KB has 6 categories, 15 answers, and at least
// the planted set the spec fixes.
func TestNoordKBShape(t *testing.T) {
	c, _ := loadNoord(t)
	if len(c.Categories) != 6 || len(c.Articles) != 15 {
		t.Fatalf("%d categories, %d articles, want 6 and 15", len(c.Categories), len(c.Articles))
	}
	var contradicting, retired, dead, duplicates int
	for _, a := range c.Articles {
		contradicting += len(a.Contradicts)
		if a.RetiredSystem != "" {
			retired++
		}
		if a.DeadContact != "" {
			dead++
		}
		if a.DuplicateOf != "" {
			duplicates++
		}
	}
	if contradicting < 2 || retired < 2 || dead < 2 || duplicates < 1 {
		t.Errorf("planted: %d contradicting, %d retired, %d dead contacts, %d duplicates; want at least 2, 2, 2, 1",
			contradicting, retired, dead, duplicates)
	}

	facts := map[string][]string{
		"password-policy-2019":  {"8 characters", "90 days"},
		"password-policy":       {"14 characters", "no forced change"},
		"account-lockout-old":   {"30 minutes"},
		"account-lockout":       {"15 minutes"},
		"mfa-sms-registration":  {"text message"},
		"new-starter-checklist": {"Mark Dekker"},
		"shared-drive-access":   {"Thomas Meijer"},
	}
	for slug, want := range facts {
		a, ok := c.Article(slug)
		if !ok {
			t.Errorf("article %s is missing", slug)
			continue
		}
		for _, w := range want {
			if !strings.Contains(a.Body, w) {
				t.Errorf("article %s does not say %q", slug, w)
			}
		}
	}
	for slug, dead := range map[string]string{"new-starter-checklist": "mark.dekker", "shared-drive-access": "thomas.meijer"} {
		if a, _ := c.Article(slug); a.DeadContact != dead {
			t.Errorf("%s dead_contact = %q, want %q", slug, a.DeadContact, dead)
		}
	}
}

// covers: AC-8. 20 tickets, mostly lockouts and resets, many on a Monday
// morning, with 2 open, 1 pending, and 17 closed.
func TestNoordTicketsShape(t *testing.T) {
	c, _ := loadNoord(t)
	if len(c.Tickets) != 20 {
		t.Fatalf("%d tickets, want 20", len(c.Tickets))
	}
	states := map[string]int{}
	var lockouts, mondays int
	for _, tk := range c.Tickets {
		states[tk.State]++
		title := strings.ToLower(tk.Title)
		if strings.Contains(title, "lock") || strings.Contains(title, "password") || strings.Contains(title, "log in") {
			lockouts++
		}
		local := tk.Created.In(Amsterdam)
		if local.Weekday() == time.Monday && local.Hour() >= 7 && local.Hour() < 10 {
			mondays++
		}
	}
	if lockouts < 14 {
		t.Errorf("%d lockout or password tickets, want at least 14", lockouts)
	}
	if mondays < 8 {
		t.Errorf("%d tickets on a Monday 07:00 to 10:00, want at least 8", mondays)
	}
	if states[StateOpen] != 2 || states[StatePending] != 1 || states[StateClosed] != 17 {
		t.Errorf("states = %v, want 2 open, 1 pending, 17 closed", states)
	}
}

func TestParseLocal(t *testing.T) {
	got, err := parseLocal("2026-09-14 08:12")
	if err != nil || got.UTC().Format(time.RFC3339) != "2026-09-14T06:12:00Z" {
		t.Errorf("summer time: %v, %v", got.UTC(), err)
	}
	got, err = parseLocal("2026-01-05")
	if err != nil || got.UTC().Format(time.RFC3339) != "2026-01-05T08:00:00Z" {
		t.Errorf("bare winter date: %v, %v", got.UTC(), err)
	}
	if _, err := parseLocal("2026-03-29 02:30"); err == nil || !strings.Contains(err.Error(), "daylight saving") {
		t.Errorf("DST gap: err = %v", err)
	}
	if _, err := parseLocal("14/09/2026"); err == nil {
		t.Error("accepted a day first date")
	}
}

// writeTree copies the real helpdesk files into a temp dir and applies
// edits (file name to new content; empty content removes the file).
func writeTree(t *testing.T, edits map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	err := filepath.WalkDir(helpdeskDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(helpdeskDir, path)
		if d.IsDir() {
			return os.MkdirAll(filepath.Join(dir, rel), 0o750)
		}
		data, err := os.ReadFile(path) //nolint:gosec // G304: test fixture
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dir, rel), data, 0o600) //nolint:gosec // G703: test copies the repo fixture into a temp dir
	})
	if err != nil {
		t.Fatal(err)
	}
	for name, content := range edits {
		p := filepath.Join(dir, name)
		if content == "" {
			if err := os.Remove(p); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func replaceIn(t *testing.T, file, old, replacement string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(helpdeskDir, file)) //nolint:gosec // G304: test fixture
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), old) {
		t.Fatalf("%s does not contain %q", file, old)
	}
	return strings.Replace(string(data), old, replacement, 1)
}

// covers: AC-10. Every rule fails on a broken file, and all problems come
// back together.
func TestValidateReportsEveryProblem(t *testing.T) {
	m, err := directory.Load(noordSeed, "")
	if err != nil {
		t.Fatal(err)
	}
	edits := map[string]string{
		"kb.yaml":                            replaceIn(t, "kb.yaml", "as_of: 2026-10-16", "as_of: 2026-10-15"),
		"kb/password-policy.md":              replaceIn(t, "kb/password-policy.md", "status: current", "status: outdated"),
		"kb/forgot-password-self-service.md": replaceIn(t, "kb/forgot-password-self-service.md", "created: 2025-06-02", "created: 2022-01-03"),
		"kb/shared-drive-access.md":          replaceIn(t, "kb/shared-drive-access.md", "dead_contact: thomas.meijer", "dead_contact: ruben.mulder"),
		"kb/printer-warehouse.md":            replaceIn(t, "kb/printer-warehouse.md", "updated: 2025-10-13", "updated: 2022-01-01"),
		"tickets.yaml": strings.NewReplacer(
			"customer: eva.peters", "customer: john.smith",
			"closed: 2026-08-10 07:55", "closed: 2026-08-10 07:00",
			"created: 2026-08-11 13:20", "created: 2026-03-29 02:30",
			"state: pending", "state: waiting",
		).Replace(replaceIn(t, "tickets.yaml", "from: ruben.mulder, at: 2026-10-14 11:05", "from: nobody.here, at: 2026-10-14 11:05")),
	}
	_, err = Load(writeTree(t, edits), m)
	if err == nil {
		t.Fatal("Load accepted broken files")
	}
	for _, want := range []string{
		"as_of",
		`contradicts "password-policy", which must be current`,
		`duplicate_of "reset-your-password" must be older`,
		`dead_contact "ruben.mulder" has not left`,
		"printer-warehouse: updated is before created",
		`customer "john.smith" is not a user`,
		"hd-002: closed is before the last article",
		"daylight saving",
		`state "waiting"`,
		`from "nobody.here", who is not a user`,
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error lacks %q\n%v", want, err)
		}
	}
}

func TestValidateCategoryRules(t *testing.T) {
	m, err := directory.Load(noordSeed, "")
	if err != nil {
		t.Fatal(err)
	}
	edits := map[string]string{
		"kb.yaml":                     replaceIn(t, "kb.yaml", "{name: Printers and hardware, icon: f02f}", "{name: Printers and hardware, icon: printer}\n  - {name: Empty, icon: f000}"),
		"kb/new-starter-checklist.md": replaceIn(t, "kb/new-starter-checklist.md", "category: Starters and leavers", "category: Onboarding"),
	}
	_, err = Load(writeTree(t, edits), m)
	if err == nil {
		t.Fatal("Load accepted broken categories")
	}
	for _, want := range []string{`icon "printer"`, `category "Empty" has no articles`, `category "Onboarding" is not in kb.yaml`, `"Starters and leavers" has no articles`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error lacks %q\n%v", want, err)
		}
	}
}

// covers: AC-10. The KB must carry the title bootstrap.rb gives it, and no
// one in noord.yaml may leave after the fiction's today.
func TestValidateKBNameAndLeavers(t *testing.T) {
	m, err := directory.Load(noordSeed, "")
	if err != nil {
		t.Fatal(err)
	}
	late := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)
	users := slices.Clone(m.Users)
	found := false
	for i := range users {
		if users[i].Username == "thomas.meijer" {
			users[i].Left, found = &late, true
		}
	}
	if !found {
		t.Fatal("thomas.meijer is not in noord.yaml")
	}
	future := m
	future.Users = users

	edits := map[string]string{"kb.yaml": replaceIn(t, "kb.yaml", "name: Noord IT Help", "name: IT Wiki")}
	_, err = Load(writeTree(t, edits), future)
	if err == nil {
		t.Fatal("Load accepted a renamed KB and a future leaver")
	}
	for _, want := range []string{`name "IT Wiki" must be "Noord IT Help"`, "user thomas.meijer left on 2026-11-02, after as_of 2026-10-16"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error lacks %q\n%v", want, err)
		}
	}
}
