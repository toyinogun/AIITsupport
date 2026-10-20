package agent_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/toyinogun/AIITsupport/internal/agent"
	"github.com/toyinogun/AIITsupport/internal/agent/agenttest"
	"github.com/toyinogun/AIITsupport/internal/mcpclient"
)

// knownThread makes Jan's DM thread a conversation the agent knows, with
// no card: an approval whose pending result never reached the agent.
func knownThread(t *testing.T, h *harness) {
	t.Helper()
	if _, err := h.store.UpsertConversation(t.Context(), janDM, firstTS, janSlack); err != nil {
		t.Fatal(err)
	}
}

// covers: spec 0012 AC-14, AC-7. The tick expires first, then adopts an
// approval created within APPROVAL_TIMEOUT plus an hour whose thread is a
// known conversation, and posts its card in the tick form; an unknown
// thread and another tool get no row.
func TestTickAdoptsAndPostsTheCard(t *testing.T) {
	h := newHarness(t, agenttest.NewModel())
	knownThread(t, h)
	h.appr.Views[approvalID] = view(mcpclient.StatusPending)
	stranger := view(mcpclient.StatusPending)
	stranger.ID, stranger.SlackThreadTS = uuid.New(), "1792400999.000100"
	h.appr.Views[stranger.ID] = stranger
	other := view(mcpclient.StatusPending)
	other.ID, other.ToolName = uuid.New(), "group_access"
	h.appr.Views[other.ID] = other

	h.a.Tick()

	if h.appr.Expires() != 1 {
		t.Errorf("expire calls = %d", h.appr.Expires())
	}
	if s := h.appr.SinceCalls(); len(s) != 1 || !s[0].Equal(now.Add(-90*time.Minute)) {
		t.Errorf("since = %v, want now minus APPROVAL_TIMEOUT and an hour", s)
	}
	cards := h.store.Cards()
	if len(cards) != 1 || cards[0].ApprovalID != approvalID || cards[0].ConversationID != h.conv().ID ||
		cards[0].CardTS == "" || cards[0].PostAttempts != 1 {
		t.Fatalf("cards = %+v", cards)
	}
	posted := h.poster.Cards()
	if len(posted) != 1 || posted[0].Channel != itChannel {
		t.Fatalf("card posts = %+v", posted)
	}
	want := []string{
		"MFA reset request",
		"Requester: <@" + janSlack + "> (" + janUser + ")",
		"Ticket: <http://zammad.test/#ticket/zoom/42|open in helpdesk>",
		"Methods: listed in the ticket note.",
		"Expires: " + expiresAt.Format("15:04") + " UTC",
		"Confirm with the person by phone or in person before approving.",
	}
	if got := posted[0].Card; strings.Join(got.Lines, "\n") != strings.Join(want, "\n") || got.ApprovalID != approvalID.String() {
		t.Errorf("card = %#v", got)
	}

	// The next tick finds the card posted and the approval pending: nothing
	// to do, and adopt skips the approval that has a card (no insert tried).
	delete(h.appr.Views, stranger.ID) // the one approval adopt would still try
	h.store.FailOn("AdoptApprovalCard")
	h.a.Tick()
	if n := len(h.poster.Cards()); n != 1 {
		t.Errorf("card posts after a quiet tick = %d", n)
	}
	if strings.Contains(h.logs.String(), `"msg":"approval_tick_failed"`) {
		t.Errorf("adopt tried an approval that has a card: %s", h.logs.String())
	}
}

// covers: spec 0012 AC-14. A card whose post keeps failing is tried at most
// three times, spaced by twice APPROVAL_POLL_INTERVAL, each attempt claimed
// before it is sent.
func TestTickCapsAndSpacesCardPosts(t *testing.T) {
	h := newHarness(t, agenttest.NewModel())
	var mu sync.Mutex
	clock := now
	h.store.Now = func() time.Time { mu.Lock(); defer mu.Unlock(); return clock }
	advance := func(d time.Duration) { mu.Lock(); defer mu.Unlock(); clock = clock.Add(d) }
	knownThread(t, h)
	h.appr.Views[approvalID] = view(mcpclient.StatusPending)
	h.poster.CardErr = errors.New("slack down")

	attempts := func() int { return h.store.Cards()[0].PostAttempts }
	h.a.Tick()
	h.a.Tick() // too soon: spaced by 60s
	if attempts() != 1 {
		t.Fatalf("attempts after two quick ticks = %d", attempts())
	}
	for range 4 {
		advance(61 * time.Second)
		h.a.Tick()
	}
	if attempts() != 3 {
		t.Errorf("attempts = %d, want the cap of 3", attempts())
	}
	if n := strings.Count(h.logs.String(), "approval_card_post_failed"); n != 3 {
		t.Errorf("post failures logged = %d", n)
	}
}

// covers: spec 0012 AC-12, AC-14, AC-15 to AC-17. An approval nobody decided
// is expired by the tool server and settled by the tick: escalation first
// with approval_expired, then the card, the user's text, and the IT post,
// once.
func TestTickSettlesAnExpiredApproval(t *testing.T) {
	h := requested(t)
	h.appr.Views[approvalID] = view(mcpclient.StatusExpired)

	h.a.Tick()

	esc := h.lastEscalation()
	if esc.Args["reason"] != "approval_expired" || esc.Args["summary"] != agent.MFAExpiredSummary ||
		esc.Requester.Initiator != mcpclient.InitiatorCode {
		t.Errorf("escalate = %+v", esc)
	}
	h.wantStatus(agent.StatusEscalated)
	h.wantPosts(agent.TextAck, answerWith(mfaAnswer, agent.FooterOpen), agent.TextMFAExpired)
	cards := h.poster.Cards()
	if last := cards[len(cards)-1]; !last.Update || last.Card.Lines[len(last.Card.Lines)-1] != "Expired with no decision. Escalated." {
		t.Errorf("update = %+v", last)
	}
	if it := h.itPosts(); len(it) != 1 || !strings.Contains(it[0], "MFA reset not approved in time") {
		t.Errorf("IT posts = %q", it)
	}

	h.a.Tick()
	h.wantPosts(agent.TextAck, answerWith(mfaAnswer, agent.FooterOpen), agent.TextMFAExpired)
	if n := len(h.poster.Cards()); n != len(cards) {
		t.Errorf("a second tick touched the card: %d", n)
	}
}

// covers: spec 0012 AC-9, AC-14. A decision whose answer never came back
// (the click timed out) is settled by the tick from the approval view.
func TestTickSettlesALostDecision(t *testing.T) {
	h := requested(t)
	h.appr.Views[approvalID] = view(mcpclient.StatusExecuted)
	h.a.Tick()
	h.wantStatus(agent.StatusResolved)
	h.wantPosts(agent.TextAck, answerWith(mfaAnswer, agent.FooterOpen), agent.TextMFACleared)
}

// covers: spec 0012 AC-14. A failing step logs approval_tick_failed with a
// fixed cause and the rest of the tick still runs; an approval that is
// still running (approved) is left alone.
func TestTickFailuresAreLoggedAndTheRestRuns(t *testing.T) {
	h := requested(t)
	h.appr.ExpireErr = &mcpclient.StatusError{Status: 503}
	h.appr.Views[approvalID] = view(mcpclient.StatusApproved)
	h.a.Tick()
	if !strings.Contains(h.logs.String(), `"msg":"approval_tick_failed"`) {
		t.Fatalf("no approval_tick_failed log: %s", h.logs.String())
	}
	if c := h.store.Cards()[0]; c.Settled {
		t.Error("an approved, still running approval was settled")
	}
	h.appr.Views[approvalID] = view(mcpclient.StatusDenied)
	h.a.Tick()
	h.wantPosts(agent.TextAck, answerWith(mfaAnswer, agent.FooterOpen), agent.TextMFADenied)
}

// covers: spec 0012 AC-15. While a turn holds the thread, ticks queue one
// settle per approval, not one each: the escalation runs once.
func TestTickQueuesOneSettlePerApproval(t *testing.T) {
	model := agenttest.NewModel(
		agenttest.CallTool("t1", "ticket_create", map[string]any{"title": "New phone", "body": "Authenticator gone."}),
		agenttest.CallTool("t2", "mfa_clear", map[string]any{}),
		agenttest.Answer(mfaAnswer),
		agenttest.Answer("Still waiting on IT."),
	)
	h := newHarness(t, model, withTools(mfaTools("")))
	h.send(dm("Ev1", "new phone"))
	h.appr.Views[approvalID] = view(mcpclient.StatusDenied)
	started := make(chan struct{})
	release := make(chan struct{})
	h.tools.Set("user_lookup", func(context.Context, agenttest.ToolCall) (mcpclient.Result, error) {
		select {
		case <-started:
		default:
			close(started)
			<-release
		}
		return agenttest.LookupResult(janUser, false), nil
	})
	h.a.Submit(dm("Ev2", "any news?"))
	<-started
	for range 3 {
		h.a.TickNoWait() // each queues a settle behind the running turn, unless one is queued
	}
	close(release)
	h.a.Wait()
	n := 0
	for _, c := range h.tools.Calls() {
		if c.Tool == "escalate" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("escalate calls = %d, want 1", n)
	}
}

// covers: spec 0012 AC-14. Run ticks at start, before any interval passes.
func TestRunTicksAtStart(t *testing.T) {
	h := newHarness(t, agenttest.NewModel())
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- h.a.Run(ctx) }()
	deadline := time.Now().Add(2 * time.Second)
	for h.appr.Expires() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if h.appr.Expires() != 1 {
		t.Errorf("expire calls = %d, want the start tick only", h.appr.Expires())
	}
}

// covers: spec 0012 AC-14, AC-21 (amended). The adopt window is read from
// the agent's own APPROVAL_TIMEOUT: now minus that and an hour.
func TestTickAdoptWindowFollowsTheAgentsTimeout(t *testing.T) {
	h := newHarness(t, agenttest.NewModel(), withConfig(func(c *agent.Config) { c.ApprovalTimeout = 2 * time.Hour }))
	h.a.Tick()
	if s := h.appr.SinceCalls(); len(s) != 1 || !s[0].Equal(now.Add(-3*time.Hour)) {
		t.Errorf("since = %v, want now minus 2h and an hour", s)
	}
}
