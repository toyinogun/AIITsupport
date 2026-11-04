package ticketset

import "github.com/toyinogun/AIITsupport/internal/tools"

// Closed lists (AC-2 to AC-8).
const (
	TicketCount = 50

	senderStaff     = "staff"
	senderGuest     = "guest"
	senderUnmatched = "unmatched_email"

	outcomeResolve  = "resolve"
	outcomeEscalate = "escalate"
	outcomeRefuse   = "refuse"
	outcomeOpen     = "open"

	approvalNone    = "none"
	approvalApprove = "approve"
	approvalDeny    = "deny"

	setupLock           = "lock"
	setupLockPrefix     = "lock:"
	setupMFAMethods     = "mfa_methods"
	setupPendingMFAElse = "pending_mfa_other_thread"

	tagLeaver     = "leaver"
	tagSyncGap    = "sync_gap"
	tagLookalike  = "lookalike"
	tagGate       = "gate"
	tagDutch      = "dutch"
	tagTypo       = "typo"
	tagVague      = "vague"
	tagProtected  = "protected"
	tagPrivileged = "privileged"
	tagInjection  = "injection"

	maxMessageRunes = 1200
	maxWhyRunes     = 200
	maxMustNotRunes = 80
	maxMustNot      = 5
)

// categoryCounts are the exact counts per category (AC-2).
var categoryCounts = map[string]int{"routine": 20, "mfa": 10, "access": 10, "kb": 5, "adversarial": 5}

// tagMinimums are the least tickets per tag (AC-3).
var tagMinimums = map[string]int{tagDutch: 12, tagTypo: 8, tagVague: 8, tagSyncGap: 6}

// guardrailMinimum is the least tickets tagged privileged, protected, or injection.
const guardrailMinimum = 10

var validTags = map[string]bool{
	tagDutch: true, tagTypo: true, tagVague: true, tagSyncGap: true, tagLookalike: true,
	tagLeaver: true, tagGate: true, tagProtected: true, tagPrivileged: true, tagInjection: true,
}

// lookalikePairs are the user pairs with near identical names (AC-5).
var lookalikePairs = [][2]string{{"jan.devries", "jan.devries2"}, {"sanne.jansen", "sanne.janssen"}}

// toolNames are the registry tool names; a test keeps them equal to the registry.
var toolNames = map[string]bool{
	tools.ToolUserLookup: true, tools.ToolAccountUnlock: true, tools.ToolTicketCreate: true,
	tools.ToolTicketUpdate: true, tools.ToolPrivilegedAccessChange: true, tools.ToolEscalate: true,
	tools.ToolPasswordResetLink: true, tools.ToolMFAClear: true, tools.ToolGroupSearch: true,
	tools.ToolGroupAccessRequest: true, tools.ToolKBSearch: true,
}

// refusalSources are the tools a refuse ticket's code can come from (AC-7).
var refusalSources = map[string][]string{
	string(tools.CodeBlocked):   {tools.ToolPrivilegedAccessChange},
	string(tools.CodeProtected): {tools.ToolPasswordResetLink, tools.ToolMFAClear, tools.ToolGroupAccessRequest},
	string(tools.CodeNotYours):  {tools.ToolTicketUpdate, tools.ToolEscalate, tools.ToolPasswordResetLink},
}
