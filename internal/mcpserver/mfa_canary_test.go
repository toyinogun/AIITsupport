package mcpserver_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/toyinogun/AIITsupport/internal/approvalapi"
	"github.com/toyinogun/AIITsupport/internal/audit"
	"github.com/toyinogun/AIITsupport/internal/canary"
	"github.com/toyinogun/AIITsupport/internal/graph"
	"github.com/toyinogun/AIITsupport/internal/graph/graphtest"
	"github.com/toyinogun/AIITsupport/internal/logging"
	"github.com/toyinogun/AIITsupport/internal/mcpclient"
	"github.com/toyinogun/AIITsupport/internal/mcpserver"
	"github.com/toyinogun/AIITsupport/internal/server"
	"github.com/toyinogun/AIITsupport/internal/telemetry"
	"github.com/toyinogun/AIITsupport/internal/tools"
	"github.com/toyinogun/AIITsupport/internal/tools/toolstest"
	"github.com/toyinogun/AIITsupport/internal/zammad"
	"github.com/toyinogun/AIITsupport/internal/zammad/zammadtest"
)

// covers: spec 0012 AC-22. A request over MCP, then an Approve through the
// approval route that runs the clear, logged through logging.New and
// exported through NewRedactingExporter: no log line or span holds the
// account, a seed name, a Slack id, a phone number, a method id, or the
// Authenticator's device name, and the phone number, method ids, and device
// name appear in no audit row, MCP result, route answer, or ticket note.
func TestCanaryMFAClear(t *testing.T) {
	const (
		approver = "U0ITLEAD01"
		phone    = "+31 6 4417 2093"
		authID   = "canary-auth-7f3e"
		title    = "New phone, Authenticator gone"
		body     = "Requester has a new phone; asking IT to clear MFA."
	)
	hunt, err := canary.Seed(filepath.Join("..", "..", "seed", "directory", "noord.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	r := requester()
	hunt.AnyCase(r.SlackID)
	hunt.AnyCase(approver)
	for _, s := range []string{title, body, phone, "44172093", authID} {
		hunt.Text(s)
	}

	exported := tracetest.NewInMemoryExporter()
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSyncer(telemetry.NewRedactingExporter(exported))))
	t.Cleanup(func() { otel.SetTracerProvider(prev) })

	logs := &bytes.Buffer{}
	m := startMFA(t, logging.New(logs, "debug", "toolserver"), approver, authID, phone)
	store, fakeZ, fakeG, evaID := m.store, m.zammad, m.graph, m.evaID
	device := "Device of " + evaID // the fake's Authenticator device name
	hunt.Text(device)
	srv := m.srv

	c, err := mcpclient.New(srv.URL+mcpserver.Path, token)
	if err != nil {
		t.Fatal(err)
	}
	created, err := c.Call(t.Context(), r, tools.ToolTicketCreate, map[string]any{"title": title, "body": body})
	if err != nil || created.IsError {
		t.Fatalf("ticket_create = %+v, %v", created, err)
	}
	r.TicketID = int64(created.Data["ticket_id"].(float64))
	pending, err := c.Call(t.Context(), r, tools.ToolMFAClear, map[string]any{})
	if err != nil || pending.Code != string(tools.CodePendingApproval) {
		t.Fatalf("mfa_clear = %+v, %v", pending, err)
	}
	id := uuid.MustParse(pending.Data["approval_id"].(string))

	ac, err := mcpclient.NewApprovalClient(srv.URL+mcpserver.Path, token)
	if err != nil {
		t.Fatal(err)
	}
	decided, err := ac.Decide(t.Context(), id, approver, "", true)
	if err != nil || decided.Approval.Status != mcpclient.StatusExecuted {
		t.Fatalf("decide = %+v, %v", decided, err)
	}
	if len(fakeG.Authenticators(evaID)) != 0 || len(fakeG.Phones(evaID)) != 0 {
		t.Fatal("the approve left methods")
	}

	out := logs.String()
	for _, want := range []string{`"tool":"mfa_clear"`, `"msg":"approval_decided"`, `"outcome":"ok"`} {
		if !strings.Contains(out, want) {
			t.Errorf("the flow did not run: no %s in\n%s", want, out)
		}
	}
	spans := exported.GetSpans()
	names := map[string]bool{}
	for _, s := range spans {
		names[s.Name] = true
	}
	if !names["approval.decide"] || !names["tool.call"] {
		t.Errorf("spans = %v, want tool.call and approval.decide", names)
	}
	values := append(canary.LogValues(logs.Bytes()), canary.SpanValues(spans)...)
	if leaks := hunt.Leaks(values); len(leaks) > 0 {
		t.Errorf("logs or spans hold personal data, a phone, a method id, a device name, or [redacted]:\n%s\nlogs:\n%s",
			strings.Join(leaks, "\n"), out)
	}

	secrets := []string{phone, "44172093", authID, graphtest.PhoneID("mobile"), device}
	answers := []any{pending, decided}
	for _, row := range store.All() {
		answers = append(answers, string(row.Arguments)+string(row.Result)+row.Error.String+row.Target.String)
	}
	for _, a := range store.Approvals {
		answers = append(answers, string(a.Arguments))
	}
	for _, art := range fakeZ.Articles(int(r.TicketID)) {
		answers = append(answers, art["body"])
	}
	for i, a := range answers {
		raw, err := json.Marshal(a)
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range secrets {
			if strings.Contains(string(raw), s) {
				t.Errorf("answer %d (an MCP result, route answer, audit row, approval, or note) holds %q", i, s)
			}
		}
	}
}

// mfaServer is the tool server with mfa_clear registered, mounted the way
// cmd/toolserver mounts it: /mcp and the approval routes on one mux behind
// the token, over fakes. eva.peters has one Authenticator and one phone.
type mfaServer struct {
	srv    *httptest.Server
	store  *toolstest.MemStore
	zammad *zammadtest.Fake
	graph  *graphtest.Fake
	evaID  string
}

func startMFA(t *testing.T, logger *slog.Logger, approver, authID, phone string) *mfaServer {
	t.Helper()
	m := &mfaServer{store: &toolstest.MemStore{}, zammad: zammadtest.New(t), graph: graphtest.New(t)}
	rec, err := audit.NewRecorder(m.store, audit.Config{WriteTimeout: time.Second, MaxPayloadBytes: 4096}, logger)
	if err != nil {
		t.Fatal(err)
	}
	m.zammad.AddUser("eva.peters")
	zc, err := zammad.New(zammad.Options{BaseURL: m.zammad.URL(), Auth: zammad.TokenAuth(zammadtest.Token), Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	m.evaID = m.graph.AddUser(map[string]any{"mailNickname": "eva.peters", "employeeType": graph.ManagedMarker})
	m.graph.SetAuthenticators(m.evaID, authID)
	m.graph.SetPhones(m.evaID, []graphtest.Phone{{ID: graphtest.PhoneID("mobile"), PhoneNumber: phone, PhoneType: "mobile"}})
	gc, err := graph.New(graph.Options{BaseURL: m.graph.URL(), Token: graph.StaticToken(graphtest.Token), Timeout: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	dir := &pageDir{Directory: toolstest.NewDirectory()}
	reg, err := tools.NewRegistry(8*time.Hour, tools.WithMFAClear([]string{approver}, 30*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	d, err := tools.NewDispatcher(reg, tools.Config{
		BaseDN: toolstest.Base, UserOUs: []string{"OU=Users,OU=Noord"}, GroupOUs: []string{"OU=Groups,OU=Noord"},
		CallTimeout: 5 * time.Second, WriteLimit: 3, WriteWindow: time.Hour, TicketCreateLimit: 5, TicketMaxPending: 720 * time.Hour,
		PrivilegedWindow: 24 * time.Hour,
		ExecuteTimeout:   5 * time.Second, ExecuteGrace: 5 * time.Minute, AccessMaxPending: 3,
	}, tools.Systems{
		Directory: dir, Groups: dir, Reader: dir, Writer: dir, Helpdesk: tools.NewHelpdesk(zc, "IT Support", "http://zammad.test"), Recorder: rec,
		KillSwitch: tools.NewKillSwitch("", false),
		MFA: &tools.MFASystem{
			Graph: gc, Reader: dir, DeniedGroups: []string{"SG-IT-Admins", "APP-Helpdesk-Agents"},
		},
	}, logger)
	if err != nil {
		t.Fatal(err)
	}
	mcp, err := mcpserver.Handler(d, mcpserver.Options{Token: token, MaxRequestBytes: 4096, Logger: logger})
	if err != nil {
		t.Fatal(err)
	}
	approvals, err := approvalapi.Handler(d, rec, approvalapi.Options{
		Token: token, MaxRequestBytes: 4096, Logger: logger,
	})
	if err != nil {
		t.Fatal(err)
	}
	mux := server.NewMux()
	mux.Handle(mcpserver.Path, mcp)
	mux.Handle(mcpclient.ApprovalsPath, approvals)
	mux.Handle(mcpclient.ApprovalsPath+"/", approvals)
	m.srv = httptest.NewServer(mux)
	t.Cleanup(m.srv.Close)
	return m
}

// covers: spec 0012 AC-2, AC-18. With mfa_clear registered, the model's
// tools/list gains only mfa_clear: no approval route is a tool, and calling
// one by name over MCP decides nothing.
func TestApprovalRoutesAreNotTools(t *testing.T) {
	m := startMFA(t, slog.New(slog.DiscardHandler), "U0ITLEAD01", "auth-1", "+1 2025550114")
	c, err := mcpclient.New(m.srv.URL+mcpserver.Path, token)
	if err != nil {
		t.Fatal(err)
	}
	list, err := c.ListTools(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range list {
		names = append(names, tool.Name)
	}
	slices.Sort(names)
	want := []string{
		"account_unlock", "escalate", "group_access_request", "group_search", "mfa_clear", "privileged_access_change",
		"ticket_create", "ticket_update", "user_lookup",
	}
	if !slices.Equal(names, want) {
		t.Errorf("tools = %v, want %v", names, want)
	}
	r := requester()
	for _, name := range []string{"approval.approve", "approval.deny", "approval.expire", "approval_decide", "approvals"} {
		res, err := c.Call(t.Context(), r, name, map[string]any{"approve": true})
		if err == nil && !res.IsError {
			t.Errorf("%s answered %+v", name, res)
		}
	}
	if n := len(m.store.Approvals); n != 0 {
		t.Errorf("%d approvals created", n)
	}
	for _, row := range m.store.All() {
		if strings.HasPrefix(row.ToolName, "approval.") {
			t.Errorf("an approval move was audited: %s", row.ToolName)
		}
	}
}
