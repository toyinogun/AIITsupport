package agent

import (
	"context"
	"log/slog"
	"time"
)

// RetentionEvery is how often the retention pass runs, after one at start.
const RetentionEvery = time.Hour

// retentionStepTimeout bounds one retention query, so a hung connection
// fails the step (cause deadline) instead of silently stopping every later
// pass.
const retentionStepTimeout = 5 * time.Minute

// Retention kinds, the kind token on retention_purged lines.
const (
	retentionIdle     = "idle_conversations"
	retentionMessages = "messages"
)

// Retention clears stored chat text TICKET_RETENTION_DAYS after its
// conversation ends (spec 0010, AC-8). It runs whether or not Slack is
// configured, as the agent's noord_agent role, with no new grants.
type Retention struct {
	store       RetentionStore
	window      time.Duration
	every       time.Duration
	stepTimeout time.Duration
	now         func() time.Time
	logger      *slog.Logger
}

// NewRetention returns a retention loop over store with a window of days.
// every is the pass interval (RetentionEvery in production); now is the
// clock the cutoff is read from.
func NewRetention(store RetentionStore, days int, every time.Duration, now func() time.Time, logger *slog.Logger) *Retention {
	return &Retention{
		store: store, window: time.Duration(days) * 24 * time.Hour, every: every,
		stepTimeout: retentionStepTimeout, now: now, logger: logger,
	}
}

// Run passes at start and then every interval until ctx ends. A failed step
// is logged and tried again on the next tick; Run never fails, and returns
// nil when ctx ends.
func (r *Retention) Run(ctx context.Context) error {
	ticker := time.NewTicker(r.every)
	defer ticker.Stop()
	for {
		r.pass(ctx)
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

// pass closes idle conversations first, so one that just went idle is
// purged in the same pass (its closed_at is its last activity).
func (r *Retention) pass(ctx context.Context) {
	cutoff := r.now().Add(-r.window)
	r.step(ctx, retentionIdle, func(ctx context.Context) (int64, error) { return r.store.CloseIdleConversations(ctx, cutoff) })
	r.step(ctx, retentionMessages, func(ctx context.Context) (int64, error) { return r.store.PurgeMessageContent(ctx, cutoff) })
}

func (r *Retention) step(ctx context.Context, kind string, run func(context.Context) (int64, error)) {
	if ctx.Err() != nil {
		return
	}
	sctx, cancel := context.WithTimeout(ctx, r.stepTimeout)
	defer cancel()
	n, err := run(sctx)
	switch {
	case err != nil && ctx.Err() != nil:
		// The agent is stopping; that is not a failure.
	case err != nil:
		r.logger.Warn("retention_purge_failed", "kind", kind, "cause", cause(err))
	default:
		r.logger.Info("retention_purged", "kind", kind, "rows", n)
	}
}
