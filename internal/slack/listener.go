// Package slack is the agent's Slack strand (spec 0006): a Socket Mode
// listener that hears a DM or an @mention, acknowledges it at once, drops
// duplicates through the slack_events table, and answers in the message's
// thread. Until the agent loop exists (#10) the answer is a fixed holding
// reply. Nothing listens on the internet: Socket Mode dials out.
package slack

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	slackapi "github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
	"github.com/slack-go/slack/socketmode"
	"golang.org/x/sync/errgroup"
)

// HoldingReply is the fixed answer until #10 plugs the agent loop in.
const HoldingReply = "Hi, I'm Noord's IT assistant. I can't handle requests yet, so please raise a ticket in the helpdesk or ask Ruben."

const (
	// purgeEvery is how often old event ids are removed (and once at start).
	purgeEvery = time.Hour
	// replyTimeout bounds one chat.postMessage call.
	replyTimeout = 10 * time.Second
	// dedupTimeout bounds one MarkEventSeen call.
	dedupTimeout = 5 * time.Second

	firstBackoff = time.Second
	maxBackoff   = 5 * time.Minute
)

// Errors that end run(): Slack refused a token. They name the setting,
// never the value (AC-13).
var (
	ErrBotTokenRejected = errors.New("SLACK_BOT_TOKEN was rejected by Slack (invalid_auth or revoked); check the token in .env")
	ErrAppTokenRejected = errors.New("SLACK_APP_TOKEN was rejected by Slack (invalid_auth or revoked); check the app level token in .env")
)

// Dedup marks Slack event ids seen (spec 0003). *queries.Queries satisfies it.
type Dedup interface {
	MarkEventSeen(ctx context.Context, eventID string) (int64, error)
	PurgeSeenEventsBefore(ctx context.Context, cutoff time.Time) (int64, error)
}

// Poster talks to the Web API with the bot token.
type Poster interface {
	// Check confirms the bot token works (auth.test).
	Check(ctx context.Context) error
	// Reply posts text in a thread.
	Reply(ctx context.Context, channel, threadTS, text string) error
}

// Socket is the Socket Mode connection with the app token.
type Socket interface {
	Events() <-chan socketmode.Event
	Ack(req socketmode.Request) error
	// Run connects, reconnects with backoff after a drop, and returns only
	// on a fatal error or when ctx ends.
	Run(ctx context.Context) error
}

// Listener answers Slack messages once each.
type Listener struct {
	Socket Socket
	Poster Poster
	Dedup  Dedup
	Logger *slog.Logger
	// TTL is how long event ids are kept (SLACK_EVENT_DEDUP_TTL).
	TTL time.Duration
	// Now defaults to time.Now.
	Now func() time.Time
	// sleep defaults to a context aware sleep; tests replace it.
	sleep func(context.Context, time.Duration) error
}

// New builds a listener on the real Slack APIs.
func New(botToken, appToken string, dedup Dedup, logger *slog.Logger, ttl time.Duration) *Listener {
	api := slackapi.New(botToken, slackapi.OptionAppLevelToken(appToken))
	return &Listener{
		Socket: socket{client: socketmode.New(api)},
		Poster: poster{api: api},
		Dedup:  dedup, Logger: logger, TTL: ttl,
	}
}

// Run checks the bot token, then listens until ctx ends. It returns an
// error only when Slack refuses a token or dedup cannot start.
func (l *Listener) Run(ctx context.Context) error {
	if err := l.checkBotToken(ctx); err != nil {
		return err
	}
	g, ctx := errgroup.WithContext(ctx)
	g.Go(func() error { return l.purgeLoop(ctx) })
	g.Go(func() error { return l.runSocket(ctx) })
	g.Go(func() error { return l.eventLoop(ctx) })
	err := g.Wait()
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

// checkBotToken retries auth.test with backoff until Slack answers; a
// refused token is fatal.
func (l *Listener) checkBotToken(ctx context.Context) error {
	for attempt := 1; ; attempt++ {
		err := l.Poster.Check(ctx)
		if err == nil {
			return nil
		}
		if isAuthError(err) {
			return ErrBotTokenRejected
		}
		wait := backoff(attempt)
		l.Logger.Warn("slack_connect_failed", "api", "auth.test", "attempt", attempt, "backoff_ms", wait.Milliseconds(), "error", err.Error())
		if err := l.sleepFor(ctx, wait); err != nil {
			return nil //nolint:nilerr // shutting down during the retry is not a failure
		}
	}
}

func (l *Listener) runSocket(ctx context.Context) error {
	err := l.Socket.Run(ctx)
	switch {
	case ctx.Err() != nil:
		return ctx.Err()
	case isAuthError(err):
		return ErrAppTokenRejected
	case err != nil:
		return fmt.Errorf("slack socket mode: %w", err)
	}
	return nil
}

func (l *Listener) eventLoop(ctx context.Context) error {
	events := l.Socket.Events()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case evt, ok := <-events:
			if !ok {
				return nil
			}
			l.handle(ctx, evt)
		}
	}
}

func (l *Listener) handle(ctx context.Context, evt socketmode.Event) {
	switch evt.Type {
	case socketmode.EventTypeConnecting:
		if e, ok := evt.Data.(*slackapi.ConnectingEvent); ok {
			l.Logger.Info("slack_connecting", "attempt", e.Attempt)
		}
	case socketmode.EventTypeConnectionError:
		if e, ok := evt.Data.(*slackapi.ConnectionErrorEvent); ok {
			l.Logger.Warn("slack_connect_failed", "api", "apps.connections.open", "attempt", e.Attempt, "backoff_ms", e.Backoff.Milliseconds(), "error", e.Error())
		}
	case socketmode.EventTypeConnected:
		l.Logger.Info("slack_connected")
	case socketmode.EventTypeDisconnect:
		l.Logger.Warn("slack_disconnected")
	case socketmode.EventTypeEventsAPI:
		if evt.Request != nil {
			if err := l.Socket.Ack(*evt.Request); err != nil {
				l.Logger.Warn("slack_ack_failed", "error", err.Error())
			}
		}
		if api, ok := evt.Data.(slackevents.EventsAPIEvent); ok {
			l.onEvent(ctx, api)
		}
	}
}

// message is the part of an event the listener needs.
type message struct {
	eventID   string
	eventType string
	channel   string
	threadTS  string
}

// onEvent decides, dedups, logs, and replies.
func (l *Listener) onEvent(ctx context.Context, api slackevents.EventsAPIEvent) {
	msg, ok := toMessage(api)
	if !ok {
		return
	}
	dctx, cancel := context.WithTimeout(ctx, dedupTimeout)
	n, err := l.Dedup.MarkEventSeen(dctx, msg.eventID)
	cancel()
	if err != nil {
		// At most once (spec 0003): no record, no reply.
		l.Logger.Error("slack_event_dedup_failed", "event_id", msg.eventID, "error", err.Error())
		return
	}
	if n == 0 {
		l.Logger.Info("slack_event_duplicate", "event_id", msg.eventID)
		return
	}
	l.Logger.Info("slack_event_received", "event_id", msg.eventID, "channel_id", msg.channel, "event_type", msg.eventType)

	rctx, cancel := context.WithTimeout(ctx, replyTimeout)
	defer cancel()
	if err := l.Poster.Reply(rctx, msg.channel, msg.threadTS, HoldingReply); err != nil {
		l.Logger.Warn("slack_reply_failed", "event_id", msg.eventID, "channel_id", msg.channel, "error", err.Error())
	}
}

// toMessage keeps a DM (message.im) or an @mention outside a DM, from a
// person, with no subtype. Everything else is ignored.
func toMessage(api slackevents.EventsAPIEvent) (message, bool) {
	outer, ok := api.Data.(*slackevents.EventsAPICallbackEvent)
	if !ok || outer.EventID == "" {
		return message{}, false
	}
	m := message{eventID: outer.EventID}
	switch e := api.InnerEvent.Data.(type) {
	case *slackevents.AppMentionEvent:
		// A DM that mentions the bot also arrives as message.im; answer that one.
		if e.BotID != "" || isDM(e.Channel) {
			return message{}, false
		}
		m.eventType, m.channel, m.threadTS = string(slackevents.AppMention), e.Channel, threadOf(e.ThreadTimeStamp, e.TimeStamp)
	case *slackevents.MessageEvent:
		if e.ChannelType != "im" || e.BotID != "" || e.SubType != "" {
			return message{}, false
		}
		m.eventType, m.channel, m.threadTS = "message.im", e.Channel, threadOf(e.ThreadTimeStamp, e.TimeStamp)
	default:
		return message{}, false
	}
	if m.channel == "" || m.threadTS == "" {
		return message{}, false
	}
	return m, true
}

// isDM reports a direct message channel id (Slack's D prefix).
func isDM(channel string) bool { return strings.HasPrefix(channel, "D") }

func threadOf(threadTS, ts string) string {
	if threadTS != "" {
		return threadTS
	}
	return ts
}

// purgeLoop removes event ids older than TTL at start and then hourly.
func (l *Listener) purgeLoop(ctx context.Context) error {
	ticker := time.NewTicker(purgeEvery)
	defer ticker.Stop()
	for {
		l.purge(ctx)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (l *Listener) purge(ctx context.Context) {
	cutoff := l.now().Add(-l.TTL)
	n, err := l.Dedup.PurgeSeenEventsBefore(ctx, cutoff)
	if err != nil {
		if ctx.Err() == nil {
			l.Logger.Warn("slack_event_purge_failed", "error", err.Error())
		}
		return
	}
	l.Logger.Info("slack_events_purged", "removed", n)
}

func (l *Listener) now() time.Time {
	if l.Now != nil {
		return l.Now()
	}
	return time.Now()
}

func (l *Listener) sleepFor(ctx context.Context, d time.Duration) error {
	if l.sleep != nil {
		return l.sleep(ctx, d)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// backoff doubles from one second, capped at five minutes.
func backoff(attempt int) time.Duration {
	if attempt > 9 {
		return maxBackoff
	}
	return min(firstBackoff<<(attempt-1), maxBackoff)
}

// isAuthError reports Slack refusing a token.
func isAuthError(err error) bool {
	if err == nil {
		return false
	}
	switch err.Error() {
	case "invalid_auth", "not_authed", "token_revoked", "account_inactive":
		return true
	}
	return false
}
