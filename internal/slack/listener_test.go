package slack

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	slackapi "github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
	"github.com/slack-go/slack/socketmode"

	"github.com/toyinogun/AIITsupport/internal/agent"
)

type fakeSocket struct {
	events chan socketmode.Event
	runErr error
	mu     sync.Mutex
	acks   []string
}

func newFakeSocket() *fakeSocket { return &fakeSocket{events: make(chan socketmode.Event, 16)} }

func (s *fakeSocket) Events() <-chan socketmode.Event { return s.events }

func (s *fakeSocket) Ack(req socketmode.Request) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.acks = append(s.acks, req.EnvelopeID)
	return nil
}

func (s *fakeSocket) Run(ctx context.Context) error {
	if s.runErr != nil {
		return s.runErr
	}
	<-ctx.Done()
	return ctx.Err()
}

type reply struct{ channel, thread, text string }

type fakePoster struct {
	checkErrs []error
	replyErr  error
	mu        sync.Mutex
	checks    int
	replies   []reply
}

const testTeam = "T0NOORD"

func (p *fakePoster) Check(context.Context) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.checks++
	if len(p.checkErrs) > 0 {
		err := p.checkErrs[0]
		p.checkErrs = p.checkErrs[1:]
		return "", err
	}
	return testTeam, nil
}

// fakeHandler records what the listener submits.
type fakeHandler struct {
	mu     sync.Mutex
	got    []agent.Inbound
	clicks []agent.Click
}

func (h *fakeHandler) Click(c agent.Click) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.clicks = append(h.clicks, c)
}

func clicked(l *Listener) []agent.Click {
	h := l.Handler.(*fakeHandler)
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]agent.Click(nil), h.clicks...)
}

func (h *fakeHandler) Submit(in agent.Inbound) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.got = append(h.got, in)
}

func submitted(l *Listener) []agent.Inbound {
	h := l.Handler.(*fakeHandler)
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]agent.Inbound(nil), h.got...)
}

func (p *fakePoster) Reply(_ context.Context, channel, thread, text string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.replies = append(p.replies, reply{channel, thread, text})
	return p.replyErr
}

func (p *fakePoster) ReplyNoUnfurl(ctx context.Context, channel, thread, text string) error {
	return p.Reply(ctx, channel, thread, text)
}

func (p *fakePoster) PostCard(context.Context, string, agent.Card) (string, error) { return "", nil }

func (p *fakePoster) UpdateCard(context.Context, string, string, agent.Card) error { return nil }

func (p *fakePoster) Ephemeral(context.Context, string, string, string) error { return nil }

func (p *fakePoster) got() []reply {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]reply(nil), p.replies...)
}

type fakeDedup struct {
	mu       sync.Mutex
	seen     map[string]bool
	err      error
	purgeErr error
	cutoffs  []time.Time
}

func (d *fakeDedup) MarkEventSeen(_ context.Context, id string) (int64, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.err != nil {
		return 0, d.err
	}
	if d.seen[id] {
		return 0, nil
	}
	d.seen[id] = true
	return 1, nil
}

func (d *fakeDedup) PurgeSeenEventsBefore(_ context.Context, cutoff time.Time) (int64, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.cutoffs = append(d.cutoffs, cutoff)
	if d.purgeErr != nil {
		return 0, d.purgeErr
	}
	return 3, nil
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

var fixedNow = time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)

func newListener(t *testing.T) (*Listener, *fakeSocket, *fakePoster, *fakeDedup, *syncBuffer) {
	t.Helper()
	logs := &syncBuffer{}
	s, p, d := newFakeSocket(), &fakePoster{}, &fakeDedup{seen: map[string]bool{}}
	l := &Listener{
		Socket: s, Poster: p, Dedup: d, TTL: 24 * time.Hour, Handler: &fakeHandler{},
		Logger: slog.New(slog.NewJSONHandler(logs, nil)),
		Now:    func() time.Time { return fixedNow },
		sleep:  func(context.Context, time.Duration) error { return nil },
	}
	return l, s, p, d, logs
}

func eventsAPI(envelope, eventID string, inner any, innerType string) socketmode.Event {
	return socketmode.Event{
		Type:    socketmode.EventTypeEventsAPI,
		Request: &socketmode.Request{EnvelopeID: envelope},
		Data: slackevents.EventsAPIEvent{
			Type:       slackevents.CallbackEvent,
			Data:       &slackevents.EventsAPICallbackEvent{EventID: eventID},
			InnerEvent: slackevents.EventsAPIInnerEvent{Type: innerType, Data: inner},
		},
	}
}

func dm(eventID, text string) socketmode.Event {
	return eventsAPI("env-"+eventID, eventID, &slackevents.MessageEvent{
		Type: "message", ChannelType: "im", Channel: "D0123", User: "U1", Text: text, TimeStamp: "1700000000.000100",
	}, "message")
}

// runWith feeds events to a running listener and stops it once settled.
func runWith(t *testing.T, l *Listener, s *fakeSocket, events ...socketmode.Event) error {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- l.Run(ctx) }()
	for _, e := range events {
		s.events <- e
	}
	deadline := time.Now().Add(2 * time.Second)
	for len(s.events) > 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond)
	cancel()
	return <-done
}

// covers: AC-12 (spec 0006), AC-2 (spec 0008). A DM is acked, marked,
// logged without its text, and submitted once with its text, sender, and
// ts; a redelivery is not submitted again. The listener posts nothing itself.
func TestDMIsSubmittedOnce(t *testing.T) {
	l, s, p, _, logs := newListener(t)
	if err := runWith(t, l, s, dm("Ev1", "my password is hunter2"), dm("Ev1", "my password is hunter2")); err != nil {
		t.Fatalf("Run: %v", err)
	}
	want := agent.Inbound{
		EventID: "Ev1", EventType: "message.im", ChannelID: "D0123", ThreadTS: "1700000000.000100",
		TS: "1700000000.000100", UserID: "U1", Text: "my password is hunter2",
	}
	if got := submitted(l); len(got) != 1 || got[0] != want {
		t.Errorf("submitted = %+v, want [%+v]", got, want)
	}
	if len(p.got()) != 0 {
		t.Errorf("the listener posted %+v; posting is the agent's job", p.got())
	}
	if len(s.acks) != 2 {
		t.Errorf("acks = %v, want both deliveries acked", s.acks)
	}
	out := logs.String()
	for _, want := range []string{`"msg":"slack_event_received"`, `"event_id":"Ev1"`, `"channel_id":"D0123"`, `"event_type":"message.im"`, `"msg":"slack_event_duplicate"`} {
		if !strings.Contains(out, want) {
			t.Errorf("logs lack %s:\n%s", want, out)
		}
	}
	if strings.Contains(out, "hunter2") || strings.Contains(out, "U1") {
		t.Errorf("logs hold message text or a user id:\n%s", out)
	}
}

// covers: AC-12. An @mention in a channel is answered in its thread; one in
// a DM, bot messages, and subtypes are ignored.
func TestFilter(t *testing.T) {
	l, s, _, _, _ := newListener(t)
	events := []socketmode.Event{
		eventsAPI("e1", "Ev1", &slackevents.AppMentionEvent{Channel: "C0IT", User: "U1", TimeStamp: "1.1", ThreadTimeStamp: "1.0"}, "app_mention"),
		eventsAPI("e2", "Ev2", &slackevents.AppMentionEvent{Channel: "D0123", TimeStamp: "2.1"}, "app_mention"),
		eventsAPI("e3", "Ev3", &slackevents.MessageEvent{ChannelType: "im", Channel: "D0123", TimeStamp: "3.1", BotID: "B1"}, "message"),
		eventsAPI("e4", "Ev4", &slackevents.MessageEvent{ChannelType: "im", Channel: "D0123", TimeStamp: "4.1", SubType: "message_changed"}, "message"),
		eventsAPI("e5", "Ev5", &slackevents.MessageEvent{ChannelType: "channel", Channel: "C0IT", TimeStamp: "5.1"}, "message"),
		eventsAPI("e6", "Ev6", &slackevents.AppMentionEvent{Channel: "C0IT", TimeStamp: "6.1", BotID: "B2"}, "app_mention"),
	}
	if err := runWith(t, l, s, events...); err != nil {
		t.Fatal(err)
	}
	got := submitted(l)
	if len(got) != 1 || got[0].ChannelID != "C0IT" || got[0].ThreadTS != "1.0" || got[0].EventType != agent.EventAppMention {
		t.Errorf("submitted = %+v, want only the channel mention, in thread 1.0", got)
	}
	if len(s.acks) != len(events) {
		t.Errorf("acked %d of %d events", len(s.acks), len(events))
	}
}

// covers: AC-12. With the database down, no record means no reply.
func TestDedupFailureSendsNothing(t *testing.T) {
	l, s, p, d, logs := newListener(t)
	d.err = errors.New("connection refused")
	if err := runWith(t, l, s, dm("Ev9", "help")); err != nil {
		t.Fatal(err)
	}
	if len(submitted(l)) != 0 || len(p.got()) != 0 {
		t.Errorf("submitted %+v despite the dedup failure", submitted(l))
	}
	if !strings.Contains(logs.String(), `"msg":"slack_event_dedup_failed"`) {
		t.Errorf("logs:\n%s", logs.String())
	}
}

// covers: AC-12. Old event ids are purged at start with the TTL cutoff.
func TestPurgeAtStart(t *testing.T) {
	l, s, _, d, _ := newListener(t)
	if err := runWith(t, l, s); err != nil {
		t.Fatal(err)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.cutoffs) == 0 || !d.cutoffs[0].Equal(fixedNow.Add(-24*time.Hour)) {
		t.Errorf("cutoffs = %v, want now minus 24h at start", d.cutoffs)
	}
}

// covers: AC-13. A refused token ends Run naming the setting, never the value.
func TestRefusedTokens(t *testing.T) {
	l, _, p, _, _ := newListener(t)
	p.checkErrs = []error{errors.New("invalid_auth")}
	if err := l.Run(t.Context()); !errors.Is(err, ErrBotTokenRejected) || !strings.Contains(err.Error(), "SLACK_BOT_TOKEN") {
		t.Errorf("bot token: %v", err)
	}

	l, s, _, _, _ := newListener(t)
	s.runErr = errors.New("invalid_auth")
	if err := l.Run(t.Context()); !errors.Is(err, ErrAppTokenRejected) || !strings.Contains(err.Error(), "SLACK_APP_TOKEN") {
		t.Errorf("app token: %v", err)
	}
}

// covers: AC-13. A failed first connect retries and logs each attempt.
func TestCheckRetries(t *testing.T) {
	l, s, p, _, logs := newListener(t)
	p.checkErrs = []error{errors.New("dial tcp: no route to host"), errors.New("dial tcp: no route to host")}
	if err := runWith(t, l, s); err != nil {
		t.Fatal(err)
	}
	if p.checks != 3 {
		t.Errorf("checks = %d, want 3", p.checks)
	}
	if n := strings.Count(logs.String(), `"msg":"slack_connect_failed"`); n != 2 {
		t.Errorf("%d slack_connect_failed lines, want 2:\n%s", n, logs.String())
	}
}

func TestBackoff(t *testing.T) {
	if backoff(1) != time.Second || backoff(3) != 4*time.Second || backoff(20) != maxBackoff {
		t.Errorf("backoff = %v %v %v", backoff(1), backoff(3), backoff(20))
	}
}

// covers: AC-13, AC-14. slack-go's own connect and reconnect attempts are
// logged one line each, with the attempt and the backoff.
func TestConnectionEventsAreLogged(t *testing.T) {
	l, s, _, _, logs := newListener(t)
	events := []socketmode.Event{
		{Type: socketmode.EventTypeConnecting, Data: &slackapi.ConnectingEvent{Attempt: 1}},
		{Type: socketmode.EventTypeConnectionError, Data: &slackapi.ConnectionErrorEvent{Attempt: 2, Backoff: 3 * time.Second, ErrorObj: errors.New("dial tcp: i/o timeout")}},
		{Type: socketmode.EventTypeConnected},
		{Type: socketmode.EventTypeDisconnect},
	}
	if err := runWith(t, l, s, events...); err != nil {
		t.Fatal(err)
	}
	out := logs.String()
	for _, want := range []string{
		`"msg":"slack_connecting","attempt":1`,
		`"msg":"slack_connect_failed","api":"apps.connections.open","attempt":2,"backoff_ms":3000`,
		`"msg":"slack_connected"`,
		`"msg":"slack_disconnected"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("logs lack %s:\n%s", want, out)
		}
	}
}

// covers: AC-12. A DM sent inside a thread is answered in that thread, not
// under the new message.
func TestThreadedDMRepliesInItsThread(t *testing.T) {
	l, s, _, _, _ := newListener(t)
	threaded := eventsAPI("e7", "Ev7", &slackevents.MessageEvent{
		ChannelType: "im", Channel: "D0123", User: "U1", TimeStamp: "7.5", ThreadTimeStamp: "7.0",
	}, "message")
	if err := runWith(t, l, s, threaded); err != nil {
		t.Fatal(err)
	}
	if got := submitted(l); len(got) != 1 || got[0].ThreadTS != "7.0" || got[0].TS != "7.5" {
		t.Errorf("submitted = %+v, want one in thread 7.0 with ts 7.5", got)
	}
}

// covers: AC-12. An event with no event id cannot be deduplicated, so it is
// acked and dropped, never answered.
func TestEventWithoutIDIsIgnored(t *testing.T) {
	l, s, p, d, _ := newListener(t)
	if err := runWith(t, l, s, dm("", "hello")); err != nil {
		t.Fatal(err)
	}
	if len(submitted(l)) != 0 || len(p.got()) != 0 {
		t.Errorf("submitted an event with no id: %+v", submitted(l))
	}
	if len(s.acks) != 1 {
		t.Errorf("acks = %v, want the delivery acked", s.acks)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.seen) != 0 {
		t.Errorf("marked %v as seen", d.seen)
	}
}

// covers: AC-12. A failed purge is logged and the listener keeps answering.
func TestPurgeFailureKeepsListening(t *testing.T) {
	l, s, _, d, logs := newListener(t)
	d.purgeErr = errors.New("connection refused")
	if err := runWith(t, l, s, dm("Ev8", "hello")); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(logs.String(), `"msg":"slack_event_purge_failed"`) {
		t.Errorf("logs lack slack_event_purge_failed:\n%s", logs.String())
	}
	if len(submitted(l)) != 1 {
		t.Errorf("submitted = %+v, want the DM handed on", submitted(l))
	}
}

// covers: AC-2 (spec 0008). A message with no sender is never handed on.
func TestMessageWithoutUserIsIgnored(t *testing.T) {
	l, s, _, _, _ := newListener(t)
	noUser := eventsAPI("e8", "Ev8", &slackevents.MessageEvent{ChannelType: "im", Channel: "D0123", TimeStamp: "8.1", Text: "hi"}, "message")
	if err := runWith(t, l, s, noUser); err != nil {
		t.Fatal(err)
	}
	if got := submitted(l); len(got) != 0 {
		t.Errorf("submitted %+v", got)
	}
}

// covers: spec 0008, Value sourcing (workspace team). CheckBotToken returns
// auth.test's team id, and Run does not check again after it.
func TestCheckBotTokenReturnsTheTeam(t *testing.T) {
	l, s, p, _, _ := newListener(t)
	team, err := l.CheckBotToken(t.Context())
	if err != nil || team != testTeam {
		t.Fatalf("CheckBotToken = %q, %v", team, err)
	}
	if err := runWith(t, l, s); err != nil {
		t.Fatal(err)
	}
	if p.checks != 1 {
		t.Errorf("checks = %d, want 1", p.checks)
	}
}

func TestRunNeedsAHandler(t *testing.T) {
	l, _, _, _, _ := newListener(t)
	l.Handler = nil
	if err := l.Run(t.Context()); !errors.Is(err, ErrNoHandler) {
		t.Errorf("Run = %v, want ErrNoHandler", err)
	}
}

// covers: AC-3 (spec 0008). users.info maps onto every gate field.
func TestToSlackUser(t *testing.T) {
	u := &slackapi.User{
		TeamID: "T1", Deleted: true, IsBot: true, IsAppUser: true, IsRestricted: true,
		IsUltraRestricted: true, IsStranger: true, IsEmailConfirmed: true,
		Profile: slackapi.UserProfile{Email: "a@noord.example"},
	}
	want := agent.SlackUser{
		TeamID: "T1", Deleted: true, IsBot: true, IsAppUser: true, IsRestricted: true,
		IsUltraRestricted: true, IsStranger: true, IsEmailConfirmed: true, Email: "a@noord.example",
	}
	if got := toSlackUser(u); got != want {
		t.Errorf("toSlackUser = %+v, want %+v", got, want)
	}
}

// covers: AC-3 (spec 0010). Listener failures log a fixed cause token, never
// the error's text: a Postgres error gives postgres_<sqlstate>, a Slack Web
// API error from auth.test or a socket connect gives its slack_ token.
func TestListenerLogsCauseNotErrorText(t *testing.T) {
	l, s, p, d, logs := newListener(t)
	d.err = &pgconn.PgError{Code: "57P01", Message: "ZEBRA terminating connection"}
	d.purgeErr = &pgconn.PgError{Code: "08006", Message: "ZEBRA connection failure"}
	p.checkErrs = []error{wrapErr(slackapi.SlackErrorResponse{Err: "ratelimited"})}
	connErr := &slackapi.ConnectionErrorEvent{Attempt: 2, Backoff: time.Second, ErrorObj: slackapi.StatusCodeError{Code: 503, Status: "ZEBRA unavailable"}}
	events := []socketmode.Event{
		{Type: socketmode.EventTypeConnectionError, Data: connErr},
		dm("Ev10", "ZEBRA help"),
	}
	if err := runWith(t, l, s, events...); err != nil {
		t.Fatal(err)
	}
	out := logs.String()
	for _, want := range []string{
		`"msg":"slack_connect_failed","api":"auth.test","attempt":1,"backoff_ms":1000,"cause":"slack_ratelimited"`,
		`"msg":"slack_connect_failed","api":"apps.connections.open","attempt":2,"backoff_ms":1000,"cause":"slack_503"`,
		`"msg":"slack_event_dedup_failed","event_id":"Ev10","cause":"postgres_57P01"`,
		`"msg":"slack_event_purge_failed","cause":"postgres_08006"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("logs lack %s:\n%s", want, out)
		}
	}
	if strings.Contains(out, "ZEBRA") || strings.Contains(out, `"error"`) {
		t.Errorf("logs carry error text:\n%s", out)
	}
}

// interaction is a Socket Mode interactive event with one block action.
func interaction(envelope, actionID, value, user string) socketmode.Event {
	cb := slackapi.InteractionCallback{Type: slackapi.InteractionTypeBlockActions, TriggerID: "TRIGGER-SECRET"}
	cb.User.ID, cb.Channel.ID = user, "G0ITDESK"
	cb.ActionCallback.BlockActions = []*slackapi.BlockAction{{ActionID: actionID, Value: value}}
	return socketmode.Event{
		Type: socketmode.EventTypeInteractive, Data: cb,
		Request: &socketmode.Request{Type: "interactive", EnvelopeID: envelope},
	}
}

// covers: spec 0012 AC-8. Approve and Deny clicks are acked and handed on
// with the clicker from the payload; a bad value and any other action are
// acked and ignored; logs hold no user id and no trigger id.
func TestApprovalClicks(t *testing.T) {
	l, s, _, _, logs := newListener(t)
	id := "6f1c2c4e-8d0a-4f53-9a51-0c3e5b7d9a10"
	err := runWith(t, l, s,
		interaction("env-1", agent.ActionApprove, id, "U0ITLEAD01"),
		interaction("env-2", agent.ActionDeny, id, "U0ITLEAD02"),
		interaction("env-3", agent.ActionApprove, "not-a-uuid", "U0ITLEAD01"),
		interaction("env-4", "something_else", id, "U0ITLEAD01"),
	)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	got := clicked(l)
	if len(got) != 2 || !got[0].Approve || got[0].UserID != "U0ITLEAD01" || got[0].ApprovalID.String() != id ||
		got[0].ChannelID != "G0ITDESK" || got[0].EventID != "env-1" || got[1].Approve || got[1].UserID != "U0ITLEAD02" {
		t.Errorf("clicks = %+v", got)
	}
	if len(s.acks) != 4 {
		t.Errorf("acks = %v, want every interaction acked", s.acks)
	}
	out := logs.String()
	for _, want := range []string{`"msg":"slack_interaction_received"`, `"kind":"approve"`, `"msg":"slack_interaction_ignored"`} {
		if !strings.Contains(out, want) {
			t.Errorf("logs lack %s:\n%s", want, out)
		}
	}
	if strings.Contains(out, "U0ITLEAD") || strings.Contains(out, "TRIGGER-SECRET") {
		t.Errorf("logs hold a user id or the trigger id:\n%s", out)
	}
}

// covers: spec 0012 AC-7. The card renders as a section of its lines plus an
// actions block named for the approval, with a confirm on Approve; the
// outcome card has no buttons.
func TestCardBlocks(t *testing.T) {
	card := agent.Card{Fallback: "MFA reset request for <@U1>", Lines: []string{"MFA reset request", "Requester: <@U1> (jan)"}, ApprovalID: "abc"}
	blocks := cardBlocks(card)
	if len(blocks) != 2 {
		t.Fatalf("blocks = %d", len(blocks))
	}
	actions, ok := blocks[1].(*slackapi.ActionBlock)
	if !ok || actions.BlockID != "approval:abc" || len(actions.Elements.ElementSet) != 2 {
		t.Fatalf("actions = %#v", blocks[1])
	}
	approve := actions.Elements.ElementSet[0].(*slackapi.ButtonBlockElement)
	deny := actions.Elements.ElementSet[1].(*slackapi.ButtonBlockElement)
	if approve.ActionID != agent.ActionApprove || approve.Value != "abc" || approve.Style != slackapi.StylePrimary ||
		approve.Confirm == nil || approve.Confirm.Text.Text != "Clear this person's MFA methods?" ||
		deny.ActionID != agent.ActionDeny || deny.Style != slackapi.StyleDanger {
		t.Errorf("buttons = %#v %#v", approve, deny)
	}
	section, ok := blocks[0].(*slackapi.SectionBlock)
	if !ok || section.Text.Text != "MFA reset request\nRequester: <@U1> (jan)" {
		t.Errorf("section = %#v", blocks[0])
	}
	card.ApprovalID = ""
	if b := cardBlocks(card); len(b) != 1 {
		t.Errorf("an outcome card has %d blocks, want only the section", len(b))
	}
}
