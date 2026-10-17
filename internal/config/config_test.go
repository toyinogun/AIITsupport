package config

import (
	"bufio"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
)

// envFrom returns a lookup func backed by a map, so tests never touch the real environment.
func envFrom(m map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		v, ok := m[key]
		return v, ok
	}
}

func minimalAgentEnv() map[string]string {
	return map[string]string{
		"DATABASE_URL":     "postgres://u:p@localhost:5432/noord",
		"TOOLSERVER_URL":   "http://localhost:8080/mcp",
		"TOOLSERVER_TOKEN": "test-token",
	}
}

func minimalToolServerEnv() map[string]string {
	return map[string]string{
		"DATABASE_URL":     "postgres://u:p@localhost:5432/noord",
		"TOOLSERVER_TOKEN": "test-token",
	}
}

func TestLoadAgentDefaults(t *testing.T) {
	cfg, err := LoadAgent(envFrom(minimalAgentEnv()))
	if err != nil {
		t.Fatalf("LoadAgent: %v", err)
	}

	checks := []struct {
		name string
		got  any
		want any
	}{
		{"LLMProvider", cfg.LLMProvider, "anthropic"},
		{"LLMModel", cfg.LLMModel, "claude-sonnet-5-5"},
		{"MaxTurns", cfg.MaxTurns, 8},
		{"ToolTimeout", cfg.ToolTimeout, 20 * time.Second},
		{"MessageTimeout", cfg.MessageTimeout, 45 * time.Second},
		{"MaxRetries", cfg.MaxRetries, 2},
		{"MaxTokensPerTicket", cfg.MaxTokensPerTicket, 30000},
		{"HTTPAddr", cfg.HTTPAddr, ":8080"},
		{"LogLevel", cfg.LogLevel, "info"},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
		}
	}
}

func TestLoadToolServerDefaults(t *testing.T) {
	cfg, err := LoadToolServer(envFrom(minimalToolServerEnv()))
	if err != nil {
		t.Fatalf("LoadToolServer: %v", err)
	}
	if cfg.HTTPAddr != ":8080" {
		t.Errorf("HTTPAddr = %q, want :8080", cfg.HTTPAddr)
	}
	if !cfg.ReadOnly {
		t.Error("ReadOnly should default to true so the tool server boots fail closed")
	}
}

func TestLoadAgentReportsEveryMissingSetting(t *testing.T) {
	_, err := LoadAgent(envFrom(map[string]string{}))
	if err == nil {
		t.Fatal("expected an error for missing required settings")
	}
	for _, name := range []string{"DATABASE_URL", "TOOLSERVER_URL", "TOOLSERVER_TOKEN"} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("error %q does not name missing setting %s", err, name)
		}
	}
}

func TestLoadToolServerReportsMissingSetting(t *testing.T) {
	env := minimalToolServerEnv()
	delete(env, "TOOLSERVER_TOKEN")
	_, err := LoadToolServer(envFrom(env))
	if err == nil || !strings.Contains(err.Error(), "TOOLSERVER_TOKEN") {
		t.Fatalf("expected an error naming TOOLSERVER_TOKEN, got %v", err)
	}
}

func TestEmptyValueCountsAsMissing(t *testing.T) {
	env := minimalAgentEnv()
	env["DATABASE_URL"] = "   "
	_, err := LoadAgent(envFrom(env))
	if err == nil || !strings.Contains(err.Error(), "DATABASE_URL") {
		t.Fatalf("expected a blank DATABASE_URL to be rejected, got %v", err)
	}
}

func TestInvalidValuesAreRejected(t *testing.T) {
	cases := map[string]string{
		"AGENT_MAX_TURNS":    "eight",
		"AGENT_TOOL_TIMEOUT": "20",
		"LLM_PROVIDER":       "openai",
		"LOG_LEVEL":          "verbose",
	}
	for key, value := range cases {
		t.Run(key, func(t *testing.T) {
			env := minimalAgentEnv()
			env[key] = value
			_, err := LoadAgent(envFrom(env))
			if err == nil || !strings.Contains(err.Error(), key) {
				t.Fatalf("expected an error naming %s for value %q, got %v", key, value, err)
			}
		})
	}
}

func TestInvalidBoolIsRejected(t *testing.T) {
	env := minimalToolServerEnv()
	env["READ_ONLY"] = "maybe"
	_, err := LoadToolServer(envFrom(env))
	if err == nil || !strings.Contains(err.Error(), "READ_ONLY") {
		t.Fatalf("expected an error naming READ_ONLY, got %v", err)
	}
}

func TestListValuesAreSplitAndTrimmed(t *testing.T) {
	env := minimalAgentEnv()
	env["SLACK_IT_APPROVERS"] = "U01, U02 ,,U03"
	cfg, err := LoadAgent(envFrom(env))
	if err != nil {
		t.Fatalf("LoadAgent: %v", err)
	}
	want := []string{"U01", "U02", "U03"}
	if !slices.Equal(cfg.SlackITApprovers, want) {
		t.Errorf("SlackITApprovers = %v, want %v", cfg.SlackITApprovers, want)
	}
}

func TestOverridesWin(t *testing.T) {
	env := minimalAgentEnv()
	env["AGENT_MAX_TURNS"] = "3"
	env["AGENT_HTTP_ADDR"] = ":9000"
	cfg, err := LoadAgent(envFrom(env))
	if err != nil {
		t.Fatalf("LoadAgent: %v", err)
	}
	if cfg.MaxTurns != 3 || cfg.HTTPAddr != ":9000" {
		t.Errorf("overrides ignored: MaxTurns=%d HTTPAddr=%q", cfg.MaxTurns, cfg.HTTPAddr)
	}
}

// TestEnvExampleMatchesConfig fails when .env.example and this package drift apart,
// so a setting is never named in one place only.
func TestEnvExampleMatchesConfig(t *testing.T) {
	f, err := os.Open("../../.env.example")
	if err != nil {
		t.Fatalf("open .env.example: %v", err)
	}
	defer f.Close()

	var inExample []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, _, ok := strings.Cut(line, "=")
		if !ok {
			t.Fatalf(".env.example line without '=': %q", line)
		}
		inExample = append(inExample, strings.TrimSpace(name))
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("read .env.example: %v", err)
	}

	known := Names()
	for _, name := range known {
		if !slices.Contains(inExample, name) {
			t.Errorf("%s is in internal/config but missing from .env.example", name)
		}
	}
	for _, name := range inExample {
		if !slices.Contains(known, name) {
			t.Errorf("%s is in .env.example but unknown to internal/config", name)
		}
	}
}
