package db_test

import (
	"context"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	schema "github.com/toyinogun/AIITsupport/db"
	"github.com/toyinogun/AIITsupport/internal/db/dbtest"
)

// appTables are the eight tables migration 00002 creates (spec 0003) plus
// 00003's reset_links (spec 0011) and 00004's approval_cards (spec 0012).
var appTables = []string{
	"agent_failures", "approval_cards", "approvals", "audit_log", "conversation_messages", "conversations",
	"eval_results", "eval_runs", "kill_switch_state", "llm_usage", "reset_links", "slack_events",
}

// emptyDatabase makes an unmigrated database owned by noord_owner and returns
// a connection to it as the superuser. dbtest.New runs first so db/roles.sql
// has been applied under dbtest's setup lock (roles are cluster wide).
func emptyDatabase(t *testing.T) (name string, admin *pgx.Conn) {
	t.Helper()
	dbtest.New(t) // skips without TEST_DATABASE_URL

	raw := os.Getenv("TEST_DATABASE_URL")
	root, err := pgx.Connect(t.Context(), raw)
	if err != nil {
		t.Fatalf("connect as superuser: %v", err)
	}
	t.Cleanup(func() { _ = root.Close(context.Background()) })

	name = fmt.Sprintf("noord_migrate_%d", os.Getpid()) + "_" + strings.ToLower(strings.ReplaceAll(t.Name(), "/", "_"))
	ident := pgx.Identifier{name}.Sanitize()
	if _, err := root.Exec(t.Context(), "CREATE DATABASE "+ident+" OWNER "+dbtest.RoleOwner); err != nil {
		t.Fatalf("create %s: %v", name, err)
	}
	t.Cleanup(func() {
		_, _ = root.Exec(context.Background(), "DROP DATABASE IF EXISTS "+ident+" WITH (FORCE)")
	})

	admin, err = pgx.Connect(t.Context(), connURL(t, raw, name, nil))
	if err != nil {
		t.Fatalf("connect to %s: %v", name, err)
	}
	t.Cleanup(func() { _ = admin.Close(context.Background()) })
	return name, admin
}

// connURL points raw at dbName, as user when it is set.
func connURL(t *testing.T, raw, dbName string, user *url.Userinfo) string {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse TEST_DATABASE_URL: %v", err)
	}
	u.Path = "/" + dbName
	if user != nil {
		u.User = user
	}
	return u.String()
}

// provider is a goose provider over the embedded migrations, connected as user
// (nil keeps the superuser from TEST_DATABASE_URL).
func provider(t *testing.T, dbName string, user *url.Userinfo) *goose.Provider {
	t.Helper()
	cfg, err := pgx.ParseConfig(connURL(t, os.Getenv("TEST_DATABASE_URL"), dbName, user))
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}
	sqlDB := stdlib.OpenDB(*cfg)
	t.Cleanup(func() { _ = sqlDB.Close() })
	migrations, err := fs.Sub(schema.Migrations, "migrations")
	if err != nil {
		t.Fatalf("open embedded migrations: %v", err)
	}
	p, err := goose.NewProvider(goose.DialectPostgres, sqlDB, migrations)
	if err != nil {
		t.Fatalf("goose provider: %v", err)
	}
	return p
}

func ownerUser() *url.Userinfo { return url.UserPassword(dbtest.RoleOwner, dbtest.DevPassword) }

func publicTables(t *testing.T, conn *pgx.Conn) []string {
	t.Helper()
	rows, err := conn.Query(t.Context(),
		"SELECT tablename FROM pg_tables WHERE schemaname = 'public' AND tablename <> 'goose_db_version' ORDER BY 1")
	if err != nil {
		t.Fatalf("list tables: %v", err)
	}
	names, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatalf("list tables: %v", err)
	}
	return names
}

func count(t *testing.T, conn *pgx.Conn, query string) int {
	t.Helper()
	var n int
	if err := conn.QueryRow(t.Context(), query).Scan(&n); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return n
}

// covers: AC-1 (spec 0003)
func TestMigrationGoesDownAndUpCleanly(t *testing.T) {
	name, admin := emptyDatabase(t)
	p := provider(t, name, ownerUser())

	if _, err := p.Up(t.Context()); err != nil {
		t.Fatalf("up as %s: %v", dbtest.RoleOwner, err)
	}
	if got := publicTables(t, admin); !slices.Equal(got, appTables) {
		t.Fatalf("tables after up = %v, want %v", got, appTables)
	}
	owners := count(t, admin, "SELECT count(*) FROM pg_tables WHERE schemaname = 'public' AND tableowner <> '"+dbtest.RoleOwner+"'")
	if owners != 0 {
		t.Errorf("%d public tables are not owned by %s", owners, dbtest.RoleOwner)
	}
	triggers := count(t, admin, "SELECT count(*) FROM pg_trigger WHERE tgrelid = 'audit_log'::regclass AND NOT tgisinternal")
	if triggers != 2 {
		t.Errorf("audit_log has %d triggers, want 2 (update/delete and truncate)", triggers)
	}

	if _, err := p.DownTo(t.Context(), 1); err != nil {
		t.Fatalf("down to 1: %v", err)
	}
	if got := publicTables(t, admin); len(got) != 0 {
		t.Errorf("tables after down = %v, want none", got)
	}
	functions := count(t, admin, "SELECT count(*) FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace WHERE n.nspname = 'public'")
	if functions != 0 {
		t.Errorf("%d functions left in public after down, want 0", functions)
	}

	if _, err := p.Up(t.Context()); err != nil {
		t.Fatalf("up again: %v", err)
	}
	if v, err := p.GetDBVersion(t.Context()); err != nil || v != 8 {
		t.Errorf("version after up again = %d, %v; want 8", v, err)
	}
}

// covers: spec 0013 (migration 00006). Going down past 00006 with two
// pending access requests for one account expires them, so the old one per
// account and tool index still builds; going up again keeps those rows,
// while a new access request with no target is still refused.
func TestMigrationDownExpiresPendingAccessRequests(t *testing.T) {
	name, admin := emptyDatabase(t)
	p := provider(t, name, ownerUser())
	if _, err := p.Up(t.Context()); err != nil {
		t.Fatalf("up: %v", err)
	}
	for _, group := range []string{"SG-Finance", "SHR-Finance-RW"} {
		if _, err := admin.Exec(t.Context(), "INSERT INTO approvals (call_id, tool_name, arguments, risk_tier, requester_slack_id, "+
			"requester_account, slack_channel_id, slack_thread_ts, approver_kind, allowed_approvers, expires_at, target) "+
			"VALUES (gen_random_uuid(), 'group_access_request', '{}', 'medium', 'u', 'noor.elamrani', 'c', 't', "+
			"'group_owner', '{ilse.smit}', now() + interval '8 hours', $1)", group); err != nil {
			t.Fatalf("insert the %s request: %v", group, err)
		}
	}
	if _, err := p.DownTo(t.Context(), 5); err != nil {
		t.Fatalf("down to 5: %v", err)
	}
	if n := count(t, admin, "SELECT count(*) FROM approvals WHERE status = 'expired' AND arguments IS NULL"); n != 2 {
		t.Errorf("%d expired requests after down, want 2", n)
	}
	if _, err := p.Up(t.Context()); err != nil {
		t.Fatalf("up again: %v", err)
	}
	if _, err := admin.Exec(t.Context(), "INSERT INTO approvals (call_id, tool_name, arguments, risk_tier, requester_slack_id, "+
		"slack_channel_id, slack_thread_ts, approver_kind, allowed_approvers, expires_at) "+
		"VALUES (gen_random_uuid(), 'group_access_request', '{}', 'medium', 'u', 'c', 't', 'group_owner', '{ilse.smit}', now())"); err == nil {
		t.Error("an access request with no target was stored after up again")
	}
}

// covers: AC-13 (spec 0003)
func TestMigrationRefusesAnyRoleButTheOwner(t *testing.T) {
	name, admin := emptyDatabase(t)

	// The superuser from TEST_DATABASE_URL is the most tempting wrong role.
	_, err := provider(t, name, nil).Up(t.Context())
	if err == nil || !strings.Contains(err.Error(), "run migrations as noord_owner, not ") {
		t.Fatalf("up as the superuser: err = %v, want the owner guard to name the problem", err)
	}
	if got := publicTables(t, admin); len(got) != 0 {
		t.Errorf("a refused migration left tables behind: %v", got)
	}
}

// covers: spec 0016 AC-6. The migration gives closed rows the outcome that
// matches their status before the pairing check is added.
func TestMigrationBackfillsOutcome(t *testing.T) {
	name, admin := emptyDatabase(t)
	p := provider(t, name, ownerUser())
	if _, err := p.UpTo(t.Context(), 7); err != nil {
		t.Fatalf("up to 7: %v", err)
	}
	for _, status := range []string{"resolved", "escalated"} {
		if _, err := admin.Exec(t.Context(), "INSERT INTO conversations (slack_channel_id, slack_thread_ts, requester_slack_id, status, closed_at) "+
			"VALUES ('c', $1, 'u', $1, now())", status); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := admin.Exec(t.Context(), "INSERT INTO conversations (slack_channel_id, slack_thread_ts, requester_slack_id) VALUES ('c', 'open', 'u')"); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Up(t.Context()); err != nil {
		t.Fatalf("up: %v", err)
	}
	if n := count(t, admin, "SELECT count(*) FROM conversations WHERE outcome = status AND status <> 'open'"); n != 2 {
		t.Errorf("%d closed rows backfilled, want 2", n)
	}
	if n := count(t, admin, "SELECT count(*) FROM conversations WHERE status = 'open' AND outcome IS NULL"); n != 1 {
		t.Errorf("open row lost its null outcome")
	}
}

// covers: spec 0016 AC-9. The migration revokes TEMPORARY on the database
// from PUBLIC, so the view only role cannot create a temporary table.
func TestMigrationRevokesTemporaryFromPublic(t *testing.T) {
	name, admin := emptyDatabase(t)
	if _, err := provider(t, name, ownerUser()).Up(t.Context()); err != nil {
		t.Fatalf("up: %v", err)
	}
	var temp bool
	if err := admin.QueryRow(t.Context(), "SELECT has_database_privilege('noord_grafana', current_database(), 'TEMP')").Scan(&temp); err != nil || temp {
		t.Errorf("noord_grafana TEMP = %v, %v; want false", temp, err)
	}
}
