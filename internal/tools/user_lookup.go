package tools

import (
	"context"
	"time"
)

// userLookup returns the requester's own account and lock state (AC-6).
func userLookup(ctx context.Context, d Deps, in Input) (Result, error) {
	lock, err := d.Directory.Lock(ctx, in.Account.DN)
	if err != nil {
		return Result{}, directoryFailure(err)
	}
	var endsAt any // null when not locked, or locked until an admin clears it
	if lock.Locked && !lock.EndsAt.IsZero() {
		endsAt = lock.EndsAt.UTC().Format(time.RFC3339)
	}
	return Result{
		Result: resultData(map[string]any{
			"username":     in.Account.Username,
			"display_name": in.Account.DisplayName,
			"department":   in.Account.Department,
			"enabled":      in.Account.Enabled,
			"locked":       lock.Locked,
			"lock_ends_at": endsAt,
		}),
		Text: "Looked up the requester's account.",
	}, nil
}
