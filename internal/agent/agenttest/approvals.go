package agenttest

import (
	"cmp"
	"context"
	"slices"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/toyinogun/AIITsupport/internal/mcpclient"
)

// Approvals is a fake of the tool server's approval routes. Decide answers
// from Decisions by approval id (a refusal when there is none); Get answers
// from Views.
type Approvals struct {
	mu        sync.Mutex
	Decisions map[uuid.UUID]mcpclient.DecisionResponse
	Views     map[uuid.UUID]mcpclient.ApprovalView
	Err       error // returned by every call
	// Block makes Decide wait for its context to end, then fail with its error.
	Block bool
	// ExpireErr fails Expire only; Expired is what each Expire answers.
	ExpireErr error
	Expired   mcpclient.ExpireResponse
	clicks    []Decided
	expires   int
	since     []time.Time
}

// Decided is one Decide call.
type Decided struct {
	ID      uuid.UUID
	Clicker string
	Approve bool
}

// NewApprovals returns an empty fake.
func NewApprovals() *Approvals {
	return &Approvals{Decisions: map[uuid.UUID]mcpclient.DecisionResponse{}, Views: map[uuid.UUID]mcpclient.ApprovalView{}}
}

// Decide records the click and answers.
func (f *Approvals) Decide(ctx context.Context, id uuid.UUID, clicker string, approve bool) (mcpclient.DecisionResponse, error) {
	if f.Block {
		<-ctx.Done()
		return mcpclient.DecisionResponse{}, ctx.Err()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.clicks = append(f.clicks, Decided{ID: id, Clicker: clicker, Approve: approve})
	if f.Err != nil {
		return mcpclient.DecisionResponse{}, f.Err
	}
	if d, ok := f.Decisions[id]; ok {
		return d, nil
	}
	return mcpclient.DecisionResponse{Reason: mcpclient.ReasonNotAllowed, Approval: f.Views[id]}, nil
}

// Get answers from Views, or ErrApprovalNotFound.
func (f *Approvals) Get(_ context.Context, id uuid.UUID) (mcpclient.ApprovalView, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Err != nil {
		return mcpclient.ApprovalView{}, f.Err
	}
	if v, ok := f.Views[id]; ok {
		return v, nil
	}
	return mcpclient.ApprovalView{}, mcpclient.ErrApprovalNotFound
}

// Since answers the views created at or after since, oldest first.
func (f *Approvals) Since(_ context.Context, since time.Time) ([]mcpclient.ApprovalView, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.since = append(f.since, since)
	if f.Err != nil {
		return nil, f.Err
	}
	var out []mcpclient.ApprovalView
	for _, v := range f.Views {
		if !v.CreatedAt.Before(since) {
			out = append(out, v)
		}
	}
	slices.SortFunc(out, func(a, b mcpclient.ApprovalView) int { return a.CreatedAt.Compare(b.CreatedAt) })
	return out, nil
}

// Expire counts the call and answers Expired.
func (f *Approvals) Expire(context.Context) (mcpclient.ExpireResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.expires++
	if err := cmp.Or(f.ExpireErr, f.Err); err != nil {
		return mcpclient.ExpireResponse{}, err
	}
	return f.Expired, nil
}

// SetView replaces one view, for a test that moves an approval along.
func (f *Approvals) SetView(v mcpclient.ApprovalView) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Views[v.ID] = v
}

// Expires is how many times Expire was called.
func (f *Approvals) Expires() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.expires
}

// SinceCalls returns every since Since was called with.
func (f *Approvals) SinceCalls() []time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.since)
}

// Clicks returns every Decide call.
func (f *Approvals) Clicks() []Decided {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Decided(nil), f.clicks...)
}
