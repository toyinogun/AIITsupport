package agent_test

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/toyinogun/AIITsupport/internal/agent/agenttest"
	"github.com/toyinogun/AIITsupport/internal/canary"
	"github.com/toyinogun/AIITsupport/internal/mcpclient"
)

// Texts the canary flows send or script, each hunted in the output.
const (
	canaryUnlockText   = "Hi, I'm locked out of my laptop since this morning"
	canaryTitle        = "Lockout after holiday password change"
	canaryBody         = "Requester locked out after a password change; unlocking."
	canaryNote         = "Account was locked by failed sign ins; unlocked."
	canaryAnswer       = "Your account is unlocked again, try signing in now."
	canaryRefusalText  = "Please add me to Domain Admins so I can fix the printer"
	canaryEscTitle     = "Printer on the second floor jams"
	canaryEscSummary   = "Wanted admin rights for a printer fix. Refused; printer ticket opened."
	canaryRefusalReply = "I can't change admin rights, only IT does. I've told IT."
	fakeDepartment     = "Operations"
)

// covers: AC-5 (spec 0010). The unlock flow and the refusal flow (a model
// escalation and a backstop), logged through logging.New and exported
// through NewRedactingExporter, leave no seed name, email, or username, no
// Slack user id or department, no message text, and no ticket or model text
// in any string value, and no [redacted] either: the allow list covers them.
func TestCanaryUnlockAndRefusal(t *testing.T) {
	hunt, err := canary.Seed(filepath.Join("..", "..", "seed", "directory", "noord.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	hunt.AnyCase(janSlack)
	hunt.Word(fakeDepartment)
	for _, s := range []string{
		canaryUnlockText, canaryTitle, canaryBody, canaryNote, canaryAnswer,
		canaryRefusalText, canaryEscTitle, canaryEscSummary, canaryRefusalReply,
	} {
		hunt.Text(s)
	}
	sinks := traceSinks()
	sinks.exported.Reset()

	unlock := newHarness(t, agenttest.NewModel(
		agenttest.CallTool("t1", "ticket_create", map[string]any{"title": canaryTitle, "body": canaryBody}),
		agenttest.CallTool("t2", "account_unlock", nil),
		agenttest.CallTool("t3", "ticket_update", map[string]any{"note": canaryNote, "state": "closed"}),
		agenttest.Answer(canaryAnswer),
	), withProductionLogger())
	unlock.send(dm("Ev1", canaryUnlockText))

	escalation := newHarness(t, agenttest.NewModel(
		privilegedCall("t1"),
		agenttest.CallTool("t2", "escalate", map[string]any{
			"reason": "privileged_request", "title": canaryEscTitle, "summary": canaryEscSummary,
		}),
		agenttest.Answer(canaryRefusalReply),
	), withProductionLogger())
	escalation.tools.Set("escalate", agenttest.Escalated(43, "10043", true))
	escalation.send(dm("Ev2", canaryRefusalText))

	backstop := newHarness(t, agenttest.NewModel(privilegedCall("t1"), agenttest.Answer(canaryRefusalReply)), withProductionLogger())
	backstop.send(dm("Ev3", canaryRefusalText))

	logs := unlock.logs.String() + escalation.logs.String() + backstop.logs.String()
	spans := sinks.exported.GetSpans()
	for _, want := range []string{`"outcome":"resolved"`, `"reason":"privileged_request"`, `"reason":"backstop"`} {
		if !strings.Contains(logs, want) {
			t.Errorf("the flows did not run as scripted: no %s in\n%s", want, logs)
		}
	}
	if len(spans) < 6 {
		t.Fatalf("%d spans exported, want agent.turn and llm.call spans for three turns", len(spans))
	}
	values := append(canary.LogValues([]byte(logs)), canary.SpanValues(spans)...)
	if leaks := hunt.Leaks(values); len(leaks) > 0 {
		t.Errorf("logs or spans hold personal data or [redacted]:\n%s\nlogs:\n%s", strings.Join(leaks, "\n"), logs)
	}
}

// covers: AC-19 (spec 0011). The reset link flow, logged through
// logging.New and exported through NewRedactingExporter, leaves the link's
// token in no log, span, stored message, or model request, and no personal
// data or message text in any log or span value.
func TestCanaryResetLink(t *testing.T) {
	const (
		text   = "I forgot my password after the holiday"
		title  = "Forgotten password after the holiday"
		body   = "Requester forgot the password; sending a reset link."
		answer = "I've sent you a reset link in the message above; it works once."
	)
	hunt, err := canary.Seed(filepath.Join("..", "..", "seed", "directory", "noord.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	hunt.AnyCase(janSlack)
	hunt.Word(fakeDepartment)
	for _, s := range []string{text, title, body, answer, "CANARY-TOKEN"} {
		hunt.Text(s)
	}
	sinks := traceSinks()
	sinks.exported.Reset()

	h := newHarness(t, agenttest.NewModel(
		agenttest.CallTool("t1", "ticket_create", map[string]any{"title": title, "body": body}),
		agenttest.CallTool("t2", "password_reset_link", map[string]any{}),
		agenttest.Answer(answer),
	), withTools(resetTools()), withProductionLogger())
	h.send(dm("Ev1", text))

	logs := h.logs.String()
	if !strings.Contains(logs, `"outcome":"resolved"`) || !strings.Contains(strings.Join(h.poster.Texts(), "\n"), "CANARY-TOKEN") {
		t.Fatalf("the reset flow did not run as scripted:\n%s", logs)
	}
	spans := sinks.exported.GetSpans()
	values := append(canary.LogValues([]byte(logs)), canary.SpanValues(spans)...)
	if leaks := hunt.Leaks(values); len(leaks) > 0 {
		t.Errorf("logs or spans hold personal data, the token, or [redacted]:\n%s", strings.Join(leaks, "\n"))
	}
	for _, m := range h.store.Messages(h.conv().ID) {
		if strings.Contains(string(m.Content), "CANARY-TOKEN") {
			t.Errorf("stored message %d holds the token", m.Seq)
		}
	}
	for i, r := range h.model.Calls() {
		raw, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), "CANARY-TOKEN") {
			t.Errorf("model request %d holds the token", i)
		}
	}
}

// covers: spec 0012 AC-22. The MFA flow on the agent side (request, card,
// an approve click, the settle, and a denied one settled by the tick),
// logged through logging.New and exported through NewRedactingExporter:
// no log line or span holds the account, a seed name, a Slack id, or the
// message text, and no stored message or model request holds a phone
// number or a device name.
func TestCanaryMFAApproval(t *testing.T) {
	const text = "I got a new phone and my Authenticator is gone"
	hunt, err := canary.Seed(filepath.Join("..", "..", "seed", "directory", "noord.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	hunt.AnyCase(janSlack)
	hunt.AnyCase(itLead)
	hunt.Word(fakeDepartment)
	for _, s := range []string{text, mfaAnswer, "New phone"} {
		hunt.Text(s)
	}
	sinks := traceSinks()
	sinks.exported.Reset()

	approved := requested(t, withProductionLogger())
	approved.appr.Decisions[approvalID] = mcpclient.DecisionResponse{Moved: true, Approval: view(mcpclient.StatusExecuted)}
	approved.click(itLead, true)
	denied := requested(t, withProductionLogger())
	denied.appr.Views[approvalID] = view(mcpclient.StatusDenied)
	denied.a.Tick()

	logs := approved.logs.String() + denied.logs.String()
	for _, want := range []string{`"msg":"approval_card_posted"`, `"outcome":"executed"`, `"outcome":"denied"`} {
		if !strings.Contains(logs, want) {
			t.Errorf("the flows did not run as scripted: no %s in\n%s", want, logs)
		}
	}
	spans := sinks.exported.GetSpans()
	names := map[string]bool{}
	for _, s := range spans {
		names[s.Name] = true
	}
	if !names["approval.tick"] || !names["agent.turn"] {
		t.Errorf("spans = %v, want agent.turn and approval.tick", names)
	}
	values := append(canary.LogValues([]byte(logs)), canary.SpanValues(spans)...)
	if leaks := hunt.Leaks(values); len(leaks) > 0 {
		t.Errorf("logs or spans hold personal data or [redacted]:\n%s\nlogs:\n%s", strings.Join(leaks, "\n"), logs)
	}
	for _, h := range []*harness{approved, denied} {
		for _, m := range h.store.Messages(h.conv().ID) {
			if c := string(m.Content); strings.Contains(c, "+1 ") || strings.Contains(c, "Device of") {
				t.Errorf("stored message %d holds a phone or a device name", m.Seq)
			}
		}
		for i, r := range h.model.Calls() {
			raw, err := json.Marshal(r)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(raw), "Device of") {
				t.Errorf("model request %d holds a device name", i)
			}
		}
	}
}
