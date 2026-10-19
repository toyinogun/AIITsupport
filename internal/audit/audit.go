// Package audit is the only way the tool server writes the audit log (spec
// 0003). Run records an intent row before an action and an outcome row after
// it, so no write ever happens without a record: "no audit row, no write".
// The database backs this up: only noord_toolserver may insert audit rows,
// and a trigger stops every role, the owner included, from changing one.
package audit

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/toyinogun/AIITsupport/internal/db/queries"
)

// Tier is a tool's risk tier; the tool registry (feature #9) sets one per tool.
type Tier string

// Risk tiers, matching the audit_log CHECK.
const (
	TierRead    Tier = "read"
	TierLow     Tier = "low"
	TierMedium  Tier = "medium"
	TierBlocked Tier = "blocked"
)

func (t Tier) valid() bool {
	switch t {
	case TierRead, TierLow, TierMedium, TierBlocked:
		return true
	}
	return false
}

// Outcome is how a call ended, matching the audit_log CHECK.
type Outcome string

// Outcomes. An intent with no outcome row means the outcome is unknown.
const (
	OutcomeOK              Outcome = "ok"
	OutcomeError           Outcome = "error"
	OutcomeDeniedPolicy    Outcome = "denied_policy"
	OutcomeDeniedIdentity  Outcome = "denied_identity"
	OutcomeReadOnly        Outcome = "read_only"
	OutcomePendingApproval Outcome = "pending_approval"
	OutcomeDeniedApproval  Outcome = "denied_approval"
	OutcomeExpired         Outcome = "expired"
)

func (o Outcome) valid() bool {
	switch o {
	case OutcomeOK, OutcomeError, OutcomeDeniedPolicy, OutcomeDeniedIdentity,
		OutcomeReadOnly, OutcomePendingApproval, OutcomeDeniedApproval, OutcomeExpired:
		return true
	}
	return false
}

// refusal reports whether o ends a call that never ran (Refuse accepts only these).
func (o Outcome) refusal() bool {
	switch o {
	case OutcomeDeniedPolicy, OutcomeDeniedIdentity, OutcomeReadOnly, OutcomeDeniedApproval, OutcomeExpired:
		return true
	}
	return false
}

var (
	// ErrAuditUnavailable means the intent could not be recorded, so the
	// action was not run and nothing changed.
	ErrAuditUnavailable = errors.New("audit log unavailable, action not run")
	// ErrAuditIncomplete means the action ran but its outcome row could not
	// be written; the intent stays visible as an unconfirmed call.
	ErrAuditIncomplete = errors.New("action ran but its audit outcome was not recorded")
	// ErrInvalidCall means the caller passed a call the audit log cannot
	// record; it is a programming error, and the action was not run.
	ErrInvalidCall = errors.New("invalid audit call")
)

// Call describes one tool call. The requester and thread come from MCP _meta
// (set by agent code, never the model); tier and stored keys from the registry.
type Call struct {
	Tool             string
	Tier             Tier
	RequesterSlackID string
	RequesterAccount string // directory account; empty when no match
	ChannelID        string
	ThreadTS         string
	TicketID         int64 // Zammad ticket id; 0 when none
	Target           string
	Args             map[string]any
	// Store and StoreResult list the top level keys kept in the audit row.
	// Every other key is dropped and only counted. Never allow a free text key.
	Store       []string
	StoreResult []string
	TraceID     string
	ApprovalID  uuid.UUID // uuid.Nil when the call is not an approved execution
	// ApproverSlackID is who approved the call or decided the approval.
	ApproverSlackID string
}

// Result is what an action reports back to Run.
type Result struct {
	Outcome Outcome // empty means OutcomeOK
	// Reason says why, for an outcome other than ok. It is stored as is, so
	// it must be fixed text the tool code writes, never request or ticket data.
	Reason string
	Data   map[string]any
}

// Config holds the audit settings from internal/config.
type Config struct {
	OutcomeRetries  int           // AUDIT_OUTCOME_RETRIES
	WriteTimeout    time.Duration // AUDIT_WRITE_TIMEOUT, per insert attempt
	MaxPayloadBytes int           // AUDIT_MAX_PAYLOAD_BYTES
}

const (
	firstBackoff = 50 * time.Millisecond
	maxBackoff   = time.Second
	// minPayloadBytes leaves room for the truncation marker itself.
	minPayloadBytes = 64
	// maxErrorRunes matches the audit_log.error CHECK.
	maxErrorRunes = 500
)

// Recorder writes audit rows through a Store.
type Recorder struct {
	store  Store
	cfg    Config
	logger *slog.Logger
	sleep  func(time.Duration)
}

// NewRecorder checks cfg, so the tool server refuses to start on a bad value.
func NewRecorder(store Store, cfg Config, logger *slog.Logger) (*Recorder, error) {
	switch {
	case cfg.OutcomeRetries < 0:
		return nil, fmt.Errorf("AUDIT_OUTCOME_RETRIES must be at least 0, got %d", cfg.OutcomeRetries)
	case cfg.WriteTimeout <= 0:
		return nil, fmt.Errorf("AUDIT_WRITE_TIMEOUT must be positive, got %s", cfg.WriteTimeout)
	case cfg.MaxPayloadBytes < minPayloadBytes:
		return nil, fmt.Errorf("AUDIT_MAX_PAYLOAD_BYTES must be at least %d, got %d", minPayloadBytes, cfg.MaxPayloadBytes)
	}
	return &Recorder{store: store, cfg: cfg, logger: logger, sleep: time.Sleep}, nil
}

// Run records the intent, runs action, then records the outcome. If the
// intent cannot be recorded, action is never called. The outcome is written
// even if ctx is cancelled meanwhile, and even if action panics (Run then
// panics again). If the outcome still fails after the configured retries,
// Run returns the action's result with ErrAuditIncomplete. An action error is
// returned whole but stored only as its WithReason reason (or "action
// failed"), and a panic only as "action panicked".
func (r *Recorder) Run(ctx context.Context, c Call, action func(context.Context) (Result, error)) (Result, error) {
	intent, err := r.intentParams(c)
	if err != nil {
		return Result{}, err
	}
	callID, err := r.insertIntent(ctx, r.store, intent)
	if err != nil {
		return Result{}, err
	}

	returned := false
	defer func() {
		if returned {
			return
		}
		p := recover()
		if p == nil { // runtime.Goexit: nothing to panic with
			_ = r.recordOutcome(ctx, callID, c, Result{}, WithReason(reasonActionExited, nil))
			return
		}
		_ = r.recordOutcome(ctx, callID, c, Result{}, WithReason(reasonActionPanicked, nil))
		panic(p)
	}()
	res, actionErr := action(context.WithValue(ctx, callIDKey{}, callID))
	returned = true
	return res, r.recordOutcome(ctx, callID, c, res, actionErr)
}

type callIDKey struct{}

// CallIDFrom returns the call id of the intent Run wrote, from the context
// Run hands its action; ok is false anywhere else.
func CallIDFrom(ctx context.Context) (id uuid.UUID, ok bool) {
	id, ok = ctx.Value(callIDKey{}).(uuid.UUID)
	return id, ok
}

// Refuse records a call that was never run, as an intent and outcome pair in
// one transaction. outcome must be a refusal (denied_policy, denied_identity,
// read_only, denied_approval, or expired). reason is stored as is, so it must
// be fixed text, never request or ticket data.
func (r *Recorder) Refuse(ctx context.Context, c Call, outcome Outcome, reason string) error {
	if !outcome.refusal() {
		return fmt.Errorf("%w: %q is not a refusal outcome", ErrInvalidCall, outcome)
	}
	intent, err := r.intentParams(c)
	if err != nil {
		return err
	}
	return r.inTx(ctx, func(q Queries) error {
		return r.writePair(ctx, q, intent, outcome, reason, uuid.Nil)
	})
}

// writePair writes an intent and its outcome on q, inside a transaction.
func (r *Recorder) writePair(ctx context.Context, q Queries, intent queries.InsertAuditIntentParams, outcome Outcome, reason string, approvalID uuid.UUID) error {
	callID, err := r.insertIntent(ctx, q, intent)
	if err != nil {
		return err
	}
	return r.insertOutcome(ctx, q, queries.InsertAuditOutcomeParams{
		CallID:     callID,
		Outcome:    string(outcome),
		Error:      text(capRunes(reason, maxErrorRunes)),
		ApprovalID: nullUUID(approvalID),
	})
}

func (r *Recorder) recordOutcome(ctx context.Context, callID uuid.UUID, c Call, res Result, actionErr error) error {
	params := r.outcomeParams(callID, c, res, actionErr)
	ctx = context.WithoutCancel(ctx)

	var err error
	delay := firstBackoff
	for attempt := 0; attempt <= r.cfg.OutcomeRetries; attempt++ {
		if attempt > 0 {
			r.sleep(delay)
			delay = min(delay*2, maxBackoff)
		}
		if err = r.insertOutcome(ctx, r.store, params); err == nil {
			return actionErr
		}
	}
	// The call id only: a database error can quote the row's values.
	r.logger.ErrorContext(ctx, "audit_outcome_incomplete",
		"call_id", callID.String(), "attempts", r.cfg.OutcomeRetries+1)
	return errors.Join(actionErr, fmt.Errorf("%w (call %s): %w", ErrAuditIncomplete, callID, err))
}

func (r *Recorder) insertIntent(ctx context.Context, q Queries, params queries.InsertAuditIntentParams) (uuid.UUID, error) {
	ctx, cancel := context.WithTimeout(ctx, r.cfg.WriteTimeout)
	defer cancel()
	row, err := q.InsertAuditIntent(ctx, params)
	if err != nil {
		return uuid.Nil, fmt.Errorf("%w: insert intent: %w", ErrAuditUnavailable, err)
	}
	return row.CallID, nil
}

func (r *Recorder) insertOutcome(ctx context.Context, q Queries, params queries.InsertAuditOutcomeParams) error {
	ctx, cancel := context.WithTimeout(ctx, r.cfg.WriteTimeout)
	defer cancel()
	if _, err := q.InsertAuditOutcome(ctx, params); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("insert outcome: no intent row for call %s", params.CallID)
		}
		return fmt.Errorf("insert outcome: %w", err)
	}
	return nil
}

// inTx runs fn in one transaction bounded by the write timeout. Any failure
// leaves nothing written and is reported as ErrAuditUnavailable.
func (r *Recorder) inTx(ctx context.Context, fn func(Queries) error) error {
	ctx, cancel := context.WithTimeout(ctx, r.cfg.WriteTimeout)
	defer cancel()
	err := r.store.InTx(ctx, fn)
	if err != nil && !errors.Is(err, ErrAuditUnavailable) && !errors.Is(err, ErrApprovalNotFound) {
		return fmt.Errorf("%w: %w", ErrAuditUnavailable, err)
	}
	return err
}

func (r *Recorder) intentParams(c Call) (queries.InsertAuditIntentParams, error) {
	switch {
	case c.Tool == "":
		return queries.InsertAuditIntentParams{}, fmt.Errorf("%w: no tool name", ErrInvalidCall)
	case !c.Tier.valid():
		return queries.InsertAuditIntentParams{}, fmt.Errorf("%w: unknown risk tier %q", ErrInvalidCall, c.Tier)
	case c.RequesterSlackID == "":
		return queries.InsertAuditIntentParams{}, fmt.Errorf("%w: no requester", ErrInvalidCall)
	}
	args, err := encodeKept(c.Args, c.Store, r.cfg.MaxPayloadBytes)
	if err != nil {
		return queries.InsertAuditIntentParams{}, fmt.Errorf("%w: %w", ErrInvalidCall, err)
	}
	return queries.InsertAuditIntentParams{
		ToolName:         c.Tool,
		RiskTier:         string(c.Tier),
		RequesterSlackID: c.RequesterSlackID,
		RequesterAccount: text(c.RequesterAccount),
		SlackChannelID:   text(c.ChannelID),
		SlackThreadTs:    text(c.ThreadTS),
		ZammadTicketID:   optInt8(c.TicketID),
		Target:           text(c.Target),
		Arguments:        args,
		ApprovalID:       nullUUID(c.ApprovalID),
		ApproverSlackID:  text(c.ApproverSlackID),
		TraceID:          text(c.TraceID),
	}, nil
}

func (r *Recorder) outcomeParams(callID uuid.UUID, c Call, res Result, actionErr error) queries.InsertAuditOutcomeParams {
	outcome, reason := settle(res, actionErr)
	params := queries.InsertAuditOutcomeParams{
		CallID:  callID,
		Outcome: string(outcome),
		Error:   text(capRunes(reason, maxErrorRunes)),
	}
	if c.Tier != TierRead && actionErr == nil {
		result, err := encodeKept(res.Data, c.StoreResult, r.cfg.MaxPayloadBytes)
		if err != nil {
			result = unencodableMarker()
		}
		params.Result = result
	}
	return params
}

// settle decides the outcome and its stored reason from what the action returned.
func settle(res Result, actionErr error) (Outcome, string) {
	if actionErr != nil {
		return OutcomeError, storedReason(actionErr)
	}
	switch {
	case res.Outcome == "":
		return OutcomeOK, ""
	case res.Outcome.valid():
		return res.Outcome, res.Reason
	default:
		return OutcomeError, fmt.Sprintf("action reported unknown outcome %q", res.Outcome)
	}
}

func text(s string) pgtype.Text { return pgtype.Text{String: s, Valid: s != ""} }

func optInt8(n int64) pgtype.Int8 { return pgtype.Int8{Int64: n, Valid: n != 0} }

func nullUUID(id uuid.UUID) uuid.NullUUID { return uuid.NullUUID{UUID: id, Valid: id != uuid.Nil} }
