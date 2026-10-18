package audit

import (
	"context"
	"fmt"
	"time"

	"github.com/toyinogun/AIITsupport/internal/db/queries"
)

// CountRecentOK counts requesterSlackID's ok outcomes for tool after since:
// the tool server's rate limit (spec 0007). It reads under the write timeout.
func (r *Recorder) CountRecentOK(ctx context.Context, tool, requesterSlackID string, since time.Time) (int64, error) {
	ctx, cancel := context.WithTimeout(ctx, r.cfg.WriteTimeout)
	defer cancel()
	n, err := r.store.CountRecentOKOutcomes(ctx, queries.CountRecentOKOutcomesParams{
		ToolName: tool, RequesterSlackID: requesterSlackID, Since: since,
	})
	if err != nil {
		return 0, fmt.Errorf("%w: count recent outcomes: %w", ErrAuditUnavailable, err)
	}
	return n, nil
}

// ThreadHasOK reports whether tool already has an ok outcome in a Slack
// thread: one ticket per conversation (spec 0007).
func (r *Recorder) ThreadHasOK(ctx context.Context, tool, channelID, threadTS string) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, r.cfg.WriteTimeout)
	defer cancel()
	ok, err := r.store.ThreadHasOKOutcome(ctx, queries.ThreadHasOKOutcomeParams{
		ToolName: tool, SlackChannelID: text(channelID), SlackThreadTs: text(threadTS),
	})
	if err != nil {
		return false, fmt.Errorf("%w: read the thread's outcomes: %w", ErrAuditUnavailable, err)
	}
	return ok, nil
}
