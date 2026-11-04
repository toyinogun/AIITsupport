// Package dbtest gives a DB test its own freshly migrated Postgres database,
// with a connection pool per app role, so grant and query tests never share
// state. Tests skip unless TEST_DATABASE_URL is a superuser connection URL
// (the Compose Postgres, or CI's service container).
//
// Roles are cluster wide and CREATE DATABASE from a template needs that
// template idle, so every setup step runs under one advisory lock: test
// packages run in parallel and would otherwise race.
package dbtest

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	schema "github.com/toyinogun/AIITsupport/db"
)

// Role names, as created by db/roles.sql.
const (
	RoleOwner      = "noord_owner"
	RoleAgent      = "noord_agent"
	RoleToolServer = "noord_toolserver"
	RoleEval       = "noord_eval"
	RoleGrafana    = "noord_grafana"
)

const (
	// DevPassword is the throwaway local password db/roles.sql gives every role.
	DevPassword = "noord-dev" //nolint:gosec // throwaway local dev value, same as compose.yaml and db/roles.sql

	// setupLockKey is any constant every test process agrees on.
	setupLockKey = 5_000_305
)

// DB is one scratch database for one test, dropped when the test ends.
type DB struct {
	Name       string
	Owner      *pgxpool.Pool
	Agent      *pgxpool.Pool
	ToolServer *pgxpool.Pool
	Eval       *pgxpool.Pool
	Grafana    *pgxpool.Pool

	base *url.URL
}

// New creates a migrated scratch database and connects as each role.
func New(t testing.TB) *DB {
	t.Helper()
	raw := os.Getenv("TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("set TEST_DATABASE_URL to a superuser connection URL to run DB tests")
	}
	base, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse TEST_DATABASE_URL: %v", err)
	}

	ctx := t.Context()
	admin, err := pgx.Connect(ctx, raw)
	if err != nil {
		t.Fatalf("connect as superuser: %v", err)
	}
	defer func() { _ = admin.Close(context.WithoutCancel(ctx)) }()

	d := &DB{Name: "noord_test_" + randomHex(t, 6), base: base}
	err = withSetupLock(ctx, admin, func() error {
		tmpl, err := ensureTemplate(ctx, admin, base)
		if err != nil {
			return err
		}
		_, err = admin.Exec(ctx, "CREATE DATABASE "+ident(d.Name)+" TEMPLATE "+ident(tmpl)+" OWNER "+RoleOwner)
		return err
	})
	if err != nil {
		t.Fatalf("create scratch database: %v", err)
	}
	t.Cleanup(func() { dropDatabase(t, raw, d.Name) })
	// CREATE DATABASE does not copy database privileges from the template, so
	// repeat the REVOKE that migration 00008 ran on the template (spec 0016).
	if _, err := admin.Exec(ctx, "REVOKE TEMPORARY ON DATABASE "+ident(d.Name)+" FROM PUBLIC"); err != nil {
		t.Fatalf("revoke temporary on scratch database: %v", err)
	}

	d.Owner = d.pool(t, RoleOwner)
	d.Agent = d.pool(t, RoleAgent)
	d.ToolServer = d.pool(t, RoleToolServer)
	d.Eval = d.pool(t, RoleEval)
	d.Grafana = d.lazyPool(t, RoleGrafana, 1)
	return d
}

// URL is the connection URL for role on this scratch database.
func (d *DB) URL(role string) string {
	u := *d.base
	u.User = url.UserPassword(role, DevPassword)
	u.Path = "/" + d.Name
	return u.String()
}

func (d *DB) pool(t testing.TB, role string) *pgxpool.Pool {
	t.Helper()
	p, err := pgxpool.New(t.Context(), d.URL(role))
	if err != nil {
		t.Fatalf("pool for %s: %v", role, err)
	}
	if err := p.Ping(t.Context()); err != nil {
		p.Close()
		t.Fatalf("connect as %s: %v", role, err)
	}
	t.Cleanup(p.Close)
	return p
}

// lazyPool is a pool that connects only when a test uses it, with at most
// maxConns connections. noord_grafana is capped at 5 connections across the
// whole server (db/roles.sql), so a test that never uses it must not hold one,
// or parallel test binaries run it out of connections.
func (d *DB) lazyPool(t testing.TB, role string, maxConns int32) *pgxpool.Pool {
	t.Helper()
	cfg, err := pgxpool.ParseConfig(d.URL(role))
	if err != nil {
		t.Fatalf("parse url for %s: %v", role, err)
	}
	cfg.MaxConns = maxConns
	p, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatalf("pool for %s: %v", role, err)
	}
	t.Cleanup(p.Close)
	return p
}

func withSetupLock(ctx context.Context, conn *pgx.Conn, fn func() error) error {
	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", setupLockKey); err != nil {
		return fmt.Errorf("take setup lock: %w", err)
	}
	defer func() {
		_, _ = conn.Exec(context.WithoutCancel(ctx), "SELECT pg_advisory_unlock($1)", setupLockKey)
	}()
	return fn()
}

// ensureTemplate applies db/roles.sql and makes sure a database migrated to
// the current migrations exists, named by their hash. Stale templates from
// older migrations are dropped. Call it under the setup lock.
func ensureTemplate(ctx context.Context, admin *pgx.Conn, base *url.URL) (string, error) {
	if _, err := admin.Exec(ctx, schema.Roles); err != nil {
		return "", fmt.Errorf("apply db/roles.sql: %w", err)
	}
	migrations, err := fs.Sub(schema.Migrations, "migrations")
	if err != nil {
		return "", fmt.Errorf("open embedded migrations: %w", err)
	}
	name, err := templateName(migrations)
	if err != nil {
		return "", err
	}
	if err := dropStaleTemplates(ctx, admin, name); err != nil {
		return "", err
	}

	var exists bool
	if err := admin.QueryRow(ctx, "SELECT EXISTS (SELECT FROM pg_database WHERE datname = $1)", name).Scan(&exists); err != nil {
		return "", fmt.Errorf("look up template: %w", err)
	}
	if exists {
		return name, nil
	}
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+ident(name)+" OWNER "+RoleOwner); err != nil {
		return "", fmt.Errorf("create template: %w", err)
	}
	if err := migrate(ctx, base, name, migrations); err != nil {
		_, _ = admin.Exec(ctx, "DROP DATABASE "+ident(name))
		return "", err
	}
	return name, nil
}

func migrate(ctx context.Context, base *url.URL, dbName string, migrations fs.FS) error {
	u := *base
	u.User = url.UserPassword(RoleOwner, DevPassword)
	u.Path = "/" + dbName
	cfg, err := pgx.ParseConfig(u.String())
	if err != nil {
		return fmt.Errorf("parse owner url: %w", err)
	}
	// Closed before returning, so the template is idle for CREATE DATABASE.
	sqlDB := stdlib.OpenDB(*cfg)
	provider, err := goose.NewProvider(goose.DialectPostgres, sqlDB, migrations)
	if err == nil {
		_, err = provider.Up(ctx)
	}
	if closeErr := sqlDB.Close(); err == nil && closeErr != nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("migrate template as %s: %w", RoleOwner, err)
	}
	return nil
}

func templateName(migrations fs.FS) (string, error) {
	h := sha256.New()
	h.Write([]byte(schema.Roles))
	err := fs.WalkDir(migrations, ".", func(path string, e fs.DirEntry, err error) error {
		if err != nil || e.IsDir() {
			return err
		}
		b, err := fs.ReadFile(migrations, path)
		if err != nil {
			return err
		}
		h.Write([]byte(path))
		h.Write(b)
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("hash migrations: %w", err)
	}
	return "noord_tmpl_" + hex.EncodeToString(h.Sum(nil))[:12], nil
}

func dropStaleTemplates(ctx context.Context, admin *pgx.Conn, keep string) error {
	rows, err := admin.Query(ctx, "SELECT datname FROM pg_database WHERE datname LIKE 'noord\\_tmpl\\_%' AND datname <> $1", keep)
	if err != nil {
		return fmt.Errorf("list templates: %w", err)
	}
	stale, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return fmt.Errorf("list templates: %w", err)
	}
	for _, name := range stale {
		if _, err := admin.Exec(ctx, "DROP DATABASE IF EXISTS "+ident(name)+" WITH (FORCE)"); err != nil {
			return fmt.Errorf("drop stale template %s: %w", name, err)
		}
	}
	return nil
}

func dropDatabase(t testing.TB, raw, name string) {
	t.Helper()
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, raw)
	if err != nil {
		t.Errorf("connect to drop %s: %v", name, err)
		return
	}
	defer func() { _ = admin.Close(ctx) }()
	if _, err := admin.Exec(ctx, "DROP DATABASE IF EXISTS "+ident(name)+" WITH (FORCE)"); err != nil {
		t.Errorf("drop %s: %v", name, err)
	}
}

func ident(name string) string { return pgx.Identifier{name}.Sanitize() }

func randomHex(t testing.TB, n int) string {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("random name: %v", err)
	}
	return hex.EncodeToString(b)
}
