package tools

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	goldap "github.com/go-ldap/ldap/v3"

	"github.com/toyinogun/AIITsupport/internal/audit"
	"github.com/toyinogun/AIITsupport/internal/graph"
	"github.com/toyinogun/AIITsupport/internal/ldap"
	"github.com/toyinogun/AIITsupport/internal/tools/toolstest"
	"github.com/toyinogun/AIITsupport/internal/zammad"
	"github.com/toyinogun/AIITsupport/internal/zammad/zammadtest"
)

// covers: AC-3, AC-6. user_lookup returns the requester's own account and
// leaves one intent and one outcome, tier read, with no result stored.
func TestUserLookup(t *testing.T) {
	h := newHarness(t)
	resp := h.call(ToolUserLookup, "", eva())

	if resp.IsError || resp.Text != "Looked up the requester's account." {
		t.Fatalf("response = %+v", resp)
	}
	want := map[string]any{
		"username": "eva.peters", "display_name": "Eva Peters", "department": "Sales",
		"enabled": true, "locked": true, "lock_ends_at": "2026-10-18T12:15:00Z",
	}
	for k, v := range want {
		if resp.Data[k] != v {
			t.Errorf("%s = %v, want %v", k, resp.Data[k], v)
		}
	}
	if len(h.intents()) != 1 || len(h.outcomes()) != 1 {
		t.Fatalf("rows = %d intents, %d outcomes; want one pair", len(h.intents()), len(h.outcomes()))
	}
	o := h.lastOutcome()
	if o.RiskTier != "read" || o.Outcome.String != "ok" || o.RequesterAccount.String != "eva.peters" ||
		o.Target.String != "eva.peters" || o.RequesterSlackID != "U01EVA" || o.Result != nil {
		t.Errorf("outcome row = %+v", o)
	}
}

// covers: AC-6. No lock end when unlocked, or when locked until an admin clears it.
func TestUserLookupLockEnds(t *testing.T) {
	h := newHarness(t)
	h.dir.Locked[toolstest.UserDN("Eva Peters", "Sales")] = time.Time{}
	if resp := h.call(ToolUserLookup, "{}", eva()); resp.Data["locked"] != true || resp.Data["lock_ends_at"] != nil {
		t.Errorf("admin only lock: %+v", resp.Data)
	}
	delete(h.dir.Locked, toolstest.UserDN("Eva Peters", "Sales"))
	if resp := h.call(ToolUserLookup, "null", eva()); resp.Data["locked"] != false || resp.Data["lock_ends_at"] != nil {
		t.Errorf("unlocked: %+v", resp.Data)
	}
}

// covers: AC-4. Identity is the one enabled user under an allowed user OU
// whose mail matches, ignoring case.
func TestIdentity(t *testing.T) {
	h := newHarness(t)
	for name, tc := range map[string]struct {
		email string
		code  Code
	}{
		"no match":               {"nobody@noord.example", CodeNotMatched},
		"admin account":          {"adm@noord.example", CodeNotMatched},
		"disabled":               {"old.leaver@noord.example", CodeNotMatched},
		"filter characters":      {"eva*@noord.example", CodeNotMatched},
		"case differs":           {"EVA.Peters@noord.example", ""},
		"two users, one address": {"shared@noord.example", CodeAmbiguous},
	} {
		t.Run(name, func(t *testing.T) {
			if name == "two users, one address" {
				h.dir.Users[0].Mail, h.dir.Users[1].Mail = "shared@noord.example", "shared@noord.example"
				defer func() {
					h.dir.Users[0].Mail, h.dir.Users[1].Mail = "eva.peters@noord.example", "ruben.mulder@noord.example"
				}()
			}
			r := eva()
			r.Email = tc.email
			resp := h.call(ToolAccountUnlock, "", r)
			if tc.code == "" {
				if resp.IsError {
					t.Fatalf("response = %+v, want success", resp)
				}
				return
			}
			wantCode(t, resp, tc.code)
			o := h.lastOutcome()
			if o.Outcome.String != "denied_identity" || o.RequesterAccount.Valid {
				t.Errorf("outcome = %+v, want denied_identity with no account", o)
			}
		})
	}
	if len(h.dir.Cleared()) != 1 {
		t.Errorf("cleared %v, want only the case insensitive match unlocked", h.dir.Cleared())
	}
}

// covers: AC-7. account_unlock unlocks only the requester, then confirms.
func TestAccountUnlock(t *testing.T) {
	h := newHarness(t)
	resp := h.call(ToolAccountUnlock, "", eva())
	if resp.IsError || resp.Data["was_locked"] != true || resp.Data["locked"] != false || resp.Text != "Account unlocked and confirmed." {
		t.Fatalf("response = %+v", resp)
	}
	if got := h.dir.Cleared(); len(got) != 1 || got[0] != toolstest.UserDN("Eva Peters", "Sales") {
		t.Fatalf("cleared %v, want only eva", got)
	}
	o := h.lastOutcome()
	if o.RiskTier != "low" || o.Target.String != "eva.peters" || string(o.Result) != `{"locked":false,"was_locked":true}` {
		t.Errorf("outcome = tier %s target %s result %s", o.RiskTier, o.Target.String, o.Result)
	}

	// Not locked: nothing written, still ok.
	resp = h.call(ToolAccountUnlock, "", eva())
	if resp.IsError || resp.Data["was_locked"] != false || resp.Text != "Account was not locked; nothing changed." {
		t.Fatalf("second response = %+v", resp)
	}
	if len(h.dir.Cleared()) != 1 {
		t.Errorf("an unlocked account was written again: %v", h.dir.Cleared())
	}
}

// covers: AC-7. A lock that is still set after the write is an error.
func TestAccountUnlockNotConfirmed(t *testing.T) {
	h := newHarness(t)
	h.dir.KeepLock = true
	wantCode(t, h.call(ToolAccountUnlock, "", eva()), CodeNotConfirmed)
	if o := h.lastOutcome(); o.Outcome.String != "error" || o.Error.String != "unlock not confirmed" {
		t.Errorf("outcome = %s %q", o.Outcome.String, o.Error.String)
	}
}

// covers: AC-8. Only a file holding exactly read_write allows an account
// change; anything else refuses it with no write, and tickets still flow.
func TestKillSwitch(t *testing.T) {
	h := newHarness(t)
	for name, prepare := range map[string]func(){
		"read_only":  func() { h.ks.set("read_only") },
		"empty":      func() { h.ks.set("") },
		"other text": func() { h.ks.set("read_write please") },
		"missing":    func() { _ = removeFile(h.ks.path) },
		"unreadable": func() { h.ks.set("read_write"); chmod(t, h.ks.path, 0o000) },
	} {
		t.Run(name, func(t *testing.T) {
			prepare()
			defer chmod(t, h.ks.path, 0o600)
			wantCode(t, h.call(ToolAccountUnlock, "", eva()), CodeReadOnly)
			if o := h.lastOutcome(); o.Outcome.String != "read_only" || o.Error.String != "account changes are paused" {
				t.Errorf("outcome = %s %q", o.Outcome.String, o.Error.String)
			}
			if len(h.dir.Cleared()) != 0 {
				t.Fatal("the directory was written while paused")
			}
			if resp := h.call(ToolUserLookup, "", eva()); resp.IsError {
				t.Errorf("user_lookup was blocked by the kill switch: %+v", resp)
			}
		})
	}
	// Tickets keep flowing while paused.
	h.ks.set("read_only")
	if resp := h.call(ToolTicketCreate, `{"title":"Locked out","body":"Locked out of Windows."}`, eva()); resp.IsError {
		t.Fatalf("ticket_create while paused: %+v", resp)
	}
	// Flipping back takes effect on the next call, no restart.
	h.ks.set("  read_write\n")
	if resp := h.call(ToolAccountUnlock, "", eva()); resp.IsError || resp.Data["was_locked"] != true {
		t.Fatalf("unlock after flipping back: %+v", resp)
	}
}

// covers: AC-8. A flip between the dispatcher's read and the handler's read
// stops the write.
func TestKillSwitchIsReadAgainBeforeTheWrite(t *testing.T) {
	h := newHarness(t)
	h.d.beforeHandler = func() { h.ks.set("read_only") }
	wantCode(t, h.call(ToolAccountUnlock, "", eva()), CodeReadOnly)
	if len(h.dir.Cleared()) != 0 {
		t.Fatal("the directory was written after the switch flipped")
	}
	if o := h.lastOutcome(); o.Outcome.String != "read_only" {
		t.Errorf("outcome = %s", o.Outcome.String)
	}
}

// covers: AC-8. With no file, READ_ONLY decides.
func TestKillSwitchWithoutAFile(t *testing.T) {
	if NewKillSwitch("", true).AccountChangesAllowed() {
		t.Error("READ_ONLY=true allowed account changes")
	}
	if !NewKillSwitch("", false).AccountChangesAllowed() {
		t.Error("READ_ONLY=false refused account changes")
	}
}

// covers: AC-9. The fourth unlock in the window is refused; only ok
// outcomes count, and the no change unlock counts too.
func TestAccountUnlockRateLimit(t *testing.T) {
	h := newHarness(t)
	for i := range 3 {
		if resp := h.call(ToolAccountUnlock, "", eva()); resp.IsError {
			t.Fatalf("unlock %d: %+v", i+1, resp)
		}
	}
	wantCode(t, h.call(ToolAccountUnlock, "", eva()), CodeRateLimited)
	if o := h.lastOutcome(); o.Outcome.String != "denied_policy" || o.Error.String != "rate limited" {
		t.Errorf("outcome = %s %q", o.Outcome.String, o.Error.String)
	}
	// Another requester has their own budget.
	ruben := eva()
	ruben.SlackID, ruben.Email = "U02RUBEN", "ruben.mulder@noord.example"
	if resp := h.call(ToolAccountUnlock, "", ruben); resp.IsError {
		t.Errorf("ruben's first unlock: %+v", resp)
	}
}

// covers: AC-9, AC-10. The sixth ticket_create across threads is refused.
func TestTicketCreateRateLimit(t *testing.T) {
	h := newHarness(t)
	for i := range 5 {
		r := eva()
		r.ThreadTS = "176000000" + string(rune('0'+i)) + ".000100"
		if resp := h.call(ToolTicketCreate, `{"title":"t","body":"b"}`, r); resp.IsError {
			t.Fatalf("ticket %d: %+v", i+1, resp)
		}
	}
	r := eva()
	r.ThreadTS = "1760000009.000100"
	wantCode(t, h.call(ToolTicketCreate, `{"title":"t","body":"b"}`, r), CodeRateLimited)
}

// covers: AC-12. The blocked tool never runs, never reaches LDAP or Zammad,
// stores no arguments and no account, whatever the arguments say.
func TestPrivilegedAccessChangeIsAlwaysBlocked(t *testing.T) {
	h := newHarness(t)
	h.dir.Err = errNetwork // AD is down: still "blocked", not "directory unavailable"
	for _, args := range []string{`{"summary":"make me Domain Admin"}`, `{"summary":"x","username":"adm.ruben.mulder"}`, `{}`, `[1,2]`} {
		resp := h.call(ToolPrivilegedAccessChange, args, eva())
		wantCode(t, resp, CodeBlocked)
		if resp.Text != "blocked: Privileged changes are never made by the assistant. Escalate to IT." {
			t.Errorf("text = %q", resp.Text)
		}
	}
	if h.dir.CallCount != 0 || len(h.zammad.Requests()) != 0 {
		t.Fatalf("a blocked call reached AD (%d calls) or Zammad (%d requests)", h.dir.CallCount, len(h.zammad.Requests()))
	}
	for _, r := range h.store.All() {
		if r.RiskTier != "blocked" || r.RequesterAccount.Valid || r.Arguments != nil || r.Target.Valid {
			t.Errorf("row = %+v, want tier blocked with no account, arguments, or target", r)
		}
		if r.Kind == "outcome" && (r.Outcome.String != "denied_policy" || r.Error.String != "tool is blocked") {
			t.Errorf("outcome = %s %q", r.Outcome.String, r.Error.String)
		}
	}
}

// covers: AC-5, AC-15. Bad arguments are refused before identity, name only
// the schema field, and store no arguments.
func TestInvalidArguments(t *testing.T) {
	h := newHarness(t)
	h.dir.Err = errNetwork // proves identity is never reached
	for _, tc := range []struct {
		tool, args, field string
	}{
		{ToolAccountUnlock, `{"username":"ruben.mulder"}`, ""},
		{ToolUserLookup, `"ruben"`, ""},
		{ToolTicketUpdate, `{"state":{"x":"free text"}}`, "state"},
		{ToolTicketUpdate, `{"state":"reopened"}`, "state"},
		{ToolTicketUpdate, `{}`, "state"},
		{ToolTicketUpdate, `{"state":"pending"}`, "pending_until"},
		{ToolTicketUpdate, `{"state":"open","pending_until":"2026-10-19T12:00:00Z"}`, "pending_until"},
		{ToolTicketUpdate, `{"state":"pending","pending_until":"tomorrow"}`, "pending_until"},
		{ToolTicketUpdate, `{"state":"pending","pending_until":"2026-10-18T11:00:00Z"}`, "pending_until"},
		{ToolTicketUpdate, `{"state":"pending","pending_until":"2027-01-01T00:00:00Z"}`, "pending_until"},
		{ToolTicketUpdate, `{"note":null}`, "note"},
		{ToolTicketCreate, `{"title":"a\nb","body":"x"}`, "title"},
		{ToolTicketCreate, `{"title":"   ","body":"x"}`, "title"},
		{ToolTicketCreate, `{"title":"ok","body":"bell\u0007"}`, "body"},
		{ToolTicketCreate, `{"title":"` + strings.Repeat("é", 201) + `","body":"x"}`, "title"},
		{ToolTicketCreate, `{"body":"x"}`, "title"},
		{ToolTicketCreate, `{"title":5,"body":"x"}`, "title"},
	} {
		resp := h.call(tc.tool, tc.args, eva())
		wantCode(t, resp, CodeInvalidArguments)
		want := "invalid_arguments: The arguments did not match the tool's schema."
		if tc.field != "" {
			want = "invalid_arguments: The arguments did not match the tool's schema (field: " + tc.field + ")."
		}
		if resp.Text != want {
			t.Errorf("%s %s: text = %q, want %q", tc.tool, tc.args, resp.Text, want)
		}
		i := h.lastIntent()
		if i.Arguments != nil || i.RequesterAccount.Valid || h.lastOutcome().Error.String != "invalid arguments" {
			t.Errorf("%s %s: intent = %+v, want no arguments and no account", tc.tool, tc.args, i)
		}
	}
	if h.dir.CallCount != 0 {
		t.Errorf("invalid calls reached the directory %d times", h.dir.CallCount)
	}
	// A valid multi line body with a tab passes.
	h.dir.Err = nil
	if resp := h.call(ToolTicketCreate, `{"title":"ok","body":"line one\n\tline two"}`, eva()); resp.IsError {
		t.Errorf("valid body refused: %+v", resp)
	}
}

// covers: AC-10. ticket_create opens one ticket with the fixed fields.
func TestTicketCreate(t *testing.T) {
	h := newHarness(t)
	resp := h.call(ToolTicketCreate, `{"title":"Locked out","body":"Eva is locked out. Unlocked her account."}`, eva())
	if resp.IsError || resp.Text != "Ticket created." || resp.Data["ticket_id"] == nil || resp.Data["ticket_number"] == "" {
		t.Fatalf("response = %+v", resp)
	}
	var create zammadtest.Seen
	for _, r := range h.zammad.Requests() {
		if r.Method == http.MethodPost && r.Path == "/api/v1/tickets" {
			create = r
		}
	}
	b := create.Body
	art, _ := b["article"].(map[string]any)
	if b["group"] != "IT Support" || b["tags"] != "noord-assistant" || b["title"] != "Locked out" ||
		b["customer_id"] != float64(h.eva) || create.From != "" {
		t.Errorf("ticket body = %+v from %q", b, create.From)
	}
	if art["type"] != "note" || art["sender"] != "Customer" || art["internal"] != false ||
		art["content_type"] != "text/plain" || art["subject"] != "Locked out" {
		t.Errorf("article = %+v", art)
	}
	o := h.lastOutcome()
	result := jsonOf(t, o.Result)
	if _, ok := result["ticket_id"]; !ok || result["ticket_number"] == nil || len(result) != 2 {
		t.Errorf("stored result = %v", result)
	}
	if h.lastIntent().Arguments == nil || strings.Contains(string(h.lastIntent().Arguments), "Locked out") {
		t.Errorf("stored arguments = %s, want the free text dropped", h.lastIntent().Arguments)
	}

	// The thread has a ticket now: a second create is refused even though
	// the first id never reached _meta.
	wantCode(t, h.call(ToolTicketCreate, `{"title":"again","body":"x"}`, eva()), CodeHasTicket)
	// And one with a ticket id in _meta, in a fresh thread.
	r := eva()
	r.ThreadTS, r.TicketID = "1760000001.000100", 9
	wantCode(t, h.call(ToolTicketCreate, `{"title":"again","body":"x"}`, r), CodeHasTicket)
}

// covers: AC-10. No Zammad user for the requester: no helpdesk account.
func TestTicketCreateWithoutHelpdeskAccount(t *testing.T) {
	h := newHarness(t)
	h.dir.Users = append(h.dir.Users, ldapUser("new.starter", "Sales"))
	r := eva()
	r.Email = "new.starter@noord.example"
	wantCode(t, h.call(ToolTicketCreate, `{"title":"t","body":"b"}`, r), CodeNoHelpdeskAccount)
	if o := h.lastOutcome(); o.Outcome.String != "denied_identity" || o.Error.String != "no helpdesk account" {
		t.Errorf("outcome = %s %q", o.Outcome.String, o.Error.String)
	}
}

// covers: AC-19. A Zammad timeout or 5xx is "helpdesk unavailable" and is
// never retried; a 403 is "action failed".
func TestTicketCreateHelpdeskFailures(t *testing.T) {
	for _, tc := range []struct {
		name   string
		fault  zammadtest.Fault
		delay  bool
		code   Code
		reason string
	}{
		{"5xx", zammadtest.Fault{Status: http.StatusBadGateway, Message: "down"}, false, CodeUnavailable, "helpdesk unavailable"},
		{"timeout", zammadtest.Fault{}, true, CodeUnavailable, "helpdesk unavailable"},
		{"403", zammadtest.Fault{Status: http.StatusForbidden, Message: "Not authorized"}, false, CodeFailed, "action failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.zammad.Intercept = func(r *http.Request) (zammadtest.Fault, bool) {
				if r.Method != http.MethodPost || r.URL.Path != "/api/v1/tickets" {
					return zammadtest.Fault{}, false
				}
				if tc.delay {
					time.Sleep(2500 * time.Millisecond) // past the 2s client timeout
					return zammadtest.Fault{Status: http.StatusCreated}, true
				}
				return tc.fault, true
			}
			resp := h.call(ToolTicketCreate, `{"title":"t","body":"b"}`, eva())
			wantCode(t, resp, tc.code)
			if strings.Contains(resp.Text, "down") || strings.Contains(resp.Text, "authorized") {
				t.Errorf("text %q echoes Zammad's message", resp.Text)
			}
			creates := 0
			for _, r := range h.zammad.Requests() {
				if r.Method == http.MethodPost && r.Path == "/api/v1/tickets" {
					creates++
				}
			}
			if creates != 1 {
				t.Errorf("Zammad saw %d creates, want exactly 1", creates)
			}
			if o := h.lastOutcome(); o.Outcome.String != "error" || o.Error.String != tc.reason {
				t.Errorf("outcome = %s %q, want error %q", o.Outcome.String, o.Error.String, tc.reason)
			}
		})
	}
}

// covers: AC-11. ticket_update sends state and note in one request, to the
// _meta ticket only.
func TestTicketUpdate(t *testing.T) {
	h := newHarness(t)
	created := h.call(ToolTicketCreate, `{"title":"t","body":"b"}`, eva())
	r := eva()
	r.TicketID = int64(created.Data["ticket_id"].(int))

	before := len(h.zammad.Requests())
	resp := h.call(ToolTicketUpdate, `{"state":"closed","note":"Unlocked and confirmed."}`, r)
	if resp.IsError || resp.Data["state"] != "closed" || resp.Text != "Ticket updated." {
		t.Fatalf("response = %+v", resp)
	}
	var puts []zammadtest.Seen
	for _, s := range h.zammad.Requests()[before:] {
		if s.Method == http.MethodPut {
			puts = append(puts, s)
		}
	}
	if len(puts) != 1 {
		t.Fatalf("updates = %d, want one request", len(puts))
	}
	art, _ := puts[0].Body["article"].(map[string]any)
	if puts[0].Body["state"] != "closed" || art["body"] != "Added by the Noord assistant: Unlocked and confirmed." ||
		art["internal"] != true || art["sender"] != "Agent" || art["type"] != "note" {
		t.Errorf("update body = %+v", puts[0].Body)
	}
	o := h.lastOutcome()
	if o.Target.String != itoa(r.TicketID) || string(h.lastIntent().Arguments) != `{"_omitted":1,"state":"closed"}` {
		t.Errorf("target %q arguments %s", o.Target.String, h.lastIntent().Arguments)
	}

	// Closed now: refused.
	wantCode(t, h.call(ToolTicketUpdate, `{"note":"more"}`, r), CodeTicketClosed)
}

// covers: AC-11. pending maps to pending reminder with pending_time.
func TestTicketUpdatePending(t *testing.T) {
	h := newHarness(t)
	created := h.call(ToolTicketCreate, `{"title":"t","body":"b"}`, eva())
	r := eva()
	r.TicketID = int64(created.Data["ticket_id"].(int))
	resp := h.call(ToolTicketUpdate, `{"state":"pending","pending_until":"2026-10-20T09:00:00+02:00"}`, r)
	if resp.IsError || resp.Data["state"] != "pending" {
		t.Fatalf("response = %+v", resp)
	}
	reqs := h.zammad.Requests()
	put := reqs[len(reqs)-1].Body
	if put["state"] != "pending reminder" || put["pending_time"] != "2026-10-20T07:00:00Z" || put["article"] != nil {
		t.Errorf("update body = %+v", put)
	}
	if got := jsonOf(t, h.lastIntent().Arguments); got["pending_until"] != "2026-10-20T09:00:00+02:00" {
		t.Errorf("stored arguments = %v", got)
	}
}

// covers: AC-11. Only the requester's own, open, _meta ticket.
func TestTicketUpdateRefusals(t *testing.T) {
	h := newHarness(t)
	ruben := eva()
	ruben.SlackID, ruben.Email, ruben.ThreadTS = "U02RUBEN", "ruben.mulder@noord.example", "1760000002.000100"
	rubens := h.call(ToolTicketCreate, `{"title":"t","body":"b"}`, ruben)

	r := eva()
	wantCode(t, h.call(ToolTicketUpdate, `{"note":"x"}`, r), CodeNoTicket)
	r.TicketID = 999999
	wantCode(t, h.call(ToolTicketUpdate, `{"note":"x"}`, r), CodeNoTicket)
	r.TicketID = int64(rubens.Data["ticket_id"].(int))
	wantCode(t, h.call(ToolTicketUpdate, `{"note":"x"}`, r), CodeNotYours)

	newStarter := eva()
	h.dir.Users = append(h.dir.Users, ldapUser("new.starter", "Sales"))
	newStarter.Email, newStarter.TicketID = "new.starter@noord.example", r.TicketID
	wantCode(t, h.call(ToolTicketUpdate, `{"note":"x"}`, newStarter), CodeNoHelpdeskAccount)
}

// covers: AC-15, AC-19. The directory down during identity is "directory
// unavailable", recorded as an error pair, with nothing echoed.
func TestDirectoryUnavailable(t *testing.T) {
	h := newHarness(t)
	h.dir.Err = errNetwork
	resp := h.call(ToolUserLookup, "", eva())
	wantCode(t, resp, CodeUnavailable)
	if resp.Text != "unavailable: The directory cannot be reached right now." {
		t.Errorf("text = %q", resp.Text)
	}
	if o := h.lastOutcome(); o.Outcome.String != "error" || o.Error.String != "directory unavailable" {
		t.Errorf("outcome = %s %q", o.Outcome.String, o.Error.String)
	}

	// Insufficient access is not an outage.
	h.dir.Err = nil
	h.dir.WriterErr = errors.New("LDAP Result Code 50 \"Insufficient Access Rights\": CN=Eva Peters")
	resp = h.call(ToolAccountUnlock, "", eva())
	wantCode(t, resp, CodeFailed)
	if strings.Contains(resp.Text, "Eva") {
		t.Errorf("text %q echoes the directory error", resp.Text)
	}
}

// covers: AC-15, AC-3. An intent that cannot be written runs nothing and
// leaves no row; an outcome that cannot be written still returns the real
// result.
func TestAuditFailures(t *testing.T) {
	h := newHarness(t)
	h.store.IntentErr = errors.New("db down")
	wantCode(t, h.call(ToolAccountUnlock, "", eva()), CodeFailed)
	if len(h.dir.Cleared()) != 0 || len(h.store.All()) != 0 {
		t.Fatalf("an unaudited call ran (%v) or left rows", h.dir.Cleared())
	}
	if !strings.Contains(h.logs.String(), `"msg":"audit_unavailable"`) {
		t.Errorf("logs lack audit_unavailable: %s", h.logs)
	}

	h.store.IntentErr, h.store.OutcomeErr = nil, errors.New("db down")
	resp := h.call(ToolAccountUnlock, "", eva())
	if resp.IsError || resp.Data["was_locked"] != true {
		t.Fatalf("response = %+v, want the real result", resp)
	}
	if len(h.intents()) != 1 || len(h.outcomes()) != 0 {
		t.Errorf("rows = %d intents, %d outcomes; want only the intent", len(h.intents()), len(h.outcomes()))
	}
	if !strings.Contains(h.logs.String(), `"msg":"audit_outcome_incomplete"`) {
		t.Errorf("logs lack audit_outcome_incomplete: %s", h.logs)
	}

	h.store.OutcomeErr, h.store.CountErr = nil, errors.New("db down")
	wantCode(t, h.call(ToolAccountUnlock, "", eva()), CodeFailed)
}

// covers: AC-20. One tool_call line per call, with only tool, tier,
// outcome, and trace id; nothing personal.
func TestToolCallLog(t *testing.T) {
	h := newHarness(t)
	h.call(ToolTicketCreate, `{"title":"Secret title","body":"Secret body"}`, eva())
	line := h.logs.String()
	if strings.Count(line, `"msg":"tool_call"`) != 1 {
		t.Fatalf("logs = %s, want one tool_call line", line)
	}
	for _, leak := range []string{"eva", "U01EVA", "noord.example", "Secret", "C01HELP"} {
		if strings.Contains(line, leak) {
			t.Errorf("log %s holds %q", line, leak)
		}
	}
	if !strings.Contains(line, `"tool":"ticket_create"`) || !strings.Contains(line, `"tier":"low"`) || !strings.Contains(line, `"outcome":"ok"`) {
		t.Errorf("log %s lacks tool, tier, or outcome", line)
	}
}

// covers: AC-19. Every call runs under TOOL_CALL_TIMEOUT.
func TestCallTimeout(t *testing.T) {
	h := newHarness(t)
	h.d.cfg.CallTimeout = 50 * time.Millisecond
	h.zammad.Intercept = func(r *http.Request) (zammadtest.Fault, bool) {
		<-r.Context().Done()
		return zammadtest.Fault{}, false
	}
	start := time.Now()
	wantCode(t, h.call(ToolTicketCreate, `{"title":"t","body":"b"}`, eva()), CodeUnavailable)
	if time.Since(start) > time.Second {
		t.Errorf("call took %s, want it cut at the call timeout", time.Since(start))
	}
}

func removeFile(path string) error { return os.Remove(path) }

func chmod(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	if err := os.Chmod(path, mode); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
}

func ldapUser(username, dept string) ldap.User {
	return ldap.User{DN: toolstest.UserDN(username, dept), Username: username, Mail: username + "@noord.example", Enabled: true, Department: dept}
}

func itoa(n int64) string { return ticketTarget(n) }

// covers: AC-3, AC-19. A directory that hangs until the call deadline still
// leaves one pair and says the directory is unavailable: the audit writes
// outside the handler do not inherit the expired deadline.
func TestDeadlineDuringIdentityStillLeavesAPair(t *testing.T) {
	h := newHarness(t)
	h.d.cfg.CallTimeout = 50 * time.Millisecond
	h.dir.Stall = true

	resp := h.call(ToolUserLookup, "", eva())

	wantCode(t, resp, CodeUnavailable)
	if len(h.intents()) != 1 || len(h.outcomes()) != 1 {
		t.Fatalf("rows = %d intents, %d outcomes; want one pair", len(h.intents()), len(h.outcomes()))
	}
	if o := h.lastOutcome(); o.Outcome.String != "error" || o.Error.String != "directory unavailable" {
		t.Errorf("outcome = %s %q, want error %q", o.Outcome.String, o.Error.String, "directory unavailable")
	}
	if strings.Contains(h.logs.String(), `"msg":"audit_unavailable"`) {
		t.Errorf("logs claim nothing was recorded: %s", h.logs)
	}
}

// covers: AC-3, AC-19. Checks that use up the budget stop the call before
// the handler, with a pair and no write.
func TestDeadlineAfterTheChecksStillLeavesAPair(t *testing.T) {
	h := newHarness(t)
	h.d.cfg.CallTimeout = 50 * time.Millisecond
	h.dir.Slow = 100 * time.Millisecond

	resp := h.call(ToolAccountUnlock, "", eva())

	wantCode(t, resp, CodeUnavailable)
	if len(h.dir.Cleared()) != 0 {
		t.Errorf("cleared %v after the deadline", h.dir.Cleared())
	}
	if len(h.intents()) != 1 || len(h.outcomes()) != 1 || h.lastOutcome().Outcome.String != "error" {
		t.Errorf("rows = %d intents, %d outcomes; want one error pair", len(h.intents()), len(h.outcomes()))
	}
}

// covers: AC-15. A failed history read inside the handler is a recorded
// failure, not an unrecorded call: the intent and outcome were written.
func TestThreadReadFailureIsRecordedNotUnavailable(t *testing.T) {
	h := newHarness(t)
	h.store.ThreadErr = errors.New("db down")

	resp := h.call(ToolTicketCreate, `{"title":"t","body":"b"}`, eva())

	wantCode(t, resp, CodeFailed)
	if o := h.lastOutcome(); o.Outcome.String != "error" || o.Error.String != "action failed" {
		t.Errorf("outcome = %s %q, want error %q", o.Outcome.String, o.Error.String, "action failed")
	}
	logs := h.logs.String()
	if strings.Contains(logs, `"msg":"audit_unavailable"`) {
		t.Errorf("logs claim nothing was recorded: %s", logs)
	}
	if !strings.Contains(logs, `"cause":"audit"`) {
		t.Errorf("logs lack the audit cause: %s", logs)
	}
}

// covers: AC-20. A failure logs one tool_call_failed line whose cause is a
// fixed token (an LDAP code, a Zammad status), never the error's text.
func TestFailureCauseIsLogged(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(*harness)
		tool  string
		args  string
		cause string
	}{
		{"LDAP insufficient access", func(h *harness) {
			h.dir.WriterErr = goldap.NewError(goldap.LDAPResultInsufficientAccessRights, errors.New("access denied for CN=Eva Peters"))
		}, ToolAccountUnlock, "", "ldap_50"},
		{"directory down", func(h *harness) { h.dir.Err = errNetwork }, ToolUserLookup, "", "ldap_200"},
		{"Zammad 403", func(h *harness) {
			h.zammad.Intercept = func(r *http.Request) (zammadtest.Fault, bool) {
				if r.Method != http.MethodPost || r.URL.Path != "/api/v1/tickets" {
					return zammadtest.Fault{}, false
				}
				return zammadtest.Fault{Status: http.StatusForbidden, Message: "Not authorized for Eva"}, true
			}
		}, ToolTicketCreate, `{"title":"Secret title","body":"Secret body"}`, "zammad_403"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			tc.setup(h)

			resp := h.call(tc.tool, tc.args, eva())

			if !resp.IsError {
				t.Fatalf("response = %+v, want a failure", resp)
			}
			logs := h.logs.String()
			if strings.Count(logs, `"msg":"tool_call_failed"`) != 1 || !strings.Contains(logs, `"cause":"`+tc.cause+`"`) {
				t.Errorf("logs = %s, want one tool_call_failed line with cause %s", logs, tc.cause)
			}
			for _, leak := range []string{"Eva", "eva", "U01EVA", "Secret", "access denied", "authorized"} {
				if strings.Contains(logs, leak) {
					t.Errorf("logs %s hold %q", logs, leak)
				}
			}
		})
	}
}

func TestCause(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{
		{nil, "none"},
		{context.DeadlineExceeded, "deadline"},
		{fmt.Errorf("search: %w", context.Canceled), "canceled"},
		{fmt.Errorf("%w: insert intent: db down", audit.ErrAuditUnavailable), "audit"},
		{zammad.ErrUserWrite, "zammad_user_write"},
		{goldap.NewError(goldap.LDAPResultBusy, errors.New("busy")), "ldap_51"},
		{fmt.Errorf("delete method: %w", &graph.Error{Status: 403, Message: "eva.peters"}), "graph_403"},
		{fmt.Errorf("user: %w", graph.ErrNotManaged), "graph_not_managed"},
		{graph.ErrNotFound, "graph_not_found"},
		{&net.OpError{Op: "dial", Err: errors.New("refused")}, "network"},
		{errors.New("something else"), "other"},
	} {
		if got := cause(tc.err); got != tc.want {
			t.Errorf("cause(%v) = %q, want %q", tc.err, got, tc.want)
		}
	}
}

// covers: AC-4. More mail matches than the search returns is ambiguous,
// never a guess at one of them.
func TestTooManyMatchesIsAmbiguous(t *testing.T) {
	h := newHarness(t)
	h.dir.Err = ldap.ErrTooManyMatches

	resp := h.call(ToolUserLookup, "", eva())

	wantCode(t, resp, CodeAmbiguous)
	if o := h.lastOutcome(); o.Outcome.String != "denied_identity" || o.RequesterAccount.Valid {
		t.Errorf("outcome = %+v, want denied_identity with no account", o)
	}
}
