package runner_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/toyinogun/AIITsupport/eval/runner"
	"github.com/toyinogun/AIITsupport/eval/ticketset"
	"github.com/toyinogun/AIITsupport/internal/seed/evalreset"
)

type fakePlayer struct {
	calls []string
	obs   func(call int, t ticketset.Ticket) (runner.Observation, error)
}

func (f *fakePlayer) Run(_ context.Context, t ticketset.Ticket) (runner.Observation, error) {
	f.calls = append(f.calls, t.ID)
	return f.obs(len(f.calls), t)
}

func tickets(ids ...string) []ticketset.Ticket {
	out := make([]ticketset.Ticket, 0, len(ids))
	for _, id := range ids {
		out = append(out, ticketset.Ticket{ID: id, Expected: ticketset.Expected{Outcome: "resolve"}})
	}
	return out
}

func okObs(cost int64) runner.Observation {
	return runner.Observation{CostMicroUSD: cost, HasConversation: true}
}

// covers: AC-18
func TestPlayRepeatsEachTicketByAttempt(t *testing.T) {
	p := &fakePlayer{obs: func(int, ticketset.Ticket) (runner.Observation, error) { return okObs(1), nil }}
	var seen []string
	res, err := runner.Play(t.Context(), p, tickets("a", "b"), runner.PlayConfig{Repeat: 2, MaxCostMicroUSD: 1000},
		func(tk ticketset.Ticket, attempt int, _ runner.Observation) error {
			seen = append(seen, fmt.Sprintf("%s%d", tk.ID, attempt))
			return nil
		})
	if err != nil || res.StopReason != "" || len(res.Attempts) != 4 {
		t.Fatalf("res = %+v, %v", res, err)
	}
	if want := []string{"a1", "b1", "a2", "b2"}; len(seen) != 4 || seen[0] != want[0] || seen[3] != want[3] {
		t.Fatalf("order = %v, want %v", seen, want)
	}
}

// covers: AC-9
func TestPlayStopsAtTheCostCap(t *testing.T) {
	p := &fakePlayer{obs: func(int, ticketset.Ticket) (runner.Observation, error) { return okObs(40), nil }}
	res, err := runner.Play(t.Context(), p, tickets("a", "b", "c", "d"), runner.PlayConfig{Repeat: 1, MaxCostMicroUSD: 100}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.StopReason != runner.StopCostCap || len(res.Attempts) != 3 {
		t.Fatalf("stop = %q after %d attempts, want cost_cap after 3 (the one that reached 120)", res.StopReason, len(res.Attempts))
	}
}

// covers: AC-8
func TestPlayStopsAfterThreeLLMFailuresInARow(t *testing.T) {
	failing := map[string]bool{"a": true, "b": true, "c": true}
	p := &fakePlayer{obs: func(_ int, tk ticketset.Ticket) (runner.Observation, error) {
		o := okObs(1)
		o.LLMFailed = failing[tk.ID]
		return o, nil
	}}
	res, err := runner.Play(t.Context(), p, tickets("a", "b", "c", "d"), runner.PlayConfig{Repeat: 1, MaxCostMicroUSD: 1000}, nil)
	if err != nil || res.StopReason != runner.StopInfra || len(res.Attempts) != 3 {
		t.Fatalf("res = %+v, %v", res, err)
	}
}

// covers: AC-8
func TestPlayInfraCountResetsAndSkipsGateTickets(t *testing.T) {
	p := &fakePlayer{obs: func(call int, _ ticketset.Ticket) (runner.Observation, error) {
		switch call {
		case 1, 2, 4, 5: // failed model calls
			o := okObs(1)
			o.LLMFailed = true
			return o, nil
		case 3: // a gate ticket: no conversation, neither adds nor resets
			return runner.Observation{CostMicroUSD: 0}, nil
		default: // 6: a good conversation resets the count
			return okObs(1), nil
		}
	}}
	res, err := runner.Play(t.Context(), p, tickets("a", "b", "c", "d", "e", "f"), runner.PlayConfig{Repeat: 1, MaxCostMicroUSD: 1000}, nil)
	// a, b fail; c is a gate ticket (skipped); d is the third failure.
	if err != nil || res.StopReason != runner.StopInfra || len(res.Attempts) != 4 {
		t.Fatalf("res = %+v, %v", res, err)
	}
}

// covers: AC-7
func TestPlayDirtyEnvironmentStopsTheRun(t *testing.T) {
	p := &fakePlayer{obs: func(call int, _ ticketset.Ticket) (runner.Observation, error) {
		if call == 2 {
			return runner.Observation{}, errors.Join(errors.New("after b"), evalreset.ErrDirty)
		}
		return okObs(1), nil
	}}
	res, err := runner.Play(t.Context(), p, tickets("a", "b", "c"), runner.PlayConfig{Repeat: 1, MaxCostMicroUSD: 1000}, nil)
	if !errors.Is(err, evalreset.ErrDirty) || res.StopReason != runner.StopEnvDirty || len(res.Attempts) != 1 {
		t.Fatalf("res = %+v, %v", res, err)
	}
}
