package tools

import (
	"context"
	"strings"
	"time"
)

// maxDirectoryNote caps the description the model reads, in bytes. The
// description is free text typed by IT, so it is shortened and put on one line.
const maxDirectoryNote = 120

// directoryNote returns the account description as one trimmed line.
func directoryNote(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > maxDirectoryNote {
		s = strings.ToValidUTF8(s[:maxDirectoryNote], "")
	}
	return s
}

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
			"username":       in.Account.Username,
			"display_name":   in.Account.DisplayName,
			"department":     in.Account.Department,
			"enabled":        in.Account.Enabled,
			"directory_note": directoryNote(in.Account.Description),
			"locked":         lock.Locked,
			"lock_ends_at":   endsAt,
		}),
		Text: "Looked up the requester's account.",
	}, nil
}
