// Package migrate applies the embedded goose migrations as noord_owner, the
// way the cluster's migration Job runs them (spec 0015, AC-9). It first waits
// until the database accepts the owner and every app role exists, because
// CloudNativePG creates the managed roles after the database, and migration
// 00002 grants to them.
package migrate

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	schema "github.com/toyinogun/AIITsupport/db"
)

// Roles are the app roles the migrations grant to; the owner runs them.
var Roles = []string{"noord_agent", "noord_toolserver", "noord_eval"}

// pollInterval is how often Wait tries again.
const pollInterval = 2 * time.Second

// ErrNotReady is returned when the database or a role is still missing at
// the deadline.
var ErrNotReady = errors.New("database not ready")

// Result says what Up did.
type Result struct {
	Applied []int64 // the versions applied by this run, oldest first
	Version int64   // the database's version afterwards
}

// Run waits up to wait for the database, then applies every pending migration.
func Run(ctx context.Context, databaseURL string, wait time.Duration) (Result, error) {
	cfg, err := pgx.ParseConfig(databaseURL)
	if err != nil {
		return Result{}, errors.New("MIGRATE_DATABASE_URL does not parse as a database URL")
	}
	if err := Wait(ctx, cfg, wait); err != nil {
		return Result{}, err
	}
	return Up(ctx, cfg)
}

// Wait polls until a connection as cfg's user succeeds and every role in
// Roles exists, or wait passes.
func Wait(ctx context.Context, cfg *pgx.ConnConfig, wait time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	var last error
	for {
		if last = ready(ctx, cfg); last == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("%w after %s: %w", ErrNotReady, wait, last)
		case <-time.After(pollInterval):
		}
	}
}

func ready(ctx context.Context, cfg *pgx.ConnConfig) error {
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return fmt.Errorf("connect as %s: %w", cfg.User, err)
	}
	defer func() { _ = conn.Close(context.WithoutCancel(ctx)) }()
	var found int
	if err := conn.QueryRow(ctx, "SELECT count(*) FROM pg_roles WHERE rolname = ANY($1)", Roles).Scan(&found); err != nil {
		return fmt.Errorf("read pg_roles: %w", err)
	}
	if found != len(Roles) {
		return fmt.Errorf("%d of %d app roles exist", found, len(Roles))
	}
	return nil
}

// Up applies every pending embedded migration as cfg's user.
func Up(ctx context.Context, cfg *pgx.ConnConfig) (res Result, err error) {
	migrations, err := Migrations()
	if err != nil {
		return Result{}, err
	}
	sqlDB := stdlib.OpenDB(*cfg)
	defer func() { err = errors.Join(err, sqlDB.Close()) }()
	provider, err := goose.NewProvider(goose.DialectPostgres, sqlDB, migrations)
	if err != nil {
		return Result{}, fmt.Errorf("open migrations: %w", err)
	}
	applied, err := provider.Up(ctx)
	if err != nil {
		return Result{}, fmt.Errorf("apply migrations as %s: %w", cfg.User, err)
	}
	for _, r := range applied {
		res.Applied = append(res.Applied, r.Source.Version)
	}
	if res.Version, err = provider.GetDBVersion(ctx); err != nil {
		return Result{}, fmt.Errorf("read the database version: %w", err)
	}
	return res, nil
}

// Migrations is the embedded migrations directory.
func Migrations() (fs.FS, error) {
	migrations, err := fs.Sub(schema.Migrations, "migrations")
	if err != nil {
		return nil, fmt.Errorf("open embedded migrations: %w", err)
	}
	return migrations, nil
}

// Latest is the newest embedded migration's version, what the smoke check
// compares the database against (AC-17).
func Latest() (int64, error) {
	migrations, err := Migrations()
	if err != nil {
		return 0, err
	}
	names, err := fs.Glob(migrations, "*.sql")
	if err != nil {
		return 0, fmt.Errorf("list migrations: %w", err)
	}
	var latest int64
	for _, name := range names {
		v, err := goose.NumericComponent(name)
		if err != nil {
			return 0, fmt.Errorf("migration %s: %w", name, err)
		}
		latest = max(latest, v)
	}
	if latest == 0 {
		return 0, errors.New("no embedded migrations")
	}
	return latest, nil
}
