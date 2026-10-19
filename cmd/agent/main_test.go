package main

import (
	"net"
	"net/http"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/toyinogun/AIITsupport/internal/db/dbtest"
)

// clearEnv blanks the settings run reads, so the developer's shell never leaks in.
// A blank value counts as unset in internal/config.
func clearEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		"AGENT_DATABASE_URL", "TOOLSERVER_TOKEN", "TOOLSERVER_URL", "OTEL_EXPORTER_OTLP_ENDPOINT", "LOG_LEVEL", "READ_ONLY", "AGENT_HTTP_ADDR",
		"SLACK_BOT_TOKEN", "SLACK_APP_TOKEN", "LLM_PROVIDER", "LLM_MODEL", "ANTHROPIC_API_KEY", "AGENT_MAX_CONCURRENT",
		"SLACK_IT_CHANNEL",
	} {
		t.Setenv(name, "")
	}
}

// requiredEnv sets every required setting, pointing the database at databaseURL.
func requiredEnv(t *testing.T, databaseURL string) {
	t.Helper()
	t.Setenv("AGENT_DATABASE_URL", databaseURL)
	t.Setenv("TOOLSERVER_TOKEN", "test-token-0123456789abcdefghijk")
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

// covers: AC-13 (spec 0006). With both Slack tokens unset the agent starts
// and serves health. Runs only when TEST_DATABASE_URL is set.
func TestRunStartsWithoutSlack(t *testing.T) {
	tdb := dbtest.New(t)
	clearEnv(t)
	requiredEnv(t, tdb.URL(dbtest.RoleAgent))
	t.Setenv("SLACK_BOT_TOKEN", "")
	t.Setenv("SLACK_APP_TOKEN", "")
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	t.Setenv("AGENT_HTTP_ADDR", addr)

	done := make(chan error, 1)
	go func() { done <- run() }()

	var status int
	for range 50 {
		resp, err := http.Get("http://" + addr + "/healthz")
		if err == nil {
			status = resp.StatusCode
			_ = resp.Body.Close()
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if status != http.StatusOK {
		t.Errorf("healthz status = %d, want 200", status)
	}
	// run() stops on SIGTERM through its signal context.
	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Errorf("run = %v, want a clean stop", err)
	}
}

// covers: AC-13. Exactly one Slack token refuses to start, naming the other.
func TestRunRefusesOneSlackToken(t *testing.T) {
	clearEnv(t)
	requiredEnv(t, "postgres://unused")
	t.Setenv("SLACK_BOT_TOKEN", "xoxb-only-the-bot-token")
	t.Setenv("SLACK_APP_TOKEN", "")
	err := run()
	if err == nil || !strings.Contains(err.Error(), "SLACK_APP_TOKEN") || strings.Contains(err.Error(), "only-the-bot-token") {
		t.Fatalf("run = %v, want an error naming SLACK_APP_TOKEN without the value", err)
	}
}

// covers: AC-15, AC-16 (spec 0008). With Slack set, a model with no price,
// the unbuilt bedrock provider, a missing key, and a zero concurrency each
// stop startup with a clear message, before any connection is made.
func TestRunRefusesABadModelSetup(t *testing.T) {
	cases := map[string]struct {
		env  map[string]string
		want string
	}{
		"no price": {map[string]string{"LLM_MODEL": "claude-unpriced-1"}, "has no price in eval/pricing.yaml"},
		"bedrock":  {map[string]string{"LLM_PROVIDER": "bedrock"}, "LLM_PROVIDER bedrock is not built yet"},
		"no key":   {map[string]string{"ANTHROPIC_API_KEY": ""}, "ANTHROPIC_API_KEY is required"},
		"no slot":  {map[string]string{"AGENT_MAX_CONCURRENT": "0"}, "AGENT_MAX_CONCURRENT must be at least 1"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			clearEnv(t)
			requiredEnv(t, "postgres://unused")
			t.Setenv("SLACK_BOT_TOKEN", "xoxb-test")
			t.Setenv("SLACK_APP_TOKEN", "xapp-test")
			t.Setenv("SLACK_IT_CHANNEL", "C0ITDESK")
			t.Setenv("ANTHROPIC_API_KEY", "sk-ant-test")
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			err := run()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("run = %v, want an error containing %q", err, tc.want)
			}
			if strings.Contains(err.Error(), "sk-ant-test") {
				t.Errorf("the error leaks the key: %v", err)
			}
		})
	}
}
