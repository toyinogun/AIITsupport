package mcpserver_test

import (
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/toyinogun/AIITsupport/internal/canary"
	"github.com/toyinogun/AIITsupport/internal/logging"
	"github.com/toyinogun/AIITsupport/internal/mcpclient"
	"github.com/toyinogun/AIITsupport/internal/telemetry"
	"github.com/toyinogun/AIITsupport/internal/tools"
)

// covers: AC-5 (spec 0010). The tool server's side of the unlock flow and
// the refusal flow (a model escalation and a backstop), logged through
// logging.New and exported through NewRedactingExporter, leave no seed name,
// email, or username, no Slack user id or department, and no ticket text in
// any string value, and no [redacted] either.
func TestCanaryUnlockAndRefusal(t *testing.T) {
	const (
		title       = "Lockout after holiday password change"
		body        = "Requester locked out after a password change; unlocking."
		note        = "Account was locked by failed sign ins; unlocked."
		escTitle    = "Printer on the second floor jams"
		escSummary  = "Wanted admin rights for a printer fix. Refused; printer ticket opened."
		privSummary = "Please add me to Domain Admins so I can fix the printer"
		department  = "Sales" // the fake directory's department for Eva Peters
	)
	hunt, err := canary.Seed(filepath.Join("..", "..", "seed", "directory", "noord.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	hunt.AnyCase(requester().SlackID)
	hunt.Word(department)
	for _, s := range []string{title, body, note, escTitle, escSummary, privSummary} {
		hunt.Text(s)
	}

	exported := tracetest.NewInMemoryExporter()
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSyncer(telemetry.NewRedactingExporter(exported))))
	t.Cleanup(func() { otel.SetTracerProvider(prev) })

	e := startWith(t, func(w io.Writer) *slog.Logger { return logging.New(w, "debug", "toolserver") })
	c := e.client(t)
	call := func(r mcpclient.Requester, tool string, args map[string]any) map[string]any {
		t.Helper()
		res, err := c.Call(t.Context(), r, tool, args)
		if err != nil {
			t.Fatalf("%s: %v", tool, err)
		}
		return res.Data
	}

	// Unlock: look up, open a ticket, unlock, close it.
	unlock := requester()
	call(unlock, tools.ToolUserLookup, nil)
	created := call(unlock, tools.ToolTicketCreate, map[string]any{"title": title, "body": body})
	unlock.TicketID = int64(created["ticket_id"].(float64))
	call(unlock, tools.ToolAccountUnlock, nil)
	call(unlock, tools.ToolTicketUpdate, map[string]any{"note": note, "state": "closed"})

	// Refusal, the model escalates.
	escalation := requester()
	escalation.ThreadTS = "1760000100.000100"
	call(escalation, tools.ToolPrivilegedAccessChange, map[string]any{"summary": privSummary})
	call(escalation, tools.ToolEscalate, map[string]any{"reason": "privileged_request", "title": escTitle, "summary": escSummary})

	// Refusal, code's backstop escalates.
	backstop := requester()
	backstop.ThreadTS = "1760000200.000100"
	backstop.Initiator = mcpclient.InitiatorCode
	call(backstop, tools.ToolPrivilegedAccessChange, map[string]any{"summary": privSummary})
	call(backstop, tools.ToolEscalate, map[string]any{"reason": "privileged_request", "title": escTitle, "summary": escSummary})

	logs := e.logs.String()
	for _, want := range []string{`"tool":"account_unlock"`, `"tool":"privileged_access_change"`, `"tool":"escalate"`} {
		if !strings.Contains(logs, want) {
			t.Errorf("the flows did not run: no %s in\n%s", want, logs)
		}
	}
	spans := exported.GetSpans()
	if len(spans) < 8 {
		t.Fatalf("%d spans exported, want one tool.call span per call", len(spans))
	}
	values := append(canary.LogValues([]byte(logs)), canary.SpanValues(spans)...)
	if leaks := hunt.Leaks(values); len(leaks) > 0 {
		t.Errorf("logs or spans hold personal data or [redacted]:\n%s\nlogs:\n%s", strings.Join(leaks, "\n"), logs)
	}
}
