package audit

import (
	"errors"
	"testing"
	"time"

	"github.com/toyinogun/AIITsupport/internal/db/queries"
)

// Spec 0013 against real Postgres: group owner approvals.

const ownerSlack = "U04ILSE"

// accessCall is a group_access_request by jdevries in the unlock call's
// thread; thread varies the thread.
func accessCall(thread string) Call {
	c := unlockCall()
	c.Tool, c.Tier = "group_access_request", TierMedium
	c.Args, c.Store = map[string]any{"group": "SG-Finance", "reason": "invoices"}, []string{"group"}
	if thread != "" {
		c.ThreadTS = thread
	}
	return c
}

func deferAccess(t *testing.T, r *Recorder, c Call, group string) queries.Approval {
	t.Helper()
	a, err := r.Defer(t.Context(), c, ApprovalRequest{
		Kind: ApproverGroupOwner, Approvers: []string{"ilse.smit"}, Target: group, ExpiresAt: time.Now().Add(8 * time.Hour),
	}, nil)
	if err != nil {
		t.Fatalf("Defer %s: %v", group, err)
	}
	return a
}

// covers: spec 0013 AC-5, AC-8 (migration 00006). The one pending index is
// per group: two groups may wait at once, the same group twice may not; the
// reads find a request by its group, per thread, and count them.
func TestPgAccessPendingPerGroup(t *testing.T) {
	r, _ := pgRecorder(t)
	a := deferAccess(t, r, accessCall(""), "SG-Finance")
	if a.Target.String != "SG-Finance" || a.ApproverKind != "group_owner" {
		t.Fatalf("approval = %+v", a)
	}
	deferAccess(t, r, accessCall("1760000000.000999"), "APP-ERP-Finance")
	_, err := r.Defer(t.Context(), accessCall("1760000000.000998"), ApprovalRequest{
		Kind: ApproverGroupOwner, Approvers: []string{"ilse.smit"}, Target: "SG-Finance", ExpiresAt: time.Now().Add(time.Hour),
	}, nil)
	if !errors.Is(err, ErrApprovalPending) {
		t.Errorf("same group twice: %v, want ErrApprovalPending", err)
	}

	got, ok, err := r.PendingApproval(t.Context(), "jdevries", "group_access_request", "SG-Finance")
	if err != nil || !ok || got.ID != a.ID {
		t.Errorf("PendingApproval = %v, %v, %v", got.ID, ok, err)
	}
	if _, ok, _ := r.PendingApproval(t.Context(), "jdevries", "group_access_request", ""); ok {
		t.Error("a null target found a group request")
	}
	if n, err := r.PendingCount(t.Context(), "jdevries", "group_access_request"); err != nil || n != 2 {
		t.Errorf("PendingCount = %d, %v", n, err)
	}
	c := accessCall("")
	if target, ok, err := r.ThreadPendingTarget(t.Context(), "group_access_request", c.ChannelID, c.ThreadTS); err != nil || !ok || target != "SG-Finance" {
		t.Errorf("ThreadPendingTarget = %q, %v, %v", target, ok, err)
	}
	if _, ok, err := r.ThreadPendingTarget(t.Context(), "group_access_request", c.ChannelID, "9.9"); err != nil || ok {
		t.Errorf("another thread: %v, %v", ok, err)
	}
	if tool, err := r.ThreadOpenApprovalTool(t.Context(), c.ChannelID, c.ThreadTS); err != nil || tool != "group_access_request" {
		t.Errorf("ThreadOpenApprovalTool = %q, %v", tool, err)
	}
	_, err = r.Defer(t.Context(), accessCall("1760000000.000997"), ApprovalRequest{
		Kind: ApproverGroupOwner, Approvers: []string{"ilse.smit", "thomas.meijer"}, Target: "X", ExpiresAt: time.Now().Add(time.Hour),
	}, nil)
	if !errors.Is(err, ErrInvalidCall) {
		t.Errorf("two owners: %v, want ErrInvalidCall", err)
	}
}

// covers: spec 0013 AC-8. A group owner approval is decided by the owner's
// AD account: the requester's account and an empty account are refused,
// decided_by holds the account and decider_slack_id the clicker, and the
// approve pair names the clicker's Slack id and the account as its target.
func TestPgAccessDecideByAccount(t *testing.T) {
	r, q := pgRecorder(t)
	a := deferAccess(t, r, accessCall(""), "SG-Finance")
	for _, by := range []Decider{{SlackID: requester, Account: "jdevries"}, {SlackID: ownerSlack}} {
		d, err := r.Decide(t.Context(), a.ID, by, true)
		if err != nil || d.Moved || d.Reason != "not an allowed approver" {
			t.Errorf("%+v: %+v, %v", by, d, err)
		}
	}
	d, err := r.Decide(t.Context(), a.ID, Decider{SlackID: ownerSlack, Account: "ilse.smit"}, true)
	if err != nil || !d.Moved || d.Approval.DecidedBy.String != "ilse.smit" || d.Approval.DeciderSlackID.String != ownerSlack {
		t.Fatalf("owner approve = %+v, %v", d, err)
	}
	rows := threadRows(t, q, accessCall(""))
	last := rows[len(rows)-1]
	if last.ToolName != ToolApprovalApprove || last.Outcome.String != "ok" || last.ApproverSlackID.String != ownerSlack ||
		last.Target.String != "ilse.smit" {
		t.Errorf("approve pair = %s %s approver %q target %q", last.ToolName, last.Outcome.String, last.ApproverSlackID.String, last.Target.String)
	}
}

// covers: spec 0013 AC-11. Cancel moves only a pending group owner approval
// to expired with an approval.cancel pair, and Cancelled tells it from a
// timeout afterwards.
func TestPgAccessCancel(t *testing.T) {
	r, q := pgRecorder(t)
	a := deferAccess(t, r, accessCall(""), "SG-Finance")
	if cancelled, err := r.Cancelled(t.Context(), a.ID); err != nil || cancelled {
		t.Fatalf("before: %v, %v", cancelled, err)
	}
	got, moved, err := r.Cancel(t.Context(), a.ID)
	if err != nil || !moved || got.Status != "expired" || got.Arguments != nil {
		t.Fatalf("Cancel = %+v, %v, %v", got, moved, err)
	}
	if _, moved, err := r.Cancel(t.Context(), a.ID); err != nil || moved {
		t.Errorf("second Cancel moved %v, %v", moved, err)
	}
	if cancelled, err := r.Cancelled(t.Context(), a.ID); err != nil || !cancelled {
		t.Errorf("after: %v, %v", cancelled, err)
	}
	rows := threadRows(t, q, accessCall(""))
	last := rows[len(rows)-1]
	if last.ToolName != ToolApprovalCancel || last.Outcome.String != "expired" || last.Error.String != ReasonOwnerUnreachable {
		t.Errorf("cancel pair = %s %s %q", last.ToolName, last.Outcome.String, last.Error.String)
	}

	it := deferMFA(t, r, []string{approverA}, time.Now().Add(time.Hour))
	if _, moved, err := r.Cancel(t.Context(), it.ID); err != nil || moved {
		t.Errorf("an IT approval was cancelled: %v, %v", moved, err)
	}
	expired := deferAccess(t, r, accessCall("1760000000.000999"), "APP-ERP-Finance")
	if _, err := q.ExpireDueApprovals(t.Context(), 100); err != nil {
		t.Fatal(err)
	}
	if cancelled, _ := r.Cancelled(t.Context(), expired.ID); cancelled {
		t.Error("an unexpired request reads as cancelled")
	}
}

// covers: spec 0013 AC-5. A protected access request is counted by its
// stored reason.
func TestPgCountRecentReason(t *testing.T) {
	r, _ := pgRecorder(t)
	c := accessCall("")
	if err := r.Refuse(t.Context(), c, OutcomeDeniedPolicy, "group is protected"); err != nil {
		t.Fatal(err)
	}
	if err := r.Refuse(t.Context(), c, OutcomeDeniedPolicy, "no group owner"); err != nil {
		t.Fatal(err)
	}
	n, err := r.CountRecentReason(t.Context(), "group_access_request", c.RequesterSlackID, OutcomeDeniedPolicy, "group is protected", time.Now().Add(-time.Hour))
	if err != nil || n != 1 {
		t.Errorf("CountRecentReason = %d, %v", n, err)
	}
}
