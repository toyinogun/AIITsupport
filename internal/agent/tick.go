package agent

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/google/uuid"

	"github.com/toyinogun/AIITsupport/internal/mcpclient"
)

// The approval tick (spec 0012, AC-14): at start and every
// APPROVAL_POLL_INTERVAL, it expires and abandons on the tool server, adopts
// approvals whose pending result never reached the agent, and reads every
// unsettled card: an unposted pending card is posted (capped and spaced), an
// ended approval is settled on its thread's worker. It never fails the
// binary; a failure is logged and tried again on the next tick.

const (
	// tickLimit is how many unsettled cards one tick reads.
	tickLimit = 100
	// adoptSlack widens the adopt window past the longer approval timeout.
	adoptSlack = time.Hour
)

// adoptedTools are the tools whose pending results the tick adopts (spec
// 0012 AC-14, spec 0013 AC-17).
var adoptedTools = []string{toolMFAClear, toolGroupAccessRequest}

// goTick starts one tick unless the agent is stopping or a tick still runs.
func (a *Agent) goTick() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.stopping || a.ticking {
		return
	}
	a.ticking = true
	a.wg.Go(func() {
		defer func() {
			a.mu.Lock()
			a.ticking = false
			a.mu.Unlock()
		}()
		a.tick(a.workCtx)
	})
}

// tick runs the three steps in order; each one runs even when an earlier
// one failed, so a down expire route never stops a settle.
func (a *Agent) tick(parent context.Context) {
	ctx, span := tracer.Start(parent, "approval.tick")
	defer span.End()
	a.tickStep(ctx, func(ctx context.Context) error {
		_, err := a.deps.Approvals.Expire(ctx)
		return err
	})
	a.tickStep(ctx, a.adopt)
	cards, err := a.unsettled(ctx)
	if err != nil {
		a.tickFailed(ctx, err)
		return
	}
	for _, card := range cards {
		if ctx.Err() != nil {
			return
		}
		a.tickCard(ctx, card)
	}
}

// tickStep runs one call under the tool timeout and logs its failure.
func (a *Agent) tickStep(parent context.Context, run func(context.Context) error) {
	ctx, cancel := a.detached(parent, a.cfg.ToolTimeout)
	defer cancel()
	if err := run(ctx); err != nil {
		a.tickFailed(parent, err)
	}
}

func (a *Agent) tickFailed(ctx context.Context, err error) {
	if ctx.Err() != nil {
		return // stopping: not a failure
	}
	a.deps.Logger.Warn("approval_tick_failed", "cause", cause(err))
}

// adopt gives every recent approval with no card row a row in the
// conversation that owns its thread (AC-14): a pending result the agent never
// stored still gets its card and its outcome. Only tools the agent builds a
// card for are adopted, and one read skips those that have a card already.
// The window is the longer of the two approval timeouts plus an hour (spec
// 0013, AC-17); an adopted group owner card has no channel until it posts.
func (a *Agent) adopt(ctx context.Context) error {
	since := a.deps.Now().Add(-(max(a.cfg.ApprovalTimeout, a.cfg.AccessApprovalTimeout) + adoptSlack))
	views, err := a.deps.Approvals.Since(ctx, since)
	if err != nil {
		return err
	}
	views = slices.DeleteFunc(views, func(v mcpclient.ApprovalView) bool { return !slices.Contains(adoptedTools, v.ToolName) })
	ids := make([]uuid.UUID, 0, len(views))
	for _, v := range views {
		ids = append(ids, v.ID)
	}
	have, err := a.deps.Store.HaveApprovalCards(ctx, ids)
	if err != nil {
		return err
	}
	var errs []error
	for _, v := range views {
		if have[v.ID] {
			continue
		}
		card := ApprovalCard{ApprovalID: v.ID, ToolName: v.ToolName, ExpiresAt: v.ExpiresAt}
		if v.ToolName == toolMFAClear {
			card.ChannelID = a.cfg.ITChannel
		}
		created, err := a.deps.Store.AdoptApprovalCard(ctx, card, v.SlackChannelID, v.SlackThreadTS)
		switch {
		case err != nil:
			errs = append(errs, err)
		case created:
			a.deps.Logger.Info("approval_card_adopted", "approval_id", v.ID.String())
		}
	}
	return errors.Join(errs...)
}

func (a *Agent) unsettled(parent context.Context) ([]ApprovalCard, error) {
	ctx, cancel := a.detached(parent, a.cfg.ToolTimeout)
	defer cancel()
	return a.deps.Store.ListUnsettledApprovalCards(ctx, tickLimit)
}

// tickCard reads one unsettled card's approval and acts on it: post a
// pending card that never posted, settle an ended one, else wait.
func (a *Agent) tickCard(parent context.Context, card ApprovalCard) {
	v, err := a.tickView(parent, card)
	if err != nil {
		a.tickFailed(parent, err)
		return
	}
	switch {
	case v.Status == mcpclient.StatusPending && card.CardTS == "" && card.ToolName == toolGroupAccessRequest:
		// Lookup, then post or cancel: never to the IT channel (spec 0013, AC-17).
		a.postOwnerCard(parent, card)
	case v.Status == mcpclient.StatusPending && card.CardTS == "" && card.PostAttempts < maxCardPosts:
		// The claim inside postCard spaces and caps the posts (AC-14).
		a.postCard(parent, card.ApprovalID, requestCard(factsFromView(v), "", card.ApprovalID))
	case isEnded(v.Status):
		a.settleOnThread(card, v)
	}
}

// tickView reads a card's approval for the tick. A group owner approval is
// peeked, without the live directory reads (spec 0013, AC-17): the post
// reads the full view itself, and an ended one is read in full once, for
// the owner's name in the settle.
func (a *Agent) tickView(parent context.Context, card ApprovalCard) (mcpclient.ApprovalView, error) {
	ctx, cancel := a.detached(parent, a.cfg.ToolTimeout)
	defer cancel()
	if card.ToolName != toolGroupAccessRequest {
		return a.deps.Approvals.Get(ctx, card.ApprovalID)
	}
	v, err := a.deps.Approvals.Peek(ctx, card.ApprovalID)
	if err != nil || !isEnded(v.Status) {
		return v, err
	}
	return a.deps.Approvals.Get(ctx, card.ApprovalID)
}

func isEnded(status string) bool {
	_, ok := settleTexts[status]
	return ok
}
