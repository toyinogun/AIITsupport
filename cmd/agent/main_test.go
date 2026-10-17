package main

import (
	"net"
	"strings"
	"testing"

	"github.com/toyinogun/AIITsupport/internal/db/dbtest"
)

// clearEnv blanks the settings run reads, so the developer's shell never leaks in.
// A blank value counts as unset in internal/config.
func clearEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{"AGENT_DATABASE_URL", "TOOLSERVER_TOKEN", "TOOLSERVER_URL", "OTEL_EXPORTER_OTLP_ENDPOINT", "LOG_LEVEL", "READ_ONLY", "AGENT_HTTP_ADDR"} {
		t.Setenv(name, "")
	}
}

// requiredEnv sets every required setting, pointing the database at databaseURL.
func requiredEnv(t *testing.T, databaseURL string) {
	t.Helper()
	t.Setenv("AGENT_DATABASE_URL", databaseURL)
	t.Setenv("TOOLSERVER_TOKEN", "test-token")
	t.Setenv("TOOLSERVER_URL", "http://localhost:18081/mcp")
}

// covers: SD-2
func TestRunRefusesToStartWithoutRequiredSettings(t *testing.T) {
	clearEnv(t)

	err := run()
	if err == nil {
		t.Fatal("run should refuse to start with no settings")
	}
	for _, name := range []string{"AGENT_DATABASE_URL", "TOOLSERVER_TOKEN", "TOOLSERVER_URL"} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("error %q does not name %s", err, name)
		}
	}
}

// covers: SD-2
func TestRunRefusesToStartWhenDatabaseIsUnreachable(t *testing.T) {
	clearEnv(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatalf("close listener: %v", err)
	}
	requiredEnv(t, "postgres://noord:x@"+addr+"/noord?sslmode=disable&connect_timeout=2")

	err = run()
	if err == nil || !strings.Contains(err.Error(), "reach database") {
		t.Fatalf("run = %v, want a reach database error", err)
	}
}

// Runs only when TEST_DATABASE_URL is set (see internal/db/dbtest).
func TestRunNamesTheAddressSettingWhenListenFails(t *testing.T) {
	tdb := dbtest.New(t)
	clearEnv(t)
	requiredEnv(t, tdb.URL(dbtest.RoleAgent))
	t.Setenv("AGENT_HTTP_ADDR", "not-an-address")

	err := run()
	if err == nil || !strings.Contains(err.Error(), "AGENT_HTTP_ADDR") {
		t.Fatalf("run = %v, want an error naming AGENT_HTTP_ADDR", err)
	}
}
