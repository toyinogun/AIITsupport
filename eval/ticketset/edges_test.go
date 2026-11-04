package ticketset

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// covers: AC-3, AC-5, AC-7, AC-8 (spec 0017): one bad ticket per rule.
func TestRuleViolations(t *testing.T) {
	cases := []struct {
		name, from, to, want string
	}{
		{"lookalike on a plain user", "category: routine\n", "category: routine\n    tags: [lookalike]\n", "tag lookalike needs a sender in a look alike pair"},
		{"gate tag on staff", "category: routine\n", "category: routine\n    tags: [gate]\n", "tag gate is only for guest"},
		{"duplicate tag", "category: routine\n", "category: routine\n    tags: [dutch, dutch]\n", "listed twice"},
		{"bad category", "category: routine", "category: other", "category \"other\" is not allowed"},
		{"bad sender kind", "kind: staff", "kind: robot", "sender kind \"robot\" is not allowed"},
		{"guest names a user", "{kind: staff, user: kevin.smits}", "{kind: guest, user: kevin.smits}", "names no user"},
		{"bad outcome", "outcome: resolve", "outcome: win", "expected.outcome \"win\""},
		{"bad approval", "approval: none", "approval: maybe", "approval \"maybe\""},
		{"deny on resolve", "approval: none\n    expected: {outcome: resolve, tools: [ticket_create, mfa_clear", "approval: deny\n    expected: {outcome: resolve, tools: [ticket_create, mfa_clear", "approval deny goes with outcome escalate"},
		{"unknown forbidden tool", "tools: [ticket_create, account_unlock, ticket_update]}", "tools: [ticket_create], forbidden_tools: [nope]}", "forbidden_tools names unknown tool"},
		{"bad final state", "why:", "expect: {final_ticket_state: maybe}\n    why:", "final_ticket_state \"maybe\""},
		{"bad refusal code", "why:", "expect: {refusal_code: nope}\n    why:", "refusal_code \"nope\" is not in the refusal table"},
		{"too many must not", "why:", "expect: {reply_must_not: [a, b, c, d, e, f]}\n    why:", "reply_must_not holds 6"},
		{"empty must not string", "why:", "expect: {reply_must_not: [\"\"]}\n    why:", "reply_must_not string has 0 runes"},
		{"refuse with wrong tool", "outcome: resolve, tools: [ticket_create, account_unlock, ticket_update]", "outcome: refuse, tools: [ticket_create]", "a refuse ticket needs"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			doc := strings.Replace(goodTicket, c.from, c.to, 1)
			if c.name == "deny on resolve" {
				doc = strings.Replace(goodTicket, "tools: [ticket_create, account_unlock, ticket_update]", "tools: [ticket_create, mfa_clear]", 1)
				doc = strings.Replace(doc, "approval: none", "approval: deny", 1)
			}
			if got := problemsOf(t, doc); !strings.Contains(got, c.want) {
				t.Errorf("want %q in:\n%s", c.want, got)
			}
		})
	}
}

// covers: AC-7 (spec 0017, amended by 0018): an open ticket is accepted, but
// open does not go with an approval click or a gate sender.
func TestOpenOutcome(t *testing.T) {
	open := strings.Replace(goodTicket, "outcome: resolve, tools: [ticket_create, account_unlock, ticket_update]", "outcome: open, tools: [ticket_create]", 1)
	if got := problemsOf(t, strings.Replace(open, "approval: none", "approval: maybe", 1)); strings.Contains(got, "expected.outcome") {
		t.Errorf("open was rejected as an outcome:\n%s", got)
	}
	approve := strings.Replace(open, "tools: [ticket_create]", "tools: [ticket_create, mfa_clear]", 1)
	approve = strings.Replace(approve, "approval: none", "approval: approve", 1)
	if got := problemsOf(t, approve); !strings.Contains(got, "approval approve goes with outcome resolve") {
		t.Errorf("open with approve not caught:\n%s", got)
	}
	guest := strings.Replace(open, "{kind: staff, user: kevin.smits}", "{kind: guest}", 1)
	if got := problemsOf(t, guest); !strings.Contains(got, "gate ticket expects") {
		t.Errorf("open on a gate sender not caught:\n%s", got)
	}
}

// covers: AC-7 (spec 0017): a refuse ticket whose code and tool agree is accepted.
func TestRefuseWithMatchingToolHasNoRefuseProblem(t *testing.T) {
	doc := strings.Replace(goodTicket, "outcome: resolve, tools: [ticket_create, account_unlock, ticket_update]",
		"outcome: refuse, tools: [privileged_access_change]}\n    expect: {refusal_code: blocked", 1)
	if got := problemsOf(t, doc); strings.Contains(got, "refuse ticket needs") || strings.Contains(got, "no tool in expected.tools") {
		t.Errorf("valid refuse ticket was rejected:\n%s", got)
	}
}

// covers: AC-9 (spec 0017): read problems are reported, not swallowed.
func TestLoadReadErrors(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "missing.yaml")
	for name, args := range map[string][3]string{
		"tickets":   {missing, realDirectory, realKB},
		"directory": {realTickets, missing, realKB},
		"kb":        {realTickets, realDirectory, missing},
	} {
		if _, err := Load(args[0], args[1], args[2]); err == nil {
			t.Errorf("%s: a missing file loaded without an error", name)
		}
	}
	empty := filepath.Join(dir, "empty.yaml")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(empty, realDirectory, realKB); err == nil || !strings.Contains(err.Error(), "empty") {
		t.Errorf("empty file: %v", err)
	}
}

// covers: AC-10 (spec 0017): a broken lock file fails instead of passing.
func TestLockFileErrors(t *testing.T) {
	dir := t.TempDir()
	tickets := filepath.Join(dir, "t.yaml")
	lock := filepath.Join(dir, "t.lock")
	if err := os.WriteFile(tickets, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"unknown key":   "version: 1\nsha256: aa\nextra: 1\n",
		"wrong version": "version: 2\nsha256: aa\n",
	} {
		if err := os.WriteFile(lock, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if frozen, err := CheckLock(tickets, lock); !frozen || err == nil {
			t.Errorf("%s: frozen=%v err=%v", name, frozen, err)
		}
	}
}

// covers: AC-11 (spec 0017): host extraction.
func TestHostOfAndFictionalHost(t *testing.T) {
	cases := map[string]bool{
		"https://help.noord.example/kb": true,
		"http://noord.example:8080/x":   true,
		"www.noord.example.":            true,
		"https://noord.example.evil.io": false,
		"www.example.org/path":          false,
		"https://user@evil.io/":         false,
	}
	for in, want := range cases {
		if got := fictionalHost(hostOf(in)); got != want {
			t.Errorf("%q: fictional=%v, want %v (host %q)", in, got, want, hostOf(in))
		}
	}
}

// covers: AC-15 (spec 0018)
func TestWriteLock(t *testing.T) {
	dir := t.TempDir()
	tickets := filepath.Join(dir, "tickets_v2.yaml")
	if got := LockPath(tickets); got != filepath.Join(dir, "tickets_v2.lock") {
		t.Fatalf("LockPath = %s", got)
	}
	lock := LockPath(tickets)
	if err := WriteLock(lock, "abc"); err != nil {
		t.Fatal(err)
	}
	if err := WriteLock(lock, "abc"); err != nil {
		t.Fatalf("the same hash again should be a no op: %v", err)
	}
	if err := WriteLock(lock, "other"); err == nil {
		t.Fatal("a different hash overwrote the lock")
	}
}
