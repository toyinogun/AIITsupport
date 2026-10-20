package agent

import (
	"context"
	"time"
)

// Wait blocks until every worker, post, and sweep the agent started has
// finished. Tests call it after Submit.
func (a *Agent) Wait() { a.wg.Wait() }

// OneToolPerStep is the result text for a tool_use block that did not run.
const OneToolPerStep = oneToolPerStep

// EscalationNote exposes the note builder to the external tests.
var EscalationNote = escalationNote

// NoteOpening is the fixed opening of a reason's escalation note.
func NoteOpening(r Reason) string { return noteOpenings[r] }

// Pass runs one retention pass, as Run does at start and on each tick.
func (r *Retention) Pass(ctx context.Context) { r.pass(ctx) }

// SetStepTimeout shortens the per query bound for a test.
func (r *Retention) SetStepTimeout(d time.Duration) { r.stepTimeout = d }

// Tick runs one approval tick and waits for the settles it queued.
func (a *Agent) Tick() {
	a.tick(a.workCtx)
	a.wg.Wait()
}

// TickNoWait runs one approval tick without waiting for its settles.
func (a *Agent) TickNoWait() { a.tick(a.workCtx) }
