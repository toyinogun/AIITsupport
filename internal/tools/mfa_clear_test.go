package tools

import (
	"context"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/toyinogun/AIITsupport/internal/audit"
	"github.com/toyinogun/AIITsupport/internal/db/queries"
	"github.com/toyinogun/AIITsupport/internal/graph"
	"github.com/toyinogun/AIITsupport/internal/graph/graphtest"
	"github.com/toyinogun/AIITsupport/internal/tools/toolstest"
	"github.com/toyinogun/AIITsupport/internal/zammad/zammadtest"
)

const itApprover = "U0ITLEAD01"

// mfaHarness is the harness with mfa_clear registered against a fake Graph
// where eva.peters has one Authenticator, a mobile, and an office phone.
type mfaHarness struct {
	*harness
	graph *graphtest.Fake
	evaID string // eva's Entra object id
	// expires is when a request made now expires; the store compares
	// expiries with the real clock, as the SQL compares with now().
	expires time.Time
}

func newMFAHarness(t *testing.T) *mfaHarness {
	t.Helper()
	g := graphtest.New(t)
	client, err := graph.New(graph.Options{BaseURL: g.URL(), Token: graph.StaticToken(graphtest.Token), Timeout: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	h := newHarnessSystems(t, nil, &MFASystem{
		Graph: client, Reader: nil, DeniedGroups: []string{"SG-IT-Admins", "APP-Helpdesk-Agents"},
	}, mfaClearTool([]string{itApprover}, 30*time.Minute))
	id := g.AddUser(map[string]any{"mailNickname": "eva.peters", "employeeType": graph.ManagedMarker})
	g.SetAuthenticators(id, "auth-1")
	g.SetPhones(id, []graphtest.Phone{
		{ID: graphtest.PhoneID("mobile"), PhoneNumber: "+1 2025550114", PhoneType: "mobile"},
		{ID: graphtest.PhoneID("office"), PhoneNumber: "+1 2025550115", PhoneType: "office"},
	})
	start := time.Now().UTC().Truncate(time.Minute)
	h.d.now = func() time.Time { return start }
	return &mfaHarness{harness: h, graph: g, evaID: id, expires: start.Add(30 * time.Minute)}
}

func (h *mfaHarness) graphWrites() []string {
	return slices.DeleteFunc(h.graph.Requests(), func(r string) bool { return strings.HasPrefix(r, "GET ") })
}

// request asks for a clear in eva's thread with a fresh ticket.
func (h *mfaHarness) request() (Response, int) {
	h.t.Helper()
	ticket := h.createTicket(eva())
	return h.call(ToolMFAClear, `{}`, withTicket(eva(), ticket)), ticket
}

// covers: spec 0012 AC-2, AC-4. The request defers with the AC-4 result,
// one approval holding {}, the pending result keys, and the ticket note; no
// method is touched.
func TestMFAClearRequestDefers(t *testing.T) {
	h := newMFAHarness(t)
	resp, ticket := h.request()

	if resp.IsError || resp.Code != CodePendingApproval || resp.Text != "This request is waiting for IT approval." {
		t.Fatalf("response = %+v", resp)
	}
	for k, want := range map[string]any{
		"code": "pending_approval", "expires_at": h.expires.Format(time.RFC3339), "expires_in_minutes": int64(30),
		"methods": "Authenticator app (1), phone (2)", "method_count": 3, "ticket_id": int64(ticket),
		"ticket_url": "http://zammad.test/#ticket/zoom/" + strconv.Itoa(ticket), "reused": false, "ticket_noted": true,
	} {
		if resp.Data[k] != want {
			t.Errorf("%s = %v (%T), want %v (%T)", k, resp.Data[k], resp.Data[k], want, want)
		}
	}
	if len(h.store.Approvals) != 1 {
		t.Fatalf("approvals = %d, want 1", len(h.store.Approvals))
	}
	a := h.store.Approvals[0]
	if string(a.Arguments) != `{}` || a.ApproverKind != "it" || !slices.Equal(a.AllowedApprovers, []string{itApprover}) ||
		a.RequesterAccount.String != "eva.peters" || a.ZammadTicketID.Int64 != int64(ticket) {
		t.Errorf("approval = %+v", a)
	}
	if resp.Data["approval_id"] == "" {
		t.Error("no approval_id")
	}
	o := h.lastOutcome()
	if o.ToolName != ToolMFAClear || o.RiskTier != "medium" || o.Outcome.String != "pending_approval" ||
		string(o.Result) != `{"_omitted":1,"expires_at":"`+h.expires.Format(time.RFC3339)+`","method_count":3,"reused":false}` {
		t.Errorf("outcome = %+v result %s", o, o.Result)
	}
	arts := h.zammad.Articles(ticket)
	last := arts[len(arts)-1]
	if last["body"] != "MFA reset requested. Waiting for IT approval until "+h.expires.Format("2006-01-02 15:04")+" UTC. Methods: Authenticator app (1), phone (2)." ||
		last["internal"] != true {
		t.Errorf("note = %v", last)
	}
	if w := h.graphWrites(); len(w) != 0 {
		t.Errorf("graph writes before approval: %v", w)
	}
	if strings.Contains(h.logs.String(), "2025550114") || strings.Contains(string(o.Result), "Device") {
		t.Error("a phone number or device name leaked")
	}
}

// covers: spec 0012 AC-2. Any argument is invalid_arguments.
func TestMFAClearTakesNoArguments(t *testing.T) {
	h := newMFAHarness(t)
	wantCode(t, h.call(ToolMFAClear, `{"account":"ruben.mulder"}`, withTicket(eva(), h.createTicket(eva()))), CodeInvalidArguments)
	if len(h.store.Approvals) != 0 {
		t.Error("an approval was created")
	}
}

// covers: spec 0012 AC-19. Paused account changes refuse at step 7, before
// any approval.
func TestMFAClearPausedAtRequest(t *testing.T) {
	h := newMFAHarness(t)
	ticket := h.createTicket(eva())
	h.ks.set("read_only")
	wantCode(t, h.call(ToolMFAClear, `{}`, withTicket(eva(), ticket)), CodeReadOnly)
	if len(h.store.Approvals) != 0 || len(h.graph.Requests()) != 0 {
		t.Errorf("approvals %d, graph %v", len(h.store.Approvals), h.graph.Requests())
	}
}

// covers: spec 0012 AC-3. Each refusal in order, with an audit pair, no
// approval row, and no Graph write.
func TestMFAClearRefusals(t *testing.T) {
	cases := []struct {
		name  string
		setup func(h *mfaHarness) Requester
		code  Code
	}{
		{"no ticket", func(*mfaHarness) Requester { return eva() }, CodeNoTicket},
		{"already escalated", func(h *mfaHarness) Requester {
			id := h.createTicket(eva())
			h.call(ToolEscalate, escalateArgs(ReasonSecurityConcern), withTicket(eva(), id))
			return withTicket(eva(), id)
		}, CodeAlreadyEscalated},
		{"escalate first", func(h *mfaHarness) Requester {
			id := h.createTicket(eva())
			h.blocked(eva())
			return withTicket(eva(), id)
		}, CodeEscalateFirst},
		{"not yours", func(h *mfaHarness) Requester {
			ruben := Requester{SlackID: "U01RUB", Email: "ruben.mulder@noord.example", ChannelID: "C01HELP", ThreadTS: "1760000000.000900"}
			return withTicket(eva(), h.createTicket(ruben))
		}, CodeNotYours},
		{"ticket closed", func(h *mfaHarness) Requester {
			id := h.createTicket(eva())
			h.closeTicket(id)
			return withTicket(eva(), id)
		}, CodeTicketClosed},
		{"protected, nested", func(h *mfaHarness) Requester {
			h.dir.UserIn = map[string][]string{evaDN: {toolstestGroup("APP-Helpdesk-Agents")}}
			return withTicket(eva(), h.createTicket(eva()))
		}, CodeProtected},
		{"pending in another thread", func(h *mfaHarness) Requester {
			other := eva()
			other.ThreadTS = "1760000000.000200"
			if resp := h.call(ToolMFAClear, `{}`, withTicket(other, h.createTicket(other))); resp.Code != CodePendingApproval {
				h.t.Fatalf("first request = %+v", resp)
			}
			return withTicket(eva(), h.createTicket(eva()))
		}, CodeAlreadyPending},
		{"not in entra", func(h *mfaHarness) Requester {
			h.graph.SetUserField(h.evaID, "employeeType", "")
			return withTicket(eva(), h.createTicket(eva()))
		}, CodeNotInEntra},
		{"missing from entra", func(h *mfaHarness) Requester {
			h.graph.SoftDelete(h.evaID)
			return withTicket(eva(), h.createTicket(eva()))
		}, CodeNotInEntra},
		{"other methods", func(h *mfaHarness) Requester {
			h.graph.SetOtherMethods(h.evaID, "softwareOath")
			return withTicket(eva(), h.createTicket(eva()))
		}, CodeOtherMethods},
		{"other methods, fido2", func(h *mfaHarness) Requester {
			h.graph.SetOtherMethods(h.evaID, "fido2")
			return withTicket(eva(), h.createTicket(eva()))
		}, CodeOtherMethods},
		{"other methods, email", func(h *mfaHarness) Requester {
			h.graph.SetOtherMethods(h.evaID, "email")
			return withTicket(eva(), h.createTicket(eva()))
		}, CodeOtherMethods},
		{"nothing to clear", func(h *mfaHarness) Requester {
			h.graph.SetAuthenticators(h.evaID)
			h.graph.SetPhones(h.evaID, nil)
			return withTicket(eva(), h.createTicket(eva()))
		}, CodeNothingToClear},
		{"entra down", func(h *mfaHarness) Requester {
			h.graph.Intercept = func(*http.Request) (graphtest.Fault, bool) {
				return graphtest.Fault{Status: http.StatusBadGateway, Code: "x"}, true
			}
			return withTicket(eva(), h.createTicket(eva()))
		}, CodeUnavailable},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newMFAHarness(t)
			r := c.setup(h)
			before := len(h.store.Approvals)
			wantCode(t, h.call(ToolMFAClear, `{}`, r), c.code)
			if len(h.store.Approvals) != before {
				t.Error("a refused request created an approval")
			}
			want := map[Code]string{CodeUnavailable: "error"}[c.code]
			if want == "" {
				want = "denied_policy"
			}
			if o := h.lastOutcome(); o.ToolName != ToolMFAClear || o.Outcome.String != want {
				t.Errorf("outcome = %s %s, want %s", o.ToolName, o.Outcome.String, want)
			}
			if w := h.graphWrites(); len(w) != 0 {
				t.Errorf("graph writes: %v", w)
			}
		})
	}
}

// covers: spec 0012 AC-3. Every audit and Zammad check comes before Graph:
// a refusal there makes no Graph call at all.
func TestMFAClearChecksBeforeGraph(t *testing.T) {
	h := newMFAHarness(t)
	wantCode(t, h.call(ToolMFAClear, `{}`, withTicket(eva(), 99999)), CodeNoTicket)
	if r := h.graph.Requests(); len(r) != 0 {
		t.Errorf("graph requests: %v", r)
	}
}

// covers: spec 0012 AC-5. A second request in the same thread reuses the
// pending approval: no Graph call, no new approval, no second note.
func TestMFAClearReuse(t *testing.T) {
	h := newMFAHarness(t)
	first, ticket := h.request()
	notes := len(h.zammad.Articles(ticket))
	h.graph.ResetCounts()

	again := h.call(ToolMFAClear, `{}`, withTicket(eva(), ticket))
	if again.Code != CodePendingApproval || again.IsError || again.Data["approval_id"] != first.Data["approval_id"] ||
		again.Data["reused"] != true || again.Data["methods"] != nil || again.Data["method_count"] != nil ||
		again.Data["expires_at"] != first.Data["expires_at"] {
		t.Fatalf("again = %+v", again.Data)
	}
	if len(h.store.Approvals) != 1 || len(h.zammad.Articles(ticket)) != notes || len(h.graph.Requests()) != 0 {
		t.Errorf("approvals %d, notes %d, graph %v", len(h.store.Approvals), len(h.zammad.Articles(ticket)), h.graph.Requests())
	}
	if o := h.lastOutcome(); o.Outcome.String != "pending_approval" || o.ApprovalID.UUID.String() != first.Data["approval_id"] ||
		string(o.Result) != `{"expires_at":"`+h.expires.Format(time.RFC3339)+`","reused":true}` {
		t.Errorf("reuse outcome = %+v %s", o, o.Result)
	}
}

// covers: spec 0012 (at most one pending mfa_clear approval per account).
// A request from another thread that passed the precheck before the first
// approval committed is refused already_pending at Defer, with an audit
// pair, no second approval, and no ticket note.
func TestMFAClearLostRaceIsAlreadyPending(t *testing.T) {
	h := newMFAHarness(t)
	if first, _ := h.request(); first.Code != CodePendingApproval {
		t.Fatalf("first = %+v", first)
	}
	other := eva()
	other.ThreadTS = "1760000000.000200"
	ticket := h.createTicket(other)
	notes := len(h.zammad.Articles(ticket))
	h.store.HidePending = true

	wantCode(t, h.call(ToolMFAClear, `{}`, withTicket(other, ticket)), CodeAlreadyPending)
	if len(h.store.Approvals) != 1 {
		t.Errorf("approvals = %d, want 1", len(h.store.Approvals))
	}
	if o := h.lastOutcome(); o.ToolName != ToolMFAClear || o.Outcome.String != "denied_policy" || o.Error.String != reasonAlreadyPending {
		t.Errorf("outcome = %s %s %q", o.ToolName, o.Outcome.String, o.Error.String)
	}
	if len(h.zammad.Articles(ticket)) != notes {
		t.Error("the refused request noted the ticket")
	}
}

// covers: spec 0012 (at most one pending mfa_clear approval per account).
// Two threads asking at the same moment leave one pending approval; the
// other request is refused already_pending.
func TestMFAClearConcurrentRequestsLeaveOneApproval(t *testing.T) {
	h := newMFAHarness(t)
	threads := []Requester{eva(), eva()}
	threads[1].ThreadTS = "1760000000.000200"
	tickets := make([]int, len(threads))
	for i, r := range threads {
		tickets[i] = h.createTicket(r)
	}

	codes := make([]Code, len(threads))
	var wg sync.WaitGroup
	for i, r := range threads {
		wg.Go(func() {
			codes[i] = h.d.Call(context.WithoutCancel(t.Context()), Request{Tool: ToolMFAClear, Args: []byte(`{}`), Requester: withTicket(r, tickets[i])}).Code
		})
	}
	wg.Wait()

	slices.Sort(codes)
	if want := []Code{CodeAlreadyPending, CodePendingApproval}; !slices.Equal(codes, want) {
		t.Errorf("codes = %v, want %v", codes, want)
	}
	if len(h.store.Approvals) != 1 {
		t.Errorf("approvals = %d, want 1", len(h.store.Approvals))
	}
}

// approve requests a clear and approves it as the IT approver.
func (h *mfaHarness) approve() (queries.Approval, int) {
	h.t.Helper()
	resp, ticket := h.request()
	id := h.store.All()[len(h.store.All())-1].ApprovalID.UUID
	d, err := h.d.sys.Recorder.Decide(h.t.Context(), id, audit.Decider{SlackID: itApprover}, true)
	if err != nil || !d.Moved {
		h.t.Fatalf("approve %v = %+v, %v (request %+v)", id, d, err, resp)
	}
	return d.Approval, ticket
}

func (h *mfaHarness) status(a queries.Approval) string {
	got, _ := h.store.Approval(a.ID)
	return got.Status
}

// covers: spec 0012 AC-11. An approved clear deletes every Authenticator
// and phone method, notes and closes the ticket, and leaves an outcome pair
// naming the approval and the approver; the approval ends executed.
func TestMFAClearExecute(t *testing.T) {
	h := newMFAHarness(t)
	a, ticket := h.approve()

	if got := h.d.Execute(t.Context(), a); got != audit.OutcomeOK {
		t.Fatalf("Execute = %s; logs %s", got, h.logs.String())
	}
	if len(h.graph.Authenticators(h.evaID)) != 0 || len(h.graph.Phones(h.evaID)) != 0 {
		t.Errorf("left: %v %v", h.graph.Authenticators(h.evaID), h.graph.Phones(h.evaID))
	}
	o := h.lastOutcome()
	if o.ToolName != ToolMFAClear || o.Outcome.String != "ok" || o.ApprovalID.UUID != a.ID || o.ApproverSlackID.String != itApprover ||
		o.RequesterAccount.String != "eva.peters" || o.ZammadTicketID.Int64 != int64(ticket) {
		t.Errorf("outcome = %+v", o)
	}
	if string(o.Result) != `{"cleared_phone":2,"ticket_closed":true,"cleared_authenticator":1}` &&
		jsonOf(t, o.Result)["cleared_phone"] != float64(2) {
		t.Errorf("result = %s", o.Result)
	}
	if o.CallID == h.store.Approvals[0].CallID {
		t.Error("the execution reused the request's call id")
	}
	if h.status(a) != "executed" {
		t.Errorf("status = %s", h.status(a))
	}
	tk, _ := h.zammad.Ticket(ticket)
	arts := h.zammad.Articles(ticket)
	if tk["state"] != "closed" || arts[len(arts)-1]["body"] != "MFA methods cleared after IT approval by "+itApprover+"." {
		t.Errorf("ticket = %v, last note %v", tk["state"], arts[len(arts)-1]["body"])
	}
	// Executing again does nothing: the approval is no longer approved.
	h.graph.ResetCounts()
	if got := h.d.Execute(t.Context(), a); got == audit.OutcomeOK || len(h.graphWrites()) != 0 {
		t.Errorf("a second execute = %s, writes %v", got, h.graphWrites())
	}
}

// covers: spec 0012 AC-11. With the default method known from
// signInPreferences, it is deleted last; the fake refuses it first.
func TestMFAClearDeletesTheDefaultLast(t *testing.T) {
	h := newMFAHarness(t)
	h.graph.SetSignInPreference(h.evaID, "push") // the Authenticator is the default
	a, _ := h.approve()
	if got := h.d.Execute(t.Context(), a); got != audit.OutcomeOK {
		t.Fatalf("Execute = %s", got)
	}
	w := h.graphWrites()
	if len(w) != 3 || !strings.Contains(w[2], "microsoftAuthenticatorMethods") {
		t.Errorf("deletes = %v, want the Authenticator last", w)
	}
}

// covers: spec 0012 AC-10, AC-11. When APPROVAL_EXECUTE_TIMEOUT runs out
// in the middle of a clear (Graph hangs on the Authenticator delete), the
// clear stops with what it deleted so far, the outcome row is still
// written, the approval ends failed, and nothing is tried again.
func TestMFAClearExecuteTimeoutMidClear(t *testing.T) {
	h := newMFAHarness(t)
	h.d.cfg.ExecuteTimeout = 300 * time.Millisecond
	a, ticket := h.approve()
	h.graph.Intercept = func(r *http.Request) (graphtest.Fault, bool) {
		if r.Method == http.MethodDelete && strings.Contains(r.URL.Path, "microsoftAuthenticatorMethods") {
			<-r.Context().Done() // hangs until the clear gives up
			return graphtest.Fault{Status: http.StatusGatewayTimeout}, true
		}
		return graphtest.Fault{}, false
	}

	start := time.Now()
	if got := h.d.Execute(t.Context(), a); got != audit.OutcomeError {
		t.Fatalf("Execute = %s", got)
	}
	if took := time.Since(start); took > 2*time.Second {
		t.Errorf("Execute took %s, want it bounded by the execute timeout", took)
	}
	o := h.lastOutcome()
	res := jsonOf(t, o.Result)
	if o.ApprovalID.UUID != a.ID || o.Error.String != "method delete failed" ||
		res["cleared_authenticator"] != float64(0) || res["cleared_phone"] != float64(2) || res["ticket_closed"] != false {
		t.Errorf("outcome = %s %q %s", o.Outcome.String, o.Error.String, o.Result)
	}
	if h.status(a) != "failed" {
		t.Errorf("status = %s, want failed", h.status(a))
	}
	if n := len(h.graph.Authenticators(h.evaID)); n != 1 {
		t.Errorf("authenticators left = %d, want the one that hung", n)
	}
	if tk, _ := h.zammad.Ticket(ticket); tk["state"] == "closed" {
		t.Error("a clear that did not finish closed the ticket")
	}
}

// covers: spec 0012 AC-11 (amended 2026-10-20). With two Authenticator
// registrations the default one cannot be known; Graph refuses it while the
// other remains, so the clear moves it to the end once and finishes.
func TestMFAClearTwoAuthenticatorsDefersTheDefault(t *testing.T) {
	for _, def := range []string{"auth-1", "auth-2"} {
		t.Run(def, func(t *testing.T) {
			h := newMFAHarness(t)
			h.graph.SetAuthenticators(h.evaID, "auth-1", "auth-2")
			h.graph.SetSignInPreference(h.evaID, "push")
			h.graph.SetDefaultAuthenticator(h.evaID, def)
			h.graph.SignInPrefsForbidden = true // the live tenant answers 400; either way the order is fixed
			a, _ := h.approve()

			if got := h.d.Execute(t.Context(), a); got != audit.OutcomeOK {
				t.Fatalf("Execute = %s; writes %v", got, h.graphWrites())
			}
			if left := h.graph.Authenticators(h.evaID); len(left) != 0 {
				t.Errorf("left = %v", left)
			}
			if w := h.graphWrites(); !strings.HasSuffix(w[len(w)-1], "/"+def) {
				t.Errorf("deletes = %v, want the default %s last", w, def)
			}
			if res := jsonOf(t, h.lastOutcome().Result); res["cleared_authenticator"] != float64(2) || res["cleared_phone"] != float64(2) {
				t.Errorf("result = %v", res)
			}
		})
	}
}

// covers: spec 0012 AC-11. A refused Authenticator is deferred at most once:
// when Graph refuses every Authenticator delete, the clear stops with error.
func TestMFAClearDefersAnAuthenticatorOnlyOnce(t *testing.T) {
	h := newMFAHarness(t)
	h.graph.SetAuthenticators(h.evaID, "auth-1", "auth-2")
	a, _ := h.approve()
	h.graph.Intercept = func(r *http.Request) (graphtest.Fault, bool) {
		if r.Method == http.MethodDelete && strings.Contains(r.URL.Path, "microsoftAuthenticatorMethods") {
			return graphtest.Fault{Status: http.StatusBadRequest, Code: "badRequest"}, true
		}
		return graphtest.Fault{}, false
	}
	if got := h.d.Execute(t.Context(), a); got != audit.OutcomeError {
		t.Fatalf("Execute = %s", got)
	}
	var authDeletes int
	for _, w := range h.graphWrites() {
		if strings.Contains(w, "microsoftAuthenticatorMethods") {
			authDeletes++
		}
	}
	if authDeletes != 3 {
		t.Errorf("Authenticator deletes = %d, want 3 (two, then the first deferred one once)", authDeletes)
	}
	if res := jsonOf(t, h.lastOutcome().Result); res["cleared_authenticator"] != float64(0) || res["cleared_phone"] != float64(2) {
		t.Errorf("result = %v", res)
	}
}

// covers: spec 0012 AC-11, AC-16. A delete that fails stops the clear: the
// outcome is error with the counts so far, and the approval ends failed.
func TestMFAClearStopsAtTheFirstFailedDelete(t *testing.T) {
	h := newMFAHarness(t)
	a, ticket := h.approve()
	h.graph.Intercept = func(r *http.Request) (graphtest.Fault, bool) {
		if r.Method == http.MethodDelete && strings.Contains(r.URL.Path, "microsoftAuthenticatorMethods") {
			return graphtest.Fault{Status: http.StatusBadRequest, Code: "badRequest"}, true
		}
		return graphtest.Fault{}, false
	}
	if got := h.d.Execute(t.Context(), a); got != audit.OutcomeError {
		t.Fatalf("Execute = %s", got)
	}
	o := h.lastOutcome()
	res := jsonOf(t, o.Result)
	if o.Error.String != "method delete failed" || res["cleared_authenticator"] != float64(0) || res["cleared_phone"] != float64(2) {
		t.Errorf("outcome = %s %s %s", o.Outcome.String, o.Error.String, o.Result)
	}
	if h.status(a) != "failed" || len(h.graph.Authenticators(h.evaID)) != 1 {
		t.Errorf("status %s, authenticators %v", h.status(a), h.graph.Authenticators(h.evaID))
	}
	if tk, _ := h.zammad.Ticket(ticket); tk["state"] == "closed" {
		t.Error("the ticket closed after a failed clear")
	}
}

// covers: spec 0012 AC-11, AC-19. The kill switch is read at the start and
// again just before the first delete; paused at either, nothing is written.
func TestMFAClearKillSwitchReads(t *testing.T) {
	h := newMFAHarness(t)
	a, _ := h.approve()
	h.ks.set("read_only")
	if got := h.d.Execute(t.Context(), a); got != audit.OutcomeReadOnly {
		t.Fatalf("paused before = %s", got)
	}
	if o := h.lastOutcome(); o.Error.String != "account changes are paused" || h.status(a) != "failed" || len(h.graphWrites()) != 0 {
		t.Errorf("outcome %s, status %s, writes %v", o.Error.String, h.status(a), h.graphWrites())
	}

	h = newMFAHarness(t)
	a, _ = h.approve()
	h.graph.Intercept = func(r *http.Request) (graphtest.Fault, bool) {
		if strings.HasSuffix(r.URL.Path, "/authentication/methods") {
			h.ks.set("read_only") // paused while the methods are read
		}
		return graphtest.Fault{}, false
	}
	if got := h.d.Execute(t.Context(), a); got != audit.OutcomeReadOnly || len(h.graphWrites()) != 0 {
		t.Errorf("paused between = %s, writes %v", got, h.graphWrites())
	}
}

// covers: spec 0012 AC-11. The account is checked again by name: disabled
// or newly protected since the request, nothing is cleared.
func TestMFAClearRechecksTheAccount(t *testing.T) {
	for name, change := range map[string]func(h *mfaHarness){
		"disabled":  func(h *mfaHarness) { h.dir.Users[0].Enabled = false },
		"protected": func(h *mfaHarness) { h.dir.UserIn = map[string][]string{evaDN: {toolstestGroup("SG-IT-Admins")}} },
		"gone":      func(h *mfaHarness) { h.dir.Users = h.dir.Users[1:] },
	} {
		t.Run(name, func(t *testing.T) {
			h := newMFAHarness(t)
			a, _ := h.approve()
			change(h)
			if got := h.d.Execute(t.Context(), a); got != audit.OutcomeDeniedPolicy {
				t.Fatalf("Execute = %s", got)
			}
			if len(h.graphWrites()) != 0 || h.status(a) != "failed" {
				t.Errorf("writes %v, status %s", h.graphWrites(), h.status(a))
			}
		})
	}
}

// covers: spec 0012 AC-2. The handler never runs from a tools/call, and a
// tools/list without the option holds no mfa_clear.
func TestMFAClearOnlyRunsFromExecute(t *testing.T) {
	reg, err := NewRegistry(8 * time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := reg.Lookup(ToolMFAClear); ok {
		t.Error("mfa_clear registered without WithMFAClear")
	}
	reg, err = NewRegistry(8*time.Hour, WithMFAClear([]string{itApprover}, 30*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	tool, ok := reg.Lookup(ToolMFAClear)
	if !ok || tool.Tier != audit.TierMedium || !tool.accountChange() {
		t.Fatalf("mfa_clear = %+v", tool)
	}
	if strings.Contains(strings.ToLower(tool.Description), "approve ") {
		t.Errorf("description offers approving: %q", tool.Description)
	}
}

// toolstestGroup is the DN the fake directory gives a protected group.
func toolstestGroup(name string) string {
	if name == "SG-IT-Admins" {
		return "CN=SG-IT-Admins,OU=Admin Accounts,OU=Noord," + base
	}
	return toolstest.GroupDN(name)
}

// covers: spec 0012 AC-11, AC-18. Execute repeats the approve guard: a row
// marked approved by the requester, by someone off the list, or after the
// expiry never runs.
func TestMFAExecuteRefusesABadApprovedRow(t *testing.T) {
	for name, spoil := range map[string]func(a *queries.Approval){
		"self":         func(a *queries.Approval) { a.DecidedBy.String = a.RequesterSlackID },
		"not listed":   func(a *queries.Approval) { a.DecidedBy.String = "U0STRANGER" },
		"after expiry": func(a *queries.Approval) { a.DecidedAt.Time = a.ExpiresAt.Add(time.Minute) },
		"no approver":  func(a *queries.Approval) { a.DecidedBy.String, a.DecidedBy.Valid = "", false },
	} {
		t.Run(name, func(t *testing.T) {
			h := newMFAHarness(t)
			a, _ := h.approve()
			row, _ := h.store.Approval(a.ID)
			spoil(&row)
			h.store.SetApproval(row)
			if got := h.d.Execute(t.Context(), a); got != audit.OutcomeError || len(h.graphWrites()) != 0 {
				t.Errorf("Execute = %s, writes %v", got, h.graphWrites())
			}
		})
	}
}

// covers: spec 0012 AC-11. An approved clear that finds nothing left to
// delete never reports success.
func TestMFAClearNothingLeftIsNotSuccess(t *testing.T) {
	h := newMFAHarness(t)
	a, _ := h.approve()
	h.graph.SetAuthenticators(h.evaID)
	h.graph.SetPhones(h.evaID, nil)
	if got := h.d.Execute(t.Context(), a); got != audit.OutcomeDeniedPolicy || h.status(a) != "failed" {
		t.Errorf("Execute = %s, status %s", got, h.status(a))
	}
}

// covers: spec 0012 AC-11, AC-20. With signInPreferences unreadable (the
// live probe got 400) and the Authenticator as the default, the fixed order
// clears the phones first and the Authenticator last, which Graph accepts.
func TestMFAClearFixedOrderPutsTheAuthenticatorLast(t *testing.T) {
	h := newMFAHarness(t)
	h.graph.SetSignInPreference(h.evaID, "push") // the fake refuses the default first
	h.graph.SignInPrefsForbidden = true          // and the clear cannot read which one it is
	a, _ := h.approve()
	if got := h.d.Execute(t.Context(), a); got != audit.OutcomeOK {
		t.Fatalf("Execute = %s", got)
	}
	w := h.graphWrites()
	if len(w) != 3 || !strings.Contains(w[0], "phoneMethods") || !strings.Contains(w[2], "microsoftAuthenticatorMethods") {
		t.Errorf("deletes = %v, want the phones first and the Authenticator last", w)
	}
}

// covers: spec 0012 AC-24. While the thread's approval is pending or
// approved, a close is refused approval_pending before any Zammad call; a
// note still lands, the execution closes the ticket itself, and once the
// approval has ended the model may close again.
func TestMFACloseGuard(t *testing.T) {
	h := newMFAHarness(t)
	resp, ticket := h.request()
	if resp.Code != CodePendingApproval {
		t.Fatalf("request = %+v", resp)
	}
	id := h.store.All()[len(h.store.All())-1].ApprovalID.UUID
	closeTicket := func() Response { return h.call(ToolTicketUpdate, `{"state":"closed"}`, withTicket(eva(), ticket)) }

	before := len(h.zammad.Requests())
	wantCode(t, closeTicket(), CodeApprovalPending)
	if n := len(h.zammad.Requests()) - before; n != 0 {
		t.Errorf("the refused close made %d Zammad requests", n)
	}
	if o := h.lastOutcome(); o.Outcome.String != "denied_policy" || o.Error.String != "approval pending" {
		t.Errorf("outcome = %s %q", o.Outcome.String, o.Error.String)
	}
	if r := h.call(ToolTicketUpdate, `{"note":"still waiting"}`, withTicket(eva(), ticket)); r.IsError {
		t.Errorf("note = %+v", r)
	}

	d, err := h.d.sys.Recorder.Decide(t.Context(), id, audit.Decider{SlackID: itApprover}, true)
	if err != nil || !d.Moved {
		t.Fatalf("approve = %+v, %v", d, err)
	}
	wantCode(t, closeTicket(), CodeApprovalPending)
	if got := h.d.Execute(t.Context(), d.Approval); got != audit.OutcomeOK {
		t.Fatalf("Execute = %s", got)
	}
	if tk, _ := h.zammad.Ticket(ticket); tk["state"] != "closed" {
		t.Errorf("the execution left the ticket %v", tk["state"])
	}
}

// covers: spec 0012 AC-24. A denied approval no longer guards the close.
func TestMFACloseGuardEndsWithTheApproval(t *testing.T) {
	h := newMFAHarness(t)
	_, ticket := h.request()
	if _, err := h.d.sys.Recorder.Decide(t.Context(), h.store.All()[len(h.store.All())-1].ApprovalID.UUID, audit.Decider{SlackID: itApprover}, false); err != nil {
		t.Fatal(err)
	}
	if r := h.call(ToolTicketUpdate, `{"state":"closed"}`, withTicket(eva(), ticket)); r.IsError {
		t.Errorf("close after a deny = %+v", r)
	}
}

// covers: spec 0012 AC-3. An MFA_DENIED_GROUPS name that does not resolve
// refuses every account protected and logs denied_group_unresolved, before
// any Graph call.
func TestMFAClearUnresolvedDeniedGroup(t *testing.T) {
	h := newMFAHarness(t)
	h.d.mfaAccounts = NewAccountFence(h.dir, []string{"SG-Does-Not-Exist"}, h.d.logger)
	wantCode(t, h.call(ToolMFAClear, `{}`, withTicket(eva(), h.createTicket(eva()))), CodeProtected)
	if len(h.store.Approvals) != 0 || len(h.graph.Requests()) != 0 {
		t.Errorf("approvals %d, graph %v", len(h.store.Approvals), h.graph.Requests())
	}
	if !strings.Contains(h.logs.String(), "denied_group_unresolved") {
		t.Errorf("no denied_group_unresolved log: %s", h.logs.String())
	}
}

// covers: spec 0012 AC-4. The approvers are a snapshot taken when the tool
// is built: changing the configured list afterwards, or the list one
// request got, never changes what the next request stores.
func TestMFAClearApproversAreASnapshot(t *testing.T) {
	configured := []string{itApprover}
	tool := mfaClearTool(configured, 30*time.Minute)
	configured[0] = "U0CHANGED1"

	first, err := tool.Approval(t.Context(), audit.Call{}, Prechecked{}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	first.Approvers[0] = "U0CHANGED2"
	second, err := tool.Approval(t.Context(), audit.Call{}, Prechecked{}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(second.Approvers, []string{itApprover}) || second.Kind != audit.ApproverIT {
		t.Errorf("approval request = %+v, want kind it and only %s", second, itApprover)
	}
}

// covers: spec 0012 AC-11. The execution checks Entra again: a user no
// longer managed, a new method of another type, or Graph failing ends the
// clear before any delete, and the approval ends failed.
func TestMFAClearExecuteRechecksEntra(t *testing.T) {
	cases := []struct {
		name    string
		change  func(h *mfaHarness)
		outcome audit.Outcome
		reason  string
	}{
		{"not managed any more", func(h *mfaHarness) { h.graph.SetUserField(h.evaID, "employeeType", "") }, audit.OutcomeDeniedPolicy, "not in entra"},
		{"gone from entra", func(h *mfaHarness) { h.graph.SoftDelete(h.evaID) }, audit.OutcomeDeniedPolicy, "not in entra"},
		{"a new other method", func(h *mfaHarness) { h.graph.SetOtherMethods(h.evaID, "fido2") }, audit.OutcomeDeniedPolicy, "other methods"},
		{"graph down", func(h *mfaHarness) {
			h.graph.Intercept = func(*http.Request) (graphtest.Fault, bool) {
				return graphtest.Fault{Status: http.StatusServiceUnavailable, Code: "x"}, true
			}
		}, audit.OutcomeError, "entra unavailable"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newMFAHarness(t)
			a, _ := h.approve()
			c.change(h)
			if got := h.d.Execute(t.Context(), a); got != c.outcome {
				t.Fatalf("Execute = %s, want %s", got, c.outcome)
			}
			if o := h.lastOutcome(); o.Error.String != c.reason || o.ApprovalID.UUID != a.ID {
				t.Errorf("outcome reason %q approval %v, want %q", o.Error.String, o.ApprovalID.UUID, c.reason)
			}
			if len(h.graphWrites()) != 0 || h.status(a) != "failed" {
				t.Errorf("writes %v, status %s", h.graphWrites(), h.status(a))
			}
		})
	}
}

// covers: spec 0012 AC-11. An account moved out of TOOL_USER_OUS since the
// request is not eligible: the stored reason says so and nothing is cleared.
func TestMFAClearRecheckNamesTheReason(t *testing.T) {
	for name, change := range map[string]func(h *mfaHarness){
		"disabled":          func(h *mfaHarness) { h.dir.Users[0].Enabled = false },
		"moved out of OUs":  func(h *mfaHarness) { h.dir.Users[0].DN = "CN=Eva Peters,OU=Admin Accounts,OU=Noord," + base },
		"gone from the dir": func(h *mfaHarness) { h.dir.Users = h.dir.Users[1:] },
	} {
		t.Run(name, func(t *testing.T) {
			h := newMFAHarness(t)
			a, _ := h.approve()
			change(h)
			if got := h.d.Execute(t.Context(), a); got != audit.OutcomeDeniedPolicy {
				t.Fatalf("Execute = %s", got)
			}
			if o := h.lastOutcome(); o.Error.String != "account not eligible" {
				t.Errorf("reason = %q, want account not eligible", o.Error.String)
			}
			if len(h.graphWrites()) != 0 {
				t.Errorf("writes %v", h.graphWrites())
			}
		})
	}
}

// covers: spec 0012 AC-11. With a phone as the default (signInPreferences
// sms), that phone is deleted last and the Authenticator before it.
func TestMFAClearDeletesADefaultPhoneLast(t *testing.T) {
	h := newMFAHarness(t)
	h.graph.SetSignInPreference(h.evaID, "sms") // the mobile phone is the default
	a, _ := h.approve()
	if got := h.d.Execute(t.Context(), a); got != audit.OutcomeOK {
		t.Fatalf("Execute = %s", got)
	}
	w := h.graphWrites()
	if len(w) != 3 || !strings.HasSuffix(w[2], "/phoneMethods/"+graphtest.PhoneID("mobile")) {
		t.Errorf("deletes = %v, want the mobile phone last", w)
	}
}

// covers: spec 0012 AC-11. A phone of a type outside the fixed order is
// still cleared, before every other method.
func TestMFAClearUnknownPhoneTypeGoesFirst(t *testing.T) {
	h := newMFAHarness(t)
	h.graph.SetPhones(h.evaID, []graphtest.Phone{
		{ID: graphtest.PhoneID("mobile"), PhoneNumber: "+1 2025550114", PhoneType: "mobile"},
		{ID: "satellite-1", PhoneNumber: "+1 2025550116", PhoneType: "satellite"},
	})
	a, _ := h.approve()
	if got := h.d.Execute(t.Context(), a); got != audit.OutcomeOK {
		t.Fatalf("Execute = %s", got)
	}
	w := h.graphWrites()
	if len(w) != 3 || !strings.HasSuffix(w[0], "/phoneMethods/satellite-1") {
		t.Errorf("deletes = %v, want the unknown phone type first", w)
	}
	if len(h.graph.Phones(h.evaID)) != 0 || len(h.graph.Authenticators(h.evaID)) != 0 {
		t.Errorf("left: %v %v", h.graph.Phones(h.evaID), h.graph.Authenticators(h.evaID))
	}
	if res := jsonOf(t, h.lastOutcome().Result); res["cleared_phone"] != float64(2) || res["cleared_authenticator"] != float64(1) {
		t.Errorf("result = %v", res)
	}
}

// covers: spec 0012 AC-11. A ticket closed since the request still gets the
// cleared note; the clear succeeds with ticket_closed false.
func TestMFAClearOnAClosedTicketStillNotes(t *testing.T) {
	h := newMFAHarness(t)
	a, ticket := h.approve()
	h.closeInZammad(ticket) // IT closed it by hand; the tool's close is guarded
	if got := h.d.Execute(t.Context(), a); got != audit.OutcomeOK {
		t.Fatalf("Execute = %s", got)
	}
	if res := jsonOf(t, h.lastOutcome().Result); res["ticket_closed"] != false {
		t.Errorf("result = %v, want ticket_closed false", res)
	}
	arts := h.zammad.Articles(ticket)
	if last := arts[len(arts)-1]; last["body"] != "MFA methods cleared after IT approval by "+itApprover+"." || last["internal"] != true {
		t.Errorf("last note = %v", last)
	}
	if h.status(a) != "executed" {
		t.Errorf("status = %s", h.status(a))
	}
}

// covers: spec 0012 AC-11, AC-12. When an abandon ends the approval while
// the clear runs, the mark finds no approved row: it logs
// approval_mark_lost and leaves the abandon's failed status standing.
func TestMFAClearMarkLostToAnAbandon(t *testing.T) {
	h := newMFAHarness(t)
	a, _ := h.approve()
	h.d.beforeHandler = func() {
		row, _ := h.store.Approval(a.ID)
		row.Status = "failed"
		h.store.SetApproval(row)
	}
	h.d.Execute(t.Context(), a)
	if !strings.Contains(h.logs.String(), `"msg":"approval_mark_lost"`) {
		t.Errorf("no approval_mark_lost log: %s", h.logs.String())
	}
	if h.status(a) != "failed" {
		t.Errorf("status = %s, want the abandon's failed", h.status(a))
	}
}

// covers: spec 0012 AC-24. The close guard reads this thread only: an
// approval pending in another thread never blocks this thread's close.
func TestMFACloseGuardIgnoresOtherThreads(t *testing.T) {
	h := newMFAHarness(t)
	if resp, _ := h.request(); resp.Code != CodePendingApproval {
		t.Fatalf("request = %+v", resp)
	}
	other := eva()
	other.ThreadTS = "1760000000.000300"
	ticket := h.createTicket(other)
	if r := h.call(ToolTicketUpdate, `{"state":"closed"}`, withTicket(other, ticket)); r.IsError {
		t.Errorf("close in another thread = %+v", r)
	}
	if tk, _ := h.zammad.Ticket(ticket); tk["state"] != "closed" {
		t.Errorf("ticket = %v", tk["state"])
	}
}

// closeInZammad closes a ticket the way a person does in Zammad, past the
// tool layer's close guard.
func (h *mfaHarness) closeInZammad(id int) {
	h.t.Helper()
	req, err := http.NewRequestWithContext(h.t.Context(), http.MethodPut,
		h.zammad.URL()+"/api/v1/tickets/"+strconv.Itoa(id), strings.NewReader(`{"state":"closed"}`))
	if err != nil {
		h.t.Fatal(err)
	}
	req.Header.Set("Authorization", "Token token="+zammadtest.Token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	_ = resp.Body.Close()
	if tk, _ := h.zammad.Ticket(id); tk["state"] != "closed" {
		h.t.Fatalf("close in Zammad: status %d, ticket %v", resp.StatusCode, tk["state"])
	}
}
