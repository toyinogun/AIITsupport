package db

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/toyinogun/AIITsupport/internal/db/dbtest"
)

const dashboardPath = "../../deploy/helm/noord/files/observability/dashboards/noord-ops.json"

type dashboardFile struct {
	UID      string `json:"uid"`
	Timezone string `json:"timezone"`
	Refresh  string `json:"refresh"`
	Time     struct {
		From string `json:"from"`
	} `json:"time"`
	Panels []struct {
		ID      int    `json:"id"`
		Title   string `json:"title"`
		Targets []struct {
			RawSQL string `json:"rawSql"`
		} `json:"targets"`
	} `json:"panels"`
}

func loadDashboard(t *testing.T) dashboardFile {
	t.Helper()
	raw, err := os.ReadFile(filepath.FromSlash(dashboardPath))
	if err != nil {
		t.Fatal(err)
	}
	var d dashboardFile
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatalf("parse dashboard: %v", err)
	}
	return d
}

var timeFilter = regexp.MustCompile(`\$__timeFilter\((\w+)\)`)

// asRange makes a panel query runnable outside Grafana: the macro becomes the
// default range, the last 24 hours.
func asRange(sql string) string {
	return timeFilter.ReplaceAllString(sql, "$1 >= now() - interval '24 hours' AND $1 <= now()")
}

// covers: spec 0016 AC-10, AC-11. The dashboard is code: its settings are the
// spec's, and every panel query runs as the view only role and reads nothing
// but the ops views.
func TestDashboardSettingsAndQueriesRunAsGrafana(t *testing.T) {
	d := loadDashboard(t)
	if d.UID != "noord-ops" || d.Timezone != "Europe/Amsterdam" || d.Refresh != "30s" || d.Time.From != "now-24h" {
		t.Errorf("dashboard settings = %+v, want noord-ops, Europe/Amsterdam, 30s, now-24h", d)
	}
	tdb := dbtest.New(t)
	queries := 0
	for _, p := range d.Panels {
		for _, tg := range p.Targets {
			queries++
			if regexp.MustCompile(`(?i)\b(public\.|FROM\s+(conversations|llm_usage|audit_log|approvals|agent_failures|kill_switch_state)\b)`).MatchString(tg.RawSQL) {
				t.Errorf("panel %q reads a table, not an ops view: %s", p.Title, tg.RawSQL)
			}
			if _, err := tdb.Grafana.Exec(t.Context(), asRange(tg.RawSQL)); err != nil {
				t.Errorf("panel %q: %v\n%s", p.Title, err, tg.RawSQL)
			}
		}
	}
	if queries < 12 {
		t.Errorf("%d panel queries, want a query for every tile and table", queries)
	}
}

func panelValues(t *testing.T, d dashboardFile, tdb *dbtest.DB, title string) []map[string]any {
	t.Helper()
	for _, p := range d.Panels {
		if p.Title != title {
			continue
		}
		rows, err := tdb.Grafana.Query(t.Context(), asRange(p.Targets[0].RawSQL))
		if err != nil {
			t.Fatalf("panel %q: %v", title, err)
		}
		out, err := pgx.CollectRows(rows, pgx.RowToMap)
		if err != nil {
			t.Fatalf("panel %q: %v", title, err)
		}
		return out
	}
	t.Fatalf("no panel %q", title)
	return nil
}

// covers: spec 0016 AC-6, AC-10. A closed ticket, a privileged refusal, an
// idle close, and an ordinary escalation are counted apart; cost and errors
// add up; a pending approval shows with no requester; the recent table has a
// trace id only where there is one.
func TestDashboardQueriesCountWhatTheyClaim(t *testing.T) {
	d := loadDashboard(t)
	tdb := dbtest.New(t)
	ctx := t.Context()
	exec := func(sql string) {
		t.Helper()
		if _, err := tdb.Owner.Exec(ctx, sql); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	exec(`INSERT INTO conversations (slack_channel_id, slack_thread_ts, requester_slack_id, status, closed_at, outcome, trace_id, created_at) VALUES
	 ('c','1','u','resolved', now() - interval '2 hours', 'resolved',  repeat('a', 32), now() - interval '3 hours'),
	 ('c','2','u','escalated',now() - interval '2 hours', 'escalated', NULL,             now() - interval '3 hours'),
	 ('c','3','u','escalated',now() - interval '2 hours', 'refused',   NULL,             now() - interval '3 hours'),
	 ('c','4','u','resolved', now() - interval '2 hours', 'abandoned', NULL,             now() - interval '3 hours'),
	 ('c','5','u','resolved', now() - interval '3 days',  'resolved',  NULL,             now() - interval '3 days')`)
	exec(`INSERT INTO llm_usage (conversation_id, model, cost_microusd, pricing_date)
	 SELECT id, 'm', 1500000, now() FROM conversations WHERE slack_thread_ts IN ('1','2')`)
	exec(`INSERT INTO agent_failures (stage, cause) VALUES ('llm','x'), ('slack','y')`)
	exec(`INSERT INTO audit_log (kind, tool_name, risk_tier, requester_slack_id, outcome) VALUES ('outcome','account_unlock','low','u','error'), ('outcome','account_unlock','low','u','ok')`)
	exec(`INSERT INTO approvals (call_id, tool_name, arguments, risk_tier, requester_slack_id, slack_channel_id, slack_thread_ts, approver_kind, allowed_approvers, expires_at)
	 VALUES (gen_random_uuid(), 'mfa_clear', '{}', 'medium', 'u', 'c', 't', 'it', '{x}', now() + interval '1 hour')`)

	for title, want := range map[string]int64{"Resolved": 1, "Escalated": 1, "Refused": 1, "Abandoned": 1, "Errors": 3} {
		rows := panelValues(t, d, tdb, title)
		if len(rows) != 1 {
			t.Fatalf("%s: %d rows", title, len(rows))
		}
		for _, v := range rows[0] {
			if v != want {
				t.Errorf("%s = %v, want %d (the 3 day old resolved row is outside the range)", title, v, want)
			}
		}
	}
	if rows := panelValues(t, d, tdb, "Cost"); rows[0]["cost_usd"] != 3.0 {
		t.Errorf("cost = %v, want 3", rows[0])
	}
	if rows := panelValues(t, d, tdb, "Cost per conversation"); rows[0]["cost_per_conversation"] != 1.5 {
		t.Errorf("cost per conversation = %v, want 1.5", rows[0])
	}
	if rows := panelValues(t, d, tdb, "Approvals waiting"); rows[0]["waiting"] != int64(1) {
		t.Errorf("approvals waiting = %v, want 1", rows[0])
	}
	pending := panelValues(t, d, tdb, "Pending approvals")
	if len(pending) != 1 {
		t.Fatalf("pending approvals = %v", pending)
	}
	for k := range pending[0] {
		if k == "requester" || k == "requester_slack_id" {
			t.Errorf("pending approvals shows %s", k)
		}
	}
	recent := panelValues(t, d, tdb, "Recent conversations")
	withTrace := 0
	for _, r := range recent {
		if r["trace"] != nil {
			withTrace++
		}
	}
	if len(recent) != 5 || withTrace != 1 {
		t.Errorf("recent conversations = %d rows, %d with a trace; want 5 and 1 (the range is ignored)", len(recent), withTrace)
	}
	if rows := panelValues(t, d, tdb, "Account changes"); len(rows) != 0 {
		t.Errorf("account changes with no state row = %v, want no rows (the tile says unknown)", rows)
	}
}

// covers: spec 0016 AC-10. With no model calls in the range, cost per
// conversation has no value (the tile says "no data"), not 0.
func TestDashboardCostPerConversationHasNoValueWithoutCalls(t *testing.T) {
	d := loadDashboard(t)
	tdb := dbtest.New(t)
	rows := panelValues(t, d, tdb, "Cost per conversation")
	if len(rows) != 1 || rows[0]["cost_per_conversation"] != nil {
		t.Errorf("cost per conversation = %v, want one row with null", rows)
	}
}
