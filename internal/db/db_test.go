package db

import (
	"net"
	"strings"
	"testing"

	"github.com/toyinogun/AIITsupport/internal/db/dbtest"
)

func TestOpenRejectsMalformedURL(t *testing.T) {
	pool, err := Open(t.Context(), "postgres://%zz")
	if err == nil || !strings.Contains(err.Error(), "parse database url") {
		t.Fatalf("Open = %v, want a parse database url error", err)
	}
	if pool != nil {
		t.Error("Open should return a nil pool on error")
	}
}

// covers: SD-2. An unreachable database stops the binary at startup, not on its first query.
func TestOpenFailsFastWhenDatabaseIsUnreachable(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	// The port is now free, so connections are refused.
	if err := ln.Close(); err != nil {
		t.Fatalf("close listener: %v", err)
	}

	pool, err := Open(t.Context(), "postgres://noord:x@"+addr+"/noord?sslmode=disable&connect_timeout=2")
	if err == nil || !strings.Contains(err.Error(), "reach database") {
		t.Fatalf("Open = %v, want a reach database error", err)
	}
	if pool != nil {
		t.Error("Open should return a nil pool on error")
	}
}

// covers: SD-1, AC-1 (spec 0003). Runs only when TEST_DATABASE_URL is a
// superuser connection URL, for example the `make db-up` one:
// TEST_DATABASE_URL=postgres://noord:noord-dev@localhost:55432/noord?sslmode=disable
func TestOpenPingsALiveDatabase(t *testing.T) {
	tdb := dbtest.New(t)

	pool, err := Open(t.Context(), tdb.URL(dbtest.RoleOwner))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer pool.Close()

	var applied int64
	if err := pool.QueryRow(t.Context(), "select max(version_id) from goose_db_version").Scan(&applied); err != nil {
		t.Fatalf("read goose_db_version: %v", err)
	}
	if applied < 2 {
		t.Errorf("goose version = %d, want at least 2 (the data model migration)", applied)
	}
}
