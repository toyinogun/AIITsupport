package agent

import (
	"context"
	"fmt"
	"time"
)

const (
	waitIdlePoll  = 25 * time.Millisecond
	waitIdleCards = 100
)

// WaitIdle blocks until the thread is settled: no worker runs for it and its
// conversation has no approval card whose outcome is still owed. A thread
// with no conversation, or a conversation left open with nothing running (the
// model only asked a question), is settled too. It returns the context error
// if ctx ends first. Only the eval harness and tests call it (spec 0018,
// AC-3); the agent itself never does.
func (a *Agent) WaitIdle(ctx context.Context, channel, threadTS string) error {
	t := time.NewTicker(waitIdlePoll)
	defer t.Stop()
	for {
		settled, err := a.threadSettled(ctx, channel, threadTS)
		if err != nil {
			return err
		}
		if settled {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
	}
}

// threadSettled is one WaitIdle check. The worker is checked before and after
// the card reads, so a worker that starts in between does not settle the thread.
func (a *Agent) threadSettled(ctx context.Context, channel, threadTS string) (bool, error) {
	if a.running(channel, threadTS) {
		return false, nil
	}
	cards, err := a.deps.Store.ListUnsettledApprovalCards(ctx, waitIdleCards)
	if err != nil {
		return false, fmt.Errorf("list unsettled approval cards: %w", err)
	}
	for _, c := range cards {
		conv, err := a.deps.Store.GetConversation(ctx, c.ConversationID)
		if err != nil {
			return false, fmt.Errorf("read the conversation of an approval card: %w", err)
		}
		if conv.ChannelID == channel && conv.ThreadTS == threadTS {
			return false, nil
		}
	}
	return !a.running(channel, threadTS), nil
}
