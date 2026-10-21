package tools

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	goldap "github.com/go-ldap/ldap/v3"

	"github.com/toyinogun/AIITsupport/internal/audit"
	"github.com/toyinogun/AIITsupport/internal/db/queries"
	"github.com/toyinogun/AIITsupport/internal/ldap"
)

// Access requests via the group owner (spec 0013): group_search lists the
// groups the requester may ask for, and group_access_request asks the
// group's owner. The add itself runs only from Dispatcher.Execute, after the
// owner approved in Slack.

// Bounds of the two tools' arguments, part of the schema the model sees.
const (
	maxSearchTerms     = 3
	minSearchTermRunes = 2
	maxSearchTermRunes = 40
	maxGroupNameRunes  = 64
	maxReasonRunes     = 200
	// maxSearchResults is how many groups a search shows; count says how
	// many matched.
	maxSearchResults = 10
)

// GroupAddedText is the success text of an approved add.
const GroupAddedText = "Added to the group."

func groupSearchTool() Tool {
	return Tool{
		Name: ToolGroupSearch,
		Description: "Find groups the requester can ask to join, by 1 to 3 short English search terms (translate and fix " +
			"typos first). Returns only groups for the requester's department. Never guess a group name; use this.",
		Tier: audit.TierRead,
		Fields: []Field{{
			Name: "terms", Description: "1 to 3 short English search terms, such as finance, fin, financ.",
			Required: true, List: true, MinItems: 1, MaxItems: maxSearchTerms,
			MinRunes: minSearchTermRunes, MaxRunes: maxSearchTermRunes, NoNewline: true, FreeText: true,
		}},
		Target:      TargetNone,
		StoreResult: []string{"count", "truncated"},
		Handler:     ReadHandler(groupSearch),
	}
}

func groupAccessRequestTool(timeout time.Duration) Tool {
	return Tool{
		Name: ToolGroupAccessRequest,
		Description: "Ask a group's owner to add the requester to it. Use an exact name from group_search and a short reason " +
			"in the requester's words. Needs this conversation's ticket first. Nothing changes until the owner approves in " +
			"Slack; the assistant's code tells the requester the outcome. Never say access is granted.",
		Tier: audit.TierMedium,
		Fields: []Field{
			{Name: "group", Description: "The exact group name from group_search.", Required: true, MinRunes: 1, MaxRunes: maxGroupNameRunes, NoNewline: true},
			{Name: "reason", Description: "Why the requester needs it, in their words.", Required: true, MinRunes: 1, MaxRunes: maxReasonRunes, FreeText: true},
		},
		Store:         []string{"group"},
		Target:        TargetGroup,
		StoreResult:   []string{"added", "already_member", "ticket_closed"},
		PendingResult: []string{"expires_at", "reused", "approver_account"},
		Approval: func(_ context.Context, _ audit.Call, pre Prechecked, now time.Time) (audit.ApprovalRequest, error) {
			if pre.Target == "" || pre.Owner.Account == "" {
				return audit.ApprovalRequest{}, errors.New("the group precheck found no group or owner")
			}
			return audit.ApprovalRequest{
				Kind: audit.ApproverGroupOwner, Approvers: []string{pre.Owner.Account},
				Target: pre.Target, ExpiresAt: now.Add(timeout),
			}, nil
		},
		GroupPrecheck: groupPrecheck,
		Handler:       GroupHandler(groupAdd),
	}
}

// groupSearch lists the groups the requester may ask for whose name or
// description holds a term (spec 0013, AC-2). The filters run cheapest
// first; a denied group, a group of another department, and a group that
// does not exist all look the same: absent, in the list and in count. The
// directory reads are batched: five sessions however many groups match.
func groupSearch(ctx context.Context, d Deps, in Input) (Result, error) {
	all, err := d.Groups.ListGroups(ctx, d.Scope.GroupOUs)
	if err != nil {
		return Result{}, directoryFailure(err)
	}
	terms := in.Lists["terms"]
	var hits []ldap.GroupInfo
	for _, g := range all {
		if g.Security && !d.Fence.DeniedName(g.Name) && taggedFor(g, in.Account.Department) && matchesTerm(g, terms) {
			hits = append(hits, g)
		}
	}
	hits, err = d.Fence.AllowedGroups(ctx, hits)
	if err != nil {
		return Result{}, err
	}
	slices.SortFunc(hits, func(a, b ldap.GroupInfo) int {
		return cmp.Or(cmp.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)), cmp.Compare(a.Name, b.Name))
	})
	count := len(hits)
	shown := hits[:min(count, maxSearchResults)]
	groups := []map[string]any{}
	if len(shown) > 0 {
		memberOf, err := d.Groups.UserGroups(ctx, in.Account.DN, d.Scope.GroupOUs)
		if err != nil {
			return Result{}, directoryFailure(err)
		}
		owned, err := groupsOwned(ctx, d, shown)
		if err != nil {
			return Result{}, err
		}
		for _, g := range shown {
			member := slices.ContainsFunc(memberOf, func(n string) bool { return strings.EqualFold(n, g.Name) })
			groups = append(groups, map[string]any{
				"name": g.Name, "description": g.Description, "owned": owned[g.DN], "requester_is_member": member,
			})
		}
	}
	return Result{
		Result: resultData(map[string]any{"groups": groups, "count": count, "truncated": count > maxSearchResults}),
		Text:   "Searched the groups this person can ask for.",
	}, nil
}

// taggedFor reports whether a group's department tags name the department,
// compared case sensitively; an empty department matches nothing.
func taggedFor(g ldap.GroupInfo, department string) bool {
	return department != "" && slices.Contains(g.Departments, department)
}

// matchesTerm reports whether any term is a case insensitive substring of
// the group's name or description.
func matchesTerm(g ldap.GroupInfo, terms []string) bool {
	name, desc := strings.ToLower(g.Name), strings.ToLower(g.Description)
	return slices.ContainsFunc(terms, func(t string) bool {
		t = strings.ToLower(t)
		return strings.Contains(name, t) || strings.Contains(desc, t)
	})
}

// groupOwner reads the group's owner in AD (spec 0013, AC-6): managedBy is
// set and names an enabled user with a mail under TOOL_USER_OUS. ok is false
// otherwise; the Slack check is agent code's, before the card is posted.
func groupOwner(ctx context.Context, d Deps, g ldap.GroupInfo) (ldap.User, bool, error) {
	if g.ManagedBy == "" {
		return ldap.User{}, false, nil
	}
	u, found, err := d.Groups.GetUser(ctx, g.ManagedBy)
	if err != nil {
		return ldap.User{}, false, directoryFailure(err)
	}
	return u, found && validOwner(d, u), nil
}

// groupsOwned is groupOwner's ok for many groups, keyed by group DN, with
// every owner read in one session.
func groupsOwned(ctx context.Context, d Deps, groups []ldap.GroupInfo) (map[string]bool, error) {
	var dns []string
	for _, g := range groups {
		if g.ManagedBy != "" && !slices.Contains(dns, g.ManagedBy) {
			dns = append(dns, g.ManagedBy)
		}
	}
	users, err := d.Groups.GetUsers(ctx, dns)
	if err != nil {
		return nil, directoryFailure(err)
	}
	owned := make(map[string]bool, len(groups))
	for _, g := range groups {
		u, found := users[g.ManagedBy]
		owned[g.DN] = g.ManagedBy != "" && found && validOwner(d, u)
	}
	return owned, nil
}

// validOwner is the AC-6 check on a found owner: enabled, with a mail and an
// account name, under TOOL_USER_OUS.
func validOwner(d Deps, u ldap.User) bool {
	if !u.Enabled || u.Mail == "" || u.Username == "" {
		return false
	}
	under, err := ldap.UnderAny(u.DN, d.Scope.UserOUs, d.Scope.BaseDN)
	return err == nil && under // a DN that does not parse is outside every OU
}

// resolution is what the directory says about a requested group.
type resolution struct {
	refusal *refusal
	group   ldap.GroupInfo // set once the name resolved to a requestable group
	owner   ldap.User
}

// resolveGroup applies the directory steps of the AC-5 order: a denied name,
// a group the requester cannot request (one code for every reason), the
// fence, a membership they already have, and the owner checks.
func resolveGroup(ctx context.Context, d GroupDeps, in Input) (resolution, error) {
	name := strings.TrimSpace(in.Args["group"])
	if d.Fence.DeniedName(name) {
		return resolution{refusal: &refuseProtected}, nil
	}
	g, ok, err := d.Groups.GroupByName(ctx, name)
	if errors.Is(err, ldap.ErrTooManyMatches) {
		return resolution{refusal: &refuseNotRequestable}, nil
	}
	if err != nil {
		return resolution{}, directoryFailure(err)
	}
	if !ok || !requestable(d.Deps, g, in.Account.Department) {
		return resolution{refusal: &refuseNotRequestable}, nil
	}
	// From here on the group is AD's own spelling.
	res := resolution{group: g}
	if err := d.Fence.CheckGroup(ctx, g.Name); errors.Is(err, ErrGroupProtected) {
		res.refusal = &refuseProtected
		return res, nil
	} else if err != nil {
		return resolution{}, err
	}
	member, err := d.Directory.UserInChainMemberOf(ctx, in.Account.DN, []string{g.DN})
	if err != nil {
		return resolution{}, directoryFailure(err)
	}
	if member {
		res.refusal = &refuseAlreadyMember
		return res, nil
	}
	owner, owned, err := groupOwner(ctx, d.Deps, g)
	switch {
	case err != nil:
		return resolution{}, err
	case !owned:
		res.refusal = &refuseNoOwner
	case strings.EqualFold(owner.Username, in.Account.Username):
		res.refusal = &refuseOwnerIsRequester
	}
	res.owner = owner
	return res, nil
}

// requestable is the group_search filter for one group: a security group
// under TOOL_GROUP_OUS, tagged with the requester's department.
func requestable(d Deps, g ldap.GroupInfo, department string) bool {
	under, err := ldap.UnderAny(g.DN, d.Scope.GroupOUs, d.Scope.BaseDN)
	return err == nil && under && g.Security && taggedFor(g, department) && !d.Fence.DeniedName(g.Name)
}

// approvalState is what the audit log holds for this request (spec 0013,
// AC-5), read before any Zammad call.
type approvalState struct {
	pending       queries.Approval
	hasPending    bool
	threadTarget  string // this thread's pending request's group, "" when none
	pendingCount  int64
	threadPending bool
}

func readApprovalState(ctx context.Context, d GroupDeps, in Input, group string) (approvalState, error) {
	var s approvalState
	var err error
	s.pending, s.hasPending, err = d.Approvals.PendingApproval(ctx, in.Account.Username, ToolGroupAccessRequest, group)
	if err != nil {
		return s, failed(err)
	}
	s.threadTarget, s.threadPending, err = d.Approvals.ThreadPendingTarget(ctx, ToolGroupAccessRequest, in.Requester.ChannelID, in.Requester.ThreadTS)
	if err != nil {
		return s, failed(err)
	}
	if s.pendingCount, err = d.Approvals.PendingCount(ctx, in.Account.Username, ToolGroupAccessRequest); err != nil {
		return s, failed(err)
	}
	return s, nil
}

// groupPrecheck refuses in the AC-5 order, reuses this thread's pending
// request for the same group, or goes ahead with the owner. Every audit read
// comes before any Zammad call; the directory is read in between.
func groupPrecheck(ctx context.Context, d GroupDeps, in Input) (Prechecked, error) {
	refuse := func(r refusal, target string) (Prechecked, error) {
		return Prechecked{Refusal: &r, Target: target}, nil
	}
	if in.Requester.TicketID == 0 {
		return refuse(refuseNoTicket, "")
	}
	if r, err := resetEscalationState(ctx, d.History, in.Requester); err != nil || r != nil {
		return precheckRefusal(r, err)
	}
	res, err := resolveGroup(ctx, d, in)
	if err != nil {
		return Prechecked{}, err
	}
	var state approvalState
	if res.refusal == nil {
		if state, err = readApprovalState(ctx, d, in, res.group.Name); err != nil {
			return Prechecked{}, err
		}
	}
	if r, err := resetTicketCheck(ctx, d.Helpdesk, in); err != nil || r != nil {
		return precheckRefusal(r, err)
	}
	target := res.group.Name
	if res.refusal != nil {
		return refuse(*res.refusal, target)
	}
	owner := Owner{Account: res.owner.Username, DisplayName: res.owner.DisplayName}
	ownerName := cmp.Or(owner.DisplayName, owner.Account)
	data := map[string]any{"group": target, "owner_name": ownerName}
	sameThread := state.hasPending && state.pending.SlackChannelID == in.Requester.ChannelID &&
		state.pending.SlackThreadTs == in.Requester.ThreadTS
	switch {
	case sameThread:
		return Prechecked{Reuse: &state.pending, Target: target, Owner: owner, Data: data}, nil
	case state.threadPending && state.threadTarget != target:
		return refuse(refuseOnePerThread, target)
	case state.hasPending:
		return refuse(refuseGroupAlreadyPending, target)
	case d.MaxPending > 0 && state.pendingCount >= int64(d.MaxPending):
		return refuse(refuseTooManyPending, target)
	}
	reason := cleanReason(in.Args["reason"])
	return Prechecked{
		Target: target, Owner: owner, Data: data,
		Args: Args{"group": target, "reason": reason},
		Note: func(expiresAt time.Time) string {
			return "Access to " + target + " requested. Waiting for the group owner (" + ownerName +
				") until " + expiresAt.UTC().Format(noteTimeLayout) + ". Reason: " + reason + "."
		},
	}, nil
}

// cleanReason trims the requester's reason and folds every run of
// whitespace, newlines included, to one space (spec 0013, AC-4).
func cleanReason(reason string) string { return strings.Join(strings.Fields(reason), " ") }

// groupAdd runs an approved access request (spec 0013, AC-14): both kill
// switch reads and the full recheck come before the one write, the add is
// read back, and nothing is retried.
func groupAdd(ctx context.Context, d GroupDeps, in Input) (Result, error) {
	if !d.Switch.AccountChangesAllowed() {
		return refused(refuseReadOnly), nil
	}
	group, requester, r, err := groupRecheck(ctx, d, in)
	if err != nil || r != nil {
		return refusedOrFailed(r, err)
	}
	escalated, err := d.History.ThreadHas(ctx, ToolEscalate, audit.OutcomeOK, in.Requester.ChannelID, in.Requester.ThreadTS)
	if err != nil {
		return Result{}, failed(err)
	}
	// Immediately before the write (spec 0007, AC-8).
	if !d.Switch.AccountChangesAllowed() {
		return refused(refuseReadOnly), nil
	}
	already := false
	if err := d.Writer.AddMember(ctx, group.DN, requester.DN); errors.Is(err, ldap.ErrAlreadyMember) {
		already = true
	} else if err != nil {
		return Result{}, directoryFailure(err)
	}
	confirmed, err := d.Groups.GroupHasMember(ctx, group.DN, requester.DN)
	if err != nil {
		return Result{}, directoryFailure(err)
	}
	if !confirmed {
		return Result{Result: audit.Result{
			Outcome: audit.OutcomeError, Reason: reasonAddNotConfirmed,
			Data: map[string]any{"added": false, "already_member": already, "ticket_closed": false},
		}}, nil
	}

	closed := false
	if id := int(in.Requester.TicketID); id != 0 {
		note := "Added to " + group.Name + " after approval by the group owner (" + in.Approver + ")."
		// IT owns an escalated thread, and a closed or merged ticket stays as
		// it is: the note still lands, and ticket_closed false says the rest.
		if !escalated {
			closed, _ = d.Helpdesk.CloseWithNote(ctx, id, note)
		}
		if !closed {
			_ = d.Helpdesk.addInternalNote(ctx, id, note)
		}
	}
	return Result{
		Result: resultData(map[string]any{"added": true, "already_member": already, "ticket_closed": closed}),
		Text:   GroupAddedText,
	}, nil
}

// groupRecheck checks again what the request decided (spec 0013, AC-14):
// the group is still requestable for the requester, outside the fence, and
// owned by the approver, and both people are still enabled under
// TOOL_USER_OUS. Any other answer is recheck failed.
func groupRecheck(ctx context.Context, d GroupDeps, in Input) (ldap.GroupInfo, ldap.User, *refusal, error) {
	requester, ok, err := enabledUser(ctx, d, in.Account.Username)
	if err != nil || !ok {
		return ldap.GroupInfo{}, ldap.User{}, recheckRefusal(err), err
	}
	owner, ok, err := enabledUser(ctx, d, in.Approver)
	if err != nil || !ok {
		return ldap.GroupInfo{}, ldap.User{}, recheckRefusal(err), err
	}
	name := in.Args["group"]
	if d.Fence.DeniedName(name) {
		return ldap.GroupInfo{}, ldap.User{}, &refuseRecheckFailed, nil
	}
	g, ok, err := d.Groups.GroupByName(ctx, name)
	switch {
	case errors.Is(err, ldap.ErrTooManyMatches):
		return ldap.GroupInfo{}, ldap.User{}, &refuseRecheckFailed, nil
	case err != nil:
		return ldap.GroupInfo{}, ldap.User{}, nil, directoryFailure(err)
	case !ok || !requestable(d.Deps, g, requester.Department) || !sameDN(g.ManagedBy, owner.DN):
		return ldap.GroupInfo{}, ldap.User{}, &refuseRecheckFailed, nil
	}
	if err := d.Fence.CheckGroup(ctx, g.Name); errors.Is(err, ErrGroupProtected) {
		return ldap.GroupInfo{}, ldap.User{}, &refuseRecheckFailed, nil
	} else if err != nil {
		return ldap.GroupInfo{}, ldap.User{}, nil, err
	}
	return g, requester, nil, nil
}

func recheckRefusal(err error) *refusal {
	if err != nil {
		return nil
	}
	return &refuseRecheckFailed
}

// enabledUser finds one enabled account by name under TOOL_USER_OUS.
func enabledUser(ctx context.Context, d GroupDeps, account string) (ldap.User, bool, error) {
	if account == "" {
		return ldap.User{}, false, nil
	}
	u, found, err := d.Reader.FindUserByAccount(ctx, account)
	if errors.Is(err, ldap.ErrTooManyMatches) {
		return ldap.User{}, false, nil
	}
	if err != nil {
		return ldap.User{}, false, directoryFailure(err)
	}
	if !found || !u.Enabled {
		return ldap.User{}, false, nil
	}
	if under, err := ldap.UnderAny(u.DN, d.Scope.UserOUs, d.Scope.BaseDN); err != nil || !under {
		return ldap.User{}, false, nil //nolint:nilerr // a DN that does not parse is outside every OU
	}
	return u, true, nil
}

// sameDN compares two DNs parsed, case insensitively; an empty or broken
// one never matches.
func sameDN(a, b string) bool {
	pa, errA := goldap.ParseDN(a)
	pb, errB := goldap.ParseDN(b)
	return errA == nil && errB == nil && a != "" && pa.EqualFold(pb)
}

// GroupOwnerView is what the approval view shows about a group owner
// approval (spec 0013, AC-10): the approver's name and mail, read live, and
// the groups an add also gives. Mail is empty when the approver is disabled,
// outside TOOL_USER_OUS, or has none.
type GroupOwnerView struct {
	Name       string
	Email      string
	NestedInto []string
}

// GroupOwnerView reads the approver account and the group's nesting live.
func (d *Dispatcher) GroupOwnerView(ctx context.Context, a queries.Approval) (GroupOwnerView, error) {
	var v GroupOwnerView
	if len(a.AllowedApprovers) != 1 {
		return v, nil
	}
	u, found, err := d.sys.Reader.FindUserByAccount(ctx, a.AllowedApprovers[0])
	if err != nil && !errors.Is(err, ldap.ErrTooManyMatches) {
		return GroupOwnerView{}, err
	}
	if found {
		v.Name = u.DisplayName
		if under, uerr := ldap.UnderAny(u.DN, d.cfg.UserOUs, d.cfg.BaseDN); u.Enabled && uerr == nil && under {
			v.Email = u.Mail
		}
	}
	g, ok, err := d.sys.Groups.GroupByName(ctx, a.Target.String)
	if err != nil && !errors.Is(err, ldap.ErrTooManyMatches) {
		return GroupOwnerView{}, err
	}
	if ok {
		if v.NestedInto, err = d.sys.Groups.GroupNestedInto(ctx, g.DN, d.cfg.GroupOUs); err != nil {
			return GroupOwnerView{}, err
		}
	}
	return v, nil
}
