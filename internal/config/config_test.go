package config

import (
	"bufio"
	"maps"
	"os"
	"reflect"
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
		"AGENT_DATABASE_URL": "postgres://u:p@localhost:5432/noord",
		"TOOLSERVER_URL":     "http://localhost:8080/mcp",
		"TOOLSERVER_TOKEN":   testToolServerToken,
	}
}

// testToolServerToken is 32 characters, the shortest either binary accepts.
const testToolServerToken = "test-token-0123456789abcdefghijk"

func minimalToolServerEnv() map[string]string {
	return map[string]string{
		"TOOLSERVER_DATABASE_URL": "postgres://u:p@localhost:5432/noord",
		"TOOLSERVER_TOKEN":        testToolServerToken,
		"LDAP_URL":                "ldaps://localhost:55636",
		"LDAP_BASE_DN":            "DC=ad,DC=noord,DC=example",
		"LDAP_BIND_DN":            "CN=svc-itsupport,OU=Service Accounts,OU=Noord,DC=ad,DC=noord,DC=example",
		"LDAP_BIND_PASSWORD":      "bind-test",
		"LDAP_CA_CERT":            ".dev/certs/ca.pem",
		"ZAMMAD_URL":              "http://127.0.0.1:58080",
		"ZAMMAD_TOKEN":            "dev-only-zammad-token-change-me-0001",
		"ZAMMAD_DEFAULT_GROUP":    "IT Support",
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
		{"MaxConcurrent", cfg.MaxConcurrent, 4},
		{"MaxResponseTokens", cfg.MaxResponseTokens, 1024},
		{"ShutdownGrace", cfg.ShutdownGrace, 25 * time.Second},
		{"ConversationsPerHour", cfg.ConversationsPerHour, 10},
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
	for _, name := range []string{"AGENT_DATABASE_URL", "TOOLSERVER_URL", "TOOLSERVER_TOKEN"} {
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
	env["AGENT_DATABASE_URL"] = "   "
	_, err := LoadAgent(envFrom(env))
	if err == nil || !strings.Contains(err.Error(), "AGENT_DATABASE_URL") {
		t.Fatalf("expected a blank AGENT_DATABASE_URL to be rejected, got %v", err)
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
	env := minimalToolServerEnv()
	env["SLACK_IT_APPROVERS"] = "U0000000001, U0000000002 ,,W0000000003"
	cfg, err := LoadToolServer(envFrom(env))
	if err != nil {
		t.Fatalf("LoadToolServer: %v", err)
	}
	want := []string{"U0000000001", "U0000000002", "W0000000003"}
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

// covers: SD-5
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

// covers: SD-2. One start attempt shows every problem, missing and malformed alike.
func TestLoadAgentJoinsMissingAndInvalidSettings(t *testing.T) {
	_, err := LoadAgent(envFrom(map[string]string{"AGENT_MAX_TURNS": "eight"}))
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, want := range []string{"AGENT_DATABASE_URL", "TOOLSERVER_TOKEN", "TOOLSERVER_URL", "AGENT_MAX_TURNS"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %s", err, want)
		}
	}
}

// covers: SD-2
func TestLoadToolServerReportsEveryMissingSetting(t *testing.T) {
	_, err := LoadToolServer(envFrom(map[string]string{}))
	if err == nil {
		t.Fatal("expected an error for missing required settings")
	}
	for _, name := range []string{
		"TOOLSERVER_DATABASE_URL", "TOOLSERVER_TOKEN", "LDAP_URL", "LDAP_BASE_DN", "LDAP_BIND_DN",
		"LDAP_BIND_PASSWORD", "LDAP_CA_CERT", "ZAMMAD_URL", "ZAMMAD_TOKEN", "ZAMMAD_DEFAULT_GROUP",
	} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("error %q does not name missing setting %s", err, name)
		}
	}
	if strings.Contains(err.Error(), "TOOLSERVER_URL") {
		t.Errorf("tool server does not read TOOLSERVER_URL, but the error names it: %q", err)
	}
}

// covers: SD-2. The prefix tells an operator which binary refused to start.
func TestErrorsNameTheBinary(t *testing.T) {
	_, agentErr := LoadAgent(envFrom(map[string]string{}))
	_, toolErr := LoadToolServer(envFrom(map[string]string{}))

	if agentErr == nil || !strings.HasPrefix(agentErr.Error(), "agent config: ") {
		t.Errorf("agent error = %v, want prefix %q", agentErr, "agent config: ")
	}
	if toolErr == nil || !strings.HasPrefix(toolErr.Error(), "toolserver config: ") {
		t.Errorf("toolserver error = %v, want prefix %q", toolErr, "toolserver config: ")
	}
}

func TestFailedLoadReturnsZeroConfig(t *testing.T) {
	env := minimalAgentEnv()
	env["AGENT_MAX_TURNS"] = "eight"
	cfg, err := LoadAgent(envFrom(env))
	if err == nil {
		t.Fatal("expected an error")
	}
	if cfg.DatabaseURL != "" || cfg.MaxTurns != 0 {
		t.Errorf("a failed load should return a zero config, got DatabaseURL=%q MaxTurns=%d", cfg.DatabaseURL, cfg.MaxTurns)
	}
}

func TestBlankOptionalValueFallsBackToDefault(t *testing.T) {
	env := minimalAgentEnv()
	env["AGENT_MAX_TURNS"] = "   "
	cfg, err := LoadAgent(envFrom(env))
	if err != nil {
		t.Fatalf("LoadAgent: %v", err)
	}
	if cfg.MaxTurns != 8 {
		t.Errorf("MaxTurns = %d, want the default 8", cfg.MaxTurns)
	}
}

func TestValuesAreTrimmed(t *testing.T) {
	env := minimalAgentEnv()
	env["AGENT_DATABASE_URL"] = "  postgres://u:p@db:5432/noord  "
	cfg, err := LoadAgent(envFrom(env))
	if err != nil {
		t.Fatalf("LoadAgent: %v", err)
	}
	if cfg.DatabaseURL != "postgres://u:p@db:5432/noord" {
		t.Errorf("DatabaseURL = %q, want surrounding spaces trimmed", cfg.DatabaseURL)
	}
}

func TestUnsetSettingWithoutDefaultStaysZero(t *testing.T) {
	cfg, err := LoadToolServer(envFrom(minimalToolServerEnv()))
	if err != nil {
		t.Fatalf("LoadToolServer: %v", err)
	}
	if cfg.SlackITApprovers != nil || cfg.KillSwitchFile != "" {
		t.Errorf("unset settings should stay zero, got SlackITApprovers=%v KillSwitchFile=%q",
			cfg.SlackITApprovers, cfg.KillSwitchFile)
	}
	if cfg.TicketRetentionDays != 30 {
		t.Errorf("TicketRetentionDays = %d, want the default 30 (spec 0010)", cfg.TicketRetentionDays)
	}
}

func TestReadOnlyCanBeTurnedOff(t *testing.T) {
	env := minimalToolServerEnv()
	env["READ_ONLY"] = "false"
	cfg, err := LoadToolServer(envFrom(env))
	if err != nil {
		t.Fatalf("LoadToolServer: %v", err)
	}
	if cfg.ReadOnly {
		t.Error("READ_ONLY=false should turn the boot default off")
	}
}

func TestDurationOverrideParses(t *testing.T) {
	env := minimalAgentEnv()
	env["AGENT_MESSAGE_TIMEOUT"] = "1m30s"
	cfg, err := LoadAgent(envFrom(env))
	if err != nil {
		t.Fatalf("LoadAgent: %v", err)
	}
	if cfg.MessageTimeout != 90*time.Second {
		t.Errorf("MessageTimeout = %v, want 1m30s", cfg.MessageTimeout)
	}
}

func TestEveryAllowedChoiceIsAccepted(t *testing.T) {
	cases := map[string][]string{
		"LOG_LEVEL":    {"debug", "info", "warn", "error"},
		"LLM_PROVIDER": {"anthropic", "bedrock"},
	}
	for key, values := range cases {
		for _, value := range values {
			t.Run(key+"="+value, func(t *testing.T) {
				env := minimalAgentEnv()
				env[key] = value
				if _, err := LoadAgent(envFrom(env)); err != nil {
					t.Errorf("LoadAgent with %s=%s: %v", key, value, err)
				}
			})
		}
	}
}

// A future setting with a type the parser cannot handle must fail loudly, not load as zero.
func TestUnsupportedFieldTypeIsAnError(t *testing.T) {
	var cfg struct {
		Ratio float64 `env:"RATIO" default:"1.5"`
	}
	err := load(&cfg, envFrom(map[string]string{}))
	if err == nil || !strings.Contains(err.Error(), "unsupported setting type") {
		t.Fatalf("expected an unsupported type error, got %v", err)
	}
}

func TestNamesAreSortedUniqueAndComplete(t *testing.T) {
	names := Names()

	if !slices.IsSorted(names) {
		t.Errorf("Names() is not sorted: %v", names)
	}
	if len(slices.Compact(slices.Clone(names))) != len(names) {
		t.Errorf("Names() has duplicates: %v", names)
	}
	for _, want := range []string{"AGENT_DATABASE_URL", "TOOLSERVER_DATABASE_URL", "MIGRATE_DATABASE_URL", "AGENT_MAX_TURNS", "TOOLSERVER_URL", "READ_ONLY", "LDAP_BIND_PASSWORD"} {
		if !slices.Contains(names, want) {
			t.Errorf("Names() is missing %s", want)
		}
	}
	if slices.Contains(names, "") {
		t.Error("Names() contains an empty name")
	}
}

// The spec keeps every client system credential in the tool server. If an
// LDAP, Graph, or Zammad setting ever lands on the agent, this fails.
func TestAgentNeverReceivesClientSystemCredentials(t *testing.T) {
	var agentNames []string
	walkFields(reflect.TypeFor[Agent](), func(f reflect.StructField) {
		agentNames = append(agentNames, f.Tag.Get("env"))
	})
	for _, name := range agentNames {
		for _, prefix := range []string{"LDAP_", "GRAPH_", "ZAMMAD_"} {
			if strings.HasPrefix(name, prefix) {
				t.Errorf("agent config declares %s; client system credentials belong to the tool server only", name)
			}
		}
	}
}

// covers: AC-14 (spec 0003). Each binary connects as its own role, so each
// reads its own URL and never the other's.
func TestEachBinaryReadsItsOwnDatabaseURL(t *testing.T) {
	env := minimalAgentEnv()
	maps.Copy(env, minimalToolServerEnv())
	env["AGENT_DATABASE_URL"] = "postgres://noord_agent@db/noord"
	env["TOOLSERVER_DATABASE_URL"] = "postgres://noord_toolserver@db/noord"

	agent, err := LoadAgent(envFrom(env))
	if err != nil {
		t.Fatalf("LoadAgent: %v", err)
	}
	tool, err := LoadToolServer(envFrom(env))
	if err != nil {
		t.Fatalf("LoadToolServer: %v", err)
	}
	if agent.DatabaseURL != env["AGENT_DATABASE_URL"] {
		t.Errorf("agent DatabaseURL = %q, want the AGENT_DATABASE_URL value", agent.DatabaseURL)
	}
	if tool.DatabaseURL != env["TOOLSERVER_DATABASE_URL"] {
		t.Errorf("tool server DatabaseURL = %q, want the TOOLSERVER_DATABASE_URL value", tool.DatabaseURL)
	}
	if slices.Contains(Names(), "DATABASE_URL") {
		t.Error("the shared DATABASE_URL should be gone; each binary has its own")
	}
}

func TestLoadSeed(t *testing.T) {
	t.Run("defaults", func(t *testing.T) {
		cfg, err := LoadSeed(envFrom(map[string]string{
			"SEED_ADMIN_PASSWORD": "admin-test",
			"SEED_USER_PASSWORD":  "user-test",
		}))
		if err != nil {
			t.Fatalf("LoadSeed: %v", err)
		}
		checks := []struct {
			name string
			got  any
			want any
		}{
			{"LDAPURL", cfg.LDAPURL, "ldaps://localhost:55636"},
			{"CACert", cfg.CACert, ".dev/certs/ca.pem"},
			{"AdminUser", cfg.AdminUser, "CN=Administrator,CN=Users,DC=ad,DC=noord,DC=example"},
			{"File", cfg.File, "seed/directory/noord.yaml"},
			{"OverlayFile", cfg.OverlayFile, "seed/directory/overlay.local.yaml"},
			{"WaitTimeout", cfg.WaitTimeout, 90 * time.Second},
		}
		for _, c := range checks {
			if c.got != c.want {
				t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
			}
		}
	})

	t.Run("both passwords required", func(t *testing.T) {
		_, err := LoadSeed(envFrom(map[string]string{}))
		if err == nil {
			t.Fatal("LoadSeed with no passwords: want an error")
		}
		for _, name := range []string{"SEED_ADMIN_PASSWORD", "SEED_USER_PASSWORD"} {
			if !strings.Contains(err.Error(), name) {
				t.Errorf("error %q does not name %s", err, name)
			}
		}
		if !strings.HasPrefix(err.Error(), "seed config: ") {
			t.Errorf("error = %q, want prefix %q", err, "seed config: ")
		}
	})
}

// covers: AC-14 (spec 0004). seed validate loads only the file paths, so it
// runs with no passwords set.
func TestLoadSeedFiles(t *testing.T) {
	t.Run("defaults with no passwords set", func(t *testing.T) {
		files, err := LoadSeedFiles(envFrom(map[string]string{}))
		if err != nil {
			t.Fatalf("LoadSeedFiles: %v", err)
		}
		if files.File != "seed/directory/noord.yaml" {
			t.Errorf("File = %q, want the committed seed", files.File)
		}
		if files.OverlayFile != "seed/directory/overlay.local.yaml" {
			t.Errorf("OverlayFile = %q, want the gitignored overlay", files.OverlayFile)
		}
	})

	t.Run("overrides win", func(t *testing.T) {
		files, err := LoadSeedFiles(envFrom(map[string]string{
			"SEED_FILE":         "scratch/seed.yaml",
			"SEED_OVERLAY_FILE": "scratch/overlay.yaml",
		}))
		if err != nil {
			t.Fatalf("LoadSeedFiles: %v", err)
		}
		if files.File != "scratch/seed.yaml" || files.OverlayFile != "scratch/overlay.yaml" {
			t.Errorf("files = %+v, want the overrides", files)
		}
	})
}

// The Administrator password is cmd/seed's alone: neither long running binary
// may declare a SEED_ setting (spec 0004).
func TestOnlyTheSeederReadsSeedSettings(t *testing.T) {
	for _, typ := range []reflect.Type{reflect.TypeFor[Agent](), reflect.TypeFor[ToolServer]()} {
		walkFields(typ, func(f reflect.StructField) {
			if name := f.Tag.Get("env"); strings.HasPrefix(name, "SEED_") {
				t.Errorf("%s declares %s; seed settings belong to cmd/seed only", typ.Name(), name)
			}
		})
	}
}

func seedPasswords() map[string]string {
	return map[string]string{"SEED_ADMIN_PASSWORD": "admin-test", "SEED_USER_PASSWORD": "user-test"}
}

// covers: AC-13 (spec 0005). No SEED_GRAPH_* means Entra is not configured,
// which is fine; a partial set is an error naming every missing setting.
func TestLoadSeedEntra(t *testing.T) {
	t.Run("unconfigured with defaults", func(t *testing.T) {
		cfg, err := LoadSeed(envFrom(seedPasswords()))
		if err != nil {
			t.Fatalf("LoadSeed: %v", err)
		}
		if cfg.EntraConfigured() {
			t.Error("EntraConfigured() = true with no SEED_GRAPH_* set")
		}
		if cfg.EntraDeleteLimit != 10 || cfg.GraphMaxRetries != 5 || cfg.GraphTimeout != 30*time.Second {
			t.Errorf("limits = %d, %d, %s; want 10, 5, 30s", cfg.EntraDeleteLimit, cfg.GraphMaxRetries, cfg.GraphTimeout)
		}
		if cfg.EntraDemoFile != "seed/entra/demo.local.yaml" {
			t.Errorf("EntraDemoFile = %q, want the gitignored demo file", cfg.EntraDemoFile)
		}
	})

	t.Run("partly set names what is missing", func(t *testing.T) {
		env := seedPasswords()
		env["SEED_GRAPH_CLIENT_ID"] = "client"
		_, err := LoadSeed(envFrom(env))
		if err == nil {
			t.Fatal("LoadSeed with a partial SEED_GRAPH_* set: want an error")
		}
		for _, name := range []string{"SEED_GRAPH_TENANT_ID", "SEED_GRAPH_CLIENT_SECRET", "SEED_ENTRA_DOMAIN"} {
			if !strings.Contains(err.Error(), name) {
				t.Errorf("error %q does not name %s", err, name)
			}
		}
		if strings.Contains(err.Error(), "SEED_GRAPH_CLIENT_ID is required") {
			t.Errorf("error %q names a setting that is set", err)
		}
	})

	t.Run("fully set", func(t *testing.T) {
		env := seedPasswords()
		maps.Copy(env, map[string]string{
			"SEED_GRAPH_TENANT_ID": "tenant", "SEED_GRAPH_CLIENT_ID": "client",
			"SEED_GRAPH_CLIENT_SECRET": "secret", "SEED_ENTRA_DOMAIN": "noordtest.onmicrosoft.com",
		})
		cfg, err := LoadSeed(envFrom(env))
		if err != nil {
			t.Fatalf("LoadSeed: %v", err)
		}
		if !cfg.EntraConfigured() {
			t.Error("EntraConfigured() = false with every SEED_GRAPH_* set")
		}
	})

	t.Run("negative delete limit", func(t *testing.T) {
		env := seedPasswords()
		maps.Copy(env, map[string]string{
			"SEED_GRAPH_TENANT_ID": "tenant", "SEED_GRAPH_CLIENT_ID": "client",
			"SEED_GRAPH_CLIENT_SECRET": "secret", "SEED_ENTRA_DOMAIN": "noordtest.onmicrosoft.com",
			"SEED_ENTRA_DELETE_LIMIT": "-1",
		})
		if _, err := LoadSeed(envFrom(env)); err == nil || !strings.Contains(err.Error(), "SEED_ENTRA_DELETE_LIMIT") {
			t.Errorf("LoadSeed = %v, want an error naming SEED_ENTRA_DELETE_LIMIT", err)
		}
	})
}

// covers: AC-9, AC-13 (spec 0005). The tool server boots with GRAPH_* empty;
// LoadGraph and the MFA proof require every credential.
func TestLoadGraph(t *testing.T) {
	if _, err := LoadToolServer(envFrom(minimalToolServerEnv())); err != nil {
		t.Fatalf("LoadToolServer with GRAPH_* empty: %v", err)
	}

	_, err := LoadGraph(envFrom(map[string]string{"GRAPH_TENANT_ID": "tenant"}))
	if err == nil {
		t.Fatal("LoadGraph with a partial set: want an error")
	}
	for _, name := range []string{"GRAPH_CLIENT_ID", "GRAPH_CLIENT_SECRET"} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("error %q does not name %s", err, name)
		}
	}

	full := map[string]string{"GRAPH_TENANT_ID": "tenant", "GRAPH_CLIENT_ID": "client", "GRAPH_CLIENT_SECRET": "secret"}
	g, err := LoadGraph(envFrom(full))
	if err != nil {
		t.Fatalf("LoadGraph: %v", err)
	}
	if g.GraphMaxRetries != 5 || g.GraphTimeout != 30*time.Second {
		t.Errorf("defaults = %d, %s; want 5, 30s", g.GraphMaxRetries, g.GraphTimeout)
	}

	mfa, err := LoadSeedEntraMFA(envFrom(full))
	if err != nil {
		t.Fatalf("LoadSeedEntraMFA: %v", err)
	}
	if mfa.ClearWait != 30*time.Second || mfa.GraphClientID != "client" {
		t.Errorf("mfa = %+v, want the tool server app and a 30s wait", mfa)
	}
	if _, err := LoadSeedEntraMFA(envFrom(map[string]string{})); err == nil || !strings.Contains(err.Error(), "GRAPH_TENANT_ID") {
		t.Errorf("LoadSeedEntraMFA with nothing set = %v, want an error naming GRAPH_TENANT_ID", err)
	}
}

// covers: AC-13 (spec 0006). Both Slack tokens or neither; each with its prefix.
func TestLoadAgentSlackTokens(t *testing.T) {
	cases := []struct {
		name      string
		bot, app  string
		wantErr   []string
		wantSlack bool
	}{
		{name: "neither set", wantSlack: false},
		{name: "both set", bot: "xoxb-1-2-abc", app: "xapp-1-A1-abc", wantSlack: true},
		{name: "only bot", bot: "xoxb-1-2-abc", wantErr: []string{"SLACK_APP_TOKEN is required"}},
		{name: "only app", app: "xapp-1-A1-abc", wantErr: []string{"SLACK_BOT_TOKEN is required"}},
		{name: "wrong prefixes", bot: "xoxp-secretvalue", app: "xoxb-secretvalue", wantErr: []string{"SLACK_BOT_TOKEN must", "SLACK_APP_TOKEN must"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := minimalAgentEnv()
			env["SLACK_BOT_TOKEN"] = tc.bot
			env["SLACK_APP_TOKEN"] = tc.app
			env["SLACK_IT_CHANNEL"] = "C0ITDESK"
			env["ANTHROPIC_API_KEY"] = "sk-ant-test"
			cfg, err := LoadAgent(envFrom(env))
			if len(tc.wantErr) == 0 {
				if err != nil {
					t.Fatalf("LoadAgent: %v", err)
				}
				if cfg.SlackConfigured() != tc.wantSlack {
					t.Errorf("SlackConfigured() = %v, want %v", cfg.SlackConfigured(), tc.wantSlack)
				}
				return
			}
			if err == nil {
				t.Fatal("LoadAgent accepted the Slack tokens, want an error")
			}
			for _, want := range tc.wantErr {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not mention %q", err, want)
				}
			}
			if strings.Contains(err.Error(), "secretvalue") {
				t.Errorf("error %q leaks a token value", err)
			}
		})
	}
}

// covers: AC-16 (spec 0008). With Slack set, the loop needs a model key and
// in range bounds; without Slack none of these checks run.
func TestLoadAgentLoopChecks(t *testing.T) {
	withSlack := func(extra map[string]string) map[string]string {
		env := minimalAgentEnv()
		env["SLACK_BOT_TOKEN"] = "xoxb-1-2-abc"
		env["SLACK_APP_TOKEN"] = "xapp-1-A1-abc"
		env["SLACK_IT_CHANNEL"] = "G0ITDESK"
		env["ANTHROPIC_API_KEY"] = "sk-ant-test"
		maps.Copy(env, extra)
		return env
	}
	cases := map[string]struct {
		env  map[string]string
		want string
	}{
		"no key":          {withSlack(map[string]string{"ANTHROPIC_API_KEY": ""}), "ANTHROPIC_API_KEY is required"},
		"zero concurrent": {withSlack(map[string]string{"AGENT_MAX_CONCURRENT": "0"}), "AGENT_MAX_CONCURRENT must be at least 1"},
		"small response":  {withSlack(map[string]string{"AGENT_MAX_RESPONSE_TOKENS": "255"}), "AGENT_MAX_RESPONSE_TOKENS must be at least 256"},
		"zero grace":      {withSlack(map[string]string{"AGENT_SHUTDOWN_GRACE": "0s"}), "AGENT_SHUTDOWN_GRACE must be positive"},
		"zero per hour":   {withSlack(map[string]string{"AGENT_CONVERSATIONS_PER_HOUR": "0"}), "AGENT_CONVERSATIONS_PER_HOUR must be at least 1"},
		"zero turns":      {withSlack(map[string]string{"AGENT_MAX_TURNS": "0"}), "AGENT_MAX_TURNS must be at least 1"},
	}
	for name, tc := range cases {
		_, err := LoadAgent(envFrom(tc.env))
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: LoadAgent = %v, want an error containing %q", name, err, tc.want)
		}
	}
	if _, err := LoadAgent(envFrom(withSlack(nil))); err != nil {
		t.Errorf("valid loop settings: %v", err)
	}
	env := minimalAgentEnv()
	env["AGENT_MAX_CONCURRENT"] = "0"
	if _, err := LoadAgent(envFrom(env)); err != nil {
		t.Errorf("without Slack the loop checks ran: %v", err)
	}
}

// covers: AC-13 (spec 0009). With both Slack tokens, SLACK_IT_CHANNEL must
// be a channel id; without Slack it is not checked.
func TestLoadAgentITChannel(t *testing.T) {
	env := minimalAgentEnv()
	env["SLACK_BOT_TOKEN"] = "xoxb-1-2-abc"
	env["SLACK_APP_TOKEN"] = "xapp-1-A1-abc"
	env["ANTHROPIC_API_KEY"] = "sk-ant-test"
	for _, bad := range []string{"", "#it", "it-escalations", "D0ITDESK", "c0itdesk", "C0", "C" + strings.Repeat("A", 21)} {
		env["SLACK_IT_CHANNEL"] = bad
		if _, err := LoadAgent(envFrom(env)); err == nil || !strings.Contains(err.Error(), "SLACK_IT_CHANNEL must") {
			t.Errorf("SLACK_IT_CHANNEL %q: LoadAgent = %v, want a refusal", bad, err)
		}
	}
	for _, good := range []string{"C0ITDESK", "G0123ABCD"} {
		env["SLACK_IT_CHANNEL"] = good
		if cfg, err := LoadAgent(envFrom(env)); err != nil || cfg.SlackITChannel != good {
			t.Errorf("SLACK_IT_CHANNEL %q: LoadAgent = %v", good, err)
		}
	}
	if _, err := LoadAgent(envFrom(minimalAgentEnv())); err != nil {
		t.Errorf("without Slack the IT channel was checked: %v", err)
	}
}

// covers: AC-13 (spec 0009). ZAMMAD_PUBLIC_URL is optional and, when set,
// a plain http or https base with no credentials and its trailing "/"
// trimmed; PRIVILEGED_ATTEMPT_WINDOW defaults to 24h and is at least 1h.
func TestLoadToolServerEscalationSettings(t *testing.T) {
	cfg, err := LoadToolServer(envFrom(minimalToolServerEnv()))
	if err != nil {
		t.Fatalf("LoadToolServer: %v", err)
	}
	if cfg.PrivilegedWindow != 24*time.Hour || cfg.ZammadPublicURL != "" {
		t.Errorf("defaults = %s, %q; want 24h and no public URL", cfg.PrivilegedWindow, cfg.ZammadPublicURL)
	}
	cases := map[string]struct {
		key, value string
		ok         bool
	}{
		"http":            {"ZAMMAD_PUBLIC_URL", "http://127.0.0.1:58080", true},
		"https with path": {"ZAMMAD_PUBLIC_URL", "https://help.noord.example/zammad/", true},
		"query":           {"ZAMMAD_PUBLIC_URL", "https://help.noord.example/?x=1", false},
		"empty query":     {"ZAMMAD_PUBLIC_URL", "https://help.noord.example/?", false},
		"fragment":        {"ZAMMAD_PUBLIC_URL", "https://help.noord.example/#ticket", false},
		"no host":         {"ZAMMAD_PUBLIC_URL", "https:///zammad", false},
		"relative":        {"ZAMMAD_PUBLIC_URL", "help.noord.example", false},
		"ftp":             {"ZAMMAD_PUBLIC_URL", "ftp://help.noord.example", false},
		"user and pass":   {"ZAMMAD_PUBLIC_URL", "https://it:secret@help.noord.example", false},
		"user only":       {"ZAMMAD_PUBLIC_URL", "https://it@help.noord.example", false},
		"one hour":        {"PRIVILEGED_ATTEMPT_WINDOW", "1h", true},
		"thirty minutes":  {"PRIVILEGED_ATTEMPT_WINDOW", "30m", false},
	}
	for name, tc := range cases {
		env := minimalToolServerEnv()
		env[tc.key] = tc.value
		_, err := LoadToolServer(envFrom(env))
		if tc.ok && err != nil {
			t.Errorf("%s: LoadToolServer = %v, want ok", name, err)
		}
		if !tc.ok && (err == nil || !strings.Contains(err.Error(), tc.key+" must")) {
			t.Errorf("%s: LoadToolServer = %v, want an error naming %s", name, err, tc.key)
		}
	}
}

// covers: AC-13 (spec 0009). LoadToolServer trims every trailing "/" from
// ZAMMAD_PUBLIC_URL, so ticket links never hold "//#ticket".
func TestZammadPublicURLTrimmed(t *testing.T) {
	cases := map[string]string{
		"http://127.0.0.1:58080":              "http://127.0.0.1:58080",
		"https://help.noord.example/":         "https://help.noord.example",
		"https://help.noord.example/zammad//": "https://help.noord.example/zammad",
	}
	for raw, want := range cases {
		env := minimalToolServerEnv()
		env["ZAMMAD_PUBLIC_URL"] = raw
		cfg, err := LoadToolServer(envFrom(env))
		if err != nil {
			t.Fatalf("%s: LoadToolServer: %v", raw, err)
		}
		if cfg.ZammadPublicURL != want {
			t.Errorf("%s: ZammadPublicURL = %q, want %q", raw, cfg.ZammadPublicURL, want)
		}
	}
}

// covers: spec 0006 configuration. ZAMMAD_TOKEN, when set, is long and plain.
func TestZammadTokenFormat(t *testing.T) {
	cases := map[string]bool{
		"dev-only-zammad-token-change-me-0001":  true,
		"short-token":                           false,
		"has spaces in it but is long enough!!": false,
		"has-a-dollar-sign-$-and-long-enough-0": false,
	}
	for token, ok := range cases {
		env := minimalToolServerEnv()
		env["ZAMMAD_TOKEN"] = token
		_, err := LoadToolServer(envFrom(env))
		if ok && err != nil {
			t.Errorf("token %q rejected: %v", token, err)
		}
		if !ok {
			if err == nil || !strings.Contains(err.Error(), "ZAMMAD_TOKEN must") {
				t.Errorf("token %q: got %v, want a ZAMMAD_TOKEN format error", token, err)
			} else if strings.Contains(err.Error(), token) {
				t.Errorf("error %q leaks the token", err)
			}
		}
	}
}

func minimalSeedHelpdeskEnv() map[string]string {
	return map[string]string{
		"SEED_ZAMMAD_BIND_PASSWORD":  "bind-test",
		"SEED_ZAMMAD_ADMIN_PASSWORD": "zammad-admin-test",
		"ZAMMAD_URL":                 "http://127.0.0.1:58080",
		"ZAMMAD_TOKEN":               "dev-only-zammad-token-change-me-0001",
		"ZAMMAD_DEFAULT_GROUP":       "IT Support",
	}
}

func TestLoadSeedHelpdesk(t *testing.T) {
	t.Run("defaults", func(t *testing.T) {
		cfg, err := LoadSeedHelpdesk(envFrom(minimalSeedHelpdeskEnv()))
		if err != nil {
			t.Fatalf("LoadSeedHelpdesk: %v", err)
		}
		if cfg.ZammadAdminEmail != "zammad-admin@noord.example" || cfg.ZammadWaitTimeout != 10*time.Minute ||
			cfg.ZammadTimeout != 15*time.Second || cfg.HelpdeskDir != "seed/helpdesk" ||
			cfg.ZammadLDAPHost != "samba" || cfg.ZammadRunner != "compose" {
			t.Errorf("unexpected defaults: %+v", cfg)
		}
	})
	// covers: AC-5, AC-11 (spec 0015). The helpdesk Job's Secret holds no
	// domain Administrator or seed user password, so seed helpdesk must not
	// need them.
	t.Run("needs no directory admin password", func(t *testing.T) {
		for _, name := range Sealed(&SeedHelpdesk{}) {
			if name == "SEED_ADMIN_PASSWORD" || name == "SEED_USER_PASSWORD" || strings.HasPrefix(name, "SEED_GRAPH_") {
				t.Errorf("SeedHelpdesk reads %s", name)
			}
		}
	})
	t.Run("reads ZAMMAD_PUBLIC_URL for the fqdn", func(t *testing.T) {
		env := minimalSeedHelpdeskEnv()
		env["ZAMMAD_PUBLIC_URL"] = "https://helpdesk.example.ts.net/"
		cfg, err := LoadSeedHelpdesk(envFrom(env))
		if err != nil || cfg.ZammadPublicURL != "https://helpdesk.example.ts.net" {
			t.Errorf("ZammadPublicURL = %q, err = %v", cfg.ZammadPublicURL, err)
		}
		env["ZAMMAD_PUBLIC_URL"] = "helpdesk.example"
		if _, err := LoadSeedHelpdesk(envFrom(env)); err == nil || !strings.Contains(err.Error(), "ZAMMAD_PUBLIC_URL") {
			t.Errorf("got %v, want a ZAMMAD_PUBLIC_URL error", err)
		}
	})
	t.Run("the local runner needs ZAMMAD_PUBLIC_URL", func(t *testing.T) {
		env := minimalSeedHelpdeskEnv()
		env["SEED_ZAMMAD_RUNNER"] = "local"
		if _, err := LoadSeedHelpdesk(envFrom(env)); err == nil || !strings.Contains(err.Error(), "ZAMMAD_PUBLIC_URL") {
			t.Errorf("got %v, want a ZAMMAD_PUBLIC_URL error", err)
		}
		env["ZAMMAD_PUBLIC_URL"] = "https://helpdesk.example.ts.net"
		if _, err := LoadSeedHelpdesk(envFrom(env)); err != nil {
			t.Errorf("with the URL: %v", err)
		}
	})
	t.Run("rejects an unknown runner", func(t *testing.T) {
		env := minimalSeedHelpdeskEnv()
		env["SEED_ZAMMAD_RUNNER"] = "ssh"
		if _, err := LoadSeedHelpdesk(envFrom(env)); err == nil || !strings.Contains(err.Error(), "SEED_ZAMMAD_RUNNER") {
			t.Errorf("got %v, want a SEED_ZAMMAD_RUNNER error", err)
		}
	})
	t.Run("reports every missing setting", func(t *testing.T) {
		_, err := LoadSeedHelpdesk(envFrom(map[string]string{}))
		if err == nil {
			t.Fatal("LoadSeedHelpdesk accepted missing settings")
		}
		for _, name := range []string{"SEED_ZAMMAD_ADMIN_PASSWORD", "SEED_ZAMMAD_BIND_PASSWORD", "ZAMMAD_URL", "ZAMMAD_TOKEN", "ZAMMAD_DEFAULT_GROUP"} {
			if !strings.Contains(err.Error(), name) {
				t.Errorf("error %q does not name %s", err, name)
			}
		}
	})
	t.Run("rejects a bad wait timeout", func(t *testing.T) {
		env := minimalSeedHelpdeskEnv()
		env["SEED_ZAMMAD_WAIT_TIMEOUT"] = "-1s"
		if _, err := LoadSeedHelpdesk(envFrom(env)); err == nil || !strings.Contains(err.Error(), "SEED_ZAMMAD_WAIT_TIMEOUT") {
			t.Errorf("got %v, want a SEED_ZAMMAD_WAIT_TIMEOUT error", err)
		}
	})
}

// covers: AC-16 (spec 0007). The tool layer's defaults.
func TestToolLayerDefaults(t *testing.T) {
	cfg, err := LoadToolServer(envFrom(minimalToolServerEnv()))
	if err != nil {
		t.Fatalf("LoadToolServer: %v", err)
	}
	checks := []struct {
		name      string
		got, want any
	}{
		{"ToolCallTimeout", cfg.ToolCallTimeout, 15 * time.Second},
		{"LDAPTimeout", cfg.LDAPTimeout, 5 * time.Second},
		{"MaxRequestBytes", cfg.MaxRequestBytes, 65536},
		{"ToolWriteLimit", cfg.ToolWriteLimit, 3},
		{"ToolWriteWindow", cfg.ToolWriteWindow, time.Hour},
		{"TicketCreateLimit", cfg.TicketCreateLimit, 5},
		{"TicketMaxPending", cfg.TicketMaxPending, 720 * time.Hour},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
		}
	}
	if !slices.Equal(cfg.ToolUserOUs, []string{"OU=Users,OU=Noord"}) || !slices.Equal(cfg.ToolGroupOUs, []string{"OU=Groups,OU=Noord"}) {
		t.Errorf("OUs = %v and %v, want the Noord users and groups OUs", cfg.ToolUserOUs, cfg.ToolGroupOUs)
	}
	if !slices.Equal(cfg.ToolDeniedGroups, []string{"SG-IT-Admins", "APP-Helpdesk-Agents", "SYNC-Entra-Users"}) {
		t.Errorf("ToolDeniedGroups = %v", cfg.ToolDeniedGroups)
	}
}

// covers: AC-16 (spec 0007). A sep list splits on its separator and keeps a
// blank entry, so the OU check can refuse it.
func TestSemicolonListsKeepBlankEntries(t *testing.T) {
	env := minimalToolServerEnv()
	env["TOOL_USER_OUS"] = "OU=Users,OU=Noord; OU=Contractors,OU=Noord ;"
	cfg, err := LoadToolServer(envFrom(env))
	if err != nil {
		t.Fatalf("LoadToolServer: %v", err)
	}
	want := []string{"OU=Users,OU=Noord", "OU=Contractors,OU=Noord", ""}
	if !slices.Equal(cfg.ToolUserOUs, want) {
		t.Errorf("ToolUserOUs = %q, want %q", cfg.ToolUserOUs, want)
	}
}

// covers: AC-16 (spec 0007). TOOLSERVER_TOKEN is at least 32 characters on
// both binaries, and the error never holds the value.
func TestToolServerTokenLength(t *testing.T) {
	short := "short-token-only-31-characters!"
	agentEnv := minimalAgentEnv()
	agentEnv["TOOLSERVER_TOKEN"] = short
	toolEnv := minimalToolServerEnv()
	toolEnv["TOOLSERVER_TOKEN"] = short

	_, agentErr := LoadAgent(envFrom(agentEnv))
	_, toolErr := LoadToolServer(envFrom(toolEnv))
	for name, err := range map[string]error{"agent": agentErr, "tool server": toolErr} {
		if err == nil || !strings.Contains(err.Error(), "TOOLSERVER_TOKEN must be at least 32") {
			t.Errorf("%s: got %v, want a token length error", name, err)
		} else if strings.Contains(err.Error(), short) {
			t.Errorf("%s: error %q leaks the token", name, err)
		}
	}
}

// covers: AC-16 (spec 0007). Out of range tool layer settings stop startup.
func TestToolLayerRanges(t *testing.T) {
	for name, value := range map[string]string{
		"TOOL_WRITE_LIMIT":             "0",
		"TICKET_CREATE_LIMIT":          "-1",
		"TOOLSERVER_MAX_REQUEST_BYTES": "0",
		"TOOL_CALL_TIMEOUT":            "0s",
		"LDAP_TIMEOUT":                 "-5s",
		"TOOL_WRITE_WINDOW":            "0s",
		"TICKET_MAX_PENDING":           "-1h",
	} {
		env := minimalToolServerEnv()
		env[name] = value
		if _, err := LoadToolServer(envFrom(env)); err == nil || !strings.Contains(err.Error(), name) {
			t.Errorf("%s=%s: got %v, want an error naming it", name, value, err)
		}
	}
}

// covers: AC-16 (spec 0007). The dialer refuses anything but LDAPS on every
// call, so startup refuses it first, naming the setting but not the value.
func TestLDAPURLMustBeLDAPS(t *testing.T) {
	for _, value := range []string{"ldap://dc.noord.example:389", "ldaps://", "dc.noord.example:636", "https://dc.noord.example", "ldaps://%zz"} {
		env := minimalToolServerEnv()
		env["LDAP_URL"] = value
		if _, err := LoadToolServer(envFrom(env)); err == nil || !strings.Contains(err.Error(), "LDAP_URL must be an ldaps:// URL") {
			t.Errorf("LDAP_URL=%s: got %v, want an ldaps error", value, err)
		}
	}
	if _, err := LoadToolServer(envFrom(minimalToolServerEnv())); err != nil {
		t.Errorf("the default ldaps URL was refused: %v", err)
	}
}

// covers: AC-7 (spec 0010). Unset gives 30; 0, 366, and abc stop the agent,
// the tool server, and seed helpdesk with an error naming the setting.
func TestTicketRetentionDays(t *testing.T) {
	loaders := map[string]struct {
		env  func() map[string]string
		load func(LookupFunc) (int, error)
	}{
		"agent": {minimalAgentEnv, func(l LookupFunc) (int, error) {
			cfg, err := LoadAgent(l)
			return cfg.TicketRetentionDays, err
		}},
		"toolserver": {minimalToolServerEnv, func(l LookupFunc) (int, error) {
			cfg, err := LoadToolServer(l)
			return cfg.TicketRetentionDays, err
		}},
		"seed helpdesk": {minimalSeedHelpdeskEnv, func(l LookupFunc) (int, error) {
			cfg, err := LoadSeedHelpdesk(l)
			return cfg.TicketRetentionDays, err
		}},
	}
	for name, c := range loaders {
		t.Run(name, func(t *testing.T) {
			if days, err := c.load(envFrom(c.env())); err != nil || days != 30 {
				t.Errorf("unset: days = %d, err = %v; want 30", days, err)
			}
			for _, v := range []string{"1", "365"} {
				env := c.env()
				env["TICKET_RETENTION_DAYS"] = v
				if _, err := c.load(envFrom(env)); err != nil {
					t.Errorf("%s: %v", v, err)
				}
			}
			for _, v := range []string{"0", "366", "abc", "-5"} {
				env := c.env()
				env["TICKET_RETENTION_DAYS"] = v
				if _, err := c.load(envFrom(env)); err == nil || !strings.Contains(err.Error(), "TICKET_RETENTION_DAYS") {
					t.Errorf("%s: err = %v, want one naming TICKET_RETENTION_DAYS", v, err)
				}
			}
		})
	}
}

func withReset(env map[string]string) map[string]string {
	env["RESET_PUBLIC_URL"] = "https://reset.noord.example/"
	env["RESET_HTTP_ADDR"] = ":18082"
	env["RESET_LDAP_BIND_DN"] = "CN=svc-pwreset,OU=Service Accounts,OU=Noord,DC=ad,DC=noord,DC=example"
	env["RESET_LDAP_BIND_PASSWORD"] = "Reset-Dev-7xQ2"
	return env
}

// covers: spec 0011 AC-17. All unset is off; all set is on with defaults and
// the trailing slash trimmed; a partial set names each missing setting.
func TestLoadToolServerResetSettings(t *testing.T) {
	off, err := LoadToolServer(envFrom(minimalToolServerEnv()))
	if err != nil || off.ResetEnabled() {
		t.Fatalf("no RESET_* = %+v, %v; want the feature off", off.Reset, err)
	}
	on, err := LoadToolServer(envFrom(withReset(minimalToolServerEnv())))
	if err != nil || !on.ResetEnabled() {
		t.Fatalf("all RESET_* = %v; want the feature on", err)
	}
	if on.ResetPublicURL != "https://reset.noord.example" || on.ResetLinkTTL != 15*time.Minute ||
		on.ResetLinkLimit != 3 || on.ResetLinkWindow != 24*time.Hour || on.ResetMaxInflight != 4 ||
		strings.Join(on.ResetDeniedGroups, ",") != "SG-IT-Admins,APP-Helpdesk-Agents" ||
		strings.Join(on.ToolDeniedGroups, ",") != "SG-IT-Admins,APP-Helpdesk-Agents,SYNC-Entra-Users" {
		t.Errorf("reset = %+v", on.Reset)
	}

	partial := minimalToolServerEnv()
	partial["RESET_PUBLIC_URL"] = "https://reset.noord.example"
	_, err = LoadToolServer(envFrom(partial))
	for _, name := range []string{"RESET_HTTP_ADDR", "RESET_LDAP_BIND_DN", "RESET_LDAP_BIND_PASSWORD"} {
		if err == nil || !strings.Contains(err.Error(), name) {
			t.Errorf("partial set: error %v does not name %s", err, name)
		}
	}
	if err != nil && strings.Contains(err.Error(), "Reset-Dev") {
		t.Error("the error holds a password")
	}
}

// covers: spec 0011 AC-17. The https rule and the ranges.
func TestLoadToolServerResetRules(t *testing.T) {
	cases := map[string]struct {
		key, value string
		ok         bool
	}{
		"http on localhost":   {"RESET_PUBLIC_URL", "http://localhost:18082", true},
		"http on loopback":    {"RESET_PUBLIC_URL", "http://127.0.0.1:18082", true},
		"http on ipv6 loop":   {"RESET_PUBLIC_URL", "http://[::1]:18082", true},
		"http on a real host": {"RESET_PUBLIC_URL", "http://reset.noord.example", false},
		"credentials":         {"RESET_PUBLIC_URL", "https://u:p@reset.noord.example", false},
		"query":               {"RESET_PUBLIC_URL", "https://reset.noord.example/?a=1", false},
		"ttl too short":       {"RESET_LINK_TTL", "4m", false},
		"ttl too long":        {"RESET_LINK_TTL", "61m", false},
		"ttl at the top":      {"RESET_LINK_TTL", "60m", true},
		"limit zero":          {"RESET_LINK_LIMIT", "0", false},
		"window too short":    {"RESET_LINK_WINDOW", "30m", false},
		"window past":         {"RESET_LINK_WINDOW", "721h", false},
		"inflight zero":       {"RESET_MAX_INFLIGHT", "0", false},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			env := withReset(minimalToolServerEnv())
			env[c.key] = c.value
			_, err := LoadToolServer(envFrom(env))
			if (err == nil) != c.ok {
				t.Errorf("%s=%s: err = %v, want ok %v", c.key, c.value, err, c.ok)
			}
		})
	}
}

// covers: spec 0012 AC-21. The approval defaults on both binaries.
func TestApprovalDefaults(t *testing.T) {
	ts, err := LoadToolServer(envFrom(minimalToolServerEnv()))
	if err != nil {
		t.Fatalf("LoadToolServer: %v", err)
	}
	if ts.ApprovalTimeout != 30*time.Minute || ts.ApprovalExecuteTimeout != time.Minute ||
		ts.ApprovalExecuteGrace != 5*time.Minute ||
		strings.Join(ts.MFADeniedGroups, ",") != "SG-IT-Admins,APP-Helpdesk-Agents" {
		t.Errorf("approvals = %+v", ts.Approvals)
	}
	ag, err := LoadAgent(envFrom(minimalAgentEnv()))
	if err != nil {
		t.Fatalf("LoadAgent: %v", err)
	}
	if ag.ApprovalPollInterval != 30*time.Second || ag.ApprovalDecideTimeout != 90*time.Second || ag.ApprovalTimeout != 30*time.Minute {
		t.Errorf("agent approvals = %v, %v, %v", ag.ApprovalPollInterval, ag.ApprovalDecideTimeout, ag.ApprovalTimeout)
	}
	// spec 0013, AC-20: both binaries read the access window; the cap is the tool server's.
	if ts.AccessApprovalTimeout != 8*time.Hour || ag.AccessApprovalTimeout != 8*time.Hour || ts.AccessMaxPending != 3 {
		t.Errorf("access = %v, %v, %d", ts.AccessApprovalTimeout, ag.AccessApprovalTimeout, ts.AccessMaxPending)
	}
}

// covers: spec 0012 AC-21. Each bad value fails at load, naming the setting.
func TestApprovalRanges(t *testing.T) {
	cases := map[string]struct {
		agent      bool
		key, value string
		name       string // the setting the error must name; empty when ok
	}{
		"approver ok":             {false, "SLACK_IT_APPROVERS", "U0123456789,W012345678", ""},
		"approver too short":      {false, "SLACK_IT_APPROVERS", "U01", "SLACK_IT_APPROVERS"},
		"approver lower case":     {false, "SLACK_IT_APPROVERS", "u0123456789", "SLACK_IT_APPROVERS"},
		"approver channel":        {false, "SLACK_IT_APPROVERS", "C0123456789", "SLACK_IT_APPROVERS"},
		"timeout too short":       {false, "APPROVAL_TIMEOUT", "4m", "APPROVAL_TIMEOUT"},
		"timeout too long":        {false, "APPROVAL_TIMEOUT", "25h", "APPROVAL_TIMEOUT"},
		"timeout at the top":      {false, "APPROVAL_TIMEOUT", "24h", ""},
		"execute too short":       {false, "APPROVAL_EXECUTE_TIMEOUT", "14s", "APPROVAL_EXECUTE_TIMEOUT"},
		"execute too long":        {false, "APPROVAL_EXECUTE_TIMEOUT", "6m", "APPROVAL_EXECUTE_TIMEOUT"},
		"grace under twice":       {false, "APPROVAL_EXECUTE_GRACE", "119s", "APPROVAL_EXECUTE_GRACE"},
		"grace at twice":          {false, "APPROVAL_EXECUTE_GRACE", "2m", ""},
		"poll too short":          {true, "APPROVAL_POLL_INTERVAL", "9s", "APPROVAL_POLL_INTERVAL"},
		"poll too long":           {true, "APPROVAL_POLL_INTERVAL", "6m", "APPROVAL_POLL_INTERVAL"},
		"decide too short":        {true, "APPROVAL_DECIDE_TIMEOUT", "29s", "APPROVAL_DECIDE_TIMEOUT"},
		"decide too long":         {true, "APPROVAL_DECIDE_TIMEOUT", "11m", "APPROVAL_DECIDE_TIMEOUT"},
		"decide at the boundary":  {true, "APPROVAL_DECIDE_TIMEOUT", "10m", ""},
		"agent timeout too short": {true, "APPROVAL_TIMEOUT", "4m", "APPROVAL_TIMEOUT"},
		"agent timeout ok":        {true, "APPROVAL_TIMEOUT", "2h", ""},
		// spec 0013, AC-20
		"access timeout too short":       {false, "ACCESS_APPROVAL_TIMEOUT", "4m", "ACCESS_APPROVAL_TIMEOUT"},
		"access timeout too long":        {false, "ACCESS_APPROVAL_TIMEOUT", "25h", "ACCESS_APPROVAL_TIMEOUT"},
		"access timeout at the top":      {false, "ACCESS_APPROVAL_TIMEOUT", "24h", ""},
		"agent access timeout too short": {true, "ACCESS_APPROVAL_TIMEOUT", "4m", "ACCESS_APPROVAL_TIMEOUT"},
		"agent access timeout ok":        {true, "ACCESS_APPROVAL_TIMEOUT", "12h", ""},
		"max pending negative":           {false, "ACCESS_MAX_PENDING", "-2", "ACCESS_MAX_PENDING"},
		"max pending too many":           {false, "ACCESS_MAX_PENDING", "11", "ACCESS_MAX_PENDING"},
		"max pending at the top":         {false, "ACCESS_MAX_PENDING", "10", ""},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			var err error
			if c.agent {
				env := minimalAgentEnv()
				env[c.key] = c.value
				_, err = LoadAgent(envFrom(env))
			} else {
				env := minimalToolServerEnv()
				env[c.key] = c.value
				_, err = LoadToolServer(envFrom(env))
			}
			switch {
			case c.name == "" && err != nil:
				t.Errorf("%s=%s: %v, want ok", c.key, c.value, err)
			case c.name != "" && (err == nil || !strings.Contains(err.Error(), c.name)):
				t.Errorf("%s=%s: %v, want an error naming %s", c.key, c.value, err, c.name)
			case c.name != "" && strings.Contains(err.Error(), c.value):
				t.Errorf("the error holds the value: %v", err)
			}
		})
	}
}

// covers: spec 0012 AC-2. mfa_clear needs the whole Graph app and an approver.
func TestMFAEnabled(t *testing.T) {
	full := map[string]string{
		"GRAPH_TENANT_ID": "t", "GRAPH_CLIENT_ID": "c", "GRAPH_CLIENT_SECRET": "s",
		"SLACK_IT_APPROVERS": "U0123456789",
	}
	for _, drop := range []string{"", "GRAPH_TENANT_ID", "GRAPH_CLIENT_ID", "GRAPH_CLIENT_SECRET", "SLACK_IT_APPROVERS"} {
		env := minimalToolServerEnv()
		maps.Copy(env, full)
		delete(env, drop)
		cfg, err := LoadToolServer(envFrom(env))
		if err != nil {
			t.Fatalf("without %q: %v", drop, err)
		}
		if got := cfg.MFAEnabled(); got != (drop == "") {
			t.Errorf("without %q: MFAEnabled = %v", drop, got)
		}
	}
}

// covers: AC-16 (spec 0014). The KB settings default to 3, 730, and 2000,
// KB_STALE_AS_OF is optional, and LoadToolServer refuses a value out of
// range or a malformed date, naming the setting.
func TestLoadToolServerKBSettings(t *testing.T) {
	cfg, err := LoadToolServer(envFrom(minimalToolServerEnv()))
	if err != nil {
		t.Fatalf("LoadToolServer: %v", err)
	}
	if cfg.KBSearchLimit != 3 || cfg.KBSearchCandidates != 10 || cfg.KBStaleAfterDays != 730 || cfg.KBBodyMaxRunes != 2000 || cfg.KBStaleAsOf != "" {
		t.Errorf("defaults = %+v", cfg.KB)
	}
	if _, ok := cfg.StaleAsOf(); ok {
		t.Error("StaleAsOf is set by default")
	}
	cases := map[string]struct {
		key, value string
		ok         bool
	}{
		"limit 1":        {"KB_SEARCH_LIMIT", "1", true},
		"limit 5":        {"KB_SEARCH_LIMIT", "5", true},
		"limit 0":        {"KB_SEARCH_LIMIT", "0", false},
		"limit 6":        {"KB_SEARCH_LIMIT", "6", false},
		"candidates 3":   {"KB_SEARCH_CANDIDATES", "3", true},
		"candidates 20":  {"KB_SEARCH_CANDIDATES", "20", true},
		"candidates 2":   {"KB_SEARCH_CANDIDATES", "2", false}, // below KB_SEARCH_LIMIT (3)
		"candidates 21":  {"KB_SEARCH_CANDIDATES", "21", false},
		"stale 30":       {"KB_STALE_AFTER_DAYS", "30", true},
		"stale 29":       {"KB_STALE_AFTER_DAYS", "29", false},
		"stale 3651":     {"KB_STALE_AFTER_DAYS", "3651", false},
		"body 200":       {"KB_BODY_MAX_RUNES", "200", true},
		"body 199":       {"KB_BODY_MAX_RUNES", "199", false},
		"body 8001":      {"KB_BODY_MAX_RUNES", "8001", false},
		"as of":          {"KB_STALE_AS_OF", "2026-10-16", true},
		"as of time":     {"KB_STALE_AS_OF", "2026-10-16T00:00:00Z", false},
		"as of day name": {"KB_STALE_AS_OF", "16-10-2026", false},
		"as of no day":   {"KB_STALE_AS_OF", "2026-02-30", false},
	}
	for name, tc := range cases {
		env := minimalToolServerEnv()
		env[tc.key] = tc.value
		cfg, err := LoadToolServer(envFrom(env))
		if tc.ok && err != nil {
			t.Errorf("%s: LoadToolServer = %v, want ok", name, err)
		}
		if !tc.ok && (err == nil || !strings.Contains(err.Error(), tc.key+" must")) {
			t.Errorf("%s: LoadToolServer = %v, want an error naming %s", name, err, tc.key)
		}
		if name == "as of" {
			if day, ok := cfg.StaleAsOf(); !ok || day.Format(time.DateOnly) != "2026-10-16" {
				t.Errorf("StaleAsOf = %s, %t", day, ok)
			}
		}
	}
	// The candidate floor follows KB_SEARCH_LIMIT.
	env := minimalToolServerEnv()
	env["KB_SEARCH_LIMIT"], env["KB_SEARCH_CANDIDATES"] = "5", "4"
	if _, err := LoadToolServer(envFrom(env)); err == nil || !strings.Contains(err.Error(), "KB_SEARCH_CANDIDATES must be between 5 and 20") {
		t.Errorf("candidates below the limit: %v", err)
	}
}

// covers: AC-9 (spec 0015). The migration Job waits a bounded time.
func TestLoadMigrate(t *testing.T) {
	url := "postgres://noord_owner@db/noord"
	cfg, err := LoadMigrate(envFrom(map[string]string{"MIGRATE_DATABASE_URL": url}))
	if err != nil {
		t.Fatalf("LoadMigrate: %v", err)
	}
	if cfg.DatabaseURL != url || cfg.WaitTimeout != 5*time.Minute {
		t.Errorf("got %+v, want the URL and a 5m wait", cfg)
	}
	for _, bad := range []string{"5s", "31m"} {
		_, err := LoadMigrate(envFrom(map[string]string{"MIGRATE_DATABASE_URL": url, "MIGRATE_WAIT_TIMEOUT": bad}))
		if err == nil || !strings.Contains(err.Error(), "MIGRATE_WAIT_TIMEOUT") {
			t.Errorf("MIGRATE_WAIT_TIMEOUT=%s: err = %v, want a range error", bad, err)
		}
	}
	if _, err := LoadMigrate(envFrom(nil)); err == nil || !strings.Contains(err.Error(), "MIGRATE_DATABASE_URL") {
		t.Errorf("no URL: err = %v, want it named", err)
	}
}

// covers: AC-2 (spec 0015). The installer's own settings.
func TestLoadInstall(t *testing.T) {
	cfg, err := LoadInstall(envFrom(map[string]string{"KUBE_CONTEXT": "k3sprox"}))
	if err != nil {
		t.Fatalf("LoadInstall: %v", err)
	}
	if cfg.InstallTimeout != 30*time.Minute || cfg.BackupS3Region != "auto" {
		t.Errorf("defaults = %v and %q, want 30m and auto", cfg.InstallTimeout, cfg.BackupS3Region)
	}
	if _, err := LoadInstall(envFrom(nil)); err == nil || !strings.Contains(err.Error(), "KUBE_CONTEXT") {
		t.Errorf("no context: err = %v, want KUBE_CONTEXT named", err)
	}
	_, err = LoadInstall(envFrom(map[string]string{"KUBE_CONTEXT": "k3sprox", "INSTALL_TIMEOUT": "1m"}))
	if err == nil || !strings.Contains(err.Error(), "INSTALL_TIMEOUT") {
		t.Errorf("INSTALL_TIMEOUT=1m: err = %v, want a range error", err)
	}
}

// covers: AC-7 (spec 0015). Sealed lists exactly the tagged fields, and the
// agent's list holds no client system credential.
func TestSealed(t *testing.T) {
	if got, want := Sealed(&Migrate{}), []string{"MIGRATE_DATABASE_URL"}; !slices.Equal(got, want) {
		t.Errorf("Sealed(Migrate) = %v, want %v", got, want)
	}
	agent := Sealed(&Agent{})
	for _, want := range []string{"AGENT_DATABASE_URL", "SLACK_BOT_TOKEN", "SLACK_IT_CHANNEL", "TOOLSERVER_TOKEN"} {
		if !slices.Contains(agent, want) {
			t.Errorf("Sealed(Agent) misses %s: %v", want, agent)
		}
	}
	for _, name := range agent {
		if strings.HasPrefix(name, "LDAP_") || strings.HasPrefix(name, "ZAMMAD_") || strings.HasPrefix(name, "GRAPH_") {
			t.Errorf("Sealed(Agent) holds %s, a client system setting", name)
		}
	}
	if slices.Contains(Sealed(&ToolServer{}), "TOOL_CALL_TIMEOUT") {
		t.Error("an untagged setting is in Sealed(ToolServer)")
	}
}
