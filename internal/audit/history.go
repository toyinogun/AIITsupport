package audit

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

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

// CountRecentReason counts requesterSlackID's outcome rows of one kind with
// one stored reason for tool after since: protected access requests in the
// privileged attempt window (spec 0013, AC-5). It reads under the write timeout.
func (r *Recorder) CountRecentReason(ctx context.Context, tool, requesterSlackID string, outcome Outcome, reason string, since time.Time) (int64, error) {
	ctx, cancel := context.WithTimeout(ctx, r.cfg.WriteTimeout)
	defer cancel()
	n, err := r.store.CountRecentReasons(ctx, queries.CountRecentReasonsParams{
		Outcome: text(string(outcome)), Error: reason, ToolName: tool, RequesterSlackID: requesterSlackID, Since: since,
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

// ThreadOpenApprovalTool is the tool of the thread's newest pending or
// approved medium approval, or "" when there is none: the ticket_update
// close guard (spec 0012 AC-24, every medium tool since spec 0013 AC-18). It
// reads under the write timeout.
func (r *Recorder) ThreadOpenApprovalTool(ctx context.Context, channelID, threadTS string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, r.cfg.WriteTimeout)
	defer cancel()
	tool, err := r.store.ThreadOpenApprovalTool(ctx, queries.ThreadOpenApprovalToolParams{
		SlackChannelID: channelID, SlackThreadTs: threadTS,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("%w: read open approvals: %w", ErrAuditUnavailable, err)
	}
	return tool, nil
}
