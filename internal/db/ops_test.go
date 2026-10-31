package db

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/toyinogun/AIITsupport/internal/db/dbtest"
	"github.com/toyinogun/AIITsupport/internal/db/queries"
)

// opsViews is the interface table of spec 0016 (AC-9).
var opsViews = []string{
	"conversation_outcomes", "llm_calls", "conversation_cost", "approvals_open",
	"tool_calls", "failures", "kill_switch",
}

func insertConversation(t *testing.T, p *pgxpool.Pool, status, outcome string) error {
	t.Helper()
	closed := "NULL"
	if status != "open" {
		closed = "now()"
	}
	out := "NULL"
	if outcome != "" {
		out = "'" + outcome + "'"
	}
	_, err := p.Exec(t.Context(), fmt.Sprintf(
		"INSERT INTO conversations (slack_channel_id, slack_thread_ts, requester_slack_id, status, closed_at, outcome) "+
			"VALUES ('c', clock_timestamp()::text, 'u', '%s', %s, %s)", status, closed, out))
	return err
}

// covers: spec 0016 AC-6. Only the storable pairings of status and outcome
// are accepted, and outcome is null exactly while the conversation is open.
func TestConversationOutcomePairing(t *testing.T) {
	tdb := dbtest.New(t)
	for _, c := range []struct {
		status, outcome string
		ok              bool
	}{
		{"open", "", true},
		{"resolved", "resolved", true},
		{"resolved", "abandoned", true},
		{"escalated", "escalated", true},
		{"escalated", "refused", true},
		{"open", "resolved", false},
		{"resolved", "escalated", false},
		{"resolved", "refused", false},
		{"escalated", "resolved", false},
		{"escalated", "abandoned", false},
		{"resolved", "bogus", false},
	} {
		err := insertConversation(t, tdb.Owner, c.status, c.outcome)
		if c.ok && err != nil {
			t.Errorf("%s with %q: %v", c.status, c.outcome, err)
		}
		if !c.ok && pgCode(err) != "23514" {
			t.Errorf("%s with %q: want a check violation (23514), got %v", c.status, c.outcome, err)
		}
	}
}

// covers: spec 0016 AC-6. During a rolling deploy an old pod closes a
// conversation without an outcome; the trigger fills it from the status.
func TestOutcomeFilledForOldStyleClose(t *testing.T) {
	tdb := dbtest.New(t)
	for _, status := range []string{"resolved", "escalated"} {
		if err := insertConversation(t, tdb.Agent, "open", ""); err != nil {
			t.Fatal(err)
		}
		_, err := tdb.Agent.Exec(t.Context(),
			"UPDATE conversations SET status = $1, closed_at = now() WHERE outcome IS NULL AND status = 'open'", status)
		if err != nil {
			t.Fatalf("old style close as %s: %v", status, err)
		}
		var got string
		if err := tdb.Owner.QueryRow(t.Context(),
			"SELECT outcome FROM conversations WHERE status = $1", status).Scan(&got); err != nil || got != status {
			t.Errorf("outcome after an old style %s close = %q, %v; want %q", status, got, err, status)
		}
	}
	if _, err := tdb.Agent.Exec(t.Context(), "UPDATE conversations SET outcome = 'refused' WHERE status = 'escalated'"); err != nil {
		t.Errorf("agent could not set outcome: %v", err)
	}
}

// covers: spec 0016 AC-7. cause is a fixed token of at most 40 characters,
// stage is one of four, and the agent can insert but no app role can delete.
func TestAgentFailureRows(t *testing.T) {
	tdb := dbtest.New(t)
	ok := "INSERT INTO agent_failures (stage, cause) VALUES ('%s', '%s')"
	for _, stage := range []string{"llm", "tool", "slack", "sweep"} {
		if _, err := tdb.Agent.Exec(t.Context(), fmt.Sprintf(ok, stage, "anthropic_529")); err != nil {
			t.Errorf("stage %s: %v", stage, err)
		}
	}
	for _, bad := range [][2]string{{"disk", "x"}, {"llm", "Has Spaces"}, {"llm", ""}, {"llm", strings.Repeat("a", 41)}} {
		if _, err := tdb.Agent.Exec(t.Context(), fmt.Sprintf(ok, bad[0], bad[1])); pgCode(err) != "23514" {
			t.Errorf("stage %q cause %q: want a check violation, got %v", bad[0], bad[1], err)
		}
	}
	if _, err := tdb.Agent.Exec(t.Context(), "INSERT INTO agent_failures (conversation_id, stage, cause) VALUES (999, 'llm', 'x')"); pgCode(err) != "23503" {
		t.Errorf("failure for a missing conversation: want a foreign key violation, got %v", err)
	}
}

// covers: spec 0016 AC-8. The kill switch state is one row.
func TestKillSwitchStateIsOneRow(t *testing.T) {
	tdb := dbtest.New(t)
	ins := "INSERT INTO kill_switch_state (mode, changed_at, checked_at) VALUES ('%s', now(), now())"
	if _, err := tdb.ToolServer.Exec(t.Context(), fmt.Sprintf(ins, "read_only")); err != nil {
		t.Fatal(err)
	}
	if _, err := tdb.ToolServer.Exec(t.Context(), fmt.Sprintf(ins, "read_write")); pgCode(err) != "23505" {
		t.Errorf("second row: want a unique violation, got %v", err)
	}
	if _, err := tdb.ToolServer.Exec(t.Context(), "UPDATE kill_switch_state SET mode = 'bogus'"); pgCode(err) != "23514" {
		t.Errorf("bad mode: want a check violation, got %v", err)
	}
	if _, err := tdb.ToolServer.Exec(t.Context(), "UPDATE kill_switch_state SET mode = 'read_write', changed_at = now(), checked_at = now()"); err != nil {
		t.Errorf("update: %v", err)
	}
}

// covers: spec 0016 AC-9. noord_grafana reads every ops view, nothing in
// public, and writes nothing, not even a temporary table.
func TestGrafanaRoleSeesOnlyOpsViews(t *testing.T) {
	tdb := dbtest.New(t)
	g := tdb.Grafana
	for _, v := range opsViews {
		if _, err := g.Exec(t.Context(), "SELECT * FROM ops."+v); err != nil {
			t.Errorf("select ops.%s: %v", v, err)
		}
	}
	for _, table := range []string{
		"conversations", "conversation_messages", "llm_usage", "audit_log", "approvals", "approval_cards",
		"agent_failures", "kill_switch_state", "reset_links", "slack_events", "eval_runs",
	} {
		if _, err := g.Exec(t.Context(), "SELECT count(*) FROM public."+table); pgCode(err) != insufficientPrivilege {
			t.Errorf("select public.%s: want permission denied, got %v", table, err)
		}
	}
	for _, sql := range []string{
		"INSERT INTO ops.failures VALUES (now(), 'llm', 'x', NULL)",
		"UPDATE ops.kill_switch SET mode = 'read_write'",
		"DELETE FROM ops.failures",
		"CREATE TEMP TABLE t (a int)",
		"CREATE TABLE ops.t (a int)",
		"CREATE TABLE public.t (a int)",
		"CREATE VIEW ops.v AS SELECT 1",
		"CREATE SCHEMA mine",
	} {
		if _, err := g.Exec(t.Context(), sql); pgCode(err) != insufficientPrivilege {
			t.Errorf("%s: want permission denied, got %v", sql, err)
		}
	}
}

// covers: spec 0016 AC-9. The role is capped: 5 connections, a 10 second
// statement timeout, and it cannot widen either itself.
func TestGrafanaRoleLimits(t *testing.T) {
	tdb := dbtest.New(t)
	var limit int
	var config []string
	if err := tdb.Owner.QueryRow(t.Context(),
		"SELECT rolconnlimit, coalesce(rolconfig, '{}') FROM pg_roles WHERE rolname = 'noord_grafana'").Scan(&limit, &config); err != nil {
		t.Fatal(err)
	}
	if limit != 5 {
		t.Errorf("connection limit = %d, want 5", limit)
	}
	if !contains(config, "statement_timeout=10s") {
		t.Errorf("role settings = %v, want statement_timeout=10s", config)
	}
	if _, err := tdb.Grafana.Exec(t.Context(), "ALTER ROLE noord_grafana CONNECTION LIMIT 100"); err == nil {
		t.Error("noord_grafana raised its own connection limit")
	}
	var timeout string
	if err := tdb.Grafana.QueryRow(t.Context(), "SHOW statement_timeout").Scan(&timeout); err != nil || timeout != "10s" {
		t.Errorf("statement_timeout = %q, %v; want 10s", timeout, err)
	}
}

func countPool(t *testing.T, p *pgxpool.Pool, query string) int {
	t.Helper()
	var n int
	if err := p.QueryRow(t.Context(), query).Scan(&n); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return n
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

// covers: spec 0016 AC-9. No ops column can carry a person, an argument, a
// result, or message text, by name.
func TestOpsColumnNames(t *testing.T) {
	tdb := dbtest.New(t)
	rows, err := tdb.Owner.Query(t.Context(),
		"SELECT table_name, column_name FROM information_schema.columns WHERE table_schema = 'ops'")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	seen := 0
	for rows.Next() {
		var view, col string
		if err := rows.Scan(&view, &col); err != nil {
			t.Fatal(err)
		}
		seen++
		if col == "tool_name" {
			continue
		}
		for _, bad := range []string{"slack", "requester", "account", "argument", "result", "content", "email", "name"} {
			if strings.Contains(col, bad) {
				t.Errorf("ops.%s column %q contains %q", view, col, bad)
			}
		}
	}
	if seen == 0 {
		t.Error("no ops columns found")
	}
}

// covers: spec 0016 AC-9, AC-10. The views count what the dashboard says
// they count, and hold no row of a closed approval.
func TestOpsViewContents(t *testing.T) {
	tdb := dbtest.New(t)
	ctx := t.Context()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := tdb.Owner.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	exec("INSERT INTO conversations (slack_channel_id, slack_thread_ts, requester_slack_id, trace_id) VALUES ('c', '1', 'u', repeat('a', 32))")
	exec("INSERT INTO llm_usage (conversation_id, model, cost_microusd, pricing_date) VALUES (1, 'm', 1500000, now()), (1, 'm', 500000, now())")
	exec("INSERT INTO approvals (call_id, tool_name, arguments, risk_tier, requester_slack_id, slack_channel_id, slack_thread_ts, " +
		"approver_kind, allowed_approvers, expires_at) VALUES (gen_random_uuid(), 'mfa_clear', '{}', 'medium', 'u', 'c', 't', 'it', '{x}', now() + interval '1 hour')")
	exec("INSERT INTO audit_log (kind, tool_name, risk_tier, requester_slack_id, outcome, trace_id) VALUES " +
		"('intent', 'ticket_create', 'low', 'u', NULL, NULL), ('outcome', 'ticket_create', 'low', 'u', 'error', 'abc')")

	var cost float64
	var calls int
	if err := tdb.Grafana.QueryRow(ctx, "SELECT cost_usd, model_calls FROM ops.conversation_cost WHERE conversation_id = 1").Scan(&cost, &calls); err != nil || cost != 2.0 || calls != 2 {
		t.Errorf("conversation_cost = %v, %d, %v; want 2.0 and 2", cost, calls, err)
	}
	if n := countPool(t, tdb.Grafana, "SELECT count(*) FROM ops.llm_calls WHERE cost_usd IN (1.5, 0.5)"); n != 2 {
		t.Errorf("llm_calls priced rows = %d, want 2", n)
	}
	if n := countPool(t, tdb.Grafana, "SELECT count(*) FROM ops.tool_calls"); n != 1 {
		t.Errorf("tool_calls = %d rows, want only the outcome row", n)
	}
	if n := countPool(t, tdb.Grafana, "SELECT count(*) FROM ops.approvals_open"); n != 1 {
		t.Errorf("approvals_open = %d, want 1", n)
	}
	exec("UPDATE approvals SET status = 'expired', arguments = NULL")
	if n := countPool(t, tdb.Grafana, "SELECT count(*) FROM ops.approvals_open"); n != 0 {
		t.Errorf("approvals_open after expiry = %d, want 0", n)
	}
	var dur *float64
	if err := tdb.Grafana.QueryRow(ctx, "SELECT duration_s FROM ops.conversation_outcomes").Scan(&dur); err != nil || dur != nil {
		t.Errorf("duration_s of an open conversation = %v, %v; want null", dur, err)
	}
}

// covers: spec 0016 AC-8. The sync query always moves checked_at and moves
// changed_at only when the mode changes.
func TestSyncKillSwitchState(t *testing.T) {
	tdb := dbtest.New(t)
	ctx := t.Context()
	q := queries.New(tdb.ToolServer)
	read := func() (mode string, changed, checked time.Time) {
		t.Helper()
		if err := tdb.Owner.QueryRow(ctx, "SELECT mode, changed_at, checked_at FROM kill_switch_state").Scan(&mode, &changed, &checked); err != nil {
			t.Fatal(err)
		}
		return mode, changed, checked
	}
	if err := q.SyncKillSwitchState(ctx, "read_only"); err != nil {
		t.Fatal(err)
	}
	_, changed1, checked1 := read()
	if err := q.SyncKillSwitchState(ctx, "read_only"); err != nil {
		t.Fatal(err)
	}
	if _, changed, checked := read(); !changed.Equal(changed1) || !checked.After(checked1) {
		t.Errorf("same mode: changed_at moved or checked_at stayed (%v, %v)", changed, checked)
	}
	if err := q.SyncKillSwitchState(ctx, "read_write"); err != nil {
		t.Fatal(err)
	}
	if mode, changed, _ := read(); mode != "read_write" || !changed.After(changed1) {
		t.Errorf("flip: mode %s, changed_at %v; want read_write and a later time", mode, changed)
	}
}

// covers: spec 0016 AC-7. The insert query takes a null conversation.
func TestInsertAgentFailureQuery(t *testing.T) {
	tdb := dbtest.New(t)
	q := queries.New(tdb.Agent)
	if err := q.InsertAgentFailure(t.Context(), queries.InsertAgentFailureParams{Stage: "slack", Cause: "slack_ratelimited"}); err != nil {
		t.Fatal(err)
	}
	if n := countPool(t, tdb.Grafana, "SELECT count(*) FROM ops.failures WHERE stage = 'slack' AND conversation_id IS NULL"); n != 1 {
		t.Errorf("%d failure rows, want 1", n)
	}
}
