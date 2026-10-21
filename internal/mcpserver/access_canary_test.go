package mcpserver_test

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/toyinogun/AIITsupport/internal/canary"
	"github.com/toyinogun/AIITsupport/internal/logging"
	"github.com/toyinogun/AIITsupport/internal/mcpclient"
	"github.com/toyinogun/AIITsupport/internal/mcpserver"
	"github.com/toyinogun/AIITsupport/internal/telemetry"
	"github.com/toyinogun/AIITsupport/internal/tools"
)

// covers: spec 0013 AC-21. A search, a request, the owner's view, the
// owner's approve with their email, and a cancel, over MCP and the approval
// routes, logged through logging.New and exported through
// NewRedactingExporter: no log line or span holds a group name, the reason,
// a search term, an email, an account, or a Slack id, and no audit row holds
// the reason or the term.
func TestCanaryAccessRequest(t *testing.T) {
	const (
		owner     = "U0ILSE0001"
		ownerMail = "ilse.smit@noord.example"
		term      = "zqcanaryterm"
		reason    = "zqcanaryreason for the month end close"
	)
	hunt, err := canary.Seed(filepath.Join("..", "..", "seed", "directory", "noord.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	noor := func(thread string) mcpclient.Requester {
		return mcpclient.Requester{SlackID: "U0NOOR0001", Email: "noor.elamrani@noord.example", ChannelID: "D0NOOR", ThreadTS: thread}
	}
	hunt.AnyCase(noor("").SlackID)
	hunt.AnyCase(owner)
	for _, s := range []string{term, reason, "SG-Finance", "APP-ERP-Finance", "SHR-Finance-RW", "Finance_Users", "fin-readonly"} {
		hunt.Text(s)
	}

	exported := tracetest.NewInMemoryExporter()
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSyncer(telemetry.NewRedactingExporter(exported))))
	t.Cleanup(func() { otel.SetTracerProvider(prev) })

	logs := &bytes.Buffer{}
	m := startMFA(t, logging.New(logs, "debug", "toolserver"), "U0ITLEAD01", "auth-1", "+1 2025550114")
	m.zammad.AddUser("noor.elamrani")
	c, err := mcpclient.New(m.srv.URL+mcpserver.Path, token)
	if err != nil {
		t.Fatal(err)
	}
	ac, err := mcpclient.NewApprovalClient(m.srv.URL+mcpserver.Path, token)
	if err != nil {
		t.Fatal(err)
	}
	request := func(thread, group string) uuid.UUID {
		t.Helper()
		r := noor(thread)
		created, err := c.Call(t.Context(), r, tools.ToolTicketCreate, map[string]any{"title": "Access", "body": "Needs a group."})
		if err != nil || created.IsError {
			t.Fatalf("ticket_create = %+v, %v", created, err)
		}
		r.TicketID = int64(created.Data["ticket_id"].(float64))
		if found, err := c.Call(t.Context(), r, tools.ToolGroupSearch, map[string]any{"terms": []string{"fin", term}}); err != nil || found.IsError {
			t.Fatalf("group_search = %+v, %v", found, err)
		}
		pending, err := c.Call(t.Context(), r, tools.ToolGroupAccessRequest, map[string]any{"group": group, "reason": reason})
		if err != nil || pending.Code != string(tools.CodePendingApproval) {
			t.Fatalf("group_access_request = %+v, %v", pending, err)
		}
		return uuid.MustParse(pending.Data["approval_id"].(string))
	}

	approved := request("1792400000.000100", "SG-Finance")
	if v, err := ac.Get(t.Context(), approved); err != nil || v.Reason != reason || v.ApproverEmail != ownerMail {
		t.Fatalf("view = %+v, %v", v, err)
	}
	decided, err := ac.Decide(t.Context(), approved, owner, ownerMail, true)
	if err != nil || decided.Approval.Status != mcpclient.StatusExecuted {
		t.Fatalf("decide = %+v, %v", decided, err)
	}
	cancelled := request("1792400000.000200", "APP-ERP-Finance")
	if v, err := ac.Cancel(t.Context(), cancelled); err != nil || v.CancelReason != mcpclient.CancelOwnerUnreachable {
		t.Fatalf("cancel = %+v, %v", v, err)
	}

	out := logs.String()
	for _, want := range []string{`"tool":"group_search"`, `"tool":"group_access_request"`, `"msg":"approval_decided"`, `"msg":"approval_cancelled"`} {
		if !strings.Contains(out, want) {
			t.Errorf("the flow did not run: no %s in\n%s", want, out)
		}
	}
	values := append(canary.LogValues(logs.Bytes()), canary.SpanValues(exported.GetSpans())...)
	if leaks := hunt.Leaks(values); len(leaks) > 0 {
		t.Errorf("logs or spans hold a group, the reason, a term, an email, an account, or a Slack id:\n%s\nlogs:\n%s",
			strings.Join(leaks, "\n"), out)
	}
	for _, row := range m.store.All() {
		stored := string(row.Arguments) + string(row.Result) + row.Error.String + row.Target.String
		if strings.Contains(stored, term) || strings.Contains(stored, "zqcanaryreason") {
			t.Errorf("audit row %d (%s %s) holds the term or the reason: %s", row.ID, row.ToolName, row.Kind, stored)
		}
	}
}
