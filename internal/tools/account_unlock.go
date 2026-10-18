package tools

import (
	"context"

	"github.com/toyinogun/AIITsupport/internal/audit"
)

// accountUnlock unlocks the requester's own account (AC-7). It reads the
// lock, reads the kill switch again just before the write (AC-8), clears
// lockoutTime, and reads the lock again to confirm.
func accountUnlock(ctx context.Context, d AccountDeps, in Input) (Result, error) {
	before, err := d.Directory.Lock(ctx, in.Account.DN)
	if err != nil {
		return Result{}, directoryFailure(err)
	}
	if !before.Locked {
		return Result{
			Result: resultData(map[string]any{"was_locked": false, "locked": false}),
			Text:   "Account was not locked; nothing changed.",
		}, nil
	}
	if !d.Switch.AccountChangesAllowed() {
		return refused(refuseReadOnly), nil
	}
	if err := d.Writer.ClearLockout(ctx, in.Account.DN); err != nil {
		return Result{}, directoryFailure(err)
	}
	after, err := d.Directory.Lock(ctx, in.Account.DN)
	if err != nil {
		return Result{}, directoryFailure(err)
	}
	if after.Locked {
		return Result{}, &failure{code: CodeNotConfirmed, reason: reasonUnlockNotConfirmed, sentence: unlockNotConfirmedPhrase}
	}
	return Result{
		Result: resultData(map[string]any{"was_locked": true, "locked": false}),
		Text:   "Account unlocked and confirmed.",
	}, nil
}

func resultData(data map[string]any) audit.Result {
	return audit.Result{Outcome: audit.OutcomeOK, Data: data}
}
