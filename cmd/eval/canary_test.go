package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/toyinogun/AIITsupport/eval/runner"
	"github.com/toyinogun/AIITsupport/eval/score"
	"github.com/toyinogun/AIITsupport/eval/ticketset"
	"github.com/toyinogun/AIITsupport/internal/canary"
)

const (
	canaryMessage = "My laptop is stuck, please call me back on the number in my profile"
	canaryReply   = "I checked with Kevin Smits in Finance and unlocked it"
)

// covers: AC-17 (spec 0018). The progress lines and the summary hold ids,
// tokens, milliseconds, and costs only: a ticket message, a reply, a name, or
// an email never reaches stdout, even for a failed attempt with posts.
func TestProgressOutputHoldsNoMessageText(t *testing.T) {
	hunt, err := canary.Seed("../../seed/directory/noord.yaml")
	if err != nil {
		t.Fatal(err)
	}
	hunt.Text(canaryMessage)
	hunt.Text(canaryReply)

	ticket := ticketset.Ticket{ID: "ev-001", Message: canaryMessage, Sender: ticketset.Sender{Kind: "staff", User: "kevin.smits"}}
	obs := runner.Observation{
		Posts: []string{canaryReply, "write to kevin.smits@noord.example"}, DurationMS: 1200, CostMicroUSD: 42,
		Score: score.Result{Outcome: "resolve", Tools: []string{"account_unlock"}, FailedChecks: []string{"tools_missing"}},
	}
	var out bytes.Buffer
	printTicket(&out, ticket, 1, obs, false)
	printSummary(&out, map[string]float64{"tickets": 1, "passed": 0})

	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if leaks := hunt.Leaks(lines); len(leaks) > 0 {
		t.Fatalf("progress output holds personal data or message text:\n%s\n%s", strings.Join(leaks, "\n"), out.String())
	}
	if !strings.Contains(out.String(), "ev-001#1") {
		t.Fatalf("the progress line is missing:\n%s", out.String())
	}
}
