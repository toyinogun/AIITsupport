package audit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/toyinogun/AIITsupport/internal/db/queries"
)

var errNotUsed = errors.New("not used by this test")

// fakeStore records what the recorder asked for, in order, and can fail on cue.
type fakeStore struct {
	mu            sync.Mutex
	events        []string
	intents       []queries.InsertAuditIntentParams
	outcomes      []queries.InsertAuditOutcomeParams
	intentErr     error
	outcomeErr    error
	outcomeCalls  int
	outcomeCtxErr error
}

func (f *fakeStore) InsertAuditIntent(_ context.Context, p queries.InsertAuditIntentParams) (queries.InsertAuditIntentRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, "intent")
	if f.intentErr != nil {
		return queries.InsertAuditIntentRow{}, f.intentErr
	}
	f.intents = append(f.intents, p)
	return queries.InsertAuditIntentRow{ID: int64(len(f.intents)), CallID: uuid.New()}, nil
}

func (f *fakeStore) InsertAuditOutcome(ctx context.Context, p queries.InsertAuditOutcomeParams) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.outcomeCalls++
	f.outcomeCtxErr = ctx.Err()
	f.events = append(f.events, "outcome")
	if f.outcomeErr != nil {
		return 0, f.outcomeErr
	}
	f.outcomes = append(f.outcomes, p)
	return int64(len(f.outcomes)), nil
}

func (f *fakeStore) CreateApproval(context.Context, queries.CreateApprovalParams) (queries.Approval, error) {
	return queries.Approval{}, errNotUsed
}

func (f *fakeStore) GetApproval(context.Context, uuid.UUID) (queries.Approval, error) {
	return queries.Approval{}, errNotUsed
}

func (f *fakeStore) ApproveApproval(context.Context, queries.ApproveApprovalParams) (queries.Approval, error) {
	return queries.Approval{}, errNotUsed
}

func (f *fakeStore) DenyApproval(context.Context, queries.DenyApprovalParams) (queries.Approval, error) {
	return queries.Approval{}, errNotUsed
}

func (f *fakeStore) ExpireDueApprovals(context.Context, int32) ([]queries.Approval, error) {
	return nil, errNotUsed
}

func (f *fakeStore) ExpireDueApprovalsFor(context.Context, queries.ExpireDueApprovalsForParams) ([]queries.Approval, error) {
	return nil, errNotUsed
}

func (f *fakeStore) AbandonStuckApprovals(context.Context, queries.AbandonStuckApprovalsParams) ([]queries.Approval, error) {
	return nil, errNotUsed
}

func (f *fakeStore) FindPendingApproval(context.Context, queries.FindPendingApprovalParams) (queries.Approval, error) {
	return queries.Approval{}, errNotUsed
}

func (f *fakeStore) ListApprovalsSince(context.Context, queries.ListApprovalsSinceParams) ([]queries.Approval, error) {
	return nil, errNotUsed
}

func (f *fakeStore) MarkApprovalExecuted(context.Context, uuid.UUID) (queries.Approval, error) {
	return queries.Approval{}, errNotUsed
}

func (f *fakeStore) MarkApprovalFailed(context.Context, uuid.UUID) (queries.Approval, error) {
	return queries.Approval{}, errNotUsed
}

func (f *fakeStore) ExecutionOutcomeForApproval(context.Context, queries.ExecutionOutcomeForApprovalParams) (queries.ExecutionOutcomeForApprovalRow, error) {
	return queries.ExecutionOutcomeForApprovalRow{}, errNotUsed
}

func (f *fakeStore) CountRecentOutcomes(context.Context, queries.CountRecentOutcomesParams) (int64, error) {
	return 0, errNotUsed
}

func (f *fakeStore) ThreadHasOutcome(context.Context, queries.ThreadHasOutcomeParams) (bool, error) {
	return false, errNotUsed
}

func (f *fakeStore) ThreadHasOpenApproval(context.Context, queries.ThreadHasOpenApprovalParams) (bool, error) {
	return false, errNotUsed
}

func (f *fakeStore) ThreadTicketID(context.Context, queries.ThreadTicketIDParams) (int64, error) {
	return 0, errNotUsed
}

func (f *fakeStore) InTx(_ context.Context, fn func(Queries) error) error { return fn(f) }

func (f *fakeStore) event(s string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, s)
}

// WriteTimeout matches the AUDIT_WRITE_TIMEOUT default: 1s timed out an insert on a
// busy self-hosted runner (Postgres on an SD card), and no test waits on it.
var testConfig = Config{OutcomeRetries: 3, WriteTimeout: 5 * time.Second, MaxPayloadBytes: 4096}

func newTestRecorder(t *testing.T, store Store) (*Recorder, *[]time.Duration) {
	t.Helper()
	r, err := NewRecorder(store, testConfig, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}
	var slept []time.Duration
	r.sleep = func(d time.Duration) { slept = append(slept, d) }
	return r, &slept
}

func unlockCall() Call {
	return Call{
		Tool:             "account_unlock",
		Tier:             TierLow,
		RequesterSlackID: "U01REQUESTER",
		RequesterAccount: "jdevries",
		ChannelID:        "C01HELP",
		ThreadTS:         "1760000000.000100",
		TicketID:         42,
		Target:           "jdevries",
		Args:             map[string]any{"account": "jdevries", "note": "free text"},
		Store:            []string{"account"},
		StoreResult:      []string{"unlocked"},
	}
}

func okAction(res Result) func(context.Context) (Result, error) {
	return func(context.Context) (Result, error) { return res, nil }
}

// covers: AC-2, AC-7 (spec 0003)
func TestRunRecordsIntentBeforeTheActionAndOutcomeAfter(t *testing.T) {
	store := &fakeStore{}
	r, _ := newTestRecorder(t, store)

	_, err := r.Run(t.Context(), unlockCall(), func(context.Context) (Result, error) {
		store.event("action")
		return Result{Data: map[string]any{"unlocked": true, "dn": "CN=x"}}, nil
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := strings.Join(store.events, ","); got != "intent,action,outcome" {
		t.Fatalf("order = %s, want intent,action,outcome", got)
	}
	if got := string(store.intents[0].Arguments); got != `{"_omitted":1,"account":"jdevries"}` {
		t.Errorf("arguments = %s", got)
	}
	out := store.outcomes[0]
	if out.Outcome != string(OutcomeOK) || out.Error.Valid {
		t.Errorf("outcome = %q error = %v, want ok and no error", out.Outcome, out.Error)
	}
	if got := string(out.Result); got != `{"_omitted":1,"unlocked":true}` {
		t.Errorf("result = %s", got)
	}
}

// covers: AC-5 (spec 0003)
func TestRunNeverCallsTheActionWhenTheIntentFails(t *testing.T) {
	store := &fakeStore{intentErr: errors.New("connection refused")}
	r, _ := newTestRecorder(t, store)

	called := 0
	_, err := r.Run(t.Context(), unlockCall(), func(context.Context) (Result, error) {
		called++
		return Result{}, nil
	})
	if !errors.Is(err, ErrAuditUnavailable) {
		t.Fatalf("err = %v, want ErrAuditUnavailable", err)
	}
	if called != 0 {
		t.Fatalf("action ran %d times without an intent row", called)
	}
	if store.outcomeCalls != 0 {
		t.Error("an outcome was written for a call that never ran")
	}
}

// covers: AC-6 (spec 0003)
func TestRunReportsAnIncompleteOutcomeAfterTheRetries(t *testing.T) {
	store := &fakeStore{outcomeErr: errors.New("connection reset")}
	r, slept := newTestRecorder(t, store)
	actionErr := errors.New("ldap: busy")

	res, err := r.Run(t.Context(), unlockCall(), func(context.Context) (Result, error) {
		return Result{Data: map[string]any{"unlocked": false}}, actionErr
	})
	if !errors.Is(err, ErrAuditIncomplete) || !errors.Is(err, actionErr) {
		t.Fatalf("err = %v, want both ErrAuditIncomplete and the action's error", err)
	}
	if res.Data["unlocked"] != false {
		t.Errorf("the action's result should still come back, got %v", res)
	}
	if store.outcomeCalls != testConfig.OutcomeRetries+1 {
		t.Errorf("outcome attempts = %d, want %d", store.outcomeCalls, testConfig.OutcomeRetries+1)
	}
	want := []time.Duration{50 * time.Millisecond, 100 * time.Millisecond, 200 * time.Millisecond}
	if len(*slept) != len(want) {
		t.Fatalf("backoff = %v, want %v", *slept, want)
	}
	for i := range want {
		if (*slept)[i] != want[i] {
			t.Errorf("backoff = %v, want %v", *slept, want)
		}
	}
}

func TestBackoffIsCappedAtOneSecond(t *testing.T) {
	store := &fakeStore{outcomeErr: errors.New("down")}
	r, slept := newTestRecorder(t, store)
	r.cfg.OutcomeRetries = 7

	_, _ = r.Run(t.Context(), unlockCall(), okAction(Result{}))
	if last := (*slept)[len(*slept)-1]; last != time.Second {
		t.Errorf("last backoff = %s, want 1s", last)
	}
}

// covers: AC-15 (spec 0003)
func TestRunWritesTheOutcomeWhenTheContextIsCancelled(t *testing.T) {
	store := &fakeStore{}
	r, _ := newTestRecorder(t, store)
	ctx, cancel := context.WithCancel(t.Context())

	_, err := r.Run(ctx, unlockCall(), func(context.Context) (Result, error) {
		cancel() // the requester gave up while the action was running
		return Result{}, nil
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(store.outcomes) != 1 || store.outcomeCtxErr != nil {
		t.Fatalf("outcomes = %d, ctx error at insert = %v; want one outcome on a live context",
			len(store.outcomes), store.outcomeCtxErr)
	}
}

// covers: AC-15 (spec 0003)
func TestRunRecordsAPanicThenPanicsAgain(t *testing.T) {
	store := &fakeStore{}
	r, _ := newTestRecorder(t, store)

	defer func() {
		if p := recover(); p != "boom" {
			t.Fatalf("recovered %v, want the original panic", p)
		}
		if len(store.outcomes) != 1 {
			t.Fatalf("outcomes = %d, want 1", len(store.outcomes))
		}
		out := store.outcomes[0]
		// The panic value is not stored: it can quote a name or a ticket.
		if out.Outcome != string(OutcomeError) || out.Error.String != "action panicked" {
			t.Errorf("outcome = %q %q, want error \"action panicked\"", out.Outcome, out.Error.String)
		}
	}()
	_, _ = r.Run(t.Context(), unlockCall(), func(context.Context) (Result, error) { panic("boom") })
}

// covers: AC-7 (spec 0003)
func TestEncodeKeptStoresOnlyAllowedKeys(t *testing.T) {
	big := strings.Repeat("x", 5000)
	cases := []struct {
		name string
		data map[string]any
		keep []string
		want string
	}{
		{"nil stores NULL", nil, []string{"a"}, ""},
		{"allowed key kept", map[string]any{"a": 1}, []string{"a"}, `{"a":1}`},
		{"other keys dropped and counted", map[string]any{"a": 1, "body": "help", "note": "x"}, []string{"a"}, `{"_omitted":2,"a":1}`},
		{"nothing allowed", map[string]any{"body": "help"}, nil, `{"_omitted":1}`},
		{"a key name is never stored", map[string]any{"jan.devries@noord.example needs a reset": 1}, nil, `{"_omitted":1}`},
		{"the count key cannot be allowed", map[string]any{"_omitted": "text"}, []string{"_omitted"}, `{"_omitted":1}`},
		{"nested values stay whole", map[string]any{"a": map[string]any{"b": 2}}, []string{"a"}, `{"a":{"b":2}}`},
		{"oversize truncated", map[string]any{"a": big}, []string{"a"}, `{"bytes":5008,"truncated":true}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := encodeKept(tc.data, tc.keep, 4096)
			if err != nil {
				t.Fatalf("encodeKept: %v", err)
			}
			if string(got) != tc.want {
				t.Errorf("got %s, want %s", got, tc.want)
			}
		})
	}
}

// covers: AC-7 (spec 0003)
func TestReadTierStoresNoResult(t *testing.T) {
	store := &fakeStore{}
	r, _ := newTestRecorder(t, store)
	c := unlockCall()
	c.Tool, c.Tier, c.StoreResult = "user_lookup", TierRead, []string{"display_name"}

	_, err := r.Run(t.Context(), c, okAction(Result{Data: map[string]any{"display_name": "Jan de Vries"}}))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if store.outcomes[0].Result != nil {
		t.Errorf("a read call stored a result: %s", store.outcomes[0].Result)
	}
}

func TestUnencodableResultIsMarked(t *testing.T) {
	store := &fakeStore{}
	r, _ := newTestRecorder(t, store)

	_, err := r.Run(t.Context(), unlockCall(), okAction(Result{Data: map[string]any{"unlocked": make(chan int)}}))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := string(store.outcomes[0].Result); got != `{"unencodable":true}` {
		t.Errorf("result = %s", got)
	}
}

// covers: AC-7 (spec 0003)
func TestActionErrorsStoreOnlyAFixedReason(t *testing.T) {
	directoryErr := errors.New(`LDAP 32: no such object "CN=Jan de Vries,OU=Staff,DC=noord,DC=example"`)
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"raw error text is never stored", directoryErr, "action failed"},
		{"a reason the action gives is stored", WithReason("account not found", directoryErr), "account not found"},
		{"the reason survives more wrapping", fmt.Errorf("unlock: %w", WithReason("account not found", directoryErr)), "account not found"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := &fakeStore{}
			r, _ := newTestRecorder(t, store)
			_, err := r.Run(t.Context(), unlockCall(), func(context.Context) (Result, error) { return Result{}, tc.err })
			if !errors.Is(err, directoryErr) {
				t.Errorf("Run error = %v, want the action's own error back for the caller", err)
			}
			if got := store.outcomes[0].Error.String; got != tc.want {
				t.Errorf("stored error = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestWithReasonKeepsTheErrorChain(t *testing.T) {
	base := errors.New("timeout")
	err := WithReason("directory unreachable", base)
	if !errors.Is(err, base) || err.Error() != "directory unreachable: timeout" {
		t.Errorf("WithReason = %q, is base %v", err, errors.Is(err, base))
	}
	if got := WithReason("account not found", nil).Error(); got != "account not found" {
		t.Errorf("WithReason with no error = %q", got)
	}
}

func TestErrorTextIsCappedAt500Characters(t *testing.T) {
	store := &fakeStore{}
	r, _ := newTestRecorder(t, store)

	_, _ = r.Run(t.Context(), unlockCall(), func(context.Context) (Result, error) {
		return Result{}, WithReason(strings.Repeat("é", 600), errors.New("x"))
	})
	if n := utf8.RuneCountInString(store.outcomes[0].Error.String); n != maxErrorRunes {
		t.Errorf("error length = %d characters, want %d", n, maxErrorRunes)
	}
}

func TestActionOutcomesAreRecorded(t *testing.T) {
	cases := []struct {
		res        Result
		want       Outcome
		wantReason string
	}{
		{Result{}, OutcomeOK, ""},
		{Result{Outcome: OutcomeDeniedIdentity, Reason: "no directory match"}, OutcomeDeniedIdentity, "no directory match"},
		{Result{Outcome: "maybe"}, OutcomeError, `action reported unknown outcome "maybe"`},
	}
	for _, tc := range cases {
		store := &fakeStore{}
		r, _ := newTestRecorder(t, store)
		if _, err := r.Run(t.Context(), unlockCall(), okAction(tc.res)); err != nil {
			t.Fatalf("Run: %v", err)
		}
		out := store.outcomes[0]
		if out.Outcome != string(tc.want) || out.Error.String != tc.wantReason {
			t.Errorf("Result %+v recorded as %q %q, want %q %q", tc.res, out.Outcome, out.Error.String, tc.want, tc.wantReason)
		}
	}
}

func TestInvalidCallsNeverReachTheStore(t *testing.T) {
	cases := map[string]func(*Call){
		"no tool":      func(c *Call) { c.Tool = "" },
		"unknown tier": func(c *Call) { c.Tier = "high" },
		"no requester": func(c *Call) { c.RequesterSlackID = "" },
		"bad argument": func(c *Call) { c.Args = map[string]any{"account": func() {}} },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			store := &fakeStore{}
			r, _ := newTestRecorder(t, store)
			c := unlockCall()
			mutate(&c)
			_, err := r.Run(t.Context(), c, okAction(Result{}))
			if !errors.Is(err, ErrInvalidCall) {
				t.Fatalf("err = %v, want ErrInvalidCall", err)
			}
			if len(store.events) != 0 {
				t.Errorf("store saw %v for an invalid call", store.events)
			}
		})
	}
}

func TestRefuseAcceptsOnlyRefusalOutcomes(t *testing.T) {
	store := &fakeStore{}
	r, _ := newTestRecorder(t, store)

	if err := r.Refuse(t.Context(), unlockCall(), OutcomeOK, ""); !errors.Is(err, ErrInvalidCall) {
		t.Errorf("Refuse(ok) = %v, want ErrInvalidCall", err)
	}
	if err := r.Refuse(t.Context(), unlockCall(), OutcomeReadOnly, "kill switch on"); err != nil {
		t.Fatalf("Refuse(read_only): %v", err)
	}
	if got := strings.Join(store.events, ","); got != "intent,outcome" {
		t.Errorf("events = %s, want intent,outcome", got)
	}
	if out := store.outcomes[0]; out.Outcome != string(OutcomeReadOnly) || out.Error.String != "kill switch on" {
		t.Errorf("outcome = %q %q", out.Outcome, out.Error.String)
	}
}

func TestRefuseReportsAFailedWriteAsUnavailable(t *testing.T) {
	store := &fakeStore{outcomeErr: errors.New("down")}
	r, _ := newTestRecorder(t, store)

	if err := r.Refuse(t.Context(), unlockCall(), OutcomeDeniedPolicy, "denied group"); !errors.Is(err, ErrAuditUnavailable) {
		t.Errorf("err = %v, want ErrAuditUnavailable", err)
	}
}

func TestNewRecorderRejectsBadSettings(t *testing.T) {
	cases := map[string]Config{
		"negative retries": {OutcomeRetries: -1, WriteTimeout: time.Second, MaxPayloadBytes: 4096},
		"zero timeout":     {OutcomeRetries: 3, MaxPayloadBytes: 4096},
		"tiny payload cap": {OutcomeRetries: 3, WriteTimeout: time.Second, MaxPayloadBytes: 10},
	}
	for name, cfg := range cases {
		if _, err := NewRecorder(&fakeStore{}, cfg, slog.New(slog.DiscardHandler)); err == nil {
			t.Errorf("%s: NewRecorder accepted %+v", name, cfg)
		}
	}
}

func TestTruncationMarkerIsValidJSON(t *testing.T) {
	got, err := encodeKept(map[string]any{"a": strings.Repeat("y", 100)}, []string{"a"}, minPayloadBytes)
	if err != nil {
		t.Fatalf("encodeKept: %v", err)
	}
	if !json.Valid(got) || len(got) > minPayloadBytes {
		t.Errorf("marker %s is not valid JSON within the cap", got)
	}
}

func TestDeferRejectsAnIncompleteRequest(t *testing.T) {
	later := time.Now().Add(time.Hour)
	cases := map[string]struct {
		mutate func(*Call)
		req    ApprovalRequest
	}{
		"unknown kind": {nil, ApprovalRequest{Kind: "manager", Approvers: []string{"U2"}, ExpiresAt: later}},
		"no approvers": {nil, ApprovalRequest{Kind: ApproverIT, ExpiresAt: later}},
		"no expiry":    {nil, ApprovalRequest{Kind: ApproverIT, Approvers: []string{"U2"}}},
		"no thread":    {func(c *Call) { c.ThreadTS = "" }, ApprovalRequest{Kind: ApproverIT, Approvers: []string{"U2"}, ExpiresAt: later}},
		"no clicker":   {nil, ApprovalRequest{}}, // checked through Decide below
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			store := &fakeStore{}
			r, _ := newTestRecorder(t, store)
			var err error
			if name == "no clicker" {
				_, err = r.Decide(t.Context(), uuid.New(), "", true)
			} else {
				c := unlockCall()
				if tc.mutate != nil {
					tc.mutate(&c)
				}
				_, err = r.Defer(t.Context(), c, tc.req, nil)
			}
			if !errors.Is(err, ErrInvalidCall) {
				t.Fatalf("err = %v, want ErrInvalidCall", err)
			}
			if len(store.events) != 0 {
				t.Errorf("store saw %v for an invalid request", store.events)
			}
		})
	}
}
