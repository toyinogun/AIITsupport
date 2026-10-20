package tools

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/toyinogun/AIITsupport/internal/audit"
	"github.com/toyinogun/AIITsupport/internal/db/queries"
	"github.com/toyinogun/AIITsupport/internal/graph"
	"github.com/toyinogun/AIITsupport/internal/ldap"
)

// MFAClearedText is the success text of an approved execution (spec 0012).
const MFAClearedText = "MFA methods cleared."

// Prechecked is what a medium tool's Precheck decided (spec 0012, AC-3 to
// AC-5): exactly one of a refusal, a pending approval in this thread to
// reuse, or the go ahead with what the response and the ticket note show.
type Prechecked struct {
	Refusal *refusal
	Reuse   *queries.Approval
	// Data is shown in the pending response; only the tool's PendingResult
	// keys reach the audit row.
	Data map[string]any
	// Note is the internal ticket note added after the approval commits.
	Note func(expiresAt time.Time) string
}

func mfaClearTool(approvers []string, timeout time.Duration) Tool {
	snapshot := slices.Clone(approvers)
	return Tool{
		Name: ToolMFAClear,
		Description: "Ask IT to clear the requester's MFA sign in methods (phone and Authenticator app) so they can register " +
			"again, for example after a new or lost phone. Needs this conversation's ticket first (call ticket_create). " +
			"Nothing changes until an IT person approves in Slack; the assistant's code tells the requester the outcome. " +
			"Never say the methods are cleared.",
		Tier:          audit.TierMedium,
		Target:        TargetRequester,
		StoreResult:   []string{"cleared_authenticator", "cleared_phone", "ticket_closed"},
		PendingResult: []string{"expires_at", "method_count", "reused"},
		Approval: func(_ context.Context, _ audit.Call, now time.Time) (audit.ApprovalRequest, error) {
			return audit.ApprovalRequest{Kind: audit.ApproverIT, Approvers: slices.Clone(snapshot), ExpiresAt: now.Add(timeout)}, nil
		},
		Precheck: mfaPrecheck,
		Handler:  MFAHandler(mfaClear),
	}
}

// mfaPrecheck refuses in the AC-3 order. Every audit read comes before any
// Zammad or Graph call, and a reuse makes no Graph call.
func mfaPrecheck(ctx context.Context, d MFADeps, in Input) (Prechecked, error) {
	refuse := func(r refusal) (Prechecked, error) { return Prechecked{Refusal: &r}, nil }
	if in.Requester.TicketID == 0 {
		return refuse(refuseNoTicket)
	}
	if r, err := resetEscalationState(ctx, d.History, in.Requester); err != nil || r != nil {
		return precheckRefusal(r, err)
	}
	// Read now, before any Zammad call; refused in its place in the order below.
	pending, ok, err := d.Approvals.PendingApproval(ctx, in.Account.Username, ToolMFAClear)
	if err != nil {
		return Prechecked{}, failed(err)
	}
	if r, err := resetTicketCheck(ctx, d.Helpdesk, in); err != nil || r != nil {
		return precheckRefusal(r, err)
	}
	if err := d.Accounts.CheckAccount(ctx, in.Account.DN); errors.Is(err, ErrGroupProtected) {
		return refuse(refuseProtected)
	} else if err != nil {
		return Prechecked{}, err
	}
	if ok {
		if pending.SlackChannelID == in.Requester.ChannelID && pending.SlackThreadTs == in.Requester.ThreadTS {
			return Prechecked{Reuse: &pending}, nil
		}
		return refuse(refuseAlreadyPending)
	}

	u, err := d.Graph.UserByUsername(ctx, in.Account.Username)
	if errors.Is(err, graph.ErrNotFound) || errors.Is(err, graph.ErrNotManaged) {
		return refuse(refuseNotInEntra)
	}
	if err != nil {
		return Prechecked{}, graphFailure(err)
	}
	methods, err := d.Graph.AuthMethods(ctx, u)
	if err != nil {
		return Prechecked{}, graphFailure(err)
	}
	n := countMethods(methods)
	switch {
	case n.other:
		return refuse(refuseOtherMethods)
	case n.authenticator+n.phone == 0:
		return refuse(refuseNothingToClear)
	}
	summary := n.summary()
	return Prechecked{
		Data: map[string]any{"methods": summary, "method_count": n.authenticator + n.phone},
		Note: func(expiresAt time.Time) string {
			return "MFA reset requested. Waiting for IT approval until " + expiresAt.UTC().Format(noteTimeLayout) +
				". Methods: " + summary + "."
		},
	}, nil
}

func precheckRefusal(r *refusal, err error) (Prechecked, error) {
	res, err := refusedOrFailed(r, err)
	if err != nil {
		return Prechecked{}, err
	}
	refusal := handlerRefusals[res.Reason]
	return Prechecked{Refusal: &refusal}, nil
}

// methodCount counts MFA methods by type; other is any MFA method that is
// neither a phone nor Microsoft Authenticator (the password is not MFA).
type methodCount struct {
	authenticator, phone int
	other                bool
}

func countMethods(methods []graph.AuthMethod) methodCount {
	var n methodCount
	for _, m := range methods {
		switch m.Type {
		case graph.MethodPassword:
		case graph.MethodAuthenticator:
			n.authenticator++
		case graph.MethodPhone:
			n.phone++
		default:
			n.other = true
		}
	}
	return n
}

// summary is the method summary: counts by type in a fixed order, no device
// name, phone type, or number (spec 0012, Fixed texts).
func (n methodCount) summary() string {
	var parts []string
	if n.authenticator > 0 {
		parts = append(parts, "Authenticator app ("+strconv.Itoa(n.authenticator)+")")
	}
	if n.phone > 0 {
		parts = append(parts, "phone ("+strconv.Itoa(n.phone)+")")
	}
	return strings.Join(parts, ", ")
}

// mfaClear runs an approved clear (spec 0012, AC-11). Nothing is deleted
// before both kill switch reads and the account recheck pass, it stops at the
// first failed delete, and it never retries.
func mfaClear(ctx context.Context, d MFADeps, in Input) (Result, error) {
	if !d.Switch.AccountChangesAllowed() {
		return refused(refuseReadOnly), nil
	}
	if r, err := mfaRecheck(ctx, d, in.Account.Username); err != nil || r != nil {
		return refusedOrFailed(r, err)
	}
	u, err := d.Graph.UserByUsername(ctx, in.Account.Username)
	if errors.Is(err, graph.ErrNotFound) || errors.Is(err, graph.ErrNotManaged) {
		return refused(refuseNotInEntra), nil
	}
	if err != nil {
		return Result{}, graphFailure(err)
	}
	methods, err := d.Graph.AuthMethods(ctx, u)
	if err != nil {
		return Result{}, graphFailure(err)
	}
	if countMethods(methods).other {
		return refused(refuseOtherMethods), nil
	}
	steps := clearOrder(ctx, d.Graph, u, methods)
	if len(steps) == 0 {
		return refused(refuseNothingToClear), nil // cleared already: nothing to report as done
	}
	// Immediately before the write (spec 0007, AC-8).
	if !d.Switch.AccountChangesAllowed() {
		return refused(refuseReadOnly), nil
	}

	clearedAuth, clearedPhone, err := deleteSteps(ctx, d.Graph, u, steps)
	if err != nil {
		// The outcome is error with the counts so far, stored like a refusal.
		return Result{Result: audit.Result{ //nolint:nilerr // a partial clear is an outcome, not a failure (spec 0012, AC-11)
			Outcome: audit.OutcomeError, Reason: reasonMethodDeleteFailed,
			Data: map[string]any{"cleared_authenticator": clearedAuth, "cleared_phone": clearedPhone, "ticket_closed": false},
		}}, nil
	}

	closed := false
	if id := int(in.Requester.TicketID); id != 0 {
		note := "MFA methods cleared after IT approval by " + in.ApproverSlackID + "."
		// Already closed or merged, or the close failed: the note still lands,
		// and ticket_closed false says the rest. The clear itself is done.
		if closed, _ = d.Helpdesk.CloseWithNote(ctx, id, note); !closed {
			_ = d.Helpdesk.addInternalNote(ctx, id, note)
		}
	}
	return Result{
		Result: resultData(map[string]any{
			"cleared_authenticator": clearedAuth, "cleared_phone": clearedPhone, "ticket_closed": closed,
		}),
		Text: MFAClearedText,
	}, nil
}

// mfaRecheck looks the account up again by name: it must still be one
// enabled account under TOOL_USER_OUS that no protected group holds.
func mfaRecheck(ctx context.Context, d MFADeps, account string) (*refusal, error) {
	u, found, err := d.Reader.FindUserByAccount(ctx, account)
	if errors.Is(err, ldap.ErrTooManyMatches) {
		return &refuseNotEligible, nil
	}
	if err != nil {
		return nil, directoryFailure(err)
	}
	if !found || !u.Enabled {
		return &refuseNotEligible, nil
	}
	if under, err := ldap.UnderAny(u.DN, d.UserOUs, d.BaseDN); err != nil || !under {
		return &refuseNotEligible, nil //nolint:nilerr // a DN that does not parse is outside every OU
	}
	if err := d.Accounts.CheckAccount(ctx, u.DN); errors.Is(err, ErrGroupProtected) {
		return &refuseProtected, nil
	} else if err != nil {
		return nil, err
	}
	return nil, nil
}

// clearStep is one method to delete: an Authenticator (phoneType empty) or
// a phone of phoneType. deferred marks an Authenticator moved to the end
// once after Graph refused it.
type clearStep struct {
	id        string
	phoneType string
	deferred  bool
}

// deleteSteps deletes in order and stops at the first failed delete, with
// one exception (spec 0012, AC-11, amended 2026-10-20): Graph refuses the
// default Authenticator registration (400) while another one remains, and
// which registration is the default cannot be read, so a refused
// Authenticator with another Authenticator still to come is moved to the end
// once. Nothing else is tried again.
func deleteSteps(ctx context.Context, g GraphAPI, u graph.ManagedUser, steps []clearStep) (auth, phone int, err error) {
	queue := slices.Clone(steps)
	for i := 0; i < len(queue); i++ {
		s := queue[i]
		if s.phoneType == "" {
			err = g.DeleteAuthenticatorMethod(ctx, u, s.id)
		} else {
			err = g.DeletePhoneMethod(ctx, u, s.id)
		}
		switch {
		case err == nil && s.phoneType == "":
			auth++
		case err == nil:
			phone++
		case canDefer(s, err, queue[i+1:]):
			s.deferred = true
			queue = append(queue, s)
		default:
			return auth, phone, err
		}
	}
	return auth, phone, nil
}

// canDefer is an Authenticator Graph refused as the default, not yet
// deferred, with another Authenticator still in rest.
func canDefer(s clearStep, err error, rest []clearStep) bool {
	return s.phoneType == "" && !s.deferred && graph.Status(err) == http.StatusBadRequest &&
		slices.ContainsFunc(rest, func(r clearStep) bool { return r.phoneType == "" })
}

// fixedClearOrder is the order used when the default method is unknown:
// alternateMobile, office, mobile, then the Authenticator. Graph refuses to
// delete a user's default method while others remain ("Cannot delete default
// method with other methods configured"), and the build step 1 probe showed
// the Authenticator is the default once registered, while signInPreferences
// answers 400 to the tool server app (journal 0041; spec 0012 AC-11 is
// amended to this order).
var fixedClearOrder = []string{graph.PhoneAlternateMobile, graph.PhoneOffice, graph.PhoneMobile, ""}

// clearOrder lists the Authenticator and phone methods non default first and
// the user's default sign in method last. The default comes from
// signInPreferences when the app can read it, else the fixed order stands.
func clearOrder(ctx context.Context, g GraphAPI, u graph.ManagedUser, methods []graph.AuthMethod) []clearStep {
	var steps []clearStep
	for _, kind := range fixedClearOrder {
		for _, m := range methods {
			switch {
			case kind == "" && m.Type == graph.MethodAuthenticator:
				steps = append(steps, clearStep{id: m.ID})
			case kind != "" && m.Type == graph.MethodPhone && m.PhoneType == kind:
				steps = append(steps, clearStep{id: m.ID, phoneType: kind})
			}
		}
	}
	// A phone of a type Graph adds later still gets cleared, before the default.
	for _, m := range methods {
		if m.Type == graph.MethodPhone && !slices.Contains(fixedClearOrder, m.PhoneType) {
			steps = append([]clearStep{{id: m.ID, phoneType: m.PhoneType}}, steps...)
		}
	}
	pref, err := g.SignInPreference(ctx, u)
	if err != nil {
		return steps
	}
	def, ok := preferredKind[pref]
	if !ok {
		return steps
	}
	isDefault := func(s clearStep) bool { return s.phoneType == def }
	return append(slices.DeleteFunc(slices.Clone(steps), isDefault), slices.DeleteFunc(slices.Clone(steps), func(s clearStep) bool { return !isDefault(s) })...)
}

// preferredKind maps a signInPreferences value to the method it names: ""
// for Microsoft Authenticator, else the phone type.
var preferredKind = map[string]string{
	"push": "", "oath": "",
	"sms": graph.PhoneMobile, "voiceMobile": graph.PhoneMobile,
	"voiceAlternateMobile": graph.PhoneAlternateMobile, "voiceOffice": graph.PhoneOffice,
}

// graphFailure classifies a Graph error: any one is "entra unavailable"
// (spec 0012, AC-3).
func graphFailure(err error) error {
	return &failure{code: CodeUnavailable, reason: reasonEntraDown, sentence: entraDownSentence, err: err}
}
