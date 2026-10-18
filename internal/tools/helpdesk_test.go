package tools

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/toyinogun/AIITsupport/internal/zammad"
)

// slowStates answers TicketStates only once release is closed, or fails
// when the caller's context ends first, as an HTTP call does.
type slowStates struct {
	HelpdeskAPI
	release chan struct{}
	calls   atomic.Int32
}

func (s *slowStates) TicketStates(ctx context.Context) ([]zammad.TicketState, error) {
	s.calls.Add(1)
	select {
	case <-s.release:
		return []zammad.TicketState{{ID: 2, Name: zammadOpen}, {ID: 4, Name: zammadClosed}}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// A caller waiting on a stalled first load still ends at its own deadline
// (TOOL_CALL_TIMEOUT), instead of queueing behind the load.
func TestStatesWaitersHonorTheirDeadline(t *testing.T) {
	api := &slowStates{release: make(chan struct{})}
	h := NewHelpdesk(api, "IT Support")
	first := make(chan error, 1)
	go func() {
		_, _, err := h.states(context.Background())
		first <- err
	}()
	for api.calls.Load() == 0 { // the first load is in flight
		time.Sleep(time.Millisecond)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, _, err := h.states(ctx)

	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > time.Second {
		t.Errorf("waiting caller = %v after %s; want its own deadline", err, time.Since(start))
	}
	close(api.release)
	if err := <-first; err != nil {
		t.Fatalf("first load: %v", err)
	}
}

func TestStatesAreCachedAfterTheFirstLoad(t *testing.T) {
	api := &slowStates{release: make(chan struct{})}
	close(api.release)
	h := NewHelpdesk(api, "IT Support")

	for range 3 {
		byName, byID, err := h.states(t.Context())
		if err != nil || byName[zammadClosed] != 4 || byID[2] != zammadOpen {
			t.Fatalf("states = %v, %v, %v", byName, byID, err)
		}
	}
	if n := api.calls.Load(); n != 1 {
		t.Errorf("TicketStates called %d times, want once", n)
	}
}

func TestStatesRetryAfterAFailedLoad(t *testing.T) {
	api := &slowStates{release: make(chan struct{})}
	h := NewHelpdesk(api, "IT Support")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, _, err := h.states(ctx); err == nil {
		t.Fatal("a failed load succeeded")
	}

	close(api.release)

	if byName, _, err := h.states(t.Context()); err != nil || byName[zammadOpen] != 2 {
		t.Errorf("states after a failed load = %v, %v; want a fresh load", byName, err)
	}
}
