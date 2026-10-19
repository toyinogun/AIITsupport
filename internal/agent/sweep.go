package agent

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"

	"github.com/toyinogun/AIITsupport/internal/mcpclient"
)

// Sweep handles open conversations a turn stopped in the middle of, for
// example a crash or a deploy (AC-14). cmd/agent runs it before the socket
// starts, and Run runs it once more after the stale window. It reruns the
// identity step, leaves IT a ticket when it can, tells the user, and sets
// the conversation escalated. No tool call from history is replayed.
func (a *Agent) Sweep(ctx context.Context) {
	staleBefore := a.deps.Now().Add(-a.cfg.staleWindow())
	convs, err := a.deps.Store.ListInterrupted(ctx, staleBefore)
	if err != nil {
		a.deps.Logger.Warn("agent_sweep_failed")
		return
	}
	for _, c := range convs {
		if ctx.Err() != nil {
			return
		}
		if a.running(c.ChannelID, c.ThreadTS) {
			continue
		}
		a.deps.Logger.Info("agent_interrupted", "conversation_id", c.ID)
		a.recoverInterrupted(ctx, c)
	}
}

// recoverInterrupted claims the conversation first (escalated), so a turn
// that starts in the thread meanwhile finds it closed and the two never act
// on it together. A conversation it cannot claim is left alone.
func (a *Agent) recoverInterrupted(ctx context.Context, c Conversation) {
	sctx, cancel := a.detached(ctx, a.cfg.ToolTimeout)
	_, err := a.deps.Store.SetStatus(sctx, c.ID, StatusChange{Status: StatusEscalated})
	cancel()
	if err != nil {
		if !errors.Is(err, ErrNotOpen) {
			a.deps.Logger.Warn("agent_status_write_failed", "conversation_id", c.ID)
		}
		return
	}
	in := Inbound{EventID: "", ChannelID: c.ChannelID, ThreadTS: c.ThreadTS}
	tk := ticketOutcome{}
	if r, ok := a.reidentify(ctx, c); ok {
		tk = a.ensureTicket(ctx, r, a.lastUserText(ctx, c.ID), escalationNote(ReasonRestart, false))
	}
	a.logEscalated(ctx, "", ReasonRestart, tk.exists)
	// The restart text says IT has it, so it needs a ticket.
	if tk.exists {
		a.post(ctx, in, TextRestart, "restart")
	} else {
		a.post(ctx, in, TextEscalatedNoTicket, "escalated_no_ticket")
	}
}

// reidentify reruns the Slack gate and user_lookup for the stored requester.
// A match must be the account the conversation already holds, if any.
func (a *Agent) reidentify(ctx context.Context, c Conversation) (mcpclient.Requester, bool) {
	uctx, cancel := a.detached(ctx, a.cfg.PostTimeout)
	user, err := a.deps.Slack.UserInfo(uctx, c.RequesterSlackID)
	cancel()
	if err != nil || !passesGate(user, a.cfg.TeamID) {
		return mcpclient.Requester{}, false
	}
	r := mcpclient.Requester{
		SlackID: c.RequesterSlackID, Email: strings.TrimSpace(user.Email),
		ChannelID: c.ChannelID, ThreadTS: c.ThreadTS, TicketID: c.TicketID,
	}
	res, answered := a.callTool(ctx, r, toolUserLookup, nil)
	if !answered || res.IsError {
		return mcpclient.Requester{}, false
	}
	username, _ := res.Data["username"].(string)
	if username == "" || (c.DirectoryAccount != "" && c.DirectoryAccount != username) {
		return mcpclient.Requester{}, false
	}
	return r, true
}

// lastUserText is the newest stored user message that holds a text block.
func (a *Agent) lastUserText(ctx context.Context, id int64) string {
	sctx, cancel := a.detached(ctx, a.cfg.ToolTimeout)
	defer cancel()
	msgs, err := a.deps.Store.ListMessages(sctx, id)
	if err != nil {
		return ""
	}
	for _, m := range slices.Backward(msgs) {
		if m.Role != RoleUser || m.Content == nil {
			continue
		}
		var blocks []anthropic.ContentBlockParamUnion
		if json.Unmarshal(m.Content, &blocks) != nil {
			continue
		}
		for _, b := range blocks {
			if b.OfText != nil {
				return b.OfText.Text
			}
		}
	}
	return ""
}
