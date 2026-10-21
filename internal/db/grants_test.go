package db

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/toyinogun/AIITsupport/internal/db/dbtest"
)

// tableProbe holds statements that touch no rows, so a test sees only the
// permission check (and no trigger or constraint fires).
type tableProbe struct {
	insert     string
	grantedCol string // a column some role may update
	lockedCol  string // a column no app role may update
}

var probes = map[string]tableProbe{
	"conversations": {
		insert:     "INSERT INTO conversations (slack_channel_id, slack_thread_ts, requester_slack_id) SELECT 'c', 't', 'u' WHERE false",
		grantedCol: "status", lockedCol: "requester_slack_id",
	},
	"conversation_messages": {
		insert:     "INSERT INTO conversation_messages (conversation_id, seq, role) SELECT 1, 1, 'user' WHERE false",
		grantedCol: "content", lockedCol: "role",
	},
	"llm_usage": {
		insert:    "INSERT INTO llm_usage (conversation_id, model, cost_microusd, pricing_date) SELECT 1, 'm', 0, current_date WHERE false",
		lockedCol: "model",
	},
	"slack_events": {
		insert:    "INSERT INTO slack_events (event_id) SELECT 'e' WHERE false",
		lockedCol: "received_at",
	},
	"approvals": {
		insert: "INSERT INTO approvals (call_id, tool_name, arguments, risk_tier, requester_slack_id, slack_channel_id, " +
			"slack_thread_ts, approver_kind, allowed_approvers, expires_at) " +
			"SELECT gen_random_uuid(), 't', '{}', 'medium', 'u', 'c', 't', 'it', '{u}', now() WHERE false",
		grantedCol: "status", lockedCol: "tool_name",
	},
	"audit_log": {
		insert:    "INSERT INTO audit_log (kind, tool_name, risk_tier, requester_slack_id) SELECT 'intent', 't', 'read', 'u' WHERE false",
		lockedCol: "outcome",
	},
	"eval_runs": {
		insert:     "INSERT INTO eval_runs (git_sha, model, ticket_set_hash) SELECT 's', 'm', 'h' WHERE false",
		grantedCol: "status", lockedCol: "git_sha",
	},
	"reset_links": {
		insert: "INSERT INTO reset_links (token_hash, directory_account, requester_slack_id, slack_channel_id, " +
			"slack_thread_ts, zammad_ticket_id, issue_call_id, expires_at) " +
			"SELECT '\\x00', 'a', 'u', 'c', 't', 1, gen_random_uuid(), now() WHERE false",
		grantedCol: "status", lockedCol: "token_hash",
	},
	"approval_cards": {
		insert: "INSERT INTO approval_cards (approval_id, conversation_id, tool_name, slack_channel_id, expires_at) " +
			"SELECT gen_random_uuid(), 1, 't', 'c', now() WHERE false",
		grantedCol: "card_ts", lockedCol: "conversation_id",
	},
	"eval_results": {
		insert: "INSERT INTO eval_results (run_id, ticket_key, expected_outcome, passed, unsafe_action, wrong_action, " +
			"duration_ms, input_tokens, output_tokens, cost_microusd) SELECT 1, 'k', 'resolve', true, false, false, 0, 0, 0, 0 WHERE false",
		lockedCol: "passed",
	},
}

// access is one cell of the spec 0003 grants table.
type access struct {
	sel, ins, del bool
	update        bool // UPDATE on the table's grantedCol
}

// grants is the spec 0003 Security model (plus spec 0011's reset_links and
// spec 0012's approval_cards; spec 0013's column grants are columnGrants),
// cell by cell. Missing means none.
var grants = map[string]map[string]access{
	dbtest.RoleAgent: {
		"conversations":         {sel: true, ins: true, update: true},
		"conversation_messages": {sel: true, ins: true, update: true},
		"llm_usage":             {sel: true, ins: true},
		"slack_events":          {sel: true, ins: true, del: true},
		"approval_cards":        {sel: true, ins: true, update: true},
	},
	dbtest.RoleToolServer: {
		"approvals":   {sel: true, ins: true, update: true},
		"audit_log":   {sel: true, ins: true},
		"reset_links": {sel: true, ins: true, del: true, update: true},
	},
	dbtest.RoleEval: {
		"audit_log":     {sel: true},
		"conversations": {sel: true},
		"llm_usage":     {sel: true},
		"eval_runs":     {sel: true, ins: true, update: true},
		"eval_results":  {sel: true, ins: true},
	},
}

const insufficientPrivilege = "42501"

func pgCode(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}

// covers: AC-4 (spec 0003). Every cell of the grants table, both ways.
func TestEachRoleReachesOnlyItsGrantedTables(t *testing.T) {
	tdb := dbtest.New(t)
	pools := map[string]*pgxpool.Pool{
		dbtest.RoleAgent: tdb.Agent, dbtest.RoleToolServer: tdb.ToolServer, dbtest.RoleEval: tdb.Eval,
	}
	tables := sortedKeys(probes)
	for _, role := range sortedKeys(pools) {
		for _, table := range tables {
			p, want := probes[table], grants[role][table]
			checks := []struct {
				op      string
				sql     string
				allowed bool
			}{
				{"select", "SELECT count(*) FROM " + table, want.sel},
				{"insert", p.insert, want.ins},
				{"delete", "DELETE FROM " + table + " WHERE false", want.del},
				{"update " + p.lockedCol, fmt.Sprintf("UPDATE %s SET %s = %[2]s WHERE false", table, p.lockedCol), false},
			}
			if p.grantedCol != "" {
				checks = append(checks, struct {
					op      string
					sql     string
					allowed bool
				}{"update " + p.grantedCol, fmt.Sprintf("UPDATE %s SET %s = %[2]s WHERE false", table, p.grantedCol), want.update})
			}
			for _, c := range checks {
				_, err := pools[role].Exec(t.Context(), c.sql)
				denied := pgCode(err) == insufficientPrivilege
				switch {
				case c.allowed && err != nil:
					t.Errorf("%s %s %s: want allowed, got %v", role, c.op, table, err)
				case !c.allowed && !denied:
					t.Errorf("%s %s %s: want permission denied (42501), got %v", role, c.op, table, err)
				}
			}
		}
	}
}

// columnGrants are the column UPDATE grants spec 0013 added (migration
// 00006): who may move a column, and that no other app role may.
var columnGrants = []struct {
	table, column string
	role          string // the one app role allowed; empty for none
}{
	{"approvals", "decider_slack_id", dbtest.RoleToolServer},
	{"approvals", "target", ""},
	{"approval_cards", "slack_channel_id", dbtest.RoleAgent},
	{"approval_cards", "approver_slack_id", dbtest.RoleAgent},
}

// covers: AC-8, AC-9 (spec 0013). The new columns' UPDATE grants, both ways.
func TestAccessRequestColumnGrants(t *testing.T) {
	tdb := dbtest.New(t)
	pools := map[string]*pgxpool.Pool{
		dbtest.RoleAgent: tdb.Agent, dbtest.RoleToolServer: tdb.ToolServer, dbtest.RoleEval: tdb.Eval,
	}
	for _, g := range columnGrants {
		for _, role := range sortedKeys(pools) {
			sql := fmt.Sprintf("UPDATE %s SET %s = %[2]s WHERE false", g.table, g.column)
			_, err := pools[role].Exec(t.Context(), sql)
			switch denied := pgCode(err) == insufficientPrivilege; {
			case role == g.role && err != nil:
				t.Errorf("%s update %s.%s: want allowed, got %v", role, g.table, g.column, err)
			case role != g.role && !denied:
				t.Errorf("%s update %s.%s: want permission denied (42501), got %v", role, g.table, g.column, err)
			}
		}
	}
}

// covers: AC-3 (spec 0003)
func TestAuditRowsCannotBeChangedByAnyRole(t *testing.T) {
	tdb := dbtest.New(t)
	_, err := tdb.ToolServer.Exec(t.Context(),
		"INSERT INTO audit_log (kind, tool_name, risk_tier, requester_slack_id) VALUES ('intent', 'user_lookup', 'read', 'U1')")
	if err != nil {
		t.Fatalf("insert as noord_toolserver: %v", err)
	}
	changes := []string{
		"UPDATE audit_log SET target = 'someone else'",
		"DELETE FROM audit_log",
		"TRUNCATE audit_log",
	}
	for _, sql := range changes {
		if _, err := tdb.ToolServer.Exec(t.Context(), sql); pgCode(err) != insufficientPrivilege {
			t.Errorf("noord_toolserver %q: want 42501, got %v", sql, err)
		}
		// The owner holds every privilege, so only the trigger stops it.
		_, err := tdb.Owner.Exec(t.Context(), sql)
		if pgCode(err) != "55000" || !strings.Contains(err.Error(), "audit_log is append only") {
			t.Errorf("noord_owner %q: want the append only trigger (55000), got %v", sql, err)
		}
	}

	var rows int
	if err := tdb.ToolServer.QueryRow(t.Context(), "SELECT count(*) FROM audit_log").Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("audit rows = %d, %v; want the one row untouched", rows, err)
	}
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
