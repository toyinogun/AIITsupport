package agent_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/toyinogun/AIITsupport/internal/agent/agenttest"
	"github.com/toyinogun/AIITsupport/internal/canary"
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
