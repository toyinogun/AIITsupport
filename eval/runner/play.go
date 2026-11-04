package runner

import (
	"context"
	"errors"
	"fmt"

	"github.com/toyinogun/AIITsupport/eval/score"
	"github.com/toyinogun/AIITsupport/eval/ticketset"
	"github.com/toyinogun/AIITsupport/internal/seed/evalreset"
)

// Stop reasons stored in the run summary (spec 0018, AC-7 to AC-9).
const (
	StopCostCap  = "cost_cap"
	StopInfra    = "infra"
	StopEnvDirty = "env_dirty"
	StopError    = "error"
)

// infraLimit is how many attempts in a row may fail at the model before the
// run stops (AC-8).
const infraLimit = 3

// Player plays one ticket attempt. *Runner is one.
type Player interface {
	Run(ctx context.Context, t ticketset.Ticket) (Observation, error)
}

// PlayConfig bounds a run.
type PlayConfig struct {
	// Repeat is how many times each ticket runs, 1 to 5 (AC-18).
	Repeat int
	// MaxCostMicroUSD stops the run once the attempts so far cost this much (AC-9).
	MaxCostMicroUSD int64
}

// PlayResult is what a run finished before it ended.
type PlayResult struct {
	Attempts []score.Attempt
	// StopReason is empty for a run that finished every attempt.
	StopReason string
}

// Play runs every ticket Repeat times, attempt by attempt, calling onAttempt
// after each finished attempt. A cost cap or infra stop ends the run with a
// stop reason and no error; a dirty environment or any other failure returns
// an error as well as the reason and the attempts so far.
func Play(ctx context.Context, p Player, tickets []ticketset.Ticket, cfg PlayConfig,
	onAttempt func(t ticketset.Ticket, attempt int, obs Observation) error,
) (PlayResult, error) {
	var res PlayResult
	var spent int64
	llmFailures := 0
	for attempt := 1; attempt <= cfg.Repeat; attempt++ {
		for _, t := range tickets {
			obs, err := p.Run(ctx, t)
			if err != nil {
				res.StopReason = StopError
				if errors.Is(err, evalreset.ErrDirty) {
					res.StopReason = StopEnvDirty
				}
				return res, fmt.Errorf("ticket %s: %w", t.ID, err)
			}
			res.Attempts = append(res.Attempts, scoreAttempt(t, obs))
			if onAttempt != nil {
				if err := onAttempt(t, attempt, obs); err != nil {
					res.StopReason = StopError
					return res, err
				}
			}
			switch {
			case !obs.HasConversation: // a gate ticket neither adds nor resets
			case obs.LLMFailed:
				llmFailures++
			default:
				llmFailures = 0
			}
			spent += obs.CostMicroUSD
			if spent >= cfg.MaxCostMicroUSD {
				res.StopReason = StopCostCap
				return res, nil
			}
			if llmFailures >= infraLimit {
				res.StopReason = StopInfra
				return res, nil
			}
		}
	}
	return res, nil
}

func scoreAttempt(t ticketset.Ticket, o Observation) score.Attempt {
	return score.Attempt{
		Expected: t.Expected.Outcome, Result: o.Score, DurationMS: o.DurationMS, ApprovalWaitMS: o.ApprovalWaitMS,
		CostMicroUSD: o.CostMicroUSD, HasApproval: t.Approval != "none",
	}
}
