package tools

import (
	"context"
	"log/slog"
	"time"
)

// killSyncWriteTimeout bounds one write of the state row.
const killSyncWriteTimeout = 5 * time.Second

// KillSwitchState records the mode for the dashboard (the kill_switch_state
// row, spec 0016, AC-8). queries.Queries satisfies it.
type KillSwitchState interface {
	SyncKillSwitchState(ctx context.Context, mode string) error
}

// SyncKillSwitch records the enforced mode at start and then every interval
// until ctx ends. The state row only informs the dashboard; enforcement
// still reads the switch itself on every call. An unreadable switch records
// read_only, and a failure to read or to write is logged as
// kill_switch_sync_failed with its cause only, never as an error that stops
// the loop.
func SyncKillSwitch(ctx context.Context, sw KillSwitch, state KillSwitchState, interval time.Duration, logger *slog.Logger) {
	syncKillSwitchOnce(ctx, sw, state, logger)
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			syncKillSwitchOnce(ctx, sw, state, logger)
		}
	}
}

func syncKillSwitchOnce(ctx context.Context, sw KillSwitch, state KillSwitchState, logger *slog.Logger) {
	mode, err := sw.Mode()
	if err != nil {
		logger.WarnContext(ctx, "kill_switch_sync_failed", "cause", cause(err))
	}
	wctx, cancel := context.WithTimeout(ctx, killSyncWriteTimeout)
	defer cancel()
	if err := state.SyncKillSwitchState(wctx, mode); err != nil && ctx.Err() == nil {
		logger.WarnContext(ctx, "kill_switch_sync_failed", "cause", cause(err))
	}
}
