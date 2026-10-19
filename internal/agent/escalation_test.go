package agent_test

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anthropics/anthropic-sdk-go"

	"github.com/toyinogun/AIITsupport/internal/agent"
	"github.com/toyinogun/AIITsupport/internal/agent/agenttest"
	"github.com/toyinogun/AIITsupport/internal/mcpclient"
)

func privilegedCall(id string) agenttest.Step {
	return agenttest.CallTool(id, "privileged_access_change", map[string]any{"summary": "Domain Admin to fix the printer"})
}

func escalateCall(id, reason, summary string) agenttest.Step {
	return agenttest.CallTool(id, "escalate", map[string]any{
		"reason": reason, "title": "Printer on the 2nd floor not working", "summary": summary,
	})
}

const printerSummary = "Asked for Domain Admin to fix the 2nd floor printer. Refused. Printer ticket opened."

// covers: AC-8, AC-9, AC-14. The printer hook: blocked, then the model's
// escalate; the conversation ends escalated with the ticket in the same
// write, IT hears before the next model call in the exact format, that call
// uses tool_choice none, and the reply carries footer_escalated.
func TestModelEscalation(t *testing.T) {
	var itBeforeAnswer atomic.Int64
	var h *harness
	answer := func(ctx context.Context, p anthropic.MessageNewParams) (*anthropic.Message, error) {
		itBeforeAnswer.Store(int64(len(h.itPosts())))
		return agenttest.Answer("I can't grant admin rights; only IT does that. I've opened a ticket for the printer and told IT.")(ctx, p)
	}
	h = newHarness(t, agenttest.NewModel(
		privilegedCall("t1"),
		escalateCall("t2", "privileged_request", printerSummary),
		answer,
	))
	h.tools.Set("escalate", agenttest.Escalated(43, "10043", true))
	h.send(dm("Ev1", "I need Domain Admin to fix the printer on the 2nd floor"))

	if c := h.conv(); c.Status != agent.StatusEscalated || c.TicketID != 43 {
		t.Errorf("conversation = %+v, want escalated with ticket 43", c)
	}
	want := ":rotating_light: *Privileged request refused*\n" +
		"Requester: <@U0JAN> (jan.devries)\n" +
		"Blocked privileged requests from this person in the last 24h: 2\n" +
		"Ran: privileged_access_change blocked, escalate ok\n" +
		"Ticket: <http://127.0.0.1:58080/#ticket/zoom/43|#10043>\n" +
		"Summary: " + printerSummary
	if it := h.itPosts(); len(it) != 1 || it[0] != want {
		t.Errorf("IT posts = %q\nwant %q", it, want)
	}
	if itBeforeAnswer.Load() != 1 {
		t.Error("the IT post went out after the next model call")
	}
	calls := h.model.Calls()
	if last := calls[len(calls)-1]; last.ToolChoice.OfNone == nil {
		t.Errorf("the call after the escalation: tool_choice %+v, want none", last.ToolChoice)
	}
	posts := h.poster.Texts()
	if reply := posts[len(posts)-1]; !strings.HasSuffix(reply, "\n\n"+agent.FooterEscalated) {
		t.Errorf("reply = %q, want footer_escalated", reply)
	}
	h.wantTools("user_lookup", "privileged_access_change", "escalate")
	if e := h.lastEscalation(); e.Requester.Initiator != "" {
		t.Errorf("the model's call carried initiator %q", e.Requester.Initiator)
	}
	if !strings.Contains(h.logs.String(), `"msg":"agent_escalated","event_id":"Ev1","reason":"privileged_request","ticket":true`) {
		t.Errorf("logs lack agent_escalated privileged_request:\n%s", h.logs)
	}
	if !strings.Contains(h.logs.String(), `"outcome":"escalated"`) {
		t.Errorf("agent_turn_done is not escalated:\n%s", h.logs)
	}
}

// covers: AC-8, AC-9. A conversation that already has a ticket keeps it; a
// first attempt has no attempt line; the account comes from this turn's match.
func TestModelEscalationKeepsTheTicket(t *testing.T) {
	h := newHarness(t, agenttest.NewModel(
		agenttest.CallTool("t1", "ticket_create", map[string]any{"title": "Eva's account", "body": "b"}),
		escalateCall("t2", "other_person", "Asked to unlock a colleague's account."),
		agenttest.Answer("I can only help with your own account; IT has this."),
	))
	h.send(dm("Ev1", "Unlock Eva's account please"))
	if c := h.conv(); c.Status != agent.StatusEscalated || c.TicketID != 42 {
		t.Errorf("conversation = %+v, want ticket 42 kept", c)
	}
	it := h.itPosts()
	if len(it) != 1 || !strings.HasPrefix(it[0], ":busts_in_silhouette: *Request to act for someone else*\nRequester: <@U0JAN> (jan.devries)\nRan: ticket_create ok, escalate ok\n") {
		t.Errorf("IT posts = %q", it)
	}
}

// covers: AC-9, AC-11. The IT post already went out when the next model call
// fails; the failure path makes no call and the user hears IT has it.
func TestEscalationSurvivesALaterFailure(t *testing.T) {
	h := newHarness(t, agenttest.NewModel(
		privilegedCall("t1"),
		escalateCall("t2", "privileged_request", printerSummary),
		agenttest.Fail(errors.New("529 overloaded")),
	))
	h.send(dm("Ev1", "I need Domain Admin"))
	h.wantTools("user_lookup", "privileged_access_change", "escalate")
	h.wantPosts(agent.TextAck, agent.TextEscalatedWithTicket)
	h.wantStatus(agent.StatusEscalated)
	if it := h.itPosts(); len(it) != 1 {
		t.Errorf("IT posts = %q, want exactly the model's one", it)
	}
}

// covers: AC-10. The model refused and ended with text but never escalated:
// code does, with the fixed texts, and the reply carries footer_escalated.
func TestBackstop(t *testing.T) {
	h := newHarness(t, agenttest.NewModel(privilegedCall("t1"), agenttest.Answer("I can't do that.")))
	h.send(dm("Ev1", "Make me Domain Admin"))

	h.wantTools("user_lookup", "privileged_access_change", "escalate")
	e := h.lastEscalation()
	if e.Requester.Initiator != mcpclient.InitiatorCode || e.Args["reason"] != "privileged_request" ||
		e.Args["title"] != agent.BackstopTitle || e.Args["summary"] != agent.BackstopSummary {
		t.Errorf("backstop call = %+v", e)
	}
	h.wantPosts(agent.TextAck, "I can't do that.\n\n"+agent.FooterEscalated)
	h.wantStatus(agent.StatusEscalated)
	if c := h.conv(); c.TicketID != 43 {
		t.Errorf("ticket id = %d, want 43", c.TicketID)
	}
	it := h.itPosts()
	if len(it) != 1 || !strings.Contains(it[0], "\nRan: privileged_access_change blocked, escalate ok\n") ||
		!strings.HasSuffix(it[0], "\nSummary: "+agent.BackstopSummary) {
		t.Errorf("IT posts = %q", it)
	}
	for _, m := range h.store.Messages(h.conv().ID) {
		if strings.Contains(string(m.Content), "escalate") {
			t.Errorf("the code call was stored: %s", m.Content)
		}
	}
	if !strings.Contains(h.logs.String(), `"msg":"agent_escalated","event_id":"Ev1","reason":"backstop","ticket":true`) {
		t.Errorf("logs lack agent_escalated backstop:\n%s", h.logs)
	}
}

// covers: AC-10. A refused backstop logs agent_backstop_failed and tells the
// user to contact IT, before footer_open; the conversation stays open.
func TestBackstopFailed(t *testing.T) {
	h := newHarness(t, agenttest.NewModel(privilegedCall("t1"), agenttest.Answer("I can't do that.")))
	h.tools.Set("escalate", agenttest.Respond(agenttest.Refused("unavailable", "The helpdesk cannot be reached right now.")))
	h.send(dm("Ev1", "Make me Domain Admin"))
	h.wantPosts(agent.TextAck, "I can't do that.\n\n"+agent.TextBackstopFailed+"\n\n"+agent.FooterOpen)
	h.wantStatus(agent.StatusOpen)
	if !strings.Contains(h.logs.String(), `"msg":"agent_backstop_failed","event_id":"Ev1","code":"unavailable","cause":"none"`) {
		t.Errorf("logs lack agent_backstop_failed:\n%s", h.logs)
	}
	if it := h.itPosts(); len(it) != 0 {
		t.Errorf("IT posts = %q, want none", it)
	}
}

// covers: AC-10. The model's own escalate failed; the backstop still tries
// once more. A model whose escalate was ok gets no backstop.
func TestBackstopAfterAFailedModelEscalation(t *testing.T) {
	h := newHarness(t, agenttest.NewModel(
		privilegedCall("t1"),
		escalateCall("t2", "privileged_request", printerSummary),
		agenttest.Answer("I couldn't reach the helpdesk."),
	))
	var n atomic.Int32
	h.tools.Set("escalate", func(ctx context.Context, c agenttest.ToolCall) (mcpclient.Result, error) {
		if n.Add(1) == 1 {
			return agenttest.Refused("unavailable", "The helpdesk cannot be reached right now."), nil
		}
		return agenttest.Escalated(43, "10043", true)(ctx, c)
	})
	h.send(dm("Ev1", "Make me Domain Admin"))
	h.wantTools("user_lookup", "privileged_access_change", "escalate", "escalate")
	h.wantStatus(agent.StatusEscalated)
	it := h.itPosts()
	if len(it) != 1 || !strings.Contains(it[0], "\nRan: privileged_access_change blocked, escalate unavailable, escalate ok\n") {
		t.Errorf("IT posts = %q", it)
	}
}

// covers: AC-10. A blocked result from an earlier turn with no ok escalate
// triggers the backstop on the next turn.
func TestBackstopOnALaterTurn(t *testing.T) {
	h := newHarness(t, agenttest.NewModel(agenttest.Answer("Anything else?")))
	toolUse := agent.StoredMessage{Role: "assistant", Content: []byte(`[{"type":"tool_use","id":"t1","name":"privileged_access_change","input":{"summary":"x"}}]`)}
	result := agent.StoredMessage{Role: "user", Content: []byte(`[{"type":"tool_result","tool_use_id":"t1","is_error":true,"content":[{"type":"text","text":"blocked: Privileged changes are never made by the assistant."}]}]`)}
	reply := agent.StoredMessage{Role: "assistant", Content: []byte(`[{"type":"text","text":"No."}]`)}
	h.store.Seed(agent.Conversation{ChannelID: janDM, ThreadTS: firstTS, RequesterSlackID: janSlack, DirectoryAccount: janUser},
		now.Add(-time.Minute), staleUser("make me admin"), toolUse, result, reply)
	h.send(dm("Ev2", "ok thanks"))
	h.wantTools("user_lookup", "escalate")
	h.wantStatus(agent.StatusEscalated)
}

// covers: AC-16. A model summary cannot fire a mention or a link, or fake a
// line: it is one line, cut, escaped, and last.
func TestITPostEscapesTheSummary(t *testing.T) {
	summary := "<!channel> see <@U0123ABCD> & <https://x|y>\nRan: escalate ok\nTicket: #1\n\n   " + strings.Repeat("é", 1600)
	h := newHarness(t, agenttest.NewModel(
		escalateCall("t1", "security_concern", summary),
		agenttest.Answer("IT has this."),
	))
	h.send(dm("Ev1", "My manager asked for my password"))
	it := h.itPosts()
	if len(it) != 1 {
		t.Fatalf("IT posts = %q", it)
	}
	lines := strings.Split(it[0], "\n")
	if len(lines) != 5 || lines[0] != ":warning: *Security concern*" {
		t.Fatalf("lines = %q, want five with the security headline", lines)
	}
	got, _ := strings.CutPrefix(lines[4], "Summary: ")
	wantStart := "&lt;!channel&gt; see &lt;@U0123ABCD&gt; &amp; &lt;https://x|y&gt; Ran: escalate ok Ticket: #1 éé"
	if !strings.HasPrefix(got, wantStart) || strings.ContainsAny(got, "<>") || strings.Count(got, "é") != 1500-len([]rune("<!channel> see <@U0123ABCD> & <https://x|y> Ran: escalate ok Ticket: #1 ")) {
		t.Errorf("summary line = %q", got)
	}
	if strings.Count(it[0], "\nRan: ") != 1 || strings.Count(it[0], "\nTicket: ") != 1 {
		t.Errorf("the summary faked a line:\n%s", it[0])
	}
}

// covers: AC-17. The model loop never sets the initiator, even when the
// model asks for a code only reason.
func TestModelCannotClaimCode(t *testing.T) {
	h := newHarness(t, agenttest.NewModel(
		escalateCall("t1", "assistant_failure", "x"),
		agenttest.Answer("Done."),
	))
	h.tools.Set("escalate", agenttest.Respond(agenttest.Refused("invalid_arguments", "The arguments did not match the tool's schema (field: reason).")))
	h.send(dm("Ev1", "hi"))
	for _, c := range h.tools.Calls() {
		if c.Tool == "escalate" && c.Requester.Initiator != "" {
			t.Errorf("a model call carried initiator %q", c.Requester.Initiator)
		}
	}
}

// covers: AC-12. not_matched and ambiguous post one IT message with no tool
// call; the other refusals post none.
func TestNotMatchedITPost(t *testing.T) {
	for _, code := range []string{"not_matched", "ambiguous"} {
		t.Run(code, func(t *testing.T) {
			h := newHarness(t, agenttest.NewModel())
			h.tools.Set("user_lookup", agenttest.Respond(agenttest.Refused(code, "No single account.")))
			h.send(dm("Ev1", "I'm locked out"))
			want := ":grey_question: *Slack user not matched to a Noord account*\nRequester: <@U0JAN>\nReason: " + code +
				"\nNo ticket: no single Noord account matches this Slack user."
			if it := h.itPosts(); len(it) != 1 || it[0] != want {
				t.Errorf("IT posts = %q, want %q", it, want)
			}
			h.wantTools("user_lookup")
		})
	}
	t.Run("not staff", func(t *testing.T) {
		h := newHarness(t, agenttest.NewModel())
		h.users.Set(janSlack, agent.SlackUser{TeamID: agenttest.Team, IsBot: true, IsEmailConfirmed: true, Email: janEmail})
		h.send(dm("Ev1", "hi"))
		if it := h.itPosts(); len(it) != 0 {
			t.Errorf("IT posts = %q, want none", it)
		}
	})
	t.Run("account changed", func(t *testing.T) {
		h := newHarness(t, agenttest.NewModel())
		h.store.Seed(agent.Conversation{ChannelID: janDM, ThreadTS: firstTS, RequesterSlackID: janSlack, DirectoryAccount: "eva.peters"}, now.Add(-time.Minute))
		h.send(dm("Ev1", "hi"))
		if it := h.itPosts(); len(it) != 0 {
			t.Errorf("IT posts = %q, want none", it)
		}
	})
}

// covers: AC-9, AC-13. A failed IT post logs agent_post_failed with its
// kind and cause and is not retried.
func TestITPostFailureIsLogged(t *testing.T) {
	h := newHarness(t, agenttest.NewModel(escalateCall("t1", "other_person", "x"), agenttest.Answer("IT has this.")))
	h.poster.Err = causeError{"slack_not_in_channel"}
	h.send(dm("Ev1", "Unlock Eva"))
	if !strings.Contains(h.logs.String(), `"msg":"agent_post_failed","event_id":"Ev1","kind":"it_escalation","cause":"slack_not_in_channel"`) {
		t.Errorf("logs lack the IT post failure:\n%s", h.logs)
	}
	if n := len(h.itPosts()); n != 1 {
		t.Errorf("IT post attempts = %d, want 1", n)
	}
}

// covers: AC-9. When the stored calls cannot be read after the model's ok
// escalate, the IT post still goes out and agent_history_read_failed is
// logged with its cause.
func TestHistoryReadFailureIsLogged(t *testing.T) {
	var h *harness
	escalate := escalateCall("t1", "other_person", "Asked to unlock a colleague.")
	step := func(ctx context.Context, p anthropic.MessageNewParams) (*anthropic.Message, error) {
		h.store.FailOn("ListMessages") // the loop already read the history; the post's read fails
		return escalate(ctx, p)
	}
	h = newHarness(t, agenttest.NewModel(step, agenttest.Answer("IT has this.")))
	h.send(dm("Ev1", "Unlock Eva"))
	if want := `"msg":"agent_history_read_failed","conversation_id":1,"cause":"other"`; !strings.Contains(h.logs.String(), want) {
		t.Errorf("logs lack %s:\n%s", want, h.logs)
	}
	if n := len(h.itPosts()); n != 1 {
		t.Errorf("IT posts = %d, want 1", n)
	}
}

// covers: spec 0009 AC-9, AC-16. The model's summary names the requester and
// the department; the IT post carries placeholders instead.
func TestITPostRemovesTheRequesterName(t *testing.T) {
	summary := "Jan de Vries (Operations) asked for Domain Admin; jan's printer is broken.\nAsk Vries, OPERATIONS knows since January."
	h := newHarness(t, agenttest.NewModel(
		privilegedCall("t1"),
		escalateCall("t2", "privileged_request", summary),
		agenttest.Answer("IT has this."),
	))
	h.send(dm("Ev1", "I need Domain Admin"))
	it := h.itPosts()
	if len(it) != 1 {
		t.Fatalf("IT posts = %q", it)
	}
	want := "\nSummary: [name] ([department]) asked for Domain Admin; [name]'s printer is broken. Ask [name], [department] knows since January."
	if !strings.HasSuffix(it[0], want) {
		t.Errorf("IT post = %q\nwant suffix %q", it[0], want)
	}
	if e := h.lastEscalation(); e.Args["summary"] != summary {
		t.Errorf("the tool call's summary changed: %q", e.Args["summary"])
	}
}

// covers: AC-10. The backstop is refused already_escalated (an earlier code
// escalation's status write failed): IT already has the thread, so the turn
// ends escalated with no ticket id, the short IT post, and footer_escalated.
func TestBackstopAlreadyEscalated(t *testing.T) {
	h := newHarness(t, agenttest.NewModel(privilegedCall("t1"), agenttest.Answer("I can't do that.")))
	h.tools.Set("escalate", agenttest.Respond(agenttest.Refused("already_escalated", "This conversation is already with IT.")))
	h.send(dm("Ev1", "Make me Domain Admin"))

	h.wantPosts(agent.TextAck, "I can't do that.\n\n"+agent.FooterEscalated)
	if c := h.conv(); c.Status != agent.StatusEscalated || c.TicketID != 0 {
		t.Errorf("conversation = %+v, want escalated with no ticket id", c)
	}
	want := ":rotating_light: *Privileged request refused*\n" +
		"Requester: <@U0JAN> (jan.devries)\n" +
		"Ticket: already escalated; see the audit log for this thread."
	if it := h.itPosts(); len(it) != 1 || it[0] != want {
		t.Errorf("IT posts = %q\nwant %q", it, want)
	}
	logs := h.logs.String()
	if !strings.Contains(logs, `"msg":"agent_escalated","event_id":"Ev1","reason":"backstop"`) {
		t.Errorf("logs lack agent_escalated backstop:\n%s", logs)
	}
	if strings.Contains(logs, "agent_backstop_failed") {
		t.Errorf("an already escalated backstop logged a failure:\n%s", logs)
	}
}

// covers: AC-10. When the stored tool results cannot be read, the backstop
// logs agent_backstop_failed with code unknown, makes no call, adds no
// sentence, and the reply keeps footer_open.
func TestBackstopCannotReadTheHistory(t *testing.T) {
	var h *harness
	answer := func(ctx context.Context, p anthropic.MessageNewParams) (*anthropic.Message, error) {
		h.store.FailOn("ListMessages") // the loop already read the history; the backstop's read fails
		return agenttest.Answer("I can't do that.")(ctx, p)
	}
	h = newHarness(t, agenttest.NewModel(privilegedCall("t1"), answer))
	h.send(dm("Ev1", "Make me Domain Admin"))

	h.wantTools("user_lookup", "privileged_access_change")
	h.wantPosts(agent.TextAck, "I can't do that.\n\n"+agent.FooterOpen)
	h.wantStatus(agent.StatusOpen)
	if !strings.Contains(h.logs.String(), `"msg":"agent_backstop_failed","event_id":"Ev1","code":"unknown"`) {
		t.Errorf("logs lack agent_backstop_failed unknown:\n%s", h.logs)
	}
	if it := h.itPosts(); len(it) != 0 {
		t.Errorf("IT posts = %q, want none", it)
	}
}

// covers: AC-10. A model that ends with text and was never blocked gets no
// backstop call, even after other refusals.
func TestNoBackstopWithoutABlockedCall(t *testing.T) {
	h := newHarness(t, agenttest.NewModel(
		agenttest.CallTool("t1", "account_unlock", map[string]any{}),
		agenttest.Answer("Done."),
	))
	h.tools.Set("account_unlock", agenttest.Respond(agenttest.Refused("rate_limited", "Too many changes.")))
	h.send(dm("Ev1", "unlock me"))
	h.wantTools("user_lookup", "account_unlock")
	h.wantStatus(agent.StatusOpen)
	if it := h.itPosts(); len(it) != 0 {
		t.Errorf("IT posts = %q, want none", it)
	}
}

// covers: AC-9. Without ZAMMAD_PUBLIC_URL the result has no ticket_url and
// the Ticket line is the number alone, not a link.
func TestITPostWithoutATicketLink(t *testing.T) {
	h := newHarness(t, agenttest.NewModel(escalateCall("t1", "other_person", "Asked to unlock a colleague."), agenttest.Answer("IT has this.")))
	h.tools.Set("escalate", agenttest.Respond(agenttest.OK("Escalated to IT.", map[string]any{
		"ticket_id": 44.0, "ticket_number": "10044", "reason": "other_person", "initiator": "model",
		"privileged": false, "created": true, "privileged_attempts": 0.0, "privileged_window_hours": 24.0,
	})))
	h.send(dm("Ev1", "Unlock Eva"))
	it := h.itPosts()
	if len(it) != 1 || !strings.Contains(it[0], "\nTicket: #10044\nSummary: ") {
		t.Errorf("IT posts = %q, want the bare ticket number", it)
	}
}
