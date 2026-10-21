package tools

import (
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/toyinogun/AIITsupport/internal/audit"
	"github.com/toyinogun/AIITsupport/internal/db/queries"
	"github.com/toyinogun/AIITsupport/internal/ldap"
	"github.com/toyinogun/AIITsupport/internal/tools/toolstest"
)

// Spec 0013: group_search, group_access_request, and the approved add.

const ilseSlack = "U01ILSE"

var noorDN = toolstest.UserDN("Noor El Amrani", "Finance")

// groupHarness is the harness on the real clock (the store compares expiries
// with now, as the SQL does), with noor and ilse known to Zammad.
type groupHarness struct {
	*harness
	start time.Time
}

func newGroupHarness(t *testing.T) *groupHarness {
	t.Helper()
	h := newHarness(t)
	h.zammad.AddUser("noor.elamrani")
	h.zammad.AddUser("ilse.smit")
	start := time.Now().UTC().Truncate(time.Minute)
	h.d.now = func() time.Time { return start }
	return &groupHarness{harness: h, start: start}
}

// noor is a Finance requester in no finance group, in thread n.
func noor(n int) Requester {
	return Requester{
		SlackID: "U01NOOR", Email: "noor.elamrani@noord.example", ChannelID: "D01NOOR",
		ThreadTS: "1792400000.00010" + strconv.Itoa(n),
	}
}

func ilse() Requester {
	return Requester{SlackID: ilseSlack, Email: "ilse.smit@noord.example", ChannelID: "D01ILSE", ThreadTS: "1792400000.000900"}
}

// setUser changes a fake directory user in place.
func (h *groupHarness) setUser(name string, change func(u *ldap.User)) {
	for i := range h.dir.Users {
		if h.dir.Users[i].Username == name {
			change(&h.dir.Users[i])
		}
	}
}

func (h *groupHarness) search(r Requester, terms string) Response {
	h.t.Helper()
	return h.call(ToolGroupSearch, `{"terms":`+terms+`}`, r)
}

func requestArgs(group, reason string) string {
	b, _ := json.Marshal(map[string]string{"group": group, "reason": reason})
	return string(b)
}

// request opens a ticket in r's thread and asks for group.
func (h *groupHarness) request(r Requester, group, reason string) (Response, int) {
	h.t.Helper()
	ticket := h.createTicket(r)
	return h.call(ToolGroupAccessRequest, requestArgs(group, reason), withTicket(r, ticket)), ticket
}

func groupNames(t *testing.T, resp Response) []string {
	t.Helper()
	groups, ok := resp.Data["groups"].([]map[string]any)
	if !ok {
		t.Fatalf("groups = %#v", resp.Data["groups"])
	}
	var names []string
	for _, g := range groups {
		names = append(names, g["name"].(string))
	}
	return names
}

// covers: spec 0013 AC-2, AC-3. A Finance search lists the finance security
// groups of the requester's department by name or description, sorted,
// with owned and membership; a distribution list, a denied group, and a
// fenced group never show up, and the audit row keeps only the counts.
func TestGroupSearchFinance(t *testing.T) {
	h := newGroupHarness(t)
	resp := h.search(noor(1), `["fin"]`)
	if resp.IsError {
		t.Fatalf("search = %+v", resp)
	}
	want := []string{"APP-ERP-Finance", "fin-readonly", "Finance_Users", "SG-Finance", "SHR-Finance-RW"}
	if got := groupNames(t, resp); !slices.Equal(got, want) {
		t.Errorf("groups = %v, want %v", got, want)
	}
	// T-Nested matched and counts, but the fence drops it from the list.
	if resp.Data["count"] != 6 || resp.Data["truncated"] != false {
		t.Errorf("count %v, truncated %v", resp.Data["count"], resp.Data["truncated"])
	}
	owned := map[string]bool{}
	for _, g := range resp.Data["groups"].([]map[string]any) {
		owned[g["name"].(string)] = g["owned"].(bool)
		if g["requester_is_member"] != false {
			t.Errorf("%s requester_is_member = %v", g["name"], g["requester_is_member"])
		}
	}
	if !owned["SG-Finance"] || owned["Finance_Users"] || owned["fin-readonly"] || !owned["SHR-Finance-RW"] {
		t.Errorf("owned = %v", owned)
	}
	o, in := h.lastOutcome(), h.lastIntent()
	if o.ToolName != ToolGroupSearch || o.Target.Valid || string(o.Result) != `{"_omitted":1,"count":6,"truncated":false}` {
		t.Errorf("outcome = %s target %q result %s", o.ToolName, o.Target.String, o.Result)
	}
	if strings.Contains(string(in.Arguments), "fin") {
		t.Errorf("the search term reached the audit row: %s", in.Arguments)
	}
}

// covers: spec 0013 AC-2. A member is marked, and more than ten matches are
// cut to the first ten by name with truncated set.
func TestGroupSearchMembershipAndTruncation(t *testing.T) {
	h := newGroupHarness(t)
	h.dir.UserIn = map[string][]string{noorDN: {toolstest.GroupDN("SG-Finance")}}
	for i := range 8 {
		name := "SHR-Finance-Extra" + strconv.Itoa(i)
		h.dir.Groups[name] = ldap.Group{DN: toolstest.GroupDN(name), Name: name}
		h.dir.Info[name] = h.dir.Info["fin-readonly"]
	}
	resp := h.search(noor(1), `["finance"]`)
	groups := resp.Data["groups"].([]map[string]any)
	if resp.Data["truncated"] != true || len(groups) > maxSearchResults {
		t.Fatalf("truncated %v with %d groups", resp.Data["truncated"], len(groups))
	}
	for _, g := range groups {
		if g["name"] == "SG-Finance" && g["requester_is_member"] != true {
			t.Errorf("SG-Finance requester_is_member = %v", g["requester_is_member"])
		}
	}
}

// covers: spec 0013 AC-2, AC-22. A Sales user never sees finance groups,
// and admin terms show no privileged, denied, or fenced group.
func TestGroupSearchScopeAndPrivileged(t *testing.T) {
	h := newGroupHarness(t)
	for _, terms := range []string{`["finance"]`, `["ERP"]`} {
		if got := groupNames(t, h.search(eva(), terms)); len(got) != 0 {
			t.Errorf("Sales search %s = %v, want none", terms, got)
		}
	}
	got := groupNames(t, h.search(noor(1), `["admin","helpdesk","IT"]`))
	for _, never := range []string{"SG-IT-Admins", "APP-Helpdesk-Agents", "SYNC-Entra-Users", "T-Nested", "Domain Admins"} {
		if slices.Contains(got, never) {
			t.Errorf("admin search offered %s: %v", never, got)
		}
	}
}

// covers: spec 0013 AC-2. terms is 1 to 3 strings of 2 to 40 characters
// after trimming; anything else is invalid_arguments naming the field.
func TestGroupSearchTerms(t *testing.T) {
	h := newGroupHarness(t)
	for _, terms := range []string{
		`[]`, `["f"]`, `["  f  "]`, `["fin","fin","fin","fin"]`, `"fin"`, `[1]`, `null`,
		`["` + strings.Repeat("x", 41) + `"]`, `["fi\u0007n"]`,
	} {
		resp := h.search(noor(1), terms)
		wantCode(t, resp, CodeInvalidArguments)
		if !strings.Contains(resp.Text, "(field: terms)") {
			t.Errorf("%s: %s", terms, resp.Text)
		}
	}
	if resp := h.search(noor(1), `["  fin  ","financ"]`); resp.IsError {
		t.Errorf("trimmed terms refused: %+v", resp)
	}
}

// covers: spec 0013 AC-4, AC-5, AC-7. A request for an owned group defers
// with the owner as the one approver, the canonical name as the target, the
// cleaned reason in the stored call (never in an audit row), the pending
// result, and the ticket note; nothing is written to the directory.
func TestGroupAccessRequestDefers(t *testing.T) {
	h := newGroupHarness(t)
	resp, ticket := h.request(noor(1), "sg-finance", "  I process\n  supplier invoices  ")
	if resp.IsError || resp.Code != CodePendingApproval || resp.Text != "This request is waiting for the group owner's approval." {
		t.Fatalf("response = %+v", resp)
	}
	expires := h.start.Add(8 * time.Hour)
	for k, want := range map[string]any{
		"group": "SG-Finance", "owner_name": "Ilse Smit", "expires_at": expires.Format(time.RFC3339),
		"expires_in_hours": int64(8), "ticket_id": int64(ticket), "reused": false, "ticket_noted": true,
		"ticket_url": "http://zammad.test/#ticket/zoom/" + strconv.Itoa(ticket),
	} {
		if resp.Data[k] != want {
			t.Errorf("%s = %v (%T), want %v (%T)", k, resp.Data[k], resp.Data[k], want, want)
		}
	}
	for _, k := range []string{"approver_account", "expires_in_minutes"} {
		if _, ok := resp.Data[k]; ok {
			t.Errorf("the response holds %s", k)
		}
	}
	if len(h.store.Approvals) != 1 {
		t.Fatalf("approvals = %d", len(h.store.Approvals))
	}
	a := h.store.Approvals[0]
	if a.ApproverKind != "group_owner" || !slices.Equal(a.AllowedApprovers, []string{"ilse.smit"}) ||
		a.Target.String != "SG-Finance" || a.RequesterAccount.String != "noor.elamrani" ||
		string(a.Arguments) != `{"group":"SG-Finance","reason":"I process supplier invoices"}` {
		t.Errorf("approval = %+v args %s", a, a.Arguments)
	}
	in, o := h.lastIntent(), h.lastOutcome()
	if in.Target.String != "SG-Finance" || string(in.Arguments) != `{"_omitted":1,"group":"SG-Finance"}` {
		t.Errorf("intent target %q args %s", in.Target.String, in.Arguments)
	}
	if string(o.Result) != `{"_omitted":2,"approver_account":"ilse.smit","expires_at":"`+expires.Format(time.RFC3339)+`","reused":false}` {
		t.Errorf("pending result = %s", o.Result)
	}
	for _, r := range h.store.All() {
		if strings.Contains(string(r.Arguments)+string(r.Result)+r.Error.String, "invoices") {
			t.Errorf("the reason reached an audit row: %+v", r)
		}
	}
	arts := h.zammad.Articles(ticket)
	note := arts[len(arts)-1]
	if note["body"] != "Access to SG-Finance requested. Waiting for the group owner (Ilse Smit) until "+
		expires.Format("2006-01-02 15:04")+" UTC. Reason: I process supplier invoices." || note["internal"] != true {
		t.Errorf("note = %v", note)
	}
	if len(h.dir.Added()) != 0 {
		t.Errorf("the request wrote the directory: %v", h.dir.Added())
	}
}

// covers: spec 0013 AC-5. The same group again in the same thread reuses
// the pending approval; spelling does not make a second request.
func TestGroupAccessRequestReuse(t *testing.T) {
	h := newGroupHarness(t)
	first, ticket := h.request(noor(1), "SG-Finance", "invoices")
	again := h.call(ToolGroupAccessRequest, requestArgs("sg-FINANCE", "still invoices"), withTicket(noor(1), ticket))
	if again.Code != CodePendingApproval || again.Data["reused"] != true || again.Data["approval_id"] != first.Data["approval_id"] ||
		again.Data["owner_name"] != "Ilse Smit" || again.Data["ticket_noted"] != false {
		t.Errorf("repeat = %+v", again)
	}
	if len(h.store.Approvals) != 1 {
		t.Errorf("approvals = %d, want 1", len(h.store.Approvals))
	}
}

// covers: spec 0013 AC-5, AC-6, AC-22. Every refusal in the precheck order,
// each with an audit pair, the right target, and no approval row.
func TestGroupAccessRequestRefusals(t *testing.T) {
	cases := []struct {
		name   string
		r      Requester
		group  string
		setup  func(h *groupHarness)
		code   Code
		reason string
		target string
	}{
		{name: "floor name", r: noor(1), group: "domain admins", code: CodeProtected, reason: "group is protected"},
		{name: "denied name", r: noor(1), group: "APP-Helpdesk-Agents", code: CodeProtected, reason: "group is protected"},
		{name: "nested into a denied group", r: noor(1), group: "T-Nested", code: CodeProtected, reason: "group is protected", target: "T-Nested"},
		{name: "another department", r: noor(1), group: "SG-Sales", code: CodeNotRequestable, reason: "group not requestable"},
		{name: "no such group", r: noor(1), group: "SG-Finanse", code: CodeNotRequestable, reason: "group not requestable"},
		{name: "distribution list", r: noor(1), group: "allstaff", code: CodeNotRequestable, reason: "group not requestable"},
		{name: "outside the group OUs", r: noor(1), group: "Outside-Group", code: CodeNotRequestable, reason: "group not requestable"},
		{
			name: "already a member", r: noor(1), group: "SG-Finance", code: CodeAlreadyMember, reason: "already a member", target: "SG-Finance",
			setup: func(h *groupHarness) {
				h.dir.UserIn = map[string][]string{noorDN: {toolstest.GroupDN("SG-Finance")}}
			},
		},
		{name: "no owner", r: noor(1), group: "Finance_Users", code: CodeNoOwner, reason: "no group owner", target: "Finance_Users"},
		{
			name: "disabled owner", r: noor(1), group: "SG-Finance", code: CodeNoOwner, reason: "no group owner", target: "SG-Finance",
			setup: func(h *groupHarness) { h.setUser("ilse.smit", func(u *ldap.User) { u.Enabled = false }) },
		},
		{
			name: "owner without mail", r: noor(1), group: "SG-Finance", code: CodeNoOwner, reason: "no group owner", target: "SG-Finance",
			setup: func(h *groupHarness) { h.setUser("ilse.smit", func(u *ldap.User) { u.Mail = "" }) },
		},
		{name: "owner asks", r: ilse(), group: "SG-Finance", code: CodeOwnerIsRequester, reason: "owner is requester", target: "SG-Finance"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newGroupHarness(t)
			if tc.setup != nil {
				tc.setup(h)
			}
			resp, _ := h.request(tc.r, tc.group, "please")
			wantCode(t, resp, tc.code)
			o := h.lastOutcome()
			if o.Outcome.String != "denied_policy" || o.Error.String != tc.reason || o.Target.String != tc.target {
				t.Errorf("outcome %s %q target %q, want denied_policy %q target %q", o.Outcome.String, o.Error.String, o.Target.String, tc.reason, tc.target)
			}
			if len(h.store.Approvals) != 0 || len(h.dir.Added()) != 0 {
				t.Errorf("approvals %d, adds %v", len(h.store.Approvals), h.dir.Added())
			}
		})
	}
}

// covers: spec 0013 AC-5. With no ticket, nothing else is read.
func TestGroupAccessRequestNeedsATicket(t *testing.T) {
	h := newGroupHarness(t)
	before := h.dir.CallCount
	wantCode(t, h.call(ToolGroupAccessRequest, requestArgs("SG-Finance", "please"), noor(1)), CodeNoTicket)
	if h.dir.CallCount-before != 1 { // identity only
		t.Errorf("directory reads = %d", h.dir.CallCount-before)
	}
}

// covers: spec 0013 AC-5. One request per thread, one per group across
// threads, and at most ACCESS_MAX_PENDING per person.
func TestGroupAccessRequestLimits(t *testing.T) {
	h := newGroupHarness(t)
	if resp, _ := h.request(noor(1), "SG-Finance", "invoices"); resp.Code != CodePendingApproval {
		t.Fatalf("first = %+v", resp)
	}
	// Another group in the same thread.
	ticket := int(h.store.Approvals[0].ZammadTicketID.Int64)
	wantCode(t, h.call(ToolGroupAccessRequest, requestArgs("APP-ERP-Finance", "erp"), withTicket(noor(1), ticket)), CodeOnePerThread)
	// The same group in another thread.
	resp, _ := h.request(noor(2), "SG-Finance", "invoices")
	wantCode(t, resp, CodeAlreadyPending)
	if resp.Text != "already_pending: A request for this group is already waiting for its owner in another conversation." {
		t.Errorf("text = %q", resp.Text)
	}
	// A second group in another thread is fine, until the cap.
	if resp, _ := h.request(noor(3), "APP-ERP-Finance", "erp"); resp.Code != CodePendingApproval {
		t.Fatalf("second group = %+v", resp)
	}
	h.d.cfg.AccessMaxPending = 2
	resp, _ = h.request(noor(4), "SHR-Finance-RW", "share")
	wantCode(t, resp, CodeTooManyPending)
	if len(h.store.Approvals) != 2 {
		t.Errorf("approvals = %d, want 2", len(h.store.Approvals))
	}
}

// covers: spec 0013 AC-4. The kill switch stops a request at step 7.
func TestGroupAccessRequestPaused(t *testing.T) {
	h := newGroupHarness(t)
	h.ks.set("read_only\n")
	resp, _ := h.request(noor(1), "SG-Finance", "invoices")
	wantCode(t, resp, CodeReadOnly)
	if len(h.store.Approvals) != 0 {
		t.Error("a paused request deferred")
	}
}

// approve requests SG-Finance for noor and approves it as ilse.
func (h *groupHarness) approve() (queries.Approval, int) {
	h.t.Helper()
	resp, ticket := h.request(noor(1), "SG-Finance", "invoices")
	if resp.Code != CodePendingApproval {
		h.t.Fatalf("request = %+v", resp)
	}
	id := h.store.All()[len(h.store.All())-1].ApprovalID.UUID
	d, err := h.d.sys.Recorder.Decide(h.t.Context(), id, audit.Decider{SlackID: ilseSlack, Account: "ilse.smit"}, true)
	if err != nil || !d.Moved {
		h.t.Fatalf("approve = %+v, %v", d, err)
	}
	return d.Approval, ticket
}

func (h *groupHarness) status(a queries.Approval) string {
	got, _ := h.store.Approval(a.ID)
	return got.Status
}

// covers: spec 0013 AC-8, AC-14. The owner's approve runs the add once: the
// requester is in the group, the ticket is closed with the note, and the
// execution pair names the group and the owner's Slack id.
func TestGroupAccessExecute(t *testing.T) {
	h := newGroupHarness(t)
	a, ticket := h.approve()
	if a.DecidedBy.String != "ilse.smit" || a.DeciderSlackID.String != ilseSlack {
		t.Errorf("decided by %q, slack %q", a.DecidedBy.String, a.DeciderSlackID.String)
	}
	if got := h.d.Execute(t.Context(), a); got != audit.OutcomeOK {
		t.Fatalf("Execute = %s", got)
	}
	if got := h.dir.Members[toolstest.GroupDN("SG-Finance")]; !slices.Equal(got, []string{noorDN}) {
		t.Errorf("members = %v", got)
	}
	o := h.lastOutcome()
	if o.ToolName != ToolGroupAccessRequest || o.Target.String != "SG-Finance" || o.ApproverSlackID.String != ilseSlack ||
		o.ApprovalID.UUID != a.ID || string(o.Result) != `{"added":true,"already_member":false,"ticket_closed":true}` {
		t.Errorf("outcome = %s target %q approver %q result %s", o.ToolName, o.Target.String, o.ApproverSlackID.String, o.Result)
	}
	if tk, _ := h.zammad.Ticket(ticket); tk["state"] != "closed" {
		t.Errorf("ticket state %v", tk["state"])
	}
	arts := h.zammad.Articles(ticket)
	if body := arts[len(arts)-1]["body"]; body != "Added to SG-Finance after approval by the group owner (ilse.smit)." {
		t.Errorf("note = %v", body)
	}
	if h.status(a) != "executed" {
		t.Errorf("status = %s", h.status(a))
	}
}

// covers: spec 0013 AC-8, AC-13. Only the listed owner's account approves:
// the requester's own account, a Slack id alone, or another account is
// refused as not an allowed approver and changes nothing.
func TestGroupAccessOnlyTheOwnerDecides(t *testing.T) {
	h := newGroupHarness(t)
	resp, _ := h.request(noor(1), "SG-Finance", "invoices")
	id := h.store.All()[len(h.store.All())-1].ApprovalID.UUID
	for _, by := range []audit.Decider{
		{SlackID: "U01NOOR", Account: "noor.elamrani"},
		{SlackID: ilseSlack},
		{SlackID: "U01THOMAS", Account: "thomas.meijer"},
	} {
		d, err := h.d.sys.Recorder.Decide(t.Context(), id, by, true)
		if err != nil || d.Moved || d.Reason != "not an allowed approver" {
			t.Errorf("%+v: decision %+v, %v (request %v)", by, d, err, resp.Code)
		}
		o := h.lastOutcome()
		if o.ToolName != audit.ToolApprovalApprove || o.Outcome.String != "denied_policy" || o.Target.Valid || o.ApproverSlackID.String != by.SlackID {
			t.Errorf("%+v: pair %s %s target %q approver %q", by, o.ToolName, o.Outcome.String, o.Target.String, o.ApproverSlackID.String)
		}
	}
	d, err := h.d.sys.Recorder.Decide(t.Context(), id, audit.Decider{SlackID: ilseSlack, Account: "ilse.smit"}, false)
	if err != nil || !d.Moved || d.Approval.Status != "denied" {
		t.Fatalf("owner deny = %+v, %v", d, err)
	}
	if o := h.lastOutcome(); o.Target.String != "ilse.smit" || o.ApproverSlackID.String != ilseSlack {
		t.Errorf("deny pair target %q approver %q", o.Target.String, o.ApproverSlackID.String)
	}
}

// covers: spec 0013 AC-14. The execution checks again before the write:
// an owner change, a disabled requester, or a removed department tag fails
// with recheck failed and writes nothing.
func TestGroupAccessExecuteRechecks(t *testing.T) {
	for name, change := range map[string]func(h *groupHarness){
		"owner changed": func(h *groupHarness) {
			info := h.dir.Info["SG-Finance"]
			info.ManagedBy = toolstest.UserDN("Thomas Meijer", "Finance")
			h.dir.Info["SG-Finance"] = info
		},
		"requester disabled": func(h *groupHarness) { h.setUser("noor.elamrani", func(u *ldap.User) { u.Enabled = false }) },
		"owner disabled":     func(h *groupHarness) { h.setUser("ilse.smit", func(u *ldap.User) { u.Enabled = false }) },
		"tag removed": func(h *groupHarness) {
			info := h.dir.Info["SG-Finance"]
			info.Departments = nil
			h.dir.Info["SG-Finance"] = info
		},
	} {
		t.Run(name, func(t *testing.T) {
			h := newGroupHarness(t)
			a, _ := h.approve()
			change(h)
			if got := h.d.Execute(t.Context(), a); got != audit.OutcomeDeniedPolicy {
				t.Fatalf("Execute = %s", got)
			}
			if o := h.lastOutcome(); o.Error.String != "recheck failed" {
				t.Errorf("reason = %q", o.Error.String)
			}
			if len(h.dir.Added()) != 0 || h.status(a) != "failed" {
				t.Errorf("adds %v, status %s", h.dir.Added(), h.status(a))
			}
		})
	}
}

// covers: spec 0013 AC-14. A member added by hand meanwhile is a success
// with already_member; an add that does not read back is an error.
func TestGroupAccessExecuteRaceAndReadBack(t *testing.T) {
	h := newGroupHarness(t)
	a, _ := h.approve()
	h.dir.Members = map[string][]string{toolstest.GroupDN("SG-Finance"): {noorDN}}
	if got := h.d.Execute(t.Context(), a); got != audit.OutcomeOK {
		t.Fatalf("race: Execute = %s", got)
	}
	if o := h.lastOutcome(); string(o.Result) != `{"added":true,"already_member":true,"ticket_closed":true}` {
		t.Errorf("race result = %s", o.Result)
	}

	h = newGroupHarness(t)
	a, ticket := h.approve()
	h.dir.DropAdds = true
	if got := h.d.Execute(t.Context(), a); got != audit.OutcomeError {
		t.Fatalf("read back: Execute = %s", got)
	}
	if o := h.lastOutcome(); o.Error.String != "add not confirmed" || h.status(a) != "failed" {
		t.Errorf("reason %q, status %s", o.Error.String, h.status(a))
	}
	if tk, _ := h.zammad.Ticket(ticket); tk["state"] == "closed" {
		t.Error("an unconfirmed add closed the ticket")
	}
}

// covers: spec 0013 AC-4, AC-14. Paused at approval time, the add never runs.
func TestGroupAccessExecutePaused(t *testing.T) {
	h := newGroupHarness(t)
	a, _ := h.approve()
	h.ks.set("read_only\n")
	if got := h.d.Execute(t.Context(), a); got != audit.OutcomeReadOnly {
		t.Fatalf("Execute = %s", got)
	}
	if len(h.dir.Added()) != 0 || h.status(a) != "failed" {
		t.Errorf("adds %v, status %s", h.dir.Added(), h.status(a))
	}
}

// covers: spec 0013 AC-14. After an escalation in the thread, the add still
// runs but the ticket stays open for IT.
func TestGroupAccessExecuteLeavesAnEscalatedTicketOpen(t *testing.T) {
	h := newGroupHarness(t)
	a, ticket := h.approve()
	esc := h.call(ToolEscalate, `{"reason":"manual_action","title":"Access","summary":"IT, please look."}`, withTicket(noor(1), ticket))
	if esc.IsError {
		t.Fatalf("escalate = %+v", esc)
	}
	if got := h.d.Execute(t.Context(), a); got != audit.OutcomeOK {
		t.Fatalf("Execute = %s", got)
	}
	if o := h.lastOutcome(); string(o.Result) != `{"added":true,"already_member":false,"ticket_closed":false}` {
		t.Errorf("result = %s", o.Result)
	}
	if tk, _ := h.zammad.Ticket(ticket); tk["state"] == "closed" {
		t.Error("the add closed an escalated ticket")
	}
}

// covers: spec 0013 AC-18. While the access request is open, the model
// cannot close the ticket; the refusal speaks of the group owner.
func TestGroupAccessCloseGuard(t *testing.T) {
	h := newGroupHarness(t)
	_, ticket := h.request(noor(1), "SG-Finance", "invoices")
	resp := h.call(ToolTicketUpdate, `{"state":"closed"}`, withTicket(noor(1), ticket))
	wantCode(t, resp, CodeApprovalPending)
	if resp.Text != "approval_pending: This ticket is waiting on the group owner's decision; the assistant's code handles it when they decide." {
		t.Errorf("text = %q", resp.Text)
	}
	if o := h.lastOutcome(); o.Error.String != "access request pending" {
		t.Errorf("reason = %q", o.Error.String)
	}
}

// covers: spec 0013 AC-5, AC-19. A protected access request counts toward
// the privileged attempt window, and no_group_owner is a model reason that
// tags the ticket escalated only.
func TestGroupAccessProtectedCountsAndNoOwnerEscalates(t *testing.T) {
	h := newGroupHarness(t)
	resp, ticket := h.request(noor(1), "Domain Admins", "please")
	wantCode(t, resp, CodeProtected)
	esc := h.call(ToolEscalate, `{"reason":"no_group_owner","title":"Access","summary":"Nobody owns it."}`, withTicket(noor(1), ticket))
	if esc.IsError || esc.Data["privileged_attempts"] != int64(1) || esc.Data["reason"] != "no_group_owner" {
		t.Fatalf("escalate = %+v", esc)
	}
	if tags := h.zammad.Tags(ticket); !slices.Contains(tags, "escalated") || slices.Contains(tags, "privileged-request") {
		t.Errorf("tags = %v", tags)
	}
}

// covers: spec 0013 AC-10. The approval view's owner fields: the owner's
// name and mail read live, and the groups an add also gives.
func TestGroupOwnerView(t *testing.T) {
	h := newGroupHarness(t)
	h.request(noor(1), "SG-Finance", "invoices")
	a, _ := h.store.Approval(h.store.All()[len(h.store.All())-1].ApprovalID.UUID)
	v, err := h.d.GroupOwnerView(t.Context(), a)
	if err != nil || v.Name != "Ilse Smit" || v.Email != "ilse.smit@noord.example" ||
		!slices.Equal(v.NestedInto, []string{"APP-ERP-Finance", "SHR-Finance-RW"}) {
		t.Errorf("view = %+v, %v", v, err)
	}
	h.setUser("ilse.smit", func(u *ldap.User) { u.Enabled = false })
	if v, _ := h.d.GroupOwnerView(t.Context(), a); v.Email != "" || v.Name != "Ilse Smit" {
		t.Errorf("disabled owner view = %+v, want a name and no mail", v)
	}
}

// covers: spec 0013 AC-13. ResolveDecider maps a confirmed email to exactly
// one enabled account under TOOL_USER_OUS.
func TestResolveDecider(t *testing.T) {
	h := newGroupHarness(t)
	if acct, ok, err := h.d.ResolveDecider(t.Context(), "Ilse.Smit@noord.example"); err != nil || !ok || acct != "ilse.smit" {
		t.Errorf("ilse = %q %v %v", acct, ok, err)
	}
	for _, email := range []string{"nobody@noord.example", "old.leaver@noord.example", "adm@noord.example"} {
		if acct, ok, err := h.d.ResolveDecider(t.Context(), email); err != nil || ok {
			t.Errorf("%s = %q %v %v, want no match", email, acct, ok, err)
		}
	}
}

// covers: spec 0013 AC-2, AC-4. tools/list shows terms as an array of 1 to 3
// strings and the request's two fields; the registry refuses a medium tool
// that mixes the two precheck families, and a GroupHandler below medium.
func TestGroupToolSchemasAndRegistryRules(t *testing.T) {
	st := groupSearchTool()
	search := st.InputSchema()
	terms := search["properties"].(map[string]any)["terms"].(map[string]any)
	items := terms["items"].(map[string]any)
	if terms["type"] != "array" || terms["minItems"] != 1 || terms["maxItems"] != 3 ||
		items["type"] != "string" || items["minLength"] != 2 || items["maxLength"] != 40 {
		t.Errorf("terms schema = %v", terms)
	}
	rt := groupAccessRequestTool(time.Hour)
	req := rt.InputSchema()
	if !slices.Equal(req["required"].([]string), []string{"group", "reason"}) {
		t.Errorf("required = %v", req["required"])
	}

	mixed := groupAccessRequestTool(time.Hour)
	mixed.Precheck = mfaPrecheck
	low := groupAccessRequestTool(time.Hour)
	low.Name, low.Tier, low.Approval, low.GroupPrecheck = "t_low_group", audit.TierLow, nil, nil
	wrongHandler := groupAccessRequestTool(time.Hour)
	wrongHandler.Name, wrongHandler.Handler = "t_wrong", MFAHandler(mfaClear)
	for _, tool := range []Tool{mixed, low, wrongHandler} {
		if _, err := newRegistry([]Tool{tool}); err == nil {
			t.Errorf("%s: want a construction error", tool.Name)
		}
	}
	if _, err := NewRegistry(0); err == nil {
		t.Error("a zero access timeout built a registry")
	}
}
