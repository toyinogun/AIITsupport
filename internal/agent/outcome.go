package agent

import (
	"context"
	"regexp"
	"time"
)

// Conversation outcomes (spec 0016, AC-6). The database CHECK pairs each with
// a status: resolved with resolved or abandoned, escalated with escalated or
// refused.
const (
	ConvResolved  = "resolved"
	ConvEscalated = "escalated"
	ConvRefused   = "refused"
	ConvAbandoned = "abandoned"
)

// resolvedChange ends a conversation as resolved.
func resolvedChange() StatusChange {
	return StatusChange{Status: StatusResolved, Outcome: ConvResolved}
}

// escalatedChange ends a conversation as escalated, or as refused when the
// escalate reason is privileged_request, wherever the call comes from (the
// model, the backstop, the failure path, the sweep).
func escalatedChange(reason string) StatusChange {
	if reason == escalatePrivileged {
		return StatusChange{Status: StatusEscalated, Outcome: ConvRefused}
	}
	return StatusChange{Status: StatusEscalated, Outcome: ConvEscalated}
}

// Failure stages of agent_failures (spec 0016, AC-7).
const (
	stageLLM   = "llm"
	stageTool  = "tool"
	stageSlack = "slack"
	stageSweep = "sweep"
)

// failureWriteTimeout bounds the insert; a slow database never delays a turn.
const failureWriteTimeout = 2 * time.Second

var causeToken = regexp.MustCompile(`^[a-z0-9_]{1,40}$`)

// recordFailure inserts one agent_failures row on a detached context. It
// never fails or delays the caller: a failed insert is logged as
// agent_failure_write_failed with its cause only. conversationID 0 means no
// conversation. causeTok is a fixed token from cause(); anything else is
// stored as other, since the column only accepts tokens.
func (a *Agent) recordFailure(parent context.Context, conversationID int64, stage, causeTok string) {
	if !causeToken.MatchString(causeTok) {
		causeTok = "other"
	}
	ctx, cancel := a.detached(parent, failureWriteTimeout)
	defer cancel()
	if err := a.deps.Store.RecordFailure(ctx, conversationID, stage, causeTok); err != nil {
		a.deps.Logger.Warn("agent_failure_write_failed", "cause", cause(err))
	}
}
