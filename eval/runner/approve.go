package runner

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/toyinogun/AIITsupport/eval/ticketset"
	"github.com/toyinogun/AIITsupport/internal/agent"
)

const approvalPoll = 50 * time.Millisecond

// approvalResult is what the approver saw.
type approvalResult struct {
	// WaitMS runs from the start of the wait to the card settling.
	WaitMS int64
	// Missing is true when the card never came or never posted.
	Missing bool
}

// approve waits for the thread's approval card and clicks it as the approver
// the ticket plays (spec 0018, AC-6): the fixed IT approver for an IT card,
// the stored owner for an owner card. It returns when the card settles, when
// ctx ends (the thread went idle or timed out), or when a seen card never
// posts within the approval wait.
func (r *Runner) approve(ctx context.Context, owner *pgxpool.Pool, a *agent.Agent, t ticketset.Ticket, thread string) approvalResult {
	start := time.Now()
	approve := t.Approval == "approve"
	var seenAt time.Time
	clicked := false
	tick := time.NewTicker(approvalPoll)
	defer tick.Stop()
	for {
		card, found := readCard(ctx, owner, thread)
		switch {
		case found && card.Settled:
			return approvalResult{WaitMS: time.Since(start).Milliseconds()}
		case found && card.Posted && !clicked:
			a.Click(agent.Click{
				EventID: "Click" + t.ID, ApprovalID: card.ID, UserID: card.approverOr(itApproverID),
				Approve: approve, ChannelID: card.Channel,
			})
			clicked = true
		case found && !card.Posted:
			if seenAt.IsZero() {
				seenAt = time.Now()
			}
			if time.Since(seenAt) > r.Config.ApprovalWait {
				return approvalResult{Missing: true}
			}
		}
		select {
		case <-ctx.Done():
			// One last read: the card may have settled as the thread went idle.
			final, ok := readCard(context.WithoutCancel(ctx), owner, thread)
			if ok && final.Settled {
				return approvalResult{WaitMS: time.Since(start).Milliseconds()}
			}
			return approvalResult{Missing: !ok || !final.Posted}
		case <-tick.C:
		}
	}
}

type cardRow struct {
	ID       uuid.UUID
	Channel  string
	Approver string
	Posted   bool
	Settled  bool
}

func (c cardRow) approverOr(fallback string) string {
	if c.Approver != "" {
		return c.Approver
	}
	return fallback
}

// readCard finds the thread's approval card from the attempt database.
func readCard(ctx context.Context, owner *pgxpool.Pool, thread string) (cardRow, bool) {
	var c cardRow
	var channel, approver, ts *string
	var settled *time.Time
	err := owner.QueryRow(ctx,
		`SELECT c.approval_id, c.slack_channel_id, c.approver_slack_id, c.card_ts, c.settled_at
		 FROM approval_cards c JOIN conversations v ON v.id = c.conversation_id
		 WHERE v.slack_channel_id = $1 AND v.slack_thread_ts = $2
		 ORDER BY c.created_at LIMIT 1`, evalChannel, thread).Scan(&c.ID, &channel, &approver, &ts, &settled)
	if err != nil {
		// No row yet, or a read error: either way keep waiting.
		return cardRow{}, false
	}
	c.Channel, c.Approver = deref(channel), deref(approver)
	c.Posted, c.Settled = ts != nil, settled != nil
	return c, true
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
