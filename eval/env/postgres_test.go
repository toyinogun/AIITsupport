package env_test

import (
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/toyinogun/AIITsupport/eval/env"
	noordldap "github.com/toyinogun/AIITsupport/internal/ldap"
)

func live(t *testing.T) {
	t.Helper()
	if os.Getenv("TEST_EVAL_ENV") == "" {
		t.Skip("set TEST_EVAL_ENV=1 to run the eval environment tests (needs Docker)")
	}
}

// covers: AC-2, AC-7 (spec 0018) and the spec's template probe. Cloning the
// migrated template works with the roles the migrations create, each clone
// starts empty, the app roles keep their grants, and noord_eval cannot delete.
func TestPostgresCloneStartsEmpty(t *testing.T) {
	live(t)
	ctx := t.Context()
	pg, err := env.StartPostgres(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pg.Close() })

	for i := range 2 {
		name, err := pg.NewAttemptDB(ctx)
		if err != nil {
			t.Fatalf("attempt %d: %v", i, err)
		}
		agent, err := pgx.Connect(ctx, pg.URL(env.RoleAgent, name))
		if err != nil {
			t.Fatalf("connect as the agent role: %v", err)
		}
		var n int
		if err := agent.QueryRow(ctx, "SELECT count(*) FROM conversations").Scan(&n); err != nil || n != 0 {
			t.Fatalf("attempt %d: conversations = %d, %v; want 0", i, n, err)
		}
		if _, err := agent.Exec(ctx, "INSERT INTO conversations (slack_channel_id, slack_thread_ts, requester_slack_id) VALUES ('D1','1.1','U1')"); err != nil {
			t.Fatalf("attempt %d: insert: %v", i, err)
		}
		_ = agent.Close(ctx)
		if err := pg.DropDatabase(ctx, name); err != nil {
			t.Fatal(err)
		}
	}

	results, err := pgx.Connect(ctx, pg.ResultsURL(env.RoleEval))
	if err != nil {
		t.Fatalf("connect to results as noord_eval: %v", err)
	}
	defer func() { _ = results.Close(ctx) }()
	if _, err := results.Exec(ctx, "DELETE FROM eval_results"); err == nil {
		t.Error("noord_eval deleted from eval_results")
	}
}

// covers: AC-2 (spec 0018). The throwaway Samba starts and seeds from the
// Noord file, and the tool server's account can bind to it.
func TestSambaStartsSeeded(t *testing.T) {
	live(t)
	s, err := env.StartSamba(t.Context(), "../..")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	conn, err := (noordldap.Dialer{URL: s.Dir.URL, RootCAs: s.Dir.RootCAs}).Bind(s.BindDN, s.BindPassword)
	if err != nil {
		t.Fatalf("bind as svc-itsupport: %v", err)
	}
	_ = conn.Close()
}

// covers: AC-2 (spec 0018) and the spec's Zammad probe. The helpdesk starts in
// its own Compose project, reaches the throwaway Samba, and seeds; the
// assistant token reads the seeded tickets.
func TestZammadStartsSeeded(t *testing.T) {
	live(t)
	ctx := t.Context()
	s, err := env.StartSamba(ctx, "../..")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	start := time.Now()
	z, err := env.StartZammad(ctx, "../..", s)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = z.Close() })
	t.Logf("zammad up and seeded in %s at %s", time.Since(start).Round(time.Second), z.URL)
}
