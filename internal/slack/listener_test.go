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

	"github.com/slack-go/slack/slackevents"
	"github.com/slack-go/slack/socketmode"
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
	mu        sync.Mutex
	checks    int
	replies   []reply
}

func (p *fakePoster) Check(context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.checks++
	if len(p.checkErrs) > 0 {
		err := p.checkErrs[0]
		p.checkErrs = p.checkErrs[1:]
		return err
	}
	return nil
}

func (p *fakePoster) Reply(_ context.Context, channel, thread, text string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.replies = append(p.replies, reply{channel, thread, text})
	return nil
}

func (p *fakePoster) got() []reply {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]reply(nil), p.replies...)
}

type fakeDedup struct {
	mu      sync.Mutex
	seen    map[string]bool
	err     error
	cutoffs []time.Time
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
		Socket: s, Poster: p, Dedup: d, TTL: 24 * time.Hour,
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

// covers: AC-12. A DM is acked, marked, logged without its text, and
// answered once in its thread; a redelivery gets no second reply.
func TestDMGetsOneHoldingReply(t *testing.T) {
	l, s, p, _, logs := newListener(t)
	if err := runWith(t, l, s, dm("Ev1", "my password is hunter2"), dm("Ev1", "my password is hunter2")); err != nil {
		t.Fatalf("Run: %v", err)
	}
	got := p.got()
	if len(got) != 1 || got[0] != (reply{"D0123", "1700000000.000100", HoldingReply}) {
		t.Errorf("replies = %+v", got)
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
	l, s, p, _, _ := newListener(t)
	events := []socketmode.Event{
		eventsAPI("e1", "Ev1", &slackevents.AppMentionEvent{Channel: "C0IT", TimeStamp: "1.1", ThreadTimeStamp: "1.0"}, "app_mention"),
		eventsAPI("e2", "Ev2", &slackevents.AppMentionEvent{Channel: "D0123", TimeStamp: "2.1"}, "app_mention"),
		eventsAPI("e3", "Ev3", &slackevents.MessageEvent{ChannelType: "im", Channel: "D0123", TimeStamp: "3.1", BotID: "B1"}, "message"),
		eventsAPI("e4", "Ev4", &slackevents.MessageEvent{ChannelType: "im", Channel: "D0123", TimeStamp: "4.1", SubType: "message_changed"}, "message"),
		eventsAPI("e5", "Ev5", &slackevents.MessageEvent{ChannelType: "channel", Channel: "C0IT", TimeStamp: "5.1"}, "message"),
		eventsAPI("e6", "Ev6", &slackevents.AppMentionEvent{Channel: "C0IT", TimeStamp: "6.1", BotID: "B2"}, "app_mention"),
	}
	if err := runWith(t, l, s, events...); err != nil {
		t.Fatal(err)
	}
	got := p.got()
	if len(got) != 1 || got[0].channel != "C0IT" || got[0].thread != "1.0" {
		t.Errorf("replies = %+v, want only the channel mention, in thread 1.0", got)
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
	if len(p.got()) != 0 {
		t.Errorf("replied despite the dedup failure: %+v", p.got())
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
