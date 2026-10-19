package agent_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
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

// causeError is an adapter error that names its own fixed cause token.
type causeError struct{ token string }

func (e causeError) Error() string { return "ZEBRA " + e.token }
func (e causeError) Cause() string { return e.token }

// covers: the review's major finding (2026-10-19) and AC-17. A failure's
// fixed cause token reaches agent_escalated, agent_post_failed, a failed
// users.info, and the llm.call and agent.turn spans; its text never does.
func TestFailureCausesAreFixedTokens(t *testing.T) {
	rec := recorder()
	before := len(rec.Ended())
	req := httptest.NewRequest(http.MethodPost, "https://api.anthropic.com/v1/messages", nil)
	denied := &anthropic.Error{StatusCode: 401, Request: req, Response: &http.Response{StatusCode: 401, Request: req}}

	model := newHarness(t, agenttest.NewModel(agenttest.Fail(denied)))
	model.send(dm("Ev1", "ZEBRA locked out"))
	slackDown := newHarness(t, agenttest.NewModel())
	slackDown.users.Err = causeError{"slack_missing_scope"}
	slackDown.poster.Err = causeError{"slack_rate_limited"}
	slackDown.send(dm("Ev2", "ZEBRA locked out"))
	noAnswer := newHarness(t, agenttest.NewModel())
	noAnswer.tools.Set("user_lookup", agenttest.NoAnswer())
	noAnswer.send(dm("Ev3", "ZEBRA locked out"))

	logs := model.logs.String() + slackDown.logs.String() + noAnswer.logs.String()
	for _, want := range []string{
		`"msg":"agent_escalated","event_id":"Ev1","reason":"model_unavailable","ticket":true,"cause":"anthropic_401"`,
		`"msg":"agent_slack_profile_failed","event_id":"Ev2","cause":"slack_missing_scope"`,
		`"msg":"agent_post_failed","event_id":"Ev2","kind":"ack","cause":"slack_rate_limited"`,
		`"msg":"agent_escalated","event_id":"Ev3","reason":"toolserver_unavailable","ticket":false,"cause":"other"`,
	} {
		if !strings.Contains(logs, want) {
			t.Errorf("logs lack %s:\n%s", want, logs)
		}
	}
	var llmCause, turnCause bool
	for _, s := range rec.Ended()[before:] {
		for _, a := range s.Attributes() {
			if a.Key == "cause" && a.Value.AsString() == "anthropic_401" {
				llmCause = llmCause || s.Name() == "llm.call"
				turnCause = turnCause || s.Name() == "agent.turn"
			}
			if strings.Contains(a.Value.String(), "ZEBRA") {
				t.Errorf("span %s attribute %s holds the error text", s.Name(), a.Key)
			}
		}
	}
	if !llmCause || !turnCause {
		t.Errorf("cause on llm.call = %v, on agent.turn = %v; want both", llmCause, turnCause)
	}
	if strings.Contains(logs, "ZEBRA") {
		t.Errorf("logs hold the error or message text:\n%s", logs)
	}
}
