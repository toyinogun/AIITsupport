package agent

import (
	"context"
	"strconv"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/toyinogun/AIITsupport/internal/mcpclient"
)

// turn is one DM message being handled. Its fields are touched only by the
// worker running it.
type turn struct {
	a     *Agent
	in    Inbound
	ctx   context.Context // the turn deadline (AGENT_MESSAGE_TIMEOUT)
	start time.Time

	conv      Conversation
	hasConv   bool
	requester *mcpclient.Requester // set once code matched the directory account
	account   string               // the matched username
	lookup    map[string]any       // this turn's user_lookup data
	resolved  bool                 // ticket_update closed it in this turn (AC-6)
	escalated bool                 // an ok escalate ended it in this turn (spec 0009, AC-8)

	modelCalls int
	toolCalls  int
	reason     Reason // set when the turn escalates
	cause      string // the fixed cause token of the error that ended it, if any
}

// failure stops the turn and takes the failure path (AC-10).
type failure struct {
	reason  Reason
	cause   string // the fixed token of the error behind it ("none" without one)
	unknown bool   // the last tool call never got an answer (AC-11)
}

func fail(r Reason) *failure { return &failure{reason: r, cause: cause(nil)} }

// failErr is a failure caused by err; only err's fixed cause token is kept.
func failErr(r Reason, err error) *failure { return &failure{reason: r, cause: cause(err)} }

// runTurn handles one DM message from the moment it holds a concurrency slot.
func (a *Agent) runTurn(in Inbound) {
	start := a.deps.Now()
	ctx, cancel := context.WithTimeout(a.workCtx, a.cfg.MessageTimeout)
	defer cancel()
	ctx, span := tracer.Start(ctx, "agent.turn", trace.WithAttributes(attribute.String("event_type", in.EventType)))
	defer span.End()

	t := &turn{a: a, in: in, ctx: ctx, start: start}
	outcome := t.run()

	attrs := []attribute.KeyValue{
		attribute.String("outcome", outcome),
		attribute.Int("model_calls", t.modelCalls),
		attribute.Int("tool_calls", t.toolCalls),
	}
	if t.reason != "" {
		attrs = append(attrs, attribute.String("escalation_reason", string(t.reason)))
	}
	if t.cause != "" {
		attrs = append(attrs, attribute.String("cause", t.cause))
	}
	span.SetAttributes(attrs...)
	a.logTurnDone(ctx, in, outcome, t.modelCalls, t.toolCalls, start)
}

// run is the turn table of spec 0008, steps 3 to 11; it returns the outcome.
func (t *turn) run() string {
	text := strings.TrimSpace(t.in.Text)
	if text == "" {
		t.post(TextEmptyMessage, "empty_message")
		return OutcomeEmpty
	}
	conv, err := t.a.deps.Store.UpsertConversation(t.ctx, t.in.ChannelID, t.in.ThreadTS, t.in.UserID)
	if err != nil {
		return t.failurePath(t.storeFailure(err))
	}
	t.conv, t.hasConv = conv, true
	if conv.Status != StatusOpen {
		t.post(TextClosedThread, "closed_thread")
		return OutcomeClosedThread
	}
	if conv.LastSeq == 0 {
		if out, done := t.checkHourlyCap(); done {
			return out
		}
	}
	t.post(TextAck, "ack")

	if out, done := t.identify(); done {
		return out
	}
	if f := t.storeUserMessage(); f != nil {
		return t.failurePath(f)
	}
	answer, f := t.loop()
	if f != nil {
		return t.failurePath(f)
	}
	var footer, outcome string
	switch {
	case t.resolved:
		footer, outcome = FooterResolved, OutcomeResolved
	case t.escalated:
		footer, outcome = FooterEscalated, OutcomeEscalated
	default:
		answer, footer, outcome = t.backstop(answer)
	}
	t.post(answer+"\n\n"+footer, "answer")
	return outcome
}

// accountName is the directory account for the IT post: the conversation's,
// or this turn's match when the row has none yet (spec 0009, AC-9).
func (t *turn) accountName() string {
	if t.conv.DirectoryAccount != "" {
		return t.conv.DirectoryAccount
	}
	return t.account
}

// newTicket is id when the conversation has no ticket yet, else 0, because
// SetConversationStatus overwrites a stored id (spec 0009, AC-8).
func (t *turn) newTicket(id int64) int64 {
	if t.conv.TicketID != 0 {
		return 0
	}
	return id
}

// checkHourlyCap stops a requester who started too many conversations in the
// last hour, this one included (AC-19).
func (t *turn) checkHourlyCap() (string, bool) {
	since := t.a.deps.Now().Add(-time.Hour)
	n, err := t.a.deps.Store.CountRecentConversations(t.ctx, t.in.UserID, since)
	if err != nil {
		return t.failurePath(t.storeFailure(err)), true
	}
	if n <= t.a.cfg.ConversationsPerHour {
		return "", false
	}
	return t.refuse(ReasonTooMany, TextTooMany), true
}

// storeUserMessage records the first matched account and appends the
// user's message as a text block (turn step 9).
func (t *turn) storeUserMessage() *failure {
	if t.conv.DirectoryAccount == "" {
		conv, err := t.a.deps.Store.SetStatus(t.ctx, t.conv.ID, StatusChange{Status: StatusOpen, DirectoryAccount: t.account})
		if err != nil {
			return t.storeFailure(err)
		}
		t.conv = conv
	}
	content, err := encodeBlocks(userText(t.in.Text))
	if err != nil {
		return failErr(ReasonStoreUnavailable, err)
	}
	if err := t.a.deps.Store.AppendMessage(t.ctx, t.conv.ID, RoleUser, content); err != nil {
		return t.storeFailure(err)
	}
	return nil
}

// storeFailure names a failed store call: the turn deadline when it has
// passed, else the database.
func (t *turn) storeFailure(err error) *failure {
	if t.deadlinePassed() {
		return failErr(ReasonMessageTimeout, err)
	}
	return failErr(ReasonStoreUnavailable, err)
}

func (t *turn) deadlinePassed() bool { return t.ctx.Err() != nil }

// refuse escalates without a ticket and posts a fixed text: the gate, the
// directory match, and the hourly cap.
func (t *turn) refuse(r Reason, text string) string {
	t.reason = r
	t.setStatus(StatusChange{Status: StatusEscalated})
	t.a.logEscalated(t.ctx, t.in.EventID, r, false, cause(nil))
	t.post(text, string(r))
	return OutcomeEscalated
}

// setStatus writes a status change on a detached context; a failure is
// logged and the turn goes on to its post.
func (t *turn) setStatus(ch StatusChange) {
	if !t.hasConv {
		return
	}
	ctx, cancel := t.a.detached(t.ctx, t.a.cfg.ToolTimeout)
	defer cancel()
	conv, err := t.a.deps.Store.SetStatus(ctx, t.conv.ID, ch)
	if err != nil {
		t.a.deps.Logger.Warn("agent_status_write_failed", "conversation_id", t.conv.ID)
		return
	}
	t.conv = conv
}

func (t *turn) post(text, kind string) { t.a.post(t.ctx, t.in, text, kind) }

func (a *Agent) logTurnDone(ctx context.Context, in Inbound, outcome string, modelCalls, toolCalls int, start time.Time) {
	latency := a.deps.Now().Sub(start)
	if sent, ok := slackTime(in.TS); ok {
		latency = a.deps.Now().Sub(sent)
	}
	a.deps.Logger.Info("agent_turn_done",
		"event_id", in.EventID, "channel_id", in.ChannelID, "outcome", outcome,
		"model_calls", modelCalls, "tool_calls", toolCalls,
		"latency_ms", latency.Milliseconds(), "trace_id", traceID(ctx))
}

// logEscalated logs one move to escalated; cause is a fixed token from cause().
func (a *Agent) logEscalated(ctx context.Context, eventID string, r Reason, ticket bool, cause string) {
	a.deps.Logger.Info("agent_escalated", "event_id", eventID, "reason", string(r), "ticket", ticket,
		"cause", cause, "trace_id", traceID(ctx))
}

// slackTime parses a Slack ts ("1760000000.000100") as a time.
func slackTime(ts string) (time.Time, bool) {
	secs, frac, _ := strings.Cut(ts, ".")
	s, err := strconv.ParseInt(secs, 10, 64)
	if err != nil {
		return time.Time{}, false
	}
	var micros int64
	if frac != "" {
		frac = (frac + "000000")[:6]
		if micros, err = strconv.ParseInt(frac, 10, 64); err != nil {
			return time.Time{}, false
		}
	}
	return time.Unix(s, micros*int64(time.Microsecond)), true
}

func traceID(ctx context.Context) string {
	sc := trace.SpanContextFromContext(ctx)
	if !sc.HasTraceID() {
		return ""
	}
	return sc.TraceID().String()
}
