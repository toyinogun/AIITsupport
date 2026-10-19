package agent_test

import (
	"errors"
	"strings"
	"sync"
	"testing"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/toyinogun/AIITsupport/internal/agent/agenttest"
)

// recorder is installed once per process: the global tracer the agent took
// at init binds to the first provider set, so a second one would see nothing.
var recorder = sync.OnceValue(func() *tracetest.SpanRecorder {
	rec := tracetest.NewSpanRecorder()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec)))
	return rec
})

// covers: AC-17. Over a resolved turn and an escalated one, no log line or
// span attribute holds the message text, an email, a Slack user id, a
// display name, a department, an account name, a ticket title, or model text.
func TestLogsAndSpansHoldNoPersonalData(t *testing.T) {
	rec := recorder()
	before := len(rec.Ended())

	resolved := newHarness(t, agenttest.NewModel(
		agenttest.CallTool("t1", "ticket_create", map[string]any{"title": "Lockout ZEBRA title", "body": "ZEBRA body"}),
		agenttest.CallTool("t2", "account_unlock", nil),
		agenttest.CallTool("t3", "ticket_update", map[string]any{"note": "ZEBRA note", "state": "closed"}),
		agenttest.Answer("ZEBRA model answer"),
	))
	resolved.send(dm("Ev1", "ZEBRA I'm locked out"))
	escalated := newHarness(t, agenttest.NewModel(agenttest.Fail(errors.New("ZEBRA upstream error"))))
	escalated.send(dm("Ev2", "ZEBRA second message"))

	var seen strings.Builder
	seen.WriteString(resolved.logs.String())
	seen.WriteString(escalated.logs.String())
	spans := rec.Ended()[before:]
	if len(spans) < 2 {
		t.Fatalf("%d spans recorded, want agent.turn and llm.call spans", len(spans))
	}
	for _, s := range spans {
		seen.WriteString(s.Name() + " ")
		for _, a := range s.Attributes() {
			seen.WriteString(string(a.Key) + "=" + a.Value.String() + " ")
		}
		seen.WriteString(s.Status().Description + "\n")
	}
	out := seen.String()
	for _, secret := range []string{"ZEBRA", janEmail, janSlack, "Jan de Vries", "Operations", janUser} {
		if strings.Contains(out, secret) {
			t.Errorf("logs or spans hold %q:\n%s", secret, out)
		}
	}
	for _, want := range []string{`"msg":"agent_turn_done"`, `"outcome":"resolved"`, `"msg":"agent_escalated"`, "agent.turn", "llm.call", "stop_reason=tool_use"} {
		if !strings.Contains(out, want) {
			t.Errorf("logs or spans lack %s", want)
		}
	}
}
