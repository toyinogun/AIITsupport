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
	"time"

	"github.com/toyinogun/AIITsupport/internal/audit"
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
	Now       time.Time
}

// Result is what a handler returns. Outcome empty means ok; a handler refuses
// with refused(...). Text is the one line the model reads on success.
type Result struct {
	audit.Result
	Text string
}

// Handler is a tool's code: a ReadHandler, an AccountHandler, or a ResetHandler.
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

func (ReadHandler) handler()    {}
func (AccountHandler) handler() {}
func (ResetHandler) handler()   {}

// Directory is what every handler may read from AD. *ldap.Client satisfies it.
type Directory interface {
	FindUsersByMail(ctx context.Context, mail string) ([]ldap.User, error)
	Lock(ctx context.Context, dn string) (ldap.Lock, error)
	GroupsByName(ctx context.Context, names []string) (map[string]ldap.Group, error)
	InChainMemberOf(ctx context.Context, name string, parents []string) (bool, error)
	UserInChainMemberOf(ctx context.Context, dn string, parents []string) (bool, error)
}

// DirectoryWriter is the directory's only write. *ldap.Writer satisfies it.
type DirectoryWriter interface {
	ClearLockout(ctx context.Context, dn string) error
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
}

// History is what a handler may read back from the audit log.
// *audit.Recorder satisfies it.
type History interface {
	ThreadHasOK(ctx context.Context, tool, channelID, threadTS string) (bool, error)
	ThreadHas(ctx context.Context, tool string, outcome audit.Outcome, channelID, threadTS string) (bool, error)
	CountRecent(ctx context.Context, tool, requesterSlackID string, outcome audit.Outcome, since time.Time) (int64, error)
	ThreadTicketID(ctx context.Context, channelID, threadTS string) (int64, error)
}

// KillSwitch says whether account changes may run right now.
type KillSwitch interface {
	AccountChangesAllowed() bool
}

// Deps is what a non account change handler may touch.
type Deps struct {
	Directory Directory
	Helpdesk  *Helpdesk
	History   History
	Fence     *Fence
	Limits    Limits
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

// Limits are the settings a handler checks.
type Limits struct {
	TicketMaxPending time.Duration // TICKET_MAX_PENDING
	PrivilegedWindow time.Duration // PRIVILEGED_ATTEMPT_WINDOW
}
