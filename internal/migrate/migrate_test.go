package migrate_test

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/toyinogun/AIITsupport/internal/db/dbtest"
	"github.com/toyinogun/AIITsupport/internal/migrate"
)

func TestLatestIsTheNewestFile(t *testing.T) {
	migrations, err := migrate.Migrations()
	if err != nil {
		t.Fatal(err)
	}
	names, err := fs.Glob(migrations, "*.sql")
	if err != nil || len(names) == 0 {
		t.Fatalf("list migrations: %v (%d files)", err, len(names))
	}
	latest, err := migrate.Latest()
	if err != nil {
		t.Fatal(err)
	}
	if want := fmt.Sprintf("%05d_", latest); !strings.HasPrefix(names[len(names)-1], want) {
		t.Errorf("Latest() = %d, but the newest file is %s", latest, names[len(names)-1])
	}
}

// covers: AC-9 (spec 0015). An unreachable database ends in ErrNotReady at
// the deadline, not an endless wait.
func TestRunStopsAtTheDeadline(t *testing.T) {
	start := time.Now()
	_, err := migrate.Run(t.Context(), "postgres://noord_owner@127.0.0.1:1/noord?connect_timeout=1", time.Second)
	if !errors.Is(err, migrate.ErrNotReady) {
		t.Fatalf("err = %v, want ErrNotReady", err)
	}
	if took := time.Since(start); took > 10*time.Second {
		t.Errorf("took %s, want about the 1s wait", took)
	}
}

func TestRunRefusesABadURL(t *testing.T) {
	_, err := migrate.Run(t.Context(), "not a url ::", time.Second)
	if err == nil || strings.Contains(err.Error(), "not a url") {
		t.Fatalf("err = %v, want an error that does not echo the value", err)
	}
}

// covers: AC-9 (spec 0015). With the roles present, Run migrates an empty
// database as the owner, and a second run applies nothing.
func TestRunMigratesThenIsANoOp(t *testing.T) {
	dbURL := emptyDatabase(t)
	first, err := migrate.Run(t.Context(), dbURL, 30*time.Second)
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	latest, err := migrate.Latest()
	if err != nil {
		t.Fatal(err)
	}
	if first.Version != latest || int64(len(first.Applied)) != latest {
		t.Errorf("first run = %+v, want every migration up to %d", first, latest)
	}
	second, err := migrate.Run(t.Context(), dbURL, 30*time.Second)
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if len(second.Applied) != 0 || second.Version != latest {
		t.Errorf("second run = %+v, want nothing applied", second)
	}
}

// emptyDatabase makes an unmigrated database owned by noord_owner and returns
// the owner's URL to it. dbtest.New runs first so the roles exist.
func emptyDatabase(t *testing.T) string {
	t.Helper()
	dbtest.New(t) // skips without TEST_DATABASE_URL

	raw := os.Getenv("TEST_DATABASE_URL")
	root, err := pgx.Connect(t.Context(), raw)
	if err != nil {
		t.Fatalf("connect as superuser: %v", err)
	}
	t.Cleanup(func() { _ = root.Close(context.Background()) })

	name := fmt.Sprintf("noord_migrate_job_%d", os.Getpid())
	ident := pgx.Identifier{name}.Sanitize()
	if _, err := root.Exec(t.Context(), "CREATE DATABASE "+ident+" OWNER "+dbtest.RoleOwner); err != nil {
		t.Fatalf("create %s: %v", name, err)
	}
	t.Cleanup(func() {
		_, _ = root.Exec(context.Background(), "DROP DATABASE IF EXISTS "+ident+" WITH (FORCE)")
	})

	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse TEST_DATABASE_URL: %v", err)
	}
	u.User = url.UserPassword(dbtest.RoleOwner, dbtest.DevPassword)
	u.Path = "/" + name
	return u.String()
}
