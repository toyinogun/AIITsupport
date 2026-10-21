package approvalapi_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/toyinogun/AIITsupport/internal/approvalapi"
	"github.com/toyinogun/AIITsupport/internal/audit"
	"github.com/toyinogun/AIITsupport/internal/db/queries"
	"github.com/toyinogun/AIITsupport/internal/graph"
	"github.com/toyinogun/AIITsupport/internal/graph/graphtest"
	"github.com/toyinogun/AIITsupport/internal/mcpclient"
	"github.com/toyinogun/AIITsupport/internal/tools"
	"github.com/toyinogun/AIITsupport/internal/tools/toolstest"
	"github.com/toyinogun/AIITsupport/internal/zammad"
	"github.com/toyinogun/AIITsupport/internal/zammad/zammadtest"
)

const (
	token    = "test-token-0123456789abcdefghijk"
	approver = "U0ITLEAD01"
	other    = "U0OTHER001"
)

type env struct {
	t      *testing.T
	d      *tools.Dispatcher
	store  *toolstest.MemStore
	graph  *graphtest.Fake
	dir    *toolstest.Directory
	evaID  string
	client *mcpclient.ApprovalClient
	srv    *httptest.Server
	// served gets each route request's context as it arrives.
	served chan context.Context
}

func newEnv(t *testing.T) *env {
	t.Helper()
	e := &env{t: t, store: &toolstest.MemStore{}, graph: graphtest.New(t), served: make(chan context.Context, 16)}
	logger := slog.New(slog.DiscardHandler)
	rec, err := audit.NewRecorder(e.store, audit.Config{WriteTimeout: time.Second, MaxPayloadBytes: 4096}, logger)
	if err != nil {
		t.Fatal(err)
	}
	z := zammadtest.New(t)
	z.AddUser("eva.peters")
	z.AddUser("noor.elamrani")
	zc, err := zammad.New(zammad.Options{BaseURL: z.URL(), Auth: zammad.TokenAuth(zammadtest.Token), Timeout: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	gc, err := graph.New(graph.Options{BaseURL: e.graph.URL(), Token: graph.StaticToken(graphtest.Token), Timeout: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	e.evaID = e.graph.AddUser(map[string]any{"mailNickname": "eva.peters", "employeeType": graph.ManagedMarker})
	e.graph.SetAuthenticators(e.evaID, "auth-1")
	e.graph.SetPhones(e.evaID, []graphtest.Phone{{ID: graphtest.PhoneID("mobile"), PhoneNumber: "+1 2025550114", PhoneType: "mobile"}})

	dir := toolstest.NewDirectory()
	e.dir = dir
	reg, err := tools.NewRegistry(8*time.Hour, tools.WithMFAClear([]string{approver, "U01EVA0001"}, 30*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	ks := t.TempDir() + "/ks"
	if err := writeFile(ks, "read_write"); err != nil {
		t.Fatal(err)
	}
	e.d, err = tools.NewDispatcher(reg, tools.Config{
		BaseDN: toolstest.Base, UserOUs: []string{"OU=Users,OU=Noord"}, GroupOUs: []string{"OU=Groups,OU=Noord"},
		CallTimeout: 5 * time.Second, WriteLimit: 3, WriteWindow: time.Hour, TicketCreateLimit: 5,
		TicketMaxPending: time.Hour, PrivilegedWindow: 24 * time.Hour,
		ExecuteTimeout: 5 * time.Second, ExecuteGrace: 5 * time.Minute, AccessMaxPending: 3,
		KB: tools.KBSettings{SearchLimit: 3, SearchCandidates: 10, StaleAfterDays: 730, BodyMaxRunes: 2000, Zone: time.UTC},
	}, tools.Systems{
		Directory: dir, Groups: dir, Reader: dir, Writer: dir, Helpdesk: tools.NewHelpdesk(zc, "IT Support", "http://zammad.test"),
		Recorder: rec, KillSwitch: tools.NewKillSwitch(ks, true),
		MFA: &tools.MFASystem{Graph: gc, Reader: dir, DeniedGroups: []string{"SG-IT-Admins"}},
	}, logger)
	if err != nil {
		t.Fatal(err)
	}
	h, err := approvalapi.Handler(e.d, rec, approvalapi.Options{Token: token, MaxRequestBytes: 4096, Logger: logger})
	if err != nil {
		t.Fatal(err)
	}
	seen := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case e.served <- r.Context():
		default:
		}
		h.ServeHTTP(w, r)
	})
	mux := http.NewServeMux()
	mux.Handle(mcpclient.ApprovalsPath, seen)
	mux.Handle(mcpclient.ApprovalsPath+"/", seen)
	e.srv = httptest.NewServer(mux)
	t.Cleanup(e.srv.Close)
	if e.client, err = mcpclient.NewApprovalClient(e.srv.URL+"/mcp", token); err != nil {
		t.Fatal(err)
	}
	return e
}

// request has eva (who is also listed as an approver) ask for a clear and
// returns the approval id.
func (e *env) request() uuid.UUID {
	e.t.Helper()
	r := tools.Requester{SlackID: "U01EVA0001", Email: "eva.peters@noord.example", ChannelID: "C01HELP", ThreadTS: "1760000000.000100"}
	tk := e.d.Call(e.t.Context(), tools.Request{Tool: tools.ToolTicketCreate, Args: []byte(`{"title":"New phone","body":"Authenticator gone."}`), Requester: r})
	r.TicketID = int64(tk.Data["ticket_id"].(int))
	resp := e.d.Call(e.t.Context(), tools.Request{Tool: tools.ToolMFAClear, Args: []byte(`{}`), Requester: r})
	if resp.Code != tools.CodePendingApproval {
		e.t.Fatalf("request = %+v", resp)
	}
	return uuid.MustParse(resp.Data["approval_id"].(string))
}

// covers: spec 0012 AC-10, AC-11, AC-13. An approve runs the clear before
// the answer, which carries the executed view with its counts.
func TestApproveExecutes(t *testing.T) {
	e := newEnv(t)
	id := e.request()

	v, err := e.client.Get(t.Context(), id)
	if err != nil || v.Status != mcpclient.StatusPending || v.RequesterAccount != "eva.peters" || v.Execution != nil ||
		v.TicketURL == "" || !strings.HasPrefix(v.TicketURL, "http://zammad.test/#ticket/zoom/") {
		t.Fatalf("pending view = %+v, %v", v, err)
	}
	res, err := e.client.Decide(t.Context(), id, approver, "", true)
	if err != nil || !res.Moved || res.Approval.Status != mcpclient.StatusExecuted || res.Approval.DecidedBy != approver {
		t.Fatalf("decide = %+v, %v", res, err)
	}
	ex := res.Approval.Execution
	if ex == nil || ex.Outcome != "ok" || ex.Result["cleared_authenticator"] != float64(1) || ex.Result["cleared_phone"] != float64(1) ||
		ex.Result["ticket_closed"] != true {
		t.Errorf("execution = %+v", ex)
	}
	if len(e.graph.Authenticators(e.evaID)) != 0 || len(e.graph.Phones(e.evaID)) != 0 {
		t.Error("methods left after the approve")
	}
	list, err := e.client.Since(t.Context(), time.Now().Add(-time.Hour))
	if err != nil || len(list) != 1 || list[0].ID != id || list[0].Status != mcpclient.StatusExecuted || list[0].Execution != nil {
		t.Errorf("since = %+v, %v", list, err)
	}
}

// covers: spec 0012 AC-9, AC-18. Self, a non approver, and a second click
// are refused with their tokens and change nothing; the requester may deny.
func TestRefusedClicks(t *testing.T) {
	e := newEnv(t)
	id := e.request()
	for clicker, want := range map[string]string{"U01EVA0001": mcpclient.ReasonSelf, other: mcpclient.ReasonNotAllowed} {
		res, err := e.client.Decide(t.Context(), id, clicker, "", true)
		if err != nil || res.Moved || res.Reason != want || res.Approval.Status != mcpclient.StatusPending {
			t.Errorf("%s approve = %+v, %v; want %s", clicker, res, err, want)
		}
	}
	res, err := e.client.Decide(t.Context(), id, "U01EVA0001", "", false)
	if err != nil || !res.Moved || res.Approval.Status != mcpclient.StatusDenied {
		t.Fatalf("own deny = %+v, %v", res, err)
	}
	res, err = e.client.Decide(t.Context(), id, approver, "", true)
	if err != nil || res.Moved || res.Reason != mcpclient.ReasonDecided {
		t.Errorf("late approve = %+v, %v", res, err)
	}
	if len(e.graph.Authenticators(e.evaID)) != 1 {
		t.Error("a refused or denied click cleared a method")
	}
}

// covers: spec 0012 AC-10, AC-13. Unknown ids, bad bodies, a bad since, and
// a missing token.
func TestRouteErrors(t *testing.T) {
	e := newEnv(t)
	if _, err := e.client.Get(t.Context(), uuid.New()); !errors.Is(err, mcpclient.ErrApprovalNotFound) {
		t.Errorf("get unknown: %v", err)
	}
	if _, err := e.client.Decide(t.Context(), uuid.New(), approver, "", true); !errors.Is(err, mcpclient.ErrApprovalNotFound) {
		t.Errorf("decide unknown: %v", err)
	}
	id := e.request()
	for name, body := range map[string]string{
		"extra key":      `{"clicker":"U0ITLEAD01","approve":true,"as":"x"}`,
		"no approve":     `{"clicker":"U0ITLEAD01"}`,
		"bad clicker":    `{"clicker":"robert","approve":true}`,
		"string bool":    `{"clicker":"U0ITLEAD01","approve":"true"}`,
		"two documents":  `{"clicker":"U0ITLEAD01","approve":true}{}`,
		"trailing brace": `{"clicker":"U0ITLEAD01","approve":true}}`,
	} {
		if got := e.post(t, "/approvals/"+id.String()+"/decision", body, token); got != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", name, got)
		}
	}
	if got := e.post(t, "/approvals/"+id.String()+"/decision", `{"clicker":"U0ITLEAD01","approve":true}`, "wrong"); got != http.StatusUnauthorized {
		t.Errorf("bad token: %d", got)
	}
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, e.srv.URL+"/approvals?since=yesterday", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("bad since: %d", resp.StatusCode)
	}
	if v, _ := e.client.Get(t.Context(), id); v.Status != mcpclient.StatusPending {
		t.Errorf("a refused request moved the approval: %s", v.Status)
	}
}

func (e *env) post(t *testing.T, path, body, tok string) int {
	t.Helper()
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, e.srv.URL+path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	return resp.StatusCode
}

func writeFile(path, content string) error { return os.WriteFile(path, []byte(content), 0o600) }

// covers: spec 0012 AC-12, AC-13. Expire ends an overdue pending approval
// and abandons an approved one decided longer ago than the grace, with an
// approval.abandon pair; the abandoned view says the execution was not
// confirmed, and nothing is cleared.
func TestExpireAndAbandon(t *testing.T) {
	e := newEnv(t)
	stuck := e.request()
	a, _ := e.store.Approval(stuck)
	a.Status, a.DecidedBy = "approved", pgtype.Text{String: approver, Valid: true}
	a.DeciderSlackID = a.DecidedBy
	a.DecidedAt = pgtype.Timestamptz{Time: time.Now().Add(-10 * time.Minute), Valid: true}
	e.store.SetApproval(a)
	overdue, err := e.store.CreateApproval(t.Context(), queries.CreateApprovalParams{
		ToolName: tools.ToolMFAClear, Arguments: []byte(`{}`), RiskTier: "medium", RequesterSlackID: "U01EVA0001",
		SlackChannelID: "C01HELP", SlackThreadTs: "1760000000.000200", ApproverKind: "it",
		AllowedApprovers: []string{approver}, ExpiresAt: time.Now().Add(-time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}

	got, err := e.client.Expire(t.Context())
	if err != nil || got.Expired != 1 || got.Abandoned != 1 {
		t.Fatalf("expire = %+v, %v; want one of each", got, err)
	}
	if again, err := e.client.Expire(t.Context()); err != nil || again.Expired != 0 || again.Abandoned != 0 {
		t.Errorf("second expire = %+v, %v", again, err)
	}
	if v, err := e.client.Get(t.Context(), overdue.ID); err != nil || v.Status != mcpclient.StatusExpired {
		t.Errorf("overdue view = %+v, %v", v, err)
	}
	v, err := e.client.Get(t.Context(), stuck)
	if err != nil || v.Status != mcpclient.StatusFailed || v.Execution == nil ||
		v.Execution.Outcome != "error" || v.Execution.Reason != audit.ReasonExecutionNotConfirmed {
		t.Errorf("abandoned view = %+v (%+v), %v", v, v.Execution, err)
	}
	rows := e.store.All()
	last := rows[len(rows)-1]
	if last.ToolName != audit.ToolApprovalAbandon || last.ApproverSlackID.String != approver || last.Target.String != "eva.peters" {
		t.Errorf("last row = %s approver %q target %q", last.ToolName, last.ApproverSlackID.String, last.Target.String)
	}
	if len(e.graph.Authenticators(e.evaID)) != 1 {
		t.Error("an abandon ran the clear")
	}
	if got := e.post(t, "/approvals/expire", "", "wrong"); got != http.StatusUnauthorized {
		t.Errorf("bad token: %d", got)
	}
}

// covers: spec 0012 AC-18, AC-10. A click after expires_at is refused with
// the expired token, the approval is not moved, and nothing is cleared.
func TestLateClickIsRefusedExpired(t *testing.T) {
	e := newEnv(t)
	id := e.request()
	a, _ := e.store.Approval(id)
	a.ExpiresAt = time.Now().Add(-time.Minute)
	e.store.SetApproval(a)

	res, err := e.client.Decide(t.Context(), id, approver, "", true)
	if err != nil || res.Moved || res.Reason != mcpclient.ReasonExpired {
		t.Fatalf("late approve = %+v, %v; want refused %s", res, err, mcpclient.ReasonExpired)
	}
	if got, _ := e.store.Approval(id); got.Status != mcpclient.StatusPending {
		t.Errorf("status = %s, want still pending until the expire route ends it", got.Status)
	}
	if len(e.graph.Authenticators(e.evaID)) != 1 || len(e.graph.Phones(e.evaID)) != 1 {
		t.Error("a late click cleared a method")
	}
	rows := e.store.All()
	if last := rows[len(rows)-1]; last.Outcome.String != "denied_policy" || last.ApproverSlackID.String != approver {
		t.Errorf("last row = %s %s approver %q", last.ToolName, last.Outcome.String, last.ApproverSlackID.String)
	}
}

// covers: spec 0012 AC-10. An approve whose client goes away mid clear
// still finishes the clear: the execution runs detached from the request.
func TestApproveFinishesWhenTheClientLeaves(t *testing.T) {
	e := newEnv(t)
	id := e.request()
	for len(e.served) > 0 {
		<-e.served // the request's own route calls, if any
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var once bool
	e.graph.Intercept = func(r *http.Request) (graphtest.Fault, bool) {
		if r.Method != http.MethodDelete || once {
			return graphtest.Fault{}, false
		}
		once = true
		cancel() // the agent gives up while the first delete is in flight
		select {
		case srv := <-e.served:
			select {
			case <-srv.Done():
			case <-time.After(2 * time.Second):
				t.Error("the route's request context never ended")
			}
		case <-time.After(2 * time.Second):
			t.Error("no route request was seen")
		}
		return graphtest.Fault{}, false
	}

	if _, err := e.client.Decide(ctx, id, approver, "", true); err == nil {
		t.Fatal("decide answered although the client went away")
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		v, err := e.client.Get(t.Context(), id)
		if err == nil && v.Status == mcpclient.StatusExecuted {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("view = %+v, %v; want executed after the client left", v, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(e.graph.Authenticators(e.evaID)) != 0 || len(e.graph.Phones(e.evaID)) != 0 {
		t.Error("the clear stopped halfway when the client left")
	}
}
