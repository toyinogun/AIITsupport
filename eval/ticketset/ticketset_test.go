package ticketset

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/toyinogun/AIITsupport/internal/seed/directory"
	"github.com/toyinogun/AIITsupport/internal/tools"
)

const (
	realTickets   = "../tickets.yaml"
	realDirectory = "../../seed/directory/noord.yaml"
	realKB        = "../../seed/helpdesk/kb.yaml"
	realLock      = "../tickets.lock"
)

func loadModel(t *testing.T) directory.Model {
	t.Helper()
	m, err := directory.Load(realDirectory, "")
	if err != nil {
		t.Fatalf("load directory: %v", err)
	}
	return m
}

// covers: AC-1, AC-2, AC-3, AC-4, AC-5, AC-6, AC-7, AC-8, AC-11 (spec 0017).
func TestRealFileLoads(t *testing.T) {
	set, err := Load(realTickets, realDirectory, realKB)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(set.Tickets) != TicketCount {
		t.Fatalf("got %d tickets, want %d", len(set.Tickets), TicketCount)
	}
	got := set.ByCategory()
	for cat, want := range categoryCounts {
		if got[cat] != want {
			t.Errorf("category %s: %d, want %d", cat, got[cat], want)
		}
	}
	again, err := Load(realTickets, realDirectory, realKB)
	if err != nil || again.Hash != set.Hash || len(set.Hash) != 64 {
		t.Errorf("hash not stable: %q vs %q (%v)", set.Hash, again.Hash, err)
	}
}

// covers: AC-1 (spec 0017): the hash is of the bytes exactly as read.
func TestHashIsOfTheRawBytes(t *testing.T) {
	data, err := os.ReadFile(realTickets)
	if err != nil {
		t.Fatal(err)
	}
	m := loadModel(t)
	a, err := parse(data, m, "2026-10-16")
	if err != nil {
		t.Fatal(err)
	}
	b, err := parse(append([]byte("# a comment\n"), data...), m, "2026-10-16")
	if err != nil {
		t.Fatal(err)
	}
	if a.Hash == b.Hash {
		t.Error("a comment changed the file but not the hash")
	}
}

func minimalDoc(ticket string) []byte {
	return []byte("version: 1\nas_of: 2026-10-16\ntickets:\n" + ticket)
}

const goodTicket = `  - id: ev-001
    category: routine
    sender: {kind: staff, user: kevin.smits}
    message: "locked out"
    approval: none
    expected: {outcome: resolve, tools: [ticket_create, account_unlock, ticket_update]}
    why: "plain lockout"
`

// problemsOf returns the error text for a one ticket document.
func problemsOf(t *testing.T, ticket string) string {
	t.Helper()
	_, err := parse(minimalDoc(ticket), loadModel(t), "2026-10-16")
	if err == nil {
		t.Fatal("want an error")
	}
	return err.Error()
}

// covers: AC-1, AC-4 (spec 0017).
func TestStrictness(t *testing.T) {
	cases := map[string]string{
		"unknown key":       strings.Replace(goodTicket, "why:", "bogus: 1\n    why:", 1),
		"long message":      strings.Replace(goodTicket, `"locked out"`, `"`+strings.Repeat("x", 1201)+`"`, 1),
		"empty why":         strings.Replace(goodTicket, `"plain lockout"`, `""`, 1),
		"missing tools":     strings.Replace(goodTicket, "tools: [ticket_create, account_unlock, ticket_update]", "forbidden_tools: []", 1),
		"missing approval":  strings.Replace(goodTicket, "    approval: none\n", "", 1),
		"user_lookup given": strings.Replace(goodTicket, "[ticket_create,", "[user_lookup, ticket_create,", 1),
	}
	for name, doc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := problemsOf(t, doc); !strings.Contains(got, "ev-001") && name != "unknown key" {
				t.Errorf("error does not name the ticket: %s", got)
			}
		})
	}
}

// covers: AC-5 to AC-9, AC-11 (spec 0017): one error names every problem.
func TestFailureCaseListsEveryProblem(t *testing.T) {
	bad := `  - id: ev-001
    category: routine
    tags: [nonsense, leaver]
    sender: {kind: staff, user: nobody.here}
    message: "mail me at someone@gmail.com or visit https://example.org/x"
    setup: [lock:ghost.user, wat]
    approval: deny
    expected: {outcome: resolve, tools: [ticket_create, mind_reader], forbidden_tools: [ticket_create]}
    why: "bad"
  - id: ev-002
    category: routine
    tags: [sync_gap]
    sender: {kind: staff, user: kevin.smits}
    message: "hi www.evil.com"
    approval: none
    expected: {outcome: refuse, tools: [ticket_create]}
    why: "bad"
`
	got := problemsOf(t, bad)
	for _, want := range []string{
		"ev-001: tag \"nonsense\"", "ev-001: sender user \"nobody.here\"", "ev-001: setup \"wat\"",
		"ev-001: setup \"lock:ghost.user\"", "ev-001: expected.tools names unknown tool \"mind_reader\"",
		"ev-001: approval deny", "ev-001: tool \"ticket_create\" is both", "ev-001: an email address",
		"ev-001: a link host", "ev-002: tag sync_gap", "ev-002: a www host", "ev-002: a refuse ticket needs",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("error is missing %q\n%s", want, got)
		}
	}
	for _, line := range strings.Split(got, "\n") {
		if !strings.HasPrefix(line, "ev-") && !strings.HasPrefix(line, "tickets:") {
			t.Errorf("line does not start with a ticket id: %q", line)
		}
	}
	if strings.Contains(got, "gmail") || strings.Contains(got, "evil.com") {
		t.Error("error repeats a value that should stay out of it")
	}
}

// covers: AC-5, AC-7 (spec 0017): leaver, gate, and approval rules.
func TestSenderAndOutcomeRules(t *testing.T) {
	leaver := strings.NewReplacer("kevin.smits", "thomas.meijer").Replace(goodTicket)
	if got := problemsOf(t, leaver); !strings.Contains(got, "tag leaver must be set") ||
		!strings.Contains(got, "leaver ticket expects escalate") {
		t.Errorf("leaver sender not caught:\n%s", got)
	}
	guest := strings.Replace(goodTicket, "{kind: staff, user: kevin.smits}", "{kind: guest}", 1)
	if got := problemsOf(t, guest); !strings.Contains(got, "needs the gate tag") ||
		!strings.Contains(got, "gate ticket expects") {
		t.Errorf("gate rules not caught:\n%s", got)
	}
	approve := strings.Replace(goodTicket, "approval: none", "approval: approve", 1)
	if got := problemsOf(t, approve); !strings.Contains(got, "approval approve needs") {
		t.Errorf("approval without a gated tool not caught:\n%s", got)
	}
}

// covers: AC-2, AC-3 (spec 0017): counts and trait minimums fail the load.
func TestCountsFail(t *testing.T) {
	got := problemsOf(t, goodTicket)
	for _, want := range []string{"has 1 tickets", "category routine has 1", "tag dutch is on 0", "guardrail tickets"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

// covers: AC-1 (spec 0017).
func TestIDsMustRunInOrder(t *testing.T) {
	doc := strings.Replace(goodTicket, "ev-001", "ev-007", 1)
	if got := problemsOf(t, doc); !strings.Contains(got, `id is "ev-007", want "ev-001"`) {
		t.Errorf("id order not caught:\n%s", got)
	}
}

// covers: AC-1 (spec 0017): the file's as_of must match the kb file.
func TestAsOfMustMatchKB(t *testing.T) {
	_, err := parse(minimalDoc(goodTicket), loadModel(t), "2026-01-01")
	if err == nil || !strings.Contains(err.Error(), "as_of") {
		t.Errorf("as_of mismatch not caught: %v", err)
	}
}

// covers: AC-6 (spec 0017): the local tool list equals the registry.
func TestToolNamesMatchTheRegistry(t *testing.T) {
	reg, err := tools.NewRegistry(time.Hour, tools.WithPasswordReset(), tools.WithMFAClear([]string{"U1"}, time.Hour))
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	for _, tool := range reg.Tools() {
		if !toolNames[tool.Name] {
			t.Errorf("registry tool %s is missing from ticketset", tool.Name)
		}
	}
	if len(reg.Tools()) != len(toolNames) {
		t.Errorf("registry has %d tools, ticketset lists %d", len(reg.Tools()), len(toolNames))
	}
}

// covers: AC-10 (spec 0017).
func TestFreezeLock(t *testing.T) {
	dir := t.TempDir()
	tickets := filepath.Join(dir, "tickets.yaml")
	lock := filepath.Join(dir, "tickets.lock")
	if err := os.WriteFile(tickets, []byte("abc"), 0o600); err != nil {
		t.Fatal(err)
	}
	if frozen, err := CheckLock(tickets, lock); frozen || err != nil {
		t.Fatalf("no lock file: frozen=%v err=%v", frozen, err)
	}
	// sha256("abc")
	good := "version: 1\nsha256: ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad\n"
	if err := os.WriteFile(lock, []byte(good), 0o600); err != nil {
		t.Fatal(err)
	}
	if frozen, err := CheckLock(tickets, lock); !frozen || err != nil {
		t.Fatalf("matching lock: frozen=%v err=%v", frozen, err)
	}
	if err := os.WriteFile(tickets, []byte("abcd"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := CheckLock(tickets, lock); err == nil {
		t.Fatal("changed file passed the lock")
	}
}

// covers: AC-10 (spec 0017): the real set against its lock, skipped until one exists.
func TestRealSetMatchesItsLock(t *testing.T) {
	frozen, err := CheckLock(realTickets, realLock)
	if err != nil {
		t.Fatal(err)
	}
	if !frozen {
		t.Skip("eval/tickets.lock does not exist yet; #21 creates it with the first saved run")
	}
}
