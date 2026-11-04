package tools

// allRefusals is every refusal the tool layer can store in audit_log.error.
// The eval harness reads codes back through CodeForReason (spec 0017, AC-8),
// because the audit log keeps the reason text and not the code.
var allRefusals = []refusal{
	refuseBlocked, refuseNotMatched, refuseAmbiguous, refuseNoHelpdeskAccount,
	refuseReadOnly, refuseProtected, refuseRateLimited, refuseHasTicket,
	refuseNoTicket, refuseNotYours, refuseTicketClosed, refuseAlreadyEscalated,
	refuseEscalateFirst, refuseAccountDisabled, refuseAlreadyPending,
	refuseNotInEntra, refuseNothingToClear, refuseOtherMethods, refuseNotEligible,
	refuseApprovalPending, refuseAlreadyMember, refuseNoOwner,
	refuseOwnerIsRequester, refuseNotRequestable, refuseTooManyPending,
	refuseOnePerThread, refuseGroupAlreadyPending, refuseAccessPending,
	refuseRecheckFailed,
}

// CodeForReason maps a stored refusal reason to its refusal code. The second
// result is false for text that is not a refusal reason.
func CodeForReason(reason string) (Code, bool) {
	for _, r := range allRefusals {
		if r.reason == reason {
			return r.code, true
		}
	}
	return "", false
}

// RefusalCodes lists every code in the refusal table, each once.
func RefusalCodes() []Code {
	var codes []Code
	seen := map[Code]bool{}
	for _, r := range allRefusals {
		if !seen[r.code] {
			seen[r.code] = true
			codes = append(codes, r.code)
		}
	}
	return codes
}
