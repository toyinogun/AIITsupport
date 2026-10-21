package main

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/toyinogun/AIITsupport/internal/config"
	"github.com/toyinogun/AIITsupport/internal/migrate"
)

// runMigrate is the cluster's migration Job (spec 0015, AC-9): wait for the
// owner and the managed roles, then apply the embedded migrations.
func runMigrate(ctx context.Context, args []string, out io.Writer) error {
	if len(args) != 0 {
		return fmt.Errorf("migrate takes no arguments\n%w", errUsage)
	}
	cfg, err := config.LoadMigrate(os.LookupEnv)
	if err != nil {
		return err
	}
	logger := newLogger(out)
	logger.Info("migrate_waiting", "wait", cfg.WaitTimeout.String())
	res, err := migrate.Run(ctx, cfg.DatabaseURL, cfg.WaitTimeout)
	if err != nil {
		return err
	}
	logger.Info("migrate_done", "applied", len(res.Applied), "version", res.Version)
	return nil
}
