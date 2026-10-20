package audit

import (
	"context"
	"fmt"
	"time"

	"github.com/toyinogun/AIITsupport/internal/db/queries"
)

// CountRecent counts requesterSlackID's outcome rows of one kind for tool
// after since: the tool server's rate limits (spec 0007) and the blocked
// attempt count (spec 0009). It reads under the write timeout.
func (r *Recorder) CountRecent(ctx context.Context, tool, requesterSlackID string, outcome Outcome, since time.Time) (int64, error) {
	ctx, cancel := context.WithTimeout(ctx, r.cfg.WriteTimeout)
	defer cancel()
	n, err := r.store.CountRecentOutcomes(ctx, queries.CountRecentOutcomesParams{
		Outcome: text(string(outcome)), ToolName: tool, RequesterSlackID: requesterSlackID, Since: since,
	})
	if err != nil {
		return 0, fmt.Errorf("%w: count recent outcomes: %w", ErrAuditUnavailable, err)
	}
	return n, nil
}

// CountRecentOK counts requesterSlackID's ok outcomes for tool after since:
// the tool server's rate limit (spec 0007).
func (r *Recorder) CountRecentOK(ctx context.Context, tool, requesterSlackID string, since time.Time) (int64, error) {
	return r.CountRecent(ctx, tool, requesterSlackID, OutcomeOK, since)
}

// ThreadHas reports whether tool has an outcome of one kind in a Slack
// thread (spec 0009: the blocked attempt, the once per thread escalation,
// and the close guard).
func (r *Recorder) ThreadHas(ctx context.Context, tool string, outcome Outcome, channelID, threadTS string) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, r.cfg.WriteTimeout)
	defer cancel()
	ok, err := r.store.ThreadHasOutcome(ctx, queries.ThreadHasOutcomeParams{
		ToolName: tool, Outcome: text(string(outcome)), SlackChannelID: text(channelID), SlackThreadTs: text(threadTS),
	})
	if err != nil {
		return false, fmt.Errorf("%w: read the thread's outcomes: %w", ErrAuditUnavailable, err)
	}
	return ok, nil
}

// ThreadHasOK reports whether tool already has an ok outcome in a Slack
// thread: one ticket per conversation (spec 0007).
func (r *Recorder) ThreadHasOK(ctx context.Context, tool, channelID, threadTS string) (bool, error) {
	return r.ThreadHas(ctx, tool, OutcomeOK, channelID, threadTS)
}

// ThreadTicketID is the ticket id the newest ok ticket_create in a Slack
// thread returned, or 0 when there is none (spec 0009).
func (r *Recorder) ThreadTicketID(ctx context.Context, channelID, threadTS string) (int64, error) {
	ctx, cancel := context.WithTimeout(ctx, r.cfg.WriteTimeout)
	defer cancel()
	id, err := r.store.ThreadTicketID(ctx, queries.ThreadTicketIDParams{
		SlackChannelID: text(channelID), SlackThreadTs: text(threadTS),
	})
	if err != nil {
		return 0, fmt.Errorf("%w: read the thread's ticket: %w", ErrAuditUnavailable, err)
	}
	return id, nil
}

// ThreadHasOpenApproval reports whether the thread holds a pending or
// approved approval of tool: the ticket_update close guard (spec 0012,
// AC-24). It reads under the write timeout.
func (r *Recorder) ThreadHasOpenApproval(ctx context.Context, tool, channelID, threadTS string) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, r.cfg.WriteTimeout)
	defer cancel()
	open, err := r.store.ThreadHasOpenApproval(ctx, queries.ThreadHasOpenApprovalParams{
		SlackChannelID: channelID, SlackThreadTs: threadTS, ToolName: tool,
	})
	if err != nil {
		return false, fmt.Errorf("%w: read open approvals: %w", ErrAuditUnavailable, err)
	}
	return open, nil
}
