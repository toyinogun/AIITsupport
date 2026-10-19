package tools

import (
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/toyinogun/AIITsupport/internal/zammad"
	"github.com/toyinogun/AIITsupport/internal/zammad/zammadtest"
)

func escalateArgs(reason string) string {
	return `{"reason":"` + reason + `","title":"Printer on the 2nd floor not working","summary":"Asked for Domain Admin to fix the printer. Refused."}`
}

// inThread is eva in another Slack thread.
func inThread(ts string) Requester {
	r := eva()
	r.ThreadTS = ts
	return r
}

// asCode is r with the code initiator set, as agent code sends it.
func asCode(r Requester) Requester {
	r.Initiator = InitiatorCode
	return r
}

func withTicket(r Requester, id int) Requester {
	r.TicketID = int64(id)
	return r
}

// createTicket opens a ticket for r through ticket_create and returns its id.
func (h *harness) createTicket(r Requester) int {
	h.t.Helper()
	resp := h.call(ToolTicketCreate, `{"title":"Printer","body":"The printer is broken."}`, r)
	if resp.IsError {
		h.t.Fatalf("ticket_create = %+v", resp)
	}
	return resp.Data["ticket_id"].(int)
}

func (h *harness) blocked(r Requester) {
	h.t.Helper()
	wantCode(h.t, h.call(ToolPrivilegedAccessChange, `{"summary":"Domain Admin"}`, r), CodeBlocked)
}

func (h *harness) closeTicket(id int) {
	h.t.Helper()
	resp := h.call(ToolTicketUpdate, `{"state":"closed","note":"done"}`, withTicket(eva(), id))
	if resp.IsError {
		h.t.Fatalf("close = %+v", resp)
	}
}

// covers: AC-2, AC-4, AC-5. The printer hook: after a blocked attempt, a
// model escalation with no ticket creates one with three tags, and the audit
// pair keeps only the reason and the result's allowed keys.
func TestEscalateCreatesATicket(t *testing.T) {
	h := newHarness(t)
	h.blocked(eva())
	resp := h.call(ToolEscalate, escalateArgs(ReasonPrivilegedRequest), eva())

	if resp.IsError || resp.Text != "Escalated to IT." {
		t.Fatalf("response = %+v", resp)
	}
	id := resp.Data["ticket_id"].(int)
	want := map[string]any{
		"ticket_number": strconv.Itoa(70000 + id), "reason": ReasonPrivilegedRequest, "initiator": "model",
		"privileged": true, "created": true, "privileged_attempts": int64(1), "privileged_window_hours": int64(24),
		"ticket_url": "http://zammad.test/#ticket/zoom/" + strconv.Itoa(id),
	}
	for k, v := range want {
		if resp.Data[k] != v {
			t.Errorf("%s = %#v, want %#v", k, resp.Data[k], v)
		}
	}
	if got := h.zammad.Tags(id); !slices.Equal(got, []string{"noord-assistant", "escalated", "privileged-request"}) {
		t.Errorf("tags = %v", got)
	}
	tk, _ := h.zammad.Ticket(id)
	arts := h.zammad.Articles(id)
	if tk["title"] != "Printer on the 2nd floor not working" || tk["group"] != "IT Support" || tk["customer_id"] != float64(h.eva) ||
		len(arts) != 1 || arts[0]["body"] != "Asked for Domain Admin to fix the printer. Refused." || arts[0]["internal"] != false {
		t.Errorf("ticket = %v, articles = %v", tk, arts)
	}

	in, out := h.lastIntent(), h.lastOutcome()
	if out.RiskTier != "low" || out.Outcome.String != "ok" || out.Target.String != "" {
		t.Errorf("outcome = tier %s %s target %q", out.RiskTier, out.Outcome.String, out.Target.String)
	}
	if args := jsonOf(t, in.Arguments); args["reason"] != ReasonPrivilegedRequest || args["title"] != nil || args["summary"] != nil {
		t.Errorf("stored arguments = %v", args)
	}
	res := jsonOf(t, out.Result)
	if res["ticket_url"] != nil || res["ticket_id"] != float64(id) || res["initiator"] != "model" || res["privileged_attempts"] != float64(1) {
		t.Errorf("stored result = %v", res)
	}
	for _, row := range h.store.All() {
		if strings.Contains(string(row.Arguments)+string(row.Result), "Printer") {
			t.Errorf("audit row %d holds free text: %s %s", row.ID, row.Arguments, row.Result)
		}
	}
}

// covers: AC-4. With the _meta ticket, escalate adds the tag, then one update
// with the internal note, and leaves the state alone. The audit target is the
// _meta id.
func TestEscalateTagsTheMetaTicket(t *testing.T) {
	h := newHarness(t)
	id := h.createTicket(eva())
	before := len(h.zammad.Requests())
	resp := h.call(ToolEscalate, escalateArgs(ReasonOtherPerson), withTicket(eva(), id))

	if resp.IsError || resp.Data["ticket_id"] != id || resp.Data["created"] != false || resp.Data["privileged"] != false {
		t.Fatalf("response = %+v", resp)
	}
	if got := h.zammad.Tags(id); !slices.Equal(got, []string{"noord-assistant", "escalated"}) {
		t.Errorf("tags = %v", got)
	}
	arts := h.zammad.Articles(id)
	note := arts[len(arts)-1]
	if note["body"] != "Escalated by the Noord assistant (other_person):\nAsked for Domain Admin to fix the printer. Refused." ||
		note["internal"] != true || note["sender"] != "Agent" {
		t.Errorf("note = %v", note)
	}
	var writes []string
	for _, r := range h.zammad.Requests()[before:] {
		if r.Method != http.MethodGet {
			writes = append(writes, r.Method+" "+r.Path)
			if _, ok := r.Body["state"]; ok {
				t.Errorf("escalate changed the state: %v", r.Body)
			}
		}
	}
	if !slices.Equal(writes, []string{"POST /api/v1/tags/add", "PUT /api/v1/tickets/" + strconv.Itoa(id)}) {
		t.Errorf("writes = %v", writes)
	}
	if o := h.lastOutcome(); o.Target.String != strconv.Itoa(id) {
		t.Errorf("target = %q, want the _meta id", o.Target.String)
	}
}

// covers: AC-4. With no _meta id, the thread's ok ticket_create is found in
// the audit log.
func TestEscalateFindsTheThreadTicket(t *testing.T) {
	h := newHarness(t)
	id := h.createTicket(eva())
	resp := h.call(ToolEscalate, escalateArgs(ReasonSecurityConcern), eva())
	if resp.IsError || resp.Data["ticket_id"] != id || resp.Data["created"] != false {
		t.Fatalf("response = %+v", resp)
	}
	if o := h.lastOutcome(); o.Target.String != "" || jsonOf(t, o.Result)["ticket_id"] != float64(id) {
		t.Errorf("target %q result %s; want an empty target and the used id in the result", o.Target.String, o.Result)
	}
}

// covers: AC-3. A second escalation in the thread is refused.
func TestEscalateOncePerThread(t *testing.T) {
	h := newHarness(t)
	if resp := h.call(ToolEscalate, escalateArgs(ReasonOtherPerson), eva()); resp.IsError {
		t.Fatalf("first = %+v", resp)
	}
	before := len(h.zammad.Requests())
	wantCode(t, h.call(ToolEscalate, escalateArgs(ReasonRestart), asCode(eva())), CodeAlreadyEscalated)
	if o := h.lastOutcome(); o.Outcome.String != "denied_policy" || o.Error.String != "thread already escalated" {
		t.Errorf("outcome = %s %q", o.Outcome.String, o.Error.String)
	}
	if n := len(h.zammad.Requests()) - before; n != 0 {
		t.Errorf("a refused escalation made %d Zammad requests", n)
	}
	if resp := h.call(ToolEscalate, escalateArgs(ReasonOtherPerson), inThread("1760000000.000200")); resp.IsError {
		t.Errorf("another thread = %+v", resp)
	}
}

// covers: AC-4. Another customer's ticket, a missing ticket, and a closed one.
func TestEscalateTicketChecks(t *testing.T) {
	h := newHarness(t)
	ruben := Requester{SlackID: "U01RUBEN", Email: "ruben.mulder@noord.example", ChannelID: "C01HELP", ThreadTS: "1760000000.000900"}
	rubens := h.createTicket(ruben)
	wantCode(t, h.call(ToolEscalate, escalateArgs(ReasonOtherPerson), withTicket(eva(), rubens)), CodeNotYours)
	wantCode(t, h.call(ToolEscalate, escalateArgs(ReasonOtherPerson), withTicket(eva(), 99999)), CodeNoTicket)

	closed := h.createTicket(eva())
	h.closeTicket(closed)
	wantCode(t, h.call(ToolEscalate, escalateArgs(ReasonOtherPerson), withTicket(eva(), closed)), CodeTicketClosed)
	if got := h.zammad.Tags(closed); slices.Contains(got, "escalated") {
		t.Errorf("a refused escalation tagged the closed ticket: %v", got)
	}

	// A privileged (a blocked attempt in the thread) or code escalation opens
	// a sibling instead of losing the flag.
	h.blocked(inThread("1760000000.000301"))
	for name, r := range map[string]Requester{
		"privileged_request": withTicket(inThread("1760000000.000301"), closed),
		"code initiator":     asCode(withTicket(inThread("1760000000.000302"), closed)),
	} {
		reason := ReasonPrivilegedRequest
		if name == "code initiator" {
			reason = ReasonAssistantFailure
		}
		resp := h.call(ToolEscalate, escalateArgs(reason), r)
		if resp.IsError || resp.Data["created"] != true || resp.Data["ticket_id"] == closed {
			t.Errorf("%s: response = %+v, want a new ticket", name, resp)
		}
	}
}

// covers: AC-4. A security concern in a thread with a blocked attempt is
// privileged and gets the privileged tag.
func TestEscalatePrivilegedByThread(t *testing.T) {
	h := newHarness(t)
	id := h.createTicket(eva())
	h.blocked(eva())
	resp := h.call(ToolEscalate, escalateArgs(ReasonSecurityConcern), withTicket(eva(), id))
	if resp.IsError || resp.Data["privileged"] != true || resp.Data["reason"] != ReasonSecurityConcern {
		t.Fatalf("response = %+v", resp)
	}
	if got := h.zammad.Tags(id); !slices.Equal(got, []string{"noord-assistant", "escalated", "privileged-request"}) {
		t.Errorf("tags = %v", got)
	}
}

// covers: AC-4. A failed audit read fails before any Zammad call; a Zammad
// failure after the first tag is unavailable, keeps the tag, and nothing is retried.
func TestEscalateFailures(t *testing.T) {
	h := newHarness(t)
	h.store.ThreadErr = errors.New("connection reset")
	before := len(h.zammad.Requests())
	wantCode(t, h.call(ToolEscalate, escalateArgs(ReasonOtherPerson), eva()), CodeFailed)
	if n := len(h.zammad.Requests()) - before; n != 0 {
		t.Errorf("a failed audit read still made %d Zammad requests", n)
	}
	h.store.ThreadErr = nil

	id := h.createTicket(eva())
	h.blocked(eva())
	tags := 0
	h.zammad.Intercept = func(r *http.Request) (zammadtest.Fault, bool) {
		if r.URL.Path != "/api/v1/tags/add" {
			return zammadtest.Fault{}, false
		}
		tags++
		return zammadtest.Fault{Status: http.StatusServiceUnavailable, Message: "down"}, tags == 2
	}
	before = len(h.zammad.Requests())
	wantCode(t, h.call(ToolEscalate, escalateArgs(ReasonPrivilegedRequest), withTicket(eva(), id)), CodeUnavailable)
	if got := h.zammad.Tags(id); !slices.Equal(got, []string{"noord-assistant", "escalated"}) {
		t.Errorf("tags = %v, want the first tag kept", got)
	}
	seen := map[string]int{}
	for _, r := range h.zammad.Requests()[before:] {
		seen[r.Method+" "+r.Path]++
	}
	for step, n := range seen {
		if n != 1 && step != "POST /api/v1/tags/add" {
			t.Errorf("%s ran %d times", step, n)
		}
	}
	if seen["POST /api/v1/tags/add"] != 2 || seen["PUT /api/v1/tickets/"+strconv.Itoa(id)] != 0 {
		t.Errorf("requests = %v, want two tag adds (the second failed) and no update", seen)
	}
	if o := h.lastOutcome(); o.Outcome.String != "error" || o.Error.String != "helpdesk unavailable" {
		t.Errorf("outcome = %s %q", o.Outcome.String, o.Error.String)
	}
}

// covers: AC-5. The attempt count spans threads inside the window only.
func TestEscalateCountsRecentAttempts(t *testing.T) {
	h := newHarness(t)
	h.blocked(inThread("1760000000.000201"))
	h.store.Backdate(time.Since(h.d.now().Add(-25 * time.Hour))) // older than the window at the harness clock
	h.blocked(inThread("1760000000.000202"))
	h.blocked(eva())
	resp := h.call(ToolEscalate, escalateArgs(ReasonPrivilegedRequest), eva())
	if resp.Data["privileged_attempts"] != int64(2) || resp.Data["privileged_window_hours"] != int64(24) {
		t.Errorf("data = %+v, want 2 attempts in 24h", resp.Data)
	}
}

// covers: AC-2, AC-17. A code only reason needs the code initiator; the
// other bounds are the schema's.
func TestEscalateArguments(t *testing.T) {
	h := newHarness(t)
	for _, reason := range []string{ReasonAssistantFailure, ReasonRestart} {
		resp := h.call(ToolEscalate, escalateArgs(reason), eva())
		wantCode(t, resp, CodeInvalidArguments)
		if !strings.Contains(resp.Text, "(field: reason)") {
			t.Errorf("%s: text = %q", reason, resp.Text)
		}
		if o := h.lastOutcome(); o.Error.String != "invalid arguments" {
			t.Errorf("%s: outcome error = %q", reason, o.Error.String)
		}
	}
	for name, args := range map[string]string{
		"unknown reason": `{"reason":"urgent","title":"t","summary":"s"}`,
		"newline title":  `{"reason":"other_person","title":"a\nb","summary":"s"}`,
		"no summary":     `{"reason":"other_person","title":"t"}`,
		"long title":     `{"reason":"other_person","title":"` + strings.Repeat("x", 201) + `","summary":"s"}`,
		"blank summary":  `{"reason":"other_person","title":"t","summary":"   "}`,
	} {
		wantCode(t, h.call(ToolEscalate, args, eva()), CodeInvalidArguments)
		_ = name
	}
	resp := h.call(ToolEscalate, escalateArgs(ReasonAssistantFailure), asCode(eva()))
	if resp.IsError || resp.Data["initiator"] != "code" {
		t.Errorf("code escalation = %+v", resp)
	}
}

// covers: AC-6. A close waits for the escalation; a note does not.
func TestCloseGuard(t *testing.T) {
	h := newHarness(t)
	id := h.createTicket(eva())
	h.blocked(eva())
	before := len(h.zammad.Requests())
	wantCode(t, h.call(ToolTicketUpdate, `{"state":"closed"}`, withTicket(eva(), id)), CodeEscalateFirst)
	if n := len(h.zammad.Requests()) - before; n != 0 {
		t.Errorf("the refused close made %d Zammad requests", n)
	}
	if o := h.lastOutcome(); o.Outcome.String != "denied_policy" || o.Error.String != "escalate first" {
		t.Errorf("outcome = %s %q", o.Outcome.String, o.Error.String)
	}
	for _, args := range []string{`{"note":"still looking"}`, `{"state":"open"}`} {
		if resp := h.call(ToolTicketUpdate, args, withTicket(eva(), id)); resp.IsError {
			t.Errorf("%s = %+v", args, resp)
		}
	}
	if resp := h.call(ToolEscalate, escalateArgs(ReasonPrivilegedRequest), withTicket(eva(), id)); resp.IsError {
		t.Fatalf("escalate = %+v", resp)
	}
	if resp := h.call(ToolTicketUpdate, `{"state":"closed"}`, withTicket(eva(), id)); resp.IsError {
		t.Errorf("close after the escalation = %+v", resp)
	}
}

// covers: AC-7. Read only mode and TICKET_CREATE_LIMIT never stop an escalation.
func TestEscalateIsNotAnAccountChange(t *testing.T) {
	h := newHarness(t)
	h.ks.set("read_only\n")
	h.blocked(eva())
	if resp := h.call(ToolEscalate, escalateArgs(ReasonPrivilegedRequest), eva()); resp.IsError {
		t.Fatalf("read only escalate = %+v", resp)
	}
	for i := range 6 {
		r := inThread("1760000000.00" + strconv.Itoa(1000+i))
		if resp := h.call(ToolEscalate, escalateArgs(ReasonOtherPerson), r); resp.IsError || resp.Data["created"] != true {
			t.Fatalf("escalation %d = %+v", i+1, resp)
		}
	}
	if resp := h.call(ToolTicketCreate, `{"title":"t","body":"b"}`, inThread("1760000000.002000")); resp.IsError {
		t.Errorf("escalations counted toward TICKET_CREATE_LIMIT: %+v", resp)
	}
}

// covers: AC-4, AC-5. The reason privileged_request alone adds the privileged
// tag, even with no blocked attempt in the thread; privileged stays false and
// the attempt count is zero.
func TestEscalatePrivilegedReasonWithoutABlockedAttempt(t *testing.T) {
	h := newHarness(t)
	resp := h.call(ToolEscalate, escalateArgs(ReasonPrivilegedRequest), eva())
	if resp.IsError || resp.Data["privileged"] != false || resp.Data["privileged_attempts"] != int64(0) {
		t.Fatalf("response = %+v", resp)
	}
	id := resp.Data["ticket_id"].(int)
	if got := h.zammad.Tags(id); !slices.Equal(got, []string{"noord-assistant", "escalated", "privileged-request"}) {
		t.Errorf("tags = %v", got)
	}
}

// covers: AC-4, AC-13. Without ZAMMAD_PUBLIC_URL the result has no ticket_url.
func TestEscalateWithoutAPublicURL(t *testing.T) {
	h := newHarness(t)
	h.d.sys.Helpdesk.publicURL = ""
	resp := h.call(ToolEscalate, escalateArgs(ReasonOtherPerson), eva())
	if resp.IsError {
		t.Fatalf("response = %+v", resp)
	}
	if _, ok := resp.Data["ticket_url"]; ok {
		t.Errorf("ticket_url = %v, want none", resp.Data["ticket_url"])
	}
	if resp.Data["ticket_number"] == nil {
		t.Errorf("ticket_number missing: %+v", resp.Data)
	}
}

// covers: AC-5. privileged_window_hours is the window in whole hours, truncated.
func TestEscalateWindowHoursAreTruncated(t *testing.T) {
	h := newHarness(t)
	h.d.cfg.PrivilegedWindow = 90 * time.Minute
	resp := h.call(ToolEscalate, escalateArgs(ReasonOtherPerson), eva())
	if resp.IsError || resp.Data["privileged_window_hours"] != int64(1) {
		t.Errorf("response = %+v, want privileged_window_hours 1", resp)
	}
}

// covers: AC-4. A merged ticket counts as closed: refused for a plain
// escalation, replaced by a new ticket for a privileged one.
func TestEscalateMergedTicket(t *testing.T) {
	h := newHarness(t)
	merged := h.createTicket(eva())
	if _, err := h.d.sys.Helpdesk.api.UpdateTicket(t.Context(), merged, zammad.TicketUpdate{State: "merged"}, ""); err != nil {
		t.Fatal(err)
	}
	wantCode(t, h.call(ToolEscalate, escalateArgs(ReasonOtherPerson), withTicket(eva(), merged)), CodeTicketClosed)

	r := inThread("1760000000.000401")
	h.blocked(r)
	resp := h.call(ToolEscalate, escalateArgs(ReasonPrivilegedRequest), withTicket(r, merged))
	if resp.IsError || resp.Data["created"] != true || resp.Data["ticket_id"] == merged {
		t.Errorf("privileged escalation on a merged ticket = %+v, want a new ticket", resp)
	}
	if got := h.zammad.Tags(merged); slices.Contains(got, "escalated") {
		t.Errorf("the merged ticket was tagged: %v", got)
	}
}

// covers: AC-4. A requester with no Zammad user is denied_identity and no
// ticket is opened.
func TestEscalateWithoutHelpdeskAccount(t *testing.T) {
	h := newHarness(t)
	h.dir.Users = append(h.dir.Users, ldapUser("new.starter", "Sales"))
	r := eva()
	r.Email = "new.starter@noord.example"
	before := len(h.zammad.Tickets())
	wantCode(t, h.call(ToolEscalate, escalateArgs(ReasonPrivilegedRequest), r), CodeNoHelpdeskAccount)
	if o := h.lastOutcome(); o.Outcome.String != "denied_identity" || o.Error.String != "no helpdesk account" {
		t.Errorf("outcome = %s %q", o.Outcome.String, o.Error.String)
	}
	if n := len(h.zammad.Tickets()) - before; n != 0 {
		t.Errorf("%d tickets opened", n)
	}
}

// covers: AC-2. The upper bounds are inclusive and counted in runes.
func TestEscalateArgumentBounds(t *testing.T) {
	h := newHarness(t)
	title, summary := strings.Repeat("é", 200), strings.Repeat("é", 10000)
	args := `{"reason":"other_person","title":"` + title + `","summary":"` + summary + `"}`
	if resp := h.call(ToolEscalate, args, eva()); resp.IsError {
		t.Errorf("a 200 rune title and a 10000 rune summary = %+v", resp)
	}
	tooLong := `{"reason":"other_person","title":"t","summary":"` + summary + `é"}`
	wantCode(t, h.call(ToolEscalate, tooLong, inThread("1760000000.000501")), CodeInvalidArguments)
}

// covers: AC-6. The guard is per thread, and only an ok escalate lifts it.
func TestCloseGuardEdges(t *testing.T) {
	h := newHarness(t)
	other := inThread("1760000000.000601")
	otherTicket := h.createTicket(other)
	id := h.createTicket(eva())
	h.blocked(eva())

	if resp := h.call(ToolTicketUpdate, `{"state":"closed"}`, withTicket(other, otherTicket)); resp.IsError {
		t.Errorf("a close in a thread with no blocked attempt = %+v", resp)
	}
	if resp := h.call(ToolTicketUpdate, `{"state":"pending","pending_until":"2026-10-20T12:00:00Z"}`, withTicket(eva(), id)); resp.IsError {
		t.Errorf("pending while unescalated = %+v", resp)
	}

	h.zammad.Intercept = func(r *http.Request) (zammadtest.Fault, bool) {
		return zammadtest.Fault{Status: http.StatusServiceUnavailable, Message: "down"}, r.URL.Path == "/api/v1/tags/add"
	}
	wantCode(t, h.call(ToolEscalate, escalateArgs(ReasonPrivilegedRequest), withTicket(eva(), id)), CodeUnavailable)
	h.zammad.Intercept = nil
	wantCode(t, h.call(ToolTicketUpdate, `{"state":"closed"}`, withTicket(eva(), id)), CodeEscalateFirst)
}
