// Package tools is the tool server's tool layer (spec 0007): the registry of
// tools with their risk tiers, and the one dispatcher every call passes
// through. The dispatcher checks, in a fixed order, whether the tool is
// blocked, whether the arguments are valid, who is asking, whether account
// changes are paused, and whether the requester hit a limit. Only then does
// the handler run, inside audit.Recorder, so every call leaves an audit pair.
//
// Handlers are typed by what they may touch. An account change handler
// receives AccountDeps, the only holder of the directory writer; every other
// handler receives Deps. The dispatcher puts every AccountDeps handler behind
// the kill switch and the rate limit, so neither can be skipped by
// forgetting a flag.
package tools

import (
	"context"
	"log/slog"
	"time"

	"github.com/toyinogun/AIITsupport/internal/audit"
	"github.com/toyinogun/AIITsupport/internal/db/queries"
	"github.com/toyinogun/AIITsupport/internal/graph"
	"github.com/toyinogun/AIITsupport/internal/ldap"
	"github.com/toyinogun/AIITsupport/internal/resetlink"
	"github.com/toyinogun/AIITsupport/internal/zammad"
)

// Requester is who is asking and where, from MCP _meta set by agent code
// (never the model). internal/mcpserver checks each value's format first.
type Requester struct {
	SlackID   string
	Email     string
	ChannelID string
	ThreadTS  string
	TicketID  int64 // 0 when the thread has no ticket yet
	// Initiator is InitiatorCode when agent code, not the model, made the
	// call (spec 0009); empty otherwise.
	Initiator string
}

// InitiatorCode is the one accepted value of the _meta initiator key: the
// call came from agent code outside the model loop (spec 0009, AC-2).
const InitiatorCode = "code"

// Request is one tools/call.
type Request struct {
	Tool      string
	Args      []byte // the raw JSON arguments, validated by the dispatcher
	Requester Requester
}

// Args are a call's arguments after validation: every value is a string.
type Args map[string]string

// Input is what a handler gets: the validated call and the matched requester.
type Input struct {
	Requester Requester
	Account   ldap.User // the requester's directory account
	Args      Args
	Lists     Lists // the list arguments (group_search's terms)
	Now       time.Time
	// ApproverSlackID is the Slack id of who approved the call, and Approver
	// the approval's decider (that Slack id for an IT approval, the owner's
	// AD account for a group owner approval); set only for an approved
	// execution (Dispatcher.Execute).
	ApproverSlackID string
	Approver        string
}

// Result is what a handler returns. Outcome empty means ok; a handler refuses
// with refused(...). Text is the one line the model reads on success.
type Result struct {
	audit.Result
	Text string
	// Hidden are Data keys the audit row may keep (through StoreResult) that
	// the model never sees (kb_search's answer_ids and stale_ids, spec 0014).
	Hidden []string
}

// Handler is a tool's code: a ReadHandler, an AccountHandler, a ResetHandler,
// an MFAHandler, or a GroupHandler.
type Handler interface{ handler() }

// ReadHandler may read the directory and use the helpdesk; it cannot write
// to the directory.
type ReadHandler func(ctx context.Context, d Deps, in Input) (Result, error)

// AccountHandler changes a directory account. Only it receives the writer,
// and the dispatcher always runs it behind the kill switch and rate limit.
type AccountHandler func(ctx context.Context, d AccountDeps, in Input) (Result, error)

// ResetHandler issues a password reset link (spec 0011). It writes no
// directory account itself, but the dispatcher treats it as an account
// change: the kill switch and TOOL_WRITE_LIMIT apply.
type ResetHandler func(ctx context.Context, d ResetDeps, in Input) (Result, error)

// MFAHandler clears MFA methods after an IT approval (spec 0012). It runs
// only from Dispatcher.Execute, never from a tools/call, and the dispatcher
// treats it as an account change: the kill switch and TOOL_WRITE_LIMIT apply
// to the request.
type MFAHandler func(ctx context.Context, d MFADeps, in Input) (Result, error)

// GroupHandler adds the requester to a group after the group owner approved
// (spec 0013). Like an MFAHandler it runs only from Dispatcher.Execute, and
// the dispatcher treats it as an account change: the kill switch and
// TOOL_WRITE_LIMIT apply to the request.
type GroupHandler func(ctx context.Context, d GroupDeps, in Input) (Result, error)

func (ReadHandler) handler()    {}
func (AccountHandler) handler() {}
func (ResetHandler) handler()   {}
func (MFAHandler) handler()     {}
func (GroupHandler) handler()   {}

// Directory is what every handler may read from AD. *ldap.Client satisfies it.
type Directory interface {
	FindUsersByMail(ctx context.Context, mail string) ([]ldap.User, error)
	Lock(ctx context.Context, dn string) (ldap.Lock, error)
	GroupsByName(ctx context.Context, names []string) (map[string]ldap.Group, error)
	InChainMemberOf(ctx context.Context, name string, parents []string) (bool, error)
	UserInChainMemberOf(ctx context.Context, dn string, parents []string) (bool, error)
	// GroupsNestedUnder names the groups under the OUs nested at any depth
	// into any of parents (DNs), in one read (spec 0013, AC-2).
	GroupsNestedUnder(ctx context.Context, parents, ous []string) ([]string, error)
}

// DirectoryWriter is the directory's writes as svc-itsupport: the unlock and
// the group add (spec 0013). *ldap.Writer satisfies it.
type DirectoryWriter interface {
	ClearLockout(ctx context.Context, dn string) error
	// AddMember is ldap.ErrAlreadyMember when the user is in the group.
	AddMember(ctx context.Context, groupDN, userDN string) error
}

// GroupReader is the directory's group reads for the access request (spec
// 0013). *ldap.Client satisfies it. No user text reaches an LDAP filter
// except an exact name through GroupByName, escaped.
type GroupReader interface {
	ListGroups(ctx context.Context, ous []string) ([]ldap.GroupInfo, error)
	GroupByName(ctx context.Context, name string) (ldap.GroupInfo, bool, error)
	GroupNestedInto(ctx context.Context, groupDN string, ous []string) ([]string, error)
	GroupHasMember(ctx context.Context, groupDN, userDN string) (bool, error)
	GetUser(ctx context.Context, dn string) (ldap.User, bool, error)
	// GetUsers reads many users by DN in one session, keyed by the DN given.
	GetUsers(ctx context.Context, dns []string) (map[string]ldap.User, error)
	// UserGroups names the security groups under the OUs that hold the user
	// at any depth, in one read.
	UserGroups(ctx context.Context, userDN string, ous []string) ([]string, error)
}

// HelpdeskAPI is the part of the Zammad client the tools use. *zammad.Client
// satisfies it; it can never write a Zammad user (spec 0006).
type HelpdeskAPI interface {
	UsersByLogin(ctx context.Context, login string) ([]zammad.User, error)
	TicketStates(ctx context.Context) ([]zammad.TicketState, error)
	CreateTicket(ctx context.Context, t zammad.NewTicket, from string) (zammad.Ticket, error)
	GetTicket(ctx context.Context, id int) (zammad.Ticket, error)
	UpdateTicket(ctx context.Context, id int, u zammad.TicketUpdate, from string) (zammad.Ticket, error)
	AddTag(ctx context.Context, ticketID int, tag string) error
	// The knowledge base reads (spec 0014).
	KBInit(ctx context.Context) (zammad.KBMeta, error)
	SearchKB(ctx context.Context, kbID int, locale, query string, limit int) ([]zammad.KBHit, error)
	GetAnswer(ctx context.Context, kbID, answerID int, contentIDs ...int) (zammad.Answer, error)
}

// History is what a handler may read back from the audit log.
// *audit.Recorder satisfies it.
type History interface {
	ThreadHasOK(ctx context.Context, tool, channelID, threadTS string) (bool, error)
	ThreadHas(ctx context.Context, tool string, outcome audit.Outcome, channelID, threadTS string) (bool, error)
	CountRecent(ctx context.Context, tool, requesterSlackID string, outcome audit.Outcome, since time.Time) (int64, error)
	// CountRecentReason also matches the stored reason (spec 0013, AC-5).
	CountRecentReason(ctx context.Context, tool, requesterSlackID string, outcome audit.Outcome, reason string, since time.Time) (int64, error)
	ThreadTicketID(ctx context.Context, channelID, threadTS string) (int64, error)
	// ThreadOpenApprovalTool is the tool of a pending or approved medium
	// approval in the thread, or "": the close guard (spec 0012 AC-24, spec
	// 0013 AC-18).
	ThreadOpenApprovalTool(ctx context.Context, channelID, threadTS string) (string, error)
}

// KillSwitch says whether account changes may run right now.
type KillSwitch interface {
	AccountChangesAllowed() bool
}

// Deps is what a non account change handler may touch.
type Deps struct {
	Directory Directory
	Groups    GroupReader
	Helpdesk  *Helpdesk
	History   History
	Fence     *Fence
	Limits    Limits
	Scope     Scope
	KB        KBSettings
	// Logger is for a handler's own fixed lines (kb_review_tag_failed); the
	// dispatcher logs every call itself.
	Logger *slog.Logger
}

// KBSettings are kb_search's settings (spec 0014, AC-4 and AC-16).
type KBSettings struct {
	SearchLimit int // KB_SEARCH_LIMIT
	// SearchCandidates is KB_SEARCH_CANDIDATES, how many of Zammad's hits
	// one search reads and ranks (at least SearchLimit).
	SearchCandidates int
	StaleAfterDays   int // KB_STALE_AFTER_DAYS
	BodyMaxRunes     int // KB_BODY_MAX_RUNES
	// StaleAsOf is KB_STALE_AS_OF, the fixed "today" for the stale flag;
	// zero means the dispatcher's clock.
	StaleAsOf time.Time
	// Zone is Europe/Amsterdam, where an updated date and today are read.
	// cmd/toolserver refuses to start without it, never falling back to UTC.
	Zone *time.Location
}

// Scope is where the tool layer may look in the directory (TOOL_USER_OUS
// and TOOL_GROUP_OUS, relative to LDAP_BASE_DN).
type Scope struct {
	BaseDN   string
	UserOUs  []string
	GroupOUs []string
}

// AccountDeps is what an account change handler may touch: Deps plus the
// directory writer and the kill switch, read again just before the write.
type AccountDeps struct {
	Deps
	Writer DirectoryWriter
	Switch KillSwitch
}

// ResetLinks is the reset_links store. *resetlink.Store satisfies it.
type ResetLinks interface {
	Issue(ctx context.Context, in resetlink.Issue) (resetlink.Issued, error)
	CountRecent(ctx context.Context, account string, window time.Duration) (int64, error)
}

// ResetSettings are the RESET_* values the issue handler uses.
type ResetSettings struct {
	PublicURL string        // RESET_PUBLIC_URL, no trailing slash
	TTL       time.Duration // RESET_LINK_TTL
	LinkLimit int           // RESET_LINK_LIMIT
	Window    time.Duration // RESET_LINK_WINDOW
}

// ResetDeps is what the reset link handler may touch: Deps plus the link
// store, its settings, and the account fence. It holds no directory writer.
type ResetDeps struct {
	Deps
	Links    ResetLinks
	Settings ResetSettings
	Accounts *AccountFence
}

// AccountReader is the directory plus a lookup by account name, for an
// execution that has no email to identify by. *ldap.Client satisfies it.
type AccountReader interface {
	Directory
	FindUserByAccount(ctx context.Context, name string) (ldap.User, bool, error)
}

// GraphAPI is the part of the Graph client the MFA tool uses. *graph.Client
// satisfies it; every call after UserByUsername takes a fenced ManagedUser.
type GraphAPI interface {
	UserByUsername(ctx context.Context, username string) (graph.ManagedUser, error)
	AuthMethods(ctx context.Context, u graph.ManagedUser) ([]graph.AuthMethod, error)
	SignInPreference(ctx context.Context, u graph.ManagedUser) (string, error)
	DeleteAuthenticatorMethod(ctx context.Context, u graph.ManagedUser, methodID string) error
	DeletePhoneMethod(ctx context.Context, u graph.ManagedUser, methodID string) error
}

// ApprovalReads are the approval reads a medium tool's precheck makes.
// *audit.Recorder satisfies it.
type ApprovalReads interface {
	// PendingApproval's target is empty for a tool without one.
	PendingApproval(ctx context.Context, account, tool, target string) (queries.Approval, bool, error)
	PendingCount(ctx context.Context, account, tool string) (int64, error)
	ThreadPendingTarget(ctx context.Context, tool, channelID, threadTS string) (string, bool, error)
}

// MFADeps is what the MFA tool may touch (spec 0012, AC-2): Deps plus Graph,
// the account reader, the approval reads, the MFA fence, and the kill switch
// it reads twice before a clear. It holds no directory writer.
type MFADeps struct {
	Deps
	Graph     GraphAPI
	Reader    AccountReader
	Approvals ApprovalReads
	Accounts  *AccountFence // the code floor plus MFA_DENIED_GROUPS
	Switch    KillSwitch
	UserOUs   []string // TOOL_USER_OUS, for the execution's account recheck
	BaseDN    string
}

// GroupDeps is what the access request may touch (spec 0013, AC-4): Deps
// plus the account reader, the directory writer with AddMember, the approval
// reads, the kill switch it reads twice before an add, and ACCESS_MAX_PENDING.
type GroupDeps struct {
	Deps
	Reader     AccountReader
	Writer     DirectoryWriter
	Approvals  ApprovalReads
	Switch     KillSwitch
	MaxPending int // ACCESS_MAX_PENDING
}

// Limits are the settings a handler checks.
type Limits struct {
	TicketMaxPending time.Duration // TICKET_MAX_PENDING
	PrivilegedWindow time.Duration // PRIVILEGED_ATTEMPT_WINDOW
}
