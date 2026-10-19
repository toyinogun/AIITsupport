package resetlink

import (
	"context"
	"log/slog"
	"time"

	"github.com/toyinogun/AIITsupport/internal/cause"
)

// Cleanup timing (spec 0011, AC-18).
const (
	cleanupEvery   = time.Hour
	cleanupTimeout = 5 * time.Minute
)

// Cleaner deletes long expired links. *Store satisfies it.
type Cleaner interface {
	DeleteExpired(ctx context.Context, retentionDays int) (int64, error)
}

// Cleanup deletes links that expired more than retentionDays ago, at start
// and then hourly, until ctx ends. A failed run is logged, never fatal.
func Cleanup(ctx context.Context, c Cleaner, retentionDays int, logger *slog.Logger) {
	ticker := time.NewTicker(cleanupEvery)
	defer ticker.Stop()
	for {
		cleanOnce(ctx, c, retentionDays, logger)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func cleanOnce(parent context.Context, c Cleaner, retentionDays int, logger *slog.Logger) {
	ctx, cancel := context.WithTimeout(parent, cleanupTimeout)
	defer cancel()
	n, err := c.DeleteExpired(ctx, retentionDays)
	if err != nil {
		if parent.Err() == nil { // a shutdown is not a failure
			logger.WarnContext(ctx, "reset_links_cleanup_failed", "cause", cause.Of(err))
		}
		return
	}
	logger.InfoContext(ctx, "reset_links_cleaned", "rows", n)
}
