package approvalapi_test

import (
	"errors"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/toyinogun/AIITsupport/internal/audit"
	"github.com/toyinogun/AIITsupport/internal/mcpclient"
	"github.com/toyinogun/AIITsupport/internal/tools"
)

// Spec 0013: the approval routes for a group owner approval.

const (
	ilseSlack = "U0ILSE0001"
	ilseMail  = "ilse.smit@noord.example"
)

// accessRequest has noor ask for SG-Finance and returns the approval id.
func (e *env) accessRequest() uuid.UUID {
	e.t.Helper()
	r := tools.Requester{SlackID: "U0NOOR0001", Email: "noor.elamrani@noord.example", ChannelID: "D0NOOR", ThreadTS: "1792400000.000100"}
	tk := e.d.Call(e.t.Context(), tools.Request{Tool: tools.ToolTicketCreate, Args: []byte(`{"title":"Finance","body":"Access."}`), Requester: r})
	r.TicketID = int64(tk.Data["ticket_id"].(int))
	resp := e.d.Call(e.t.Context(), tools.Request{
		Tool: tools.ToolGroupAccessRequest, Args: []byte(`{"group":"SG-Finance","reason":"I book invoices"}`), Requester: r,
	})
	if resp.Code != tools.CodePendingApproval {
		e.t.Fatalf("request = %+v", resp)
	}
	return uuid.MustParse(resp.Data["approval_id"].(string))
}

// covers: spec 0013 AC-10. The single view carries the kind, the group, the
// owner read live, the nesting, and the reason; the list carries only the
// kind and the group.
func TestGroupOwnerView(t *testing.T) {
	e := newEnv(t)
	id := e.accessRequest()
	v, err := e.client.Get(t.Context(), id)
	if err != nil || v.ApproverKind != mcpclient.ApproverKindGroupOwner || v.Target != "SG-Finance" ||
		v.ApproverAccount != "ilse.smit" || v.ApproverName != "Ilse Smit" || v.ApproverEmail != ilseMail ||
		!slices.Equal(v.NestedInto, []string{"APP-ERP-Finance", "SHR-Finance-RW"}) || v.Reason != "I book invoices" || v.CancelReason != "" {
		t.Errorf("view = %+v, %v", v, err)
	}
	list, err := e.client.Since(t.Context(), time.Now().Add(-time.Hour))
	if err != nil || len(list) != 1 || list[0].ApproverKind != "group_owner" || list[0].Target != "SG-Finance" ||
		list[0].ApproverName != "" || list[0].Reason != "" {
		t.Errorf("list = %+v, %v", list, err)
	}
}

// covers: spec 0013 AC-10, AC-17. A peek leaves out the live owner fields
// and reads nothing from the directory; a failed live read answers with the
// owner fields empty and owner_unread set, and the reason still there.
func TestGroupOwnerViewPeekAndUnread(t *testing.T) {
	e := newEnv(t)
	id := e.accessRequest()
	before := e.dir.CallCount
	v, err := e.client.Peek(t.Context(), id)
	if err != nil || v.Status != mcpclient.StatusPending || v.Target != "SG-Finance" || v.ApproverAccount != "ilse.smit" ||
		v.ApproverName != "" || v.ApproverEmail != "" || v.NestedInto != nil || v.OwnerUnread {
		t.Errorf("peek = %+v, %v", v, err)
	}
	if e.dir.CallCount != before {
		t.Errorf("a peek read the directory %d times", e.dir.CallCount-before)
	}
	e.dir.Err = errors.New("ldap down")
	v, err = e.client.Get(t.Context(), id)
	if err != nil || !v.OwnerUnread || v.ApproverName != "" || v.ApproverEmail != "" || v.Reason != "I book invoices" {
		t.Errorf("view with the directory down = %+v, %v", v, err)
	}
}

// covers: spec 0013 AC-13, AC-14. The owner's click with their email
// approves and runs the add before the answer.
func TestGroupOwnerApproveExecutes(t *testing.T) {
	e := newEnv(t)
	id := e.accessRequest()
	res, err := e.client.Decide(t.Context(), id, ilseSlack, ilseMail, true)
	if err != nil || !res.Moved || res.Approval.Status != mcpclient.StatusExecuted || res.Approval.Execution == nil ||
		res.Approval.Execution.Result["added"] != true || res.Approval.DecidedBy != "ilse.smit" {
		t.Fatalf("decision = %+v (%+v), %v", res, res.Approval.Execution, err)
	}
	rows := e.store.All()
	var pair bool
	for _, r := range rows {
		if r.ToolName == audit.ToolApprovalApprove && r.Kind == "outcome" {
			pair = r.ApproverSlackID.String == ilseSlack && r.Target.String == "ilse.smit"
		}
	}
	if !pair {
		t.Error("no approval.approve pair naming the clicker and the owner's account")
	}
}

// covers: spec 0013 AC-13. A click with no email, the requester's email, or
// an email nobody has is refused not_allowed with a denied_policy pair and
// no decider account; the approval stays pending.
func TestGroupOwnerRefusedClicks(t *testing.T) {
	e := newEnv(t)
	id := e.accessRequest()
	for _, email := range []string{"", "noor.elamrani@noord.example", "nobody@noord.example"} {
		res, err := e.client.Decide(t.Context(), id, ilseSlack, email, true)
		if err != nil || res.Moved || res.Reason != mcpclient.ReasonNotAllowed {
			t.Errorf("email %q: %+v, %v", email, res, err)
		}
		rows := e.store.All()
		last := rows[len(rows)-1]
		if last.ToolName != audit.ToolApprovalApprove || last.Outcome.String != "denied_policy" ||
			last.Error.String != "not an allowed approver" || last.Target.Valid {
			t.Errorf("email %q: pair %s %s %q target %q", email, last.ToolName, last.Outcome.String, last.Error.String, last.Target.String)
		}
	}
	if v, _ := e.client.Get(t.Context(), id); v.Status != mcpclient.StatusPending {
		t.Errorf("status = %s", v.Status)
	}
}

// covers: spec 0013 AC-13. The body is clicker and approve plus at most a
// well formed clicker_email; anything else is 400 and writes nothing. An IT
// approval ignores the email.
func TestDecisionBodyWithEmail(t *testing.T) {
	e := newEnv(t)
	id := e.accessRequest()
	before := len(e.store.All())
	for _, body := range []string{
		`{"clicker":"U0ILSE0001","approve":true,"clicker_email":"not-an-email"}`,
		`{"clicker":"U0ILSE0001","approve":true,"clicker_email":"a@b@c"}`,
		`{"clicker":"U0ILSE0001","approve":true,"clicker_email":"a b@noord.example"}`,
		`{"clicker":"U0ILSE0001","approve":true,"clicker_email":7}`,
		`{"clicker":"U0ILSE0001","approve":true,"email":"ilse.smit@noord.example"}`,
		`{"clicker":"U0ILSE0001","approve":true,"clicker_email":"ilse.smit@noord.example","x":1}`,
	} {
		if got := e.post(t, "/approvals/"+id.String()+"/decision", body, token); got != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", body, got)
		}
	}
	if len(e.store.All()) != before {
		t.Error("a bad body wrote audit rows")
	}
	mfa := e.request()
	res, err := e.client.Decide(t.Context(), mfa, approver, "someone@noord.example", true)
	if err != nil || !res.Moved {
		t.Errorf("IT approve with an email = %+v, %v", res, err)
	}
}

// covers: spec 0013 AC-11. Cancel ends a pending group owner approval with
// the owner unreachable reason in the view; anything else is 409, a bad
// body 400, and an unknown id 404.
func TestCancelRoute(t *testing.T) {
	e := newEnv(t)
	id := e.accessRequest()
	v, err := e.client.Cancel(t.Context(), id)
	if err != nil || v.Status != mcpclient.StatusExpired || v.CancelReason != mcpclient.CancelOwnerUnreachable || v.Reason != "" {
		t.Fatalf("cancel = %+v, %v", v, err)
	}
	if got, _ := e.client.Get(t.Context(), id); got.CancelReason != mcpclient.CancelOwnerUnreachable {
		t.Errorf("view after = %+v", got)
	}
	var se *mcpclient.StatusError
	if _, err := e.client.Cancel(t.Context(), id); !errors.As(err, &se) || se.Status != http.StatusConflict {
		t.Errorf("second cancel = %v", err)
	}
	if _, err := e.client.Cancel(t.Context(), e.request()); !errors.As(err, &se) || se.Status != http.StatusConflict {
		t.Errorf("IT cancel = %v", err)
	}
	if _, err := e.client.Cancel(t.Context(), uuid.New()); !errors.Is(err, mcpclient.ErrApprovalNotFound) {
		t.Errorf("unknown = %v", err)
	}
	for _, body := range []string{`{}`, `{"reason":"timeout"}`, `{"reason":"owner_unreachable","x":1}`, `[]`} {
		if got := e.post(t, "/approvals/"+uuid.New().String()+"/cancel", body, token); got != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", body, got)
		}
	}
	if got := e.post(t, "/approvals/"+id.String()+"/cancel", `{"reason":"owner_unreachable"}`, "wrong"); got != http.StatusUnauthorized {
		t.Errorf("wrong token: %d", got)
	}
}
