package tools

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/toyinogun/AIITsupport/internal/audit"
)

// Target says what a call acts on, for the audit row's target column.
type Target int

// Targets.
const (
	TargetNone      Target = iota // a blocked tool
	TargetRequester               // the requester's own account
	TargetTicket                  // the ticket named in _meta
	// TargetGroup is the group's canonical name, set only once the precheck
	// resolved it (spec 0013, AC-4); a refusal before that has no target.
	TargetGroup
)

// Tool is one registry entry: everything the dispatcher needs to run a call.
type Tool struct {
	Name        string
	Description string // what the model reads in tools/list
	Tier        audit.Tier
	Fields      []Field
	// Check runs after the field checks; it returns the failing field's name.
	Check func(a Args, env CheckEnv) (field string, ok bool)
	// Store and StoreResult are the argument and result keys the audit row keeps.
	Store       []string
	StoreResult []string
	Target      Target
	// TicketLimited puts the tool under TICKET_CREATE_LIMIT. Every
	// AccountHandler tool is under TOOL_WRITE_LIMIT whatever this says.
	TicketLimited bool
	// Approval builds the approval request for a medium tool from what its
	// precheck found; required for tier medium. now is the dispatcher's clock.
	Approval func(ctx context.Context, c audit.Call, pre Prechecked, now time.Time) (audit.ApprovalRequest, error)
	// Precheck runs before a medium tool is deferred (spec 0012, AC-3): it
	// refuses, reuses a pending approval, or says what the approval card and
	// the pending result show. A medium tool has exactly one: Precheck with
	// an MFAHandler, or GroupPrecheck with a GroupHandler (spec 0013, AC-4).
	Precheck      func(ctx context.Context, d MFADeps, in Input) (Prechecked, error)
	GroupPrecheck func(ctx context.Context, d GroupDeps, in Input) (Prechecked, error)
	// PendingResult lists the result keys the pending_approval outcome keeps.
	PendingResult []string
	// Handler is nil exactly for a blocked tool, which never runs.
	Handler Handler
}

// accountChange reports whether the tool changes a directory account: exactly
// when its handler takes AccountDeps, issues a reset link (ResetDeps), or
// runs after an approval (MFADeps, GroupDeps).
func (t *Tool) accountChange() bool {
	switch t.Handler.(type) {
	case AccountHandler, ResetHandler, MFAHandler, GroupHandler:
		return true
	}
	return false
}

// Registry is the fixed set of tools the tool server serves.
type Registry struct {
	tools []Tool
}

// RegistryOption adds an optional tool to the production registry.
type RegistryOption func(*[]Tool)

// WithPasswordReset registers password_reset_link (spec 0011), only when the
// RESET_* settings are set.
func WithPasswordReset() RegistryOption {
	return func(tools *[]Tool) { *tools = append(*tools, passwordResetLinkTool()) }
}

// WithMFAClear registers mfa_clear (spec 0012), only when the GRAPH_*
// settings are complete and SLACK_IT_APPROVERS is set. approvers is the
// snapshot each approval gets; timeout is APPROVAL_TIMEOUT.
func WithMFAClear(approvers []string, timeout time.Duration) RegistryOption {
	return func(tools *[]Tool) { *tools = append(*tools, mfaClearTool(approvers, timeout)) }
}

// NewRegistry builds the production registry. accessTimeout is
// ACCESS_APPROVAL_TIMEOUT: group_search and group_access_request need only
// LDAP, Zammad, and Postgres, so they are always registered (spec 0013, AC-4).
func NewRegistry(accessTimeout time.Duration, opts ...RegistryOption) (*Registry, error) {
	if accessTimeout <= 0 {
		return nil, errors.New("build tool registry: the access approval timeout must be positive")
	}
	tools := append(productionTools(), groupSearchTool(), groupAccessRequestTool(accessTimeout))
	for _, opt := range opts {
		opt(&tools)
	}
	return newRegistry(tools)
}

func newRegistry(tools []Tool) (*Registry, error) {
	var errs []error
	seen := map[string]bool{}
	for i := range tools {
		t := &tools[i]
		if seen[t.Name] {
			errs = append(errs, fmt.Errorf("tool %q is registered twice", t.Name))
		}
		seen[t.Name] = true
		errs = append(errs, t.validate()...)
	}
	if err := errors.Join(errs...); err != nil {
		return nil, fmt.Errorf("build tool registry: %w", err)
	}
	return &Registry{tools: slices.Clone(tools)}, nil
}

func (t *Tool) validate() []error {
	var errs []error
	bad := func(format string, a ...any) {
		errs = append(errs, fmt.Errorf("tool %q: "+format, append([]any{t.Name}, a...)...))
	}
	if t.Name == "" || t.Description == "" {
		bad("needs a name and a description")
	}
	switch t.Tier {
	case audit.TierBlocked:
		if t.Handler != nil {
			bad("a blocked tool must have no handler")
		}
	case audit.TierRead, audit.TierLow, audit.TierMedium:
		if t.Handler == nil {
			bad("needs a handler")
		}
	default:
		bad("unknown tier %q", t.Tier)
	}
	_, mfa := t.Handler.(MFAHandler)
	_, group := t.Handler.(GroupHandler)
	if t.Tier == audit.TierMedium {
		if t.Approval == nil {
			bad("a medium tool needs an Approval hook")
		}
		switch {
		case mfa && t.Precheck != nil && t.GroupPrecheck == nil:
		case group && t.GroupPrecheck != nil && t.Precheck == nil:
		default:
			bad("a medium tool needs a Precheck with an MFAHandler, or a GroupPrecheck with a GroupHandler")
		}
	}
	if (mfa || group) && t.Tier != audit.TierMedium {
		bad("an MFAHandler or GroupHandler runs only behind an approval (tier medium)")
	}
	if t.Tier != audit.TierMedium && (t.Precheck != nil || t.GroupPrecheck != nil || t.Approval != nil) {
		bad("only a medium tool has a precheck or an Approval hook")
	}
	for _, key := range t.Store {
		i := slices.IndexFunc(t.Fields, func(f Field) bool { return f.Name == key })
		switch {
		case i < 0:
			bad("stores %q, which is not one of its fields", key)
		case t.Fields[i].FreeText:
			bad("stores the free text field %q", key)
		}
	}
	return errs
}

// Tools lists the registry in order.
func (r *Registry) Tools() []Tool { return slices.Clone(r.tools) }

// Lookup finds a tool by name.
func (r *Registry) Lookup(name string) (*Tool, bool) {
	for i := range r.tools {
		if r.tools[i].Name == name {
			return &r.tools[i], true
		}
	}
	return nil, false
}

// Tool names.
const (
	ToolUserLookup             = "user_lookup"
	ToolAccountUnlock          = "account_unlock"
	ToolTicketCreate           = "ticket_create"
	ToolTicketUpdate           = "ticket_update"
	ToolPrivilegedAccessChange = "privileged_access_change"
	ToolEscalate               = "escalate"
	ToolPasswordResetLink      = "password_reset_link"
	ToolMFAClear               = "mfa_clear"
	ToolGroupSearch            = "group_search"
	ToolGroupAccessRequest     = "group_access_request"
)

// Argument bounds and values, part of the schema the model sees.
const (
	maxTitleRunes   = 200
	maxBodyRunes    = 10000
	maxNoteRunes    = 10000
	maxSummaryRunes = 1000

	maxEscalationSummaryRunes = 10000

	stateOpen    = "open"
	statePending = "pending"
	stateClosed  = "closed"
)

func productionTools() []Tool {
	return []Tool{
		{
			Name: ToolUserLookup,
			Description: "Look up the requester's own Noord account: whether it is enabled, whether it is locked, " +
				"and when the lock ends. Takes no arguments and can only return the requester's own account.",
			Tier:    audit.TierRead,
			Target:  TargetRequester,
			Handler: ReadHandler(userLookup),
		},
		{
			Name: ToolAccountUnlock,
			Description: "Unlock the requester's own Noord account if it is locked. Takes no arguments and only ever " +
				"acts on the requester's own account; it cannot unlock anyone else. Reports whether the account was locked.",
			Tier:        audit.TierLow,
			Target:      TargetRequester,
			StoreResult: []string{"was_locked", "locked"},
			Handler:     AccountHandler(accountUnlock),
		},
		{
			Name: ToolTicketCreate,
			Description: "Open a helpdesk ticket for the requester for this conversation, with a short title and a " +
				"plain text description of the problem and what was done. One ticket per conversation; use ticket_update after that.",
			Tier: audit.TierLow,
			Fields: []Field{
				{Name: "title", Description: "One line summary of the problem.", Required: true, MinRunes: 1, MaxRunes: maxTitleRunes, NoNewline: true, FreeText: true},
				{Name: "body", Description: "Plain text: the problem and what was done.", Required: true, MinRunes: 1, MaxRunes: maxBodyRunes, FreeText: true},
			},
			Target:        TargetRequester,
			TicketLimited: true,
			StoreResult:   []string{"ticket_id", "ticket_number"},
			Handler:       ReadHandler(ticketCreate),
		},
		{
			Name: ToolTicketUpdate,
			Description: "Update this conversation's ticket: add an internal note, set the state (open, pending with " +
				"pending_until, or closed), or both. Only this conversation's ticket can be changed.",
			Tier: audit.TierLow,
			Fields: []Field{
				{Name: "state", Description: "open, pending, or closed.", MinRunes: 1, Enum: []string{stateOpen, statePending, stateClosed}},
				{Name: "pending_until", Description: "RFC 3339 time; required exactly when state is pending.", MinRunes: 1, MaxRunes: 64, NoNewline: true},
				{Name: "note", Description: "An internal note for IT.", MinRunes: 1, MaxRunes: maxNoteRunes, FreeText: true},
			},
			Check:       checkTicketUpdate,
			Target:      TargetTicket,
			Store:       []string{"state", "pending_until"},
			StoreResult: []string{"ticket_id", "state"},
			Handler:     ReadHandler(ticketUpdate),
		},
		{
			Name: ToolPrivilegedAccessChange,
			Description: "Use for any request for admin rights, privileged group membership, or changes to admin " +
				"accounts. It is always refused; tell the user, and escalate to IT.",
			Tier: audit.TierBlocked,
			Fields: []Field{
				{Name: "summary", Description: "What was asked for.", Required: true, MinRunes: 1, MaxRunes: maxSummaryRunes, FreeText: true},
			},
			Target: TargetNone,
		},
		{
			Name: ToolEscalate,
			Description: "Hand this conversation to IT with a summary: for a refused privileged request, a request to act " +
				"for someone else, or a security concern. Tags this conversation's ticket for IT, or opens one if there is " +
				"none yet, and notifies IT. Use it once, after privileged_access_change when that applies. Ends the conversation.",
			Tier: audit.TierLow,
			Fields: []Field{
				{
					Name: "reason", Required: true, MinRunes: 1, Enum: escalationReasons,
					Description: "Why IT should look at this. Use privileged_request, other_person, security_concern, " +
						"manual_action (a fix the assistant cannot make itself), or no_group_owner (an access request no group owner " +
						"can decide); the other values are reserved for the assistant's own code.",
				},
				{
					Name: "title", Required: true, MinRunes: 1, MaxRunes: maxTitleRunes, NoNewline: true, FreeText: true,
					Description: "A short title naming the user's underlying problem, or the request when there is none. " +
						"Used only when this conversation has no ticket yet.",
				},
				{
					Name: "summary", Required: true, MinRunes: 1, MaxRunes: maxEscalationSummaryRunes, FreeText: true,
					Description: "What the user asked, what was refused or could not be done, and what IT should look at. Plain text.",
				},
			},
			Check:  checkEscalate,
			Target: TargetTicket,
			Store:  []string{"reason"},
			StoreResult: []string{
				"ticket_id", "ticket_number", "reason", "initiator", "privileged", "created",
				"privileged_attempts", "privileged_window_hours",
			},
			Handler: ReadHandler(escalate),
		},
	}
}
