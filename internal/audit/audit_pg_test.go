package audit

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/toyinogun/AIITsupport/internal/db/dbtest"
	"github.com/toyinogun/AIITsupport/internal/db/queries"
)

// These tests run against a scratch Postgres as noord_toolserver, the role
// the tool server uses, so they also prove its grants are enough.

func pgRecorder(t *testing.T) (*Recorder, *queries.Queries) {
	t.Helper()
	tdb := dbtest.New(t)
	r, err := NewRecorder(NewPgStore(tdb.ToolServer), testConfig, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}
	return r, queries.New(tdb.ToolServer)
}

func threadRows(t *testing.T, q *queries.Queries, c Call) []queries.AuditLog {
	t.Helper()
	rows, err := q.ListAuditByThread(t.Context(), queries.ListAuditByThreadParams{
		SlackChannelID: text(c.ChannelID), SlackThreadTs: text(c.ThreadTS),
	})
	if err != nil {
		t.Fatalf("ListAuditByThread: %v", err)
	}
	return rows
}

func sqlState(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}

// covers: AC-2, AC-7 (spec 0003)
func TestPgRunWritesAnIntentAndAnOutcome(t *testing.T) {
	r, q := pgRecorder(t)
	c := unlockCall()

	_, err := r.Run(t.Context(), c, okAction(Result{Data: map[string]any{"unlocked": true}}))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	rows := threadRows(t, q, c)
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want an intent and an outcome", len(rows))
	}
	intent, outcome := rows[0], rows[1]
	if intent.Kind != "intent" || outcome.Kind != "outcome" || intent.CallID != outcome.CallID {
		t.Fatalf("rows are %s/%s with call ids %s/%s", intent.Kind, outcome.Kind, intent.CallID, outcome.CallID)
	}
	var args map[string]any
	if err := json.Unmarshal(intent.Arguments, &args); err != nil {
		t.Fatalf("arguments: %v", err)
	}
	if _, kept := args["note"]; args["account"] != "jdevries" || kept || args[omittedKey] != 1.0 {
		t.Errorf("arguments = %v, want account kept and note dropped and counted", args)
	}
	// The outcome copies who, what, and where from its intent.
	if outcome.ToolName != c.Tool || outcome.RequesterSlackID != c.RequesterSlackID ||
		outcome.RequesterAccount.String != c.RequesterAccount || outcome.Target.String != c.Target ||
		outcome.ZammadTicketID.Int64 != c.TicketID || outcome.RiskTier != string(c.Tier) {
		t.Errorf("outcome identity columns were not copied from the intent: %+v", outcome)
	}
	if outcome.Arguments != nil || outcome.Outcome.String != "ok" || string(outcome.Result) != `{"unlocked": true}` {
		t.Errorf("outcome = args %s, outcome %q, result %s", outcome.Arguments, outcome.Outcome.String, outcome.Result)
	}
}

// covers: AC-2 (spec 0003)
func TestPgAnOutcomeNeedsAnIntent(t *testing.T) {
	_, q := pgRecorder(t)

	_, err := q.InsertAuditOutcome(t.Context(), queries.InsertAuditOutcomeParams{CallID: uuid.New(), Outcome: "ok"})
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("outcome without intent: err = %v, want no rows inserted", err)
	}
}

// covers: AC-2 (spec 0003)
func TestPgASecondOutcomeIsRejected(t *testing.T) {
	r, q := pgRecorder(t)
	c := unlockCall()
	if _, err := r.Run(t.Context(), c, okAction(Result{})); err != nil {
		t.Fatalf("Run: %v", err)
	}
	callID := threadRows(t, q, c)[0].CallID

	_, err := q.InsertAuditOutcome(t.Context(), queries.InsertAuditOutcomeParams{CallID: callID, Outcome: "error"})
	if sqlState(err) != "23505" {
		t.Fatalf("second outcome: err = %v, want a unique violation (23505)", err)
	}
}

// covers: AC-6 (spec 0003)
func TestPgAnIntentWithoutOutcomeIsListedAsUnconfirmed(t *testing.T) {
	r, q := pgRecorder(t)
	params, err := r.intentParams(unlockCall())
	if err != nil {
		t.Fatalf("intentParams: %v", err)
	}
	orphan, err := q.InsertAuditIntent(t.Context(), params) // the process died before the outcome
	if err != nil {
		t.Fatalf("InsertAuditIntent: %v", err)
	}
	if _, err := r.Run(t.Context(), unlockCall(), okAction(Result{})); err != nil {
		t.Fatalf("Run: %v", err)
	}

	rows, err := q.ListUnconfirmedCalls(t.Context(), queries.ListUnconfirmedCallsParams{
		OlderThan: time.Now().Add(time.Minute), MaxRows: 10,
	})
	if err != nil {
		t.Fatalf("ListUnconfirmedCalls: %v", err)
	}
	if len(rows) != 1 || rows[0].CallID != orphan.CallID {
		t.Fatalf("unconfirmed = %+v, want only the orphaned intent", rows)
	}
}

func TestPgRefuseWritesAPair(t *testing.T) {
	r, q := pgRecorder(t)
	c := unlockCall()
	c.Tool, c.Tier, c.Target = "group_add", TierBlocked, "Domain Admins"

	if err := r.Refuse(t.Context(), c, OutcomeDeniedPolicy, "privileged group"); err != nil {
		t.Fatalf("Refuse: %v", err)
	}
	rows := threadRows(t, q, c)
	if len(rows) != 2 || rows[1].Outcome.String != "denied_policy" || rows[1].Error.String != "privileged group" {
		t.Fatalf("rows = %+v, want an intent and a denied_policy outcome", rows)
	}
}

// --- approvals ---

const (
	requester = "U01REQUESTER"
	approverA = "U02ITLEAD"
	approverB = "U03ITDESK"
)

func mfaCall() Call {
	c := unlockCall()
	c.Tool, c.Tier = "mfa_reset", TierMedium
	c.Args, c.Store = map[string]any{"account": "jdevries"}, []string{"account"}
	return c
}

func deferMFA(t *testing.T, r *Recorder, approvers []string, expires time.Time) queries.Approval {
	t.Helper()
	a, err := r.Defer(t.Context(), mfaCall(), ApprovalRequest{Kind: ApproverIT, Approvers: approvers, ExpiresAt: expires})
	if err != nil {
		t.Fatalf("Defer: %v", err)
	}
	return a
}

func outcomesOf(rows []queries.AuditLog) []string {
	var out []string
	for _, row := range rows {
		if row.Kind == "outcome" {
			out = append(out, row.ToolName+":"+row.Outcome.String)
		}
	}
	return out
}

// covers: AC-8 (spec 0003)
func TestPgDeferRecordsThePendingApproval(t *testing.T) {
	r, q := pgRecorder(t)
	a := deferMFA(t, r, []string{approverA}, time.Now().Add(time.Hour))

	if a.Status != "pending" || string(a.Arguments) != `{"account": "jdevries"}` {
		t.Errorf("approval = %+v, want pending with the exact call", a)
	}
	rows := threadRows(t, q, mfaCall())
	if len(rows) != 2 || rows[0].CallID != a.CallID {
		t.Fatalf("rows = %+v, want the intent behind the approval", rows)
	}
	if rows[1].Outcome.String != "pending_approval" || rows[1].ApprovalID.UUID != a.ID {
		t.Errorf("outcome = %q approval %v, want pending_approval naming %s", rows[1].Outcome.String, rows[1].ApprovalID, a.ID)
	}
}

// covers: AC-8 (spec 0003)
func TestPgAnApprovalIsApprovedOnlyOnce(t *testing.T) {
	r, q := pgRecorder(t)
	a := deferMFA(t, r, []string{approverA, approverB}, time.Now().Add(time.Hour))

	first, err := r.Decide(t.Context(), a.ID, approverA, true)
	if err != nil || !first.Moved || first.Approval.Status != "approved" || first.Approval.DecidedBy.String != approverA {
		t.Fatalf("first click = %+v, %v; want approved by %s", first, err, approverA)
	}
	second, err := r.Decide(t.Context(), a.ID, approverB, true)
	if err != nil || second.Moved || second.Reason != "already decided" {
		t.Fatalf("second click = %+v, %v; want refused as already decided", second, err)
	}

	executed, err := q.MarkApprovalExecuted(t.Context(), a.ID)
	if err != nil || executed.Status != "executed" {
		t.Fatalf("MarkApprovalExecuted = %+v, %v", executed, err)
	}
	if _, err := q.MarkApprovalExecuted(t.Context(), a.ID); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("a second execution moved the approval: %v", err)
	}

	want := []string{"mfa_reset:pending_approval", "approval.approve:ok", "approval.approve:denied_policy"}
	got := outcomesOf(threadRows(t, q, mfaCall()))
	if len(got) != len(want) {
		t.Fatalf("audit outcomes = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("audit outcomes = %v, want %v", got, want)
		}
	}
}

// covers: AC-7, AC-8 (spec 0003)
func TestPgAnEndedApprovalForgetsItsArguments(t *testing.T) {
	r, q := pgRecorder(t)
	const full = `{"account": "jdevries"}`

	denied := deferMFA(t, r, []string{approverA}, time.Now().Add(time.Hour))
	d, err := r.Decide(t.Context(), denied.ID, approverA, false)
	if err != nil || !d.Moved || d.Approval.Arguments != nil {
		t.Errorf("deny = %+v, %v; want arguments cleared", d.Approval, err)
	}

	approved := deferMFA(t, r, []string{approverA}, time.Now().Add(time.Hour))
	d, err = r.Decide(t.Context(), approved.ID, approverA, true)
	if err != nil || string(d.Approval.Arguments) != full {
		t.Fatalf("approve = %+v, %v; want the full call kept until it runs", d.Approval, err)
	}
	executed, err := q.MarkApprovalExecuted(t.Context(), approved.ID)
	if err != nil || executed.Arguments != nil {
		t.Errorf("executed = %s, %v; want arguments cleared", executed.Arguments, err)
	}

	failing := deferMFA(t, r, []string{approverA}, time.Now().Add(time.Hour))
	if _, err := r.Decide(t.Context(), failing.ID, approverA, true); err != nil {
		t.Fatalf("approve: %v", err)
	}
	if failed, err := q.MarkApprovalFailed(t.Context(), failing.ID); err != nil || failed.Arguments != nil {
		t.Errorf("failed = %s, %v; want arguments cleared", failed.Arguments, err)
	}

	late := deferMFA(t, r, []string{approverA}, time.Now().Add(-time.Minute))
	expired, err := r.Expire(t.Context(), 10)
	if err != nil || len(expired) != 1 || expired[0].ID != late.ID || expired[0].Arguments != nil {
		t.Errorf("expire = %+v, %v; want the late approval expired with arguments cleared", expired, err)
	}
}

// covers: AC-8 (spec 0003)
func TestPgApprovalArgumentsCanOnlyBeCleared(t *testing.T) {
	tdb := dbtest.New(t)
	r, err := NewRecorder(NewPgStore(tdb.ToolServer), testConfig, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}
	a := deferMFA(t, r, []string{approverA}, time.Now().Add(time.Hour))

	for role, pool := range map[string]interface {
		Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	}{dbtest.RoleToolServer: tdb.ToolServer, dbtest.RoleOwner: tdb.Owner} {
		_, err := pool.Exec(t.Context(), `UPDATE approvals SET arguments = '{"account": "someone_else"}' WHERE id = $1`, a.ID)
		if sqlState(err) != "55000" {
			t.Errorf("%s rewrote a pending call's arguments: err = %v, want 55000", role, err)
		}
	}
	_, err = tdb.ToolServer.Exec(t.Context(), `UPDATE approvals SET arguments = NULL WHERE id = $1`, a.ID)
	if sqlState(err) != "23514" {
		t.Errorf("cleared the arguments of a call that can still run: err = %v, want a check violation", err)
	}
	_, err = tdb.ToolServer.Exec(t.Context(),
		`UPDATE approvals SET status = 'denied', decided_by = $2, decided_at = now() WHERE id = $1`, a.ID, approverA)
	if sqlState(err) != "23514" {
		t.Errorf("ended an approval without clearing its arguments: err = %v, want a check violation", err)
	}
}

// covers: AC-8 (spec 0003)
func TestPgOnlyAnApprovedApprovalCanRunOrFail(t *testing.T) {
	r, q := pgRecorder(t)

	pending := deferMFA(t, r, []string{approverA}, time.Now().Add(time.Hour))
	if _, err := q.MarkApprovalExecuted(t.Context(), pending.ID); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("a pending approval was executed: %v", err)
	}
	if _, err := q.MarkApprovalFailed(t.Context(), pending.ID); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("a pending approval was marked failed: %v", err)
	}

	if _, err := r.Decide(t.Context(), pending.ID, approverA, false); err != nil {
		t.Fatalf("deny: %v", err)
	}
	if _, err := q.MarkApprovalExecuted(t.Context(), pending.ID); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("a denied approval was executed: %v", err)
	}

	approved := deferMFA(t, r, []string{approverA}, time.Now().Add(time.Hour))
	if d, err := r.Decide(t.Context(), approved.ID, approverA, true); err != nil || !d.Moved {
		t.Fatalf("approve = %+v, %v", d, err)
	}
	failed, err := q.MarkApprovalFailed(t.Context(), approved.ID)
	if err != nil || failed.Status != "failed" || !failed.ExecutedAt.Valid {
		t.Fatalf("MarkApprovalFailed = %+v, %v; want failed with executed_at set", failed, err)
	}
	if _, err := q.MarkApprovalExecuted(t.Context(), approved.ID); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("a failed approval was executed afterwards: %v", err)
	}
}

// covers: AC-8 (spec 0003)
func TestPgRefusedClicksChangeNothing(t *testing.T) {
	cases := []struct {
		name      string
		approvers []string
		expires   time.Duration
		clicker   string
		approve   bool
		reason    string
	}{
		{"not an approver", []string{approverA}, time.Hour, "U09STRANGER", true, "not an allowed approver"},
		{"own request", []string{approverA, requester}, time.Hour, requester, true, "requester cannot approve"},
		{"too late to approve", []string{approverA}, -time.Minute, approverA, true, "expired"},
		{"too late to deny", []string{approverA}, -time.Minute, approverA, false, "expired"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, q := pgRecorder(t)
			a := deferMFA(t, r, tc.approvers, time.Now().Add(tc.expires))

			d, err := r.Decide(t.Context(), a.ID, tc.clicker, tc.approve)
			if err != nil || d.Moved || d.Reason != tc.reason {
				t.Fatalf("Decide = %+v, %v; want refused: %s", d, err, tc.reason)
			}
			if d.Approval.Status != "pending" {
				t.Errorf("status = %s, want still pending", d.Approval.Status)
			}
			rows := threadRows(t, q, mfaCall())
			last := rows[len(rows)-1]
			if last.Outcome.String != "denied_policy" || last.Error.String != tc.reason || last.ApproverSlackID.String != tc.clicker {
				t.Errorf("audit = %q %q by %q, want denied_policy %q by %s",
					last.Outcome.String, last.Error.String, last.ApproverSlackID.String, tc.reason, tc.clicker)
			}
		})
	}
}

// covers: AC-8 (spec 0003)
func TestPgApproveAndDenyRaceHasOneWinner(t *testing.T) {
	r, _ := pgRecorder(t)
	for range 5 {
		a := deferMFA(t, r, []string{approverA, approverB}, time.Now().Add(time.Hour))

		var wg sync.WaitGroup
		results := make([]Decision, 2)
		errs := make([]error, 2)
		for i, approve := range []bool{true, false} {
			wg.Go(func() {
				results[i], errs[i] = r.Decide(context.WithoutCancel(t.Context()), a.ID, []string{approverA, approverB}[i], approve)
			})
		}
		wg.Wait()

		if errs[0] != nil || errs[1] != nil {
			t.Fatalf("Decide errors: %v, %v", errs[0], errs[1])
		}
		if results[0].Moved == results[1].Moved {
			t.Fatalf("moved = %v and %v, want exactly one winner", results[0].Moved, results[1].Moved)
		}
	}
}

// covers: AC-8 (spec 0003)
func TestPgExpireMovesOnlyOverdueApprovals(t *testing.T) {
	r, q := pgRecorder(t)
	overdue := deferMFA(t, r, []string{approverA}, time.Now().Add(-time.Minute))
	deferMFA(t, r, []string{approverA}, time.Now().Add(time.Hour))

	expired, err := r.Expire(t.Context(), 10)
	if err != nil {
		t.Fatalf("Expire: %v", err)
	}
	if len(expired) != 1 || expired[0].ID != overdue.ID || expired[0].Status != "expired" {
		t.Fatalf("expired = %+v, want only the overdue approval", expired)
	}
	if again, err := r.Expire(t.Context(), 10); err != nil || len(again) != 0 {
		t.Errorf("second Expire = %d rows, %v; want none", len(again), err)
	}
	rows := threadRows(t, q, mfaCall())
	last := rows[len(rows)-1]
	if last.ToolName != ToolApprovalExpire || last.Outcome.String != "expired" || last.ApprovalID.UUID != overdue.ID {
		t.Errorf("last audit row = %s %s for %v", last.ToolName, last.Outcome.String, last.ApprovalID)
	}
}

// covers: AC-8 (spec 0003)
func TestPgEachApprovalBelongsToOneCall(t *testing.T) {
	r, q := pgRecorder(t)
	a := deferMFA(t, r, []string{approverA}, time.Now().Add(time.Hour))

	_, err := q.CreateApproval(t.Context(), queries.CreateApprovalParams{
		CallID: a.CallID, ToolName: "mfa_reset", Arguments: json.RawMessage(`{}`), RiskTier: "medium",
		RequesterSlackID: requester, SlackChannelID: "C01HELP", SlackThreadTs: "1",
		ApproverKind: "it", AllowedApprovers: []string{approverA}, ExpiresAt: time.Now().Add(time.Hour),
	})
	if sqlState(err) != "23505" {
		t.Fatalf("second approval for one call: err = %v, want a unique violation", err)
	}
}

func TestPgDecidingAnUnknownApprovalFails(t *testing.T) {
	r, _ := pgRecorder(t)
	if _, err := r.Decide(t.Context(), uuid.New(), approverA, true); !errors.Is(err, ErrApprovalNotFound) {
		t.Fatalf("err = %v, want ErrApprovalNotFound", err)
	}
}
