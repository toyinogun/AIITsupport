package runner

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/toyinogun/AIITsupport/eval/score"
	"github.com/toyinogun/AIITsupport/internal/agent"
	"github.com/toyinogun/AIITsupport/internal/tools"
)

// usage is the model spend of one attempt.
type usage struct {
	Input, Output int
	CostMicroUSD  int64
	Rows          int
}

// readEvidence reads one attempt's evidence as the attempt database's owner
// (spec 0018, AC-7). Ticket state and replies come from the caller.
func readEvidence(ctx context.Context, owner *pgxpool.Pool, thread string) (score.Evidence, int64, usage, error) {
	var ev score.Evidence
	var zammadID *int64
	err := owner.QueryRow(ctx,
		`SELECT COALESCE(outcome, ''), zammad_ticket_id FROM conversations
		 WHERE slack_channel_id = $1 AND slack_thread_ts = $2`, evalChannel, thread).Scan(&ev.ConversationOutcome, &zammadID)
	switch {
	case err == nil:
		ev.HasConversation = true
	case !errors.Is(err, pgx.ErrNoRows):
		return ev, 0, usage{}, fmt.Errorf("read the conversation: %w", err)
	}
	var ticket int64
	if zammadID != nil {
		ticket = *zammadID
	}
	if ev.Calls, ev.KBReview, err = readCalls(ctx, owner, thread); err != nil {
		return ev, 0, usage{}, err
	}
	u, err := readUsage(ctx, owner, thread)
	return ev, ticket, u, err
}

func readCalls(ctx context.Context, owner *pgxpool.Pool, thread string) ([]score.Call, bool, error) {
	rows, err := owner.Query(ctx,
		`SELECT i.tool_name, COALESCE(i.target, ''), COALESCE(o.outcome, ''), COALESCE(o.error, ''),
		        COALESCE(i.arguments ->> 'kb_review', '')
		 FROM audit_log i
		 LEFT JOIN audit_log o ON o.call_id = i.call_id AND o.kind = 'outcome'
		 WHERE i.kind = 'intent' AND i.slack_channel_id = $1 AND i.slack_thread_ts = $2 AND i.tool_name <> 'user_lookup'
		 ORDER BY i.id`, evalChannel, thread)
	if err != nil {
		return nil, false, fmt.Errorf("read the audit log: %w", err)
	}
	defer rows.Close()
	var calls []score.Call
	kbReview := false
	for rows.Next() {
		var c score.Call
		var reason, kb string
		if err := rows.Scan(&c.Tool, &c.Target, &c.Outcome, &reason, &kb); err != nil {
			return nil, false, fmt.Errorf("read an audit row: %w", err)
		}
		if code, ok := tools.CodeForReason(reason); ok {
			c.Code = string(code)
		}
		if c.Tool == "ticket_update" && kb == "true" {
			kbReview = true
		}
		calls = append(calls, c)
	}
	return calls, kbReview, rows.Err()
}

func readUsage(ctx context.Context, owner *pgxpool.Pool, thread string) (usage, error) {
	var u usage
	err := owner.QueryRow(ctx,
		`SELECT COALESCE(SUM(u.input_tokens), 0), COALESCE(SUM(u.output_tokens), 0),
		        COALESCE(SUM(u.cost_microusd), 0), COUNT(u.id)
		 FROM llm_usage u JOIN conversations c ON c.id = u.conversation_id
		 WHERE c.slack_channel_id = $1 AND c.slack_thread_ts = $2`, evalChannel, thread).
		Scan(&u.Input, &u.Output, &u.CostMicroUSD, &u.Rows)
	if err != nil {
		return u, fmt.Errorf("read the model usage: %w", err)
	}
	return u, nil
}

// gateTextSeen is true when a fixed gate text was posted.
func gateTextSeen(posts []string) bool {
	for _, p := range posts {
		if strings.Contains(p, agent.TextNotStaff) || strings.Contains(p, agent.TextNotMatched) {
			return true
		}
	}
	return false
}

// ticketStateName maps a Zammad state name to the ticket file's words.
func ticketStateName(zammadName string) string {
	switch n := strings.ToLower(zammadName); n {
	case "new", "open":
		return "open"
	default:
		return n
	}
}
