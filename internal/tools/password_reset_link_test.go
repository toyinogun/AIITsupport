package tools

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/toyinogun/AIITsupport/internal/audit"
	"github.com/toyinogun/AIITsupport/internal/resetlink"
	"github.com/toyinogun/AIITsupport/internal/tools/toolstest"
	"github.com/toyinogun/AIITsupport/internal/zammad/zammadtest"
)

// fakeLinks is an in memory reset link store.
type fakeLinks struct {
	mu        sync.Mutex
	issued    []resetlink.Issue
	recent    int64
	countErr  error
	issueErr  error
	expiresAt time.Time
	// countAccount and countWindow are the last CountRecent arguments.
	countAccount string
	countWindow  time.Duration
}

func (f *fakeLinks) Issue(_ context.Context, in resetlink.Issue) (resetlink.Issued, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.issueErr != nil {
		return resetlink.Issued{}, f.issueErr
	}
	f.issued = append(f.issued, in)
	return resetlink.Issued{
		Link:   resetlink.Link{ID: uuid.New(), Account: in.Account, ExpiresAt: f.expiresAt},
		Token:  "TOKEN-tok-tok-tok-tok-tok-tok-tok-tok-tok-tk",
		Voided: 1,
	}, nil
}

func (f *fakeLinks) CountRecent(_ context.Context, account string, window time.Duration) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.countAccount, f.countWindow = account, window
	return f.recent, f.countErr
}

const resetTestURL = "https://reset.noord.test"

// newResetHarness is the harness with password_reset_link registered.
func newResetHarness(t *testing.T) (*harness, *fakeLinks) {
	t.Helper()
	links := &fakeLinks{expiresAt: time.Date(2026, 10, 18, 12, 15, 0, 0, time.UTC)}
	h := newHarnessWith(t, &ResetSystem{Links: links, DeniedGroups: []string{"SG-IT-Admins", "APP-Helpdesk-Agents"}, Settings: ResetSettings{
		PublicURL: resetTestURL, TTL: 15 * time.Minute, LinkLimit: 3, Window: 24 * time.Hour,
	}}, passwordResetLinkTool())
	return h, links
}

// covers: spec 0011 AC-2, AC-4. The happy path: the link, the stored keys,
// the note, and the issuing call id.
func TestPasswordResetLink(t *testing.T) {
	h, links := newResetHarness(t)
	ticket := h.createTicket(eva())

	resp := h.call(ToolPasswordResetLink, `{}`, withTicket(eva(), ticket))
	if resp.IsError || resp.Text != ResetLinkSentText {
		t.Fatalf("response = %+v", resp)
	}
	url, _ := resp.Data[ResetURLKey].(string)
	if !strings.HasPrefix(url, resetTestURL+"/reset/TOKEN-") {
		t.Errorf("reset_url = %q", url)
	}
	for k, want := range map[string]any{"expires_at": "2026-10-18T12:15:00Z", "expires_in_minutes": int64(15), "voided_previous": int64(1), "ticket_noted": true} {
		if resp.Data[k] != want {
			t.Errorf("%s = %v (%T), want %v", k, resp.Data[k], resp.Data[k], want)
		}
	}
	if len(links.issued) != 1 {
		t.Fatalf("issued %d links, want 1", len(links.issued))
	}
	in := links.issued[0]
	out := h.lastOutcome()
	if in.Account != "eva.peters" || in.TicketID != int64(ticket) || in.TTL != 15*time.Minute || in.IssueCallID != out.CallID {
		t.Errorf("issue = %+v, want eva.peters, ticket %d, 15m, call %s", in, ticket, out.CallID)
	}
	stored := jsonOf(t, out.Result)
	if _, ok := stored[ResetURLKey]; ok || stored["link_id"] == nil {
		t.Errorf("stored result = %v, want link_id and no reset_url", stored)
	}
	if strings.Contains(string(out.Result), "TOKEN") || out.Outcome.String != "ok" || out.RiskTier != "low" {
		t.Errorf("outcome row = %+v", out)
	}
	arts := h.zammad.Articles(ticket)
	last := arts[len(arts)-1]
	if last["body"] != "Password reset link sent to the requester. Single use, valid until 2026-10-18 12:15 UTC." || last["internal"] != true {
		t.Errorf("note = %v", last)
	}
}

// covers: spec 0011 AC-3. The refusals in order, each with no link issued.
func TestPasswordResetLinkRefusals(t *testing.T) {
	cases := []struct {
		name  string
		setup func(h *harness, links *fakeLinks) Requester
		code  Code
	}{
		{"no ticket", func(*harness, *fakeLinks) Requester { return eva() }, CodeNoTicket},
		{"already escalated", func(h *harness, _ *fakeLinks) Requester {
			id := h.createTicket(eva())
			if resp := h.call(ToolEscalate, escalateArgs(ReasonSecurityConcern), withTicket(eva(), id)); resp.IsError {
				h.t.Fatalf("escalate = %+v", resp)
			}
			return withTicket(eva(), id)
		}, CodeAlreadyEscalated},
		{"escalate first", func(h *harness, _ *fakeLinks) Requester {
			id := h.createTicket(eva())
			h.blocked(eva())
			return withTicket(eva(), id)
		}, CodeEscalateFirst},
		{"not yours", func(h *harness, _ *fakeLinks) Requester {
			ruben := Requester{SlackID: "U01RUB", Email: "ruben.mulder@noord.example", ChannelID: "C01HELP", ThreadTS: "1760000000.000900"}
			return withTicket(eva(), h.createTicket(ruben))
		}, CodeNotYours},
		{"ticket not found", func(*harness, *fakeLinks) Requester { return withTicket(eva(), 99999) }, CodeNoTicket},
		{"no helpdesk account", func(h *harness, _ *fakeLinks) Requester {
			h.dir.Users = append(h.dir.Users, ldapUser("new.starter", "Sales"))
			r := withTicket(eva(), h.createTicket(eva()))
			r.Email = "new.starter@noord.example"
			return r
		}, CodeNoHelpdeskAccount},
		{"ticket closed", func(h *harness, _ *fakeLinks) Requester {
			id := h.createTicket(eva())
			h.closeTicket(id)
			return withTicket(eva(), id)
		}, CodeTicketClosed},
		{"protected, nested", func(h *harness, _ *fakeLinks) Requester {
			h.dir.UserIn = map[string][]string{evaDN: {"CN=SG-IT-Admins,OU=Admin Accounts,OU=Noord," + base}}
			return withTicket(eva(), h.createTicket(eva()))
		}, CodeProtected},
		{"protected, code floor", func(h *harness, _ *fakeLinks) Requester {
			h.dir.UserIn = map[string][]string{evaDN: {"CN=Domain Admins,CN=Users," + base}}
			return withTicket(eva(), h.createTicket(eva()))
		}, CodeProtected},
		{"rate limited", func(h *harness, links *fakeLinks) Requester {
			links.recent = 3
			return withTicket(eva(), h.createTicket(eva()))
		}, CodeRateLimited},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h, links := newResetHarness(t)
			r := c.setup(h, links)
			wantCode(t, h.call(ToolPasswordResetLink, `{}`, r), c.code)
			if len(links.issued) != 0 {
				t.Errorf("issued %d links, want none", len(links.issued))
			}
			want := "denied_policy"
			if c.code == CodeNoHelpdeskAccount {
				want = "denied_identity"
			}
			if o := h.lastOutcome(); o.ToolName != ToolPasswordResetLink || o.Outcome.String != want {
				t.Errorf("outcome = %s %s, want %s", o.ToolName, o.Outcome.String, want)
			}
		})
	}
}

var evaDN = toolstest.UserDN("Eva Peters", "Sales")

// covers: spec 0011 AC-3. A member of SYNC-Entra-Users, which
// TOOL_DENIED_GROUPS fences for group changes, still gets a link.
func TestPasswordResetLinkSyncMemberAllowed(t *testing.T) {
	h, links := newResetHarness(t)
	h.dir.UserIn = map[string][]string{evaDN: {toolstest.GroupDN("SYNC-Entra-Users")}}
	if resp := h.call(ToolPasswordResetLink, `{}`, withTicket(eva(), h.createTicket(eva()))); resp.IsError {
		t.Fatalf("response = %+v, want a link", resp)
	}
	if len(links.issued) != 1 {
		t.Errorf("issued %d links, want 1", len(links.issued))
	}
}

// covers: spec 0011 AC-3. A RESET_DENIED_GROUPS name that does not resolve
// refuses every account and logs denied_group_unresolved.
func TestPasswordResetLinkUnresolvedDeniedGroup(t *testing.T) {
	h, links := newResetHarness(t)
	h.d.accounts = NewAccountFence(h.dir, []string{"SG-Does-Not-Exist"}, h.d.logger)
	wantCode(t, h.call(ToolPasswordResetLink, `{}`, withTicket(eva(), h.createTicket(eva()))), CodeProtected)
	if len(links.issued) != 0 || !strings.Contains(h.logs.String(), "denied_group_unresolved") {
		t.Errorf("issued %d, logs %s", len(links.issued), h.logs.String())
	}
}

// covers: spec 0011 AC-2. Arguments are refused; the kill switch and an
// unmatched requester stop it before the handler.
func TestPasswordResetLinkDispatcherSteps(t *testing.T) {
	h, links := newResetHarness(t)
	id := h.createTicket(eva())
	wantCode(t, h.call(ToolPasswordResetLink, `{"account":"ruben.mulder"}`, withTicket(eva(), id)), CodeInvalidArguments)

	stranger := withTicket(eva(), id)
	stranger.Email = "nobody@noord.example"
	wantCode(t, h.call(ToolPasswordResetLink, `{}`, stranger), CodeNotMatched)

	h.ks.set("read_only\n")
	wantCode(t, h.call(ToolPasswordResetLink, `{}`, withTicket(eva(), id)), CodeReadOnly)
	if len(links.issued) != 0 {
		t.Errorf("issued %d links, want none", len(links.issued))
	}
}

// covers: spec 0011 AC-4. A failed note does not fail the call.
func TestPasswordResetLinkNoteFails(t *testing.T) {
	h, _ := newResetHarness(t)
	id := h.createTicket(eva())
	h.zammad.Intercept = func(r *http.Request) (zammadtest.Fault, bool) {
		return zammadtest.Fault{Status: http.StatusBadGateway, Message: "down"}, r.Method == http.MethodPut
	}
	resp := h.call(ToolPasswordResetLink, `{}`, withTicket(eva(), id))
	if resp.IsError || resp.Data["ticket_noted"] != false {
		t.Fatalf("response = %+v, want ok with ticket_noted false", resp)
	}
}

// covers: spec 0011 AC-4. A store failure fails the call; nothing leaks.
func TestPasswordResetLinkStoreFails(t *testing.T) {
	h, links := newResetHarness(t)
	links.issueErr = errors.New("insert failed: duplicate key")
	resp := h.call(ToolPasswordResetLink, `{}`, withTicket(eva(), h.createTicket(eva())))
	wantCode(t, resp, CodeFailed)
	if o := h.lastOutcome(); o.Outcome.String != string(audit.OutcomeError) {
		t.Errorf("outcome = %s", o.Outcome.String)
	}
}

// covers: spec 0011 AC-17. The tool is listed only when registered, and the
// dispatcher refuses it without a store.
func TestPasswordResetLinkRegistration(t *testing.T) {
	plain, err := NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := plain.Lookup(ToolPasswordResetLink); ok {
		t.Error("password_reset_link is registered without WithPasswordReset")
	}
	withReset, err := NewRegistry(WithPasswordReset())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := withReset.Lookup(ToolPasswordResetLink); !ok {
		t.Fatal("WithPasswordReset did not register password_reset_link")
	}
	h := newHarness(t)
	if _, err := NewDispatcher(withReset, h.d.cfg, h.d.sys, h.d.logger); err == nil {
		t.Error("NewDispatcher accepted password_reset_link without a reset store")
	}
}

// covers: spec 0011 AC-3. The refusals keep their order: a thread IT already
// holds is refused as already_escalated even when the account is also
// protected and over the link limit, and that refusal is decided from the
// audit log before any Zammad call.
func TestPasswordResetLinkRefusalOrder(t *testing.T) {
	h, links := newResetHarness(t)
	id := h.createTicket(eva())
	if resp := h.call(ToolEscalate, escalateArgs(ReasonSecurityConcern), withTicket(eva(), id)); resp.IsError {
		t.Fatalf("escalate = %+v", resp)
	}
	h.dir.UserIn = map[string][]string{evaDN: {toolstest.GroupDN("APP-Helpdesk-Agents")}}
	links.recent = 99
	before := len(h.zammad.Requests())

	wantCode(t, h.call(ToolPasswordResetLink, `{}`, withTicket(eva(), id)), CodeAlreadyEscalated)
	if n := len(h.zammad.Requests()) - before; n != 0 {
		t.Errorf("%d Zammad requests before the audit refusal, want 0", n)
	}
	if len(links.issued) != 0 {
		t.Errorf("issued %d links, want none", len(links.issued))
	}
}

// covers: spec 0011 AC-3. The link limit counts the requester's own
// directory account inside RESET_LINK_WINDOW, and a count failure fails the
// call without a link.
func TestPasswordResetLinkLimitCount(t *testing.T) {
	h, links := newResetHarness(t)
	id := h.createTicket(eva())
	links.recent = 2 // one under RESET_LINK_LIMIT 3
	if resp := h.call(ToolPasswordResetLink, `{}`, withTicket(eva(), id)); resp.IsError {
		t.Fatalf("response = %+v, want a link under the limit", resp)
	}
	if links.countAccount != "eva.peters" || links.countWindow != 24*time.Hour {
		t.Errorf("CountRecent(%q, %s), want eva.peters and 24h", links.countAccount, links.countWindow)
	}

	links.countErr = errors.New("db down")
	wantCode(t, h.call(ToolPasswordResetLink, `{}`, withTicket(eva(), id)), CodeFailed)
	if len(links.issued) != 1 {
		t.Errorf("issued %d links, want only the first", len(links.issued))
	}
}

// covers: spec 0011 AC-2. TOOL_WRITE_LIMIT counts the tool's own ok
// outcomes per Slack id, separately from the link limit.
func TestPasswordResetLinkWriteLimit(t *testing.T) {
	h, links := newResetHarness(t) // TOOL_WRITE_LIMIT 3
	id := h.createTicket(eva())
	for i := range 3 {
		if resp := h.call(ToolPasswordResetLink, `{}`, withTicket(eva(), id)); resp.IsError {
			t.Fatalf("call %d = %+v", i+1, resp)
		}
	}
	wantCode(t, h.call(ToolPasswordResetLink, `{}`, withTicket(eva(), id)), CodeRateLimited)
	if len(links.issued) != 3 {
		t.Errorf("issued %d links, want 3", len(links.issued))
	}
}

// covers: spec 0011 AC-3, AC-6. A disabled account never matches at the
// identity step, so it is refused not_matched before the handler; an email
// shared by two accounts is refused ambiguous. Neither gets a link.
func TestPasswordResetLinkIdentityRefusals(t *testing.T) {
	h, links := newResetHarness(t)
	id := h.createTicket(eva())

	leaver := withTicket(eva(), id)
	leaver.Email = "old.leaver@noord.example"
	wantCode(t, h.call(ToolPasswordResetLink, `{}`, leaver), CodeNotMatched)

	h.dir.Users[0].Mail, h.dir.Users[1].Mail = "shared@noord.example", "shared@noord.example"
	shared := withTicket(eva(), id)
	shared.Email = "shared@noord.example"
	wantCode(t, h.call(ToolPasswordResetLink, `{}`, shared), CodeAmbiguous)

	if len(links.issued) != 0 {
		t.Errorf("issued %d links, want none", len(links.issued))
	}
}

// covers: spec 0011 AC-3, AC-17. RESET_DENIED_GROUPS is configurable: with
// SG-Finance listed, a Finance member is refused protected.
func TestPasswordResetLinkConfiguredDeniedGroup(t *testing.T) {
	h, links := newResetHarness(t)
	h.d.accounts = NewAccountFence(h.dir, []string{"SG-Finance"}, h.d.logger)
	h.dir.UserIn = map[string][]string{evaDN: {toolstest.GroupDN("SG-Finance")}}
	wantCode(t, h.call(ToolPasswordResetLink, `{}`, withTicket(eva(), h.createTicket(eva()))), CodeProtected)
	if len(links.issued) != 0 {
		t.Errorf("issued %d links, want none", len(links.issued))
	}
}

// covers: spec 0011 AC-11. CloseWithNote adds one internal note and closes
// an open ticket; on a closed ticket it does nothing and reports false.
func TestCloseWithNote(t *testing.T) {
	h, _ := newResetHarness(t)
	id := h.createTicket(eva())
	desk := h.d.sys.Helpdesk

	closed, err := desk.CloseWithNote(t.Context(), id, "The requester set a new password with the reset link.")
	if err != nil || !closed {
		t.Fatalf("CloseWithNote(open) = %v, %v; want true", closed, err)
	}
	ticket, _ := h.zammad.Ticket(id)
	arts := h.zammad.Articles(id)
	last := arts[len(arts)-1]
	if last["body"] != "The requester set a new password with the reset link." || last["internal"] != true {
		t.Errorf("note = %v", last)
	}
	if closedID, _ := stateIDOf("closed"); ticket["state_id"] != closedID {
		t.Errorf("ticket state_id = %v, want closed (%d)", ticket["state_id"], closedID)
	}

	before := len(h.zammad.Articles(id))
	closed, err = desk.CloseWithNote(t.Context(), id, "again")
	if err != nil || closed {
		t.Errorf("CloseWithNote(closed) = %v, %v; want false", closed, err)
	}
	if n := len(h.zammad.Articles(id)) - before; n != 0 {
		t.Errorf("%d notes added to a closed ticket", n)
	}
}

// stateIDOf is the fake Zammad's id for a ticket state name.
func stateIDOf(name string) (int, bool) {
	for _, st := range zammadtest.States {
		if st["name"] == name {
			return st["id"].(int), true
		}
	}
	return 0, false
}
