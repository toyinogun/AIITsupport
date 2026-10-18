// Package config is the single source of every setting the agent and the
// tool server read. Each binary parses and validates its settings at startup
// and refuses to start when a required value is missing or malformed.
//
// A setting is declared once, as a struct field tag:
//
//	env:"NAME"          the environment variable
//	default:"value"     used when the variable is unset or blank
//	required:"true"     the binary refuses to start without it
//	oneof:"a|b"         the only accepted values
//
// Later features flip a setting to required when their code starts using it.
package config

import (
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"
)

// LookupFunc reads one environment variable. Pass os.LookupEnv in main and a
// map backed func in tests.
type LookupFunc func(key string) (string, bool)

// Common holds settings both binaries read.
type Common struct {
	LogLevel            string `env:"LOG_LEVEL" default:"info" oneof:"debug|info|warn|error"`
	ToolServerToken     string `env:"TOOLSERVER_TOKEN" required:"true"`
	OTelEndpoint        string `env:"OTEL_EXPORTER_OTLP_ENDPOINT"`
	TicketRetentionDays int    `env:"TICKET_RETENTION_DAYS"` // default owned by feature #12
}

// Agent holds the Slack facing agent's settings. It never receives client
// system credentials.
type Agent struct {
	Common

	HTTPAddr    string `env:"AGENT_HTTP_ADDR" default:":8080"`
	DatabaseURL string `env:"AGENT_DATABASE_URL" required:"true"` // connects as noord_agent

	LLMProvider        string `env:"LLM_PROVIDER" default:"anthropic" oneof:"anthropic|bedrock"`
	LLMModel           string `env:"LLM_MODEL" default:"claude-sonnet-5-5"`
	AnthropicAPIKey    string `env:"ANTHROPIC_API_KEY"`
	AWSRegion          string `env:"AWS_REGION"`
	AWSAccessKeyID     string `env:"AWS_ACCESS_KEY_ID"`
	AWSSecretAccessKey string `env:"AWS_SECRET_ACCESS_KEY"`

	MaxTurns           int           `env:"AGENT_MAX_TURNS" default:"8"`
	ToolTimeout        time.Duration `env:"AGENT_TOOL_TIMEOUT" default:"20s"`
	MessageTimeout     time.Duration `env:"AGENT_MESSAGE_TIMEOUT" default:"45s"`
	MaxRetries         int           `env:"AGENT_MAX_RETRIES" default:"2"`
	MaxTokensPerTicket int           `env:"AGENT_MAX_TOKENS_PER_TICKET" default:"30000"`

	SlackBotToken    string   `env:"SLACK_BOT_TOKEN"`
	SlackAppToken    string   `env:"SLACK_APP_TOKEN"`
	SlackITChannel   string   `env:"SLACK_IT_CHANNEL"`   // feature #11
	SlackITApprovers []string `env:"SLACK_IT_APPROVERS"` // feature #14

	ToolServerURL string `env:"TOOLSERVER_URL" required:"true"`

	// SlackEventDedupTTL is how long seen Slack event ids are kept (spec 0003).
	SlackEventDedupTTL time.Duration `env:"SLACK_EVENT_DEDUP_TTL" default:"24h"`
}

// ToolServer holds the MCP tool server's settings. It alone holds the
// credentials for Samba AD, Entra, and Zammad.
type ToolServer struct {
	Common

	HTTPAddr    string `env:"TOOLSERVER_HTTP_ADDR" default:":8080"`
	DatabaseURL string `env:"TOOLSERVER_DATABASE_URL" required:"true"` // connects as noord_toolserver

	LDAPURL          string `env:"LDAP_URL"`
	LDAPBaseDN       string `env:"LDAP_BASE_DN"`
	LDAPBindDN       string `env:"LDAP_BIND_DN"`
	LDAPBindPassword string `env:"LDAP_BIND_PASSWORD"`
	LDAPCACert       string `env:"LDAP_CA_CERT"`

	Graph
	Zammad

	// ReadOnly is only the boot default for the kill switch; the mounted
	// KillSwitchFile is read on every write call (feature #9).
	ReadOnly        bool          `env:"READ_ONLY" default:"true"`
	KillSwitchFile  string        `env:"KILL_SWITCH_FILE"`
	ApprovalTimeout time.Duration `env:"APPROVAL_TIMEOUT"` // feature #14

	// Audit log writes (spec 0003). internal/audit validates the ranges.
	AuditOutcomeRetries  int           `env:"AUDIT_OUTCOME_RETRIES" default:"3"`
	AuditWriteTimeout    time.Duration `env:"AUDIT_WRITE_TIMEOUT" default:"5s"`
	AuditMaxPayloadBytes int           `env:"AUDIT_MAX_PAYLOAD_BYTES" default:"4096"`
}

// Graph is the tool server's Entra app registration (spec 0005). It may read
// users and their MFA methods and change only phone methods. The tool server
// boots with it empty (the AD only fallback); LoadGraph requires it.
type Graph struct {
	GraphTenantID     string        `env:"GRAPH_TENANT_ID"`
	GraphClientID     string        `env:"GRAPH_CLIENT_ID"`
	GraphClientSecret string        `env:"GRAPH_CLIENT_SECRET"`
	GraphMaxRetries   int           `env:"GRAPH_MAX_RETRIES" default:"5"`
	GraphTimeout      time.Duration `env:"GRAPH_TIMEOUT" default:"30s"`
}

// Zammad is the helpdesk's REST API as the noord-assistant user (spec 0006).
// The tool server holds it; cmd/seed holds it too, to install the token.
type Zammad struct {
	ZammadURL          string        `env:"ZAMMAD_URL"`
	ZammadToken        string        `env:"ZAMMAD_TOKEN"`
	ZammadDefaultGroup string        `env:"ZAMMAD_DEFAULT_GROUP"`
	ZammadTimeout      time.Duration `env:"ZAMMAD_TIMEOUT" default:"15s"`
}

// SeedEntraMFA is what `seed entra mfa` reads: the tool server's Graph app,
// never the seed app, and how long a phone clear may take to settle.
type SeedEntraMFA struct {
	Graph

	ClearWait time.Duration `env:"SEED_ENTRA_CLEAR_WAIT" default:"30s"`
}

// Migrate holds the settings for running migrations as the table owner role.
// `make migrate` reads MIGRATE_DATABASE_URL from the shell today; the
// migration Job in feature #17 adds a loader. Declaring it here keeps the
// .env.example drift test covering the name.
type Migrate struct {
	DatabaseURL string `env:"MIGRATE_DATABASE_URL" required:"true"` // connects as noord_owner
}

// Seed holds the settings for cmd/seed, which builds Noord's directory as the
// domain Administrator (spec 0004). Only cmd/seed reads it; the Administrator
// password never reaches the agent or the tool server.
type Seed struct {
	SeedFiles

	LDAPURL       string        `env:"SEED_LDAP_URL" default:"ldaps://localhost:55636"`
	CACert        string        `env:"SEED_CA_CERT" default:".dev/certs/ca.pem"`
	AdminUser     string        `env:"SEED_ADMIN_USER" default:"CN=Administrator,CN=Users,DC=ad,DC=noord,DC=example"`
	AdminPassword string        `env:"SEED_ADMIN_PASSWORD" required:"true"`
	UserPassword  string        `env:"SEED_USER_PASSWORD" required:"true"`
	WaitTimeout   time.Duration `env:"SEED_WAIT_TIMEOUT" default:"90s"`

	// ZammadBindPassword is svc-zammad's AD password (spec 0006). Optional
	// here so LoadSeed callers that never touch Zammad keep working; `seed
	// directory` and `seed helpdesk` each refuse to run without it.
	ZammadBindPassword string `env:"SEED_ZAMMAD_BIND_PASSWORD"`

	// The seed app registration (spec 0005), which may create, update,
	// delete, and restore users. All three unset means Entra is not
	// configured and `seed entra sync` skips; partly set is an error.
	GraphTenantID     string        `env:"SEED_GRAPH_TENANT_ID"`
	GraphClientID     string        `env:"SEED_GRAPH_CLIENT_ID"`
	GraphClientSecret string        `env:"SEED_GRAPH_CLIENT_SECRET"`
	GraphMaxRetries   int           `env:"SEED_GRAPH_MAX_RETRIES" default:"5"`
	GraphTimeout      time.Duration `env:"SEED_GRAPH_TIMEOUT" default:"30s"`

	EntraDomain       string `env:"SEED_ENTRA_DOMAIN"`
	EntraDemoPassword string `env:"SEED_ENTRA_DEMO_PASSWORD"`
	EntraDeleteLimit  int    `env:"SEED_ENTRA_DELETE_LIMIT" default:"10"`
}

// EntraConfigured reports whether any SEED_GRAPH_* credential is set.
// LoadSeed has already refused a partial set.
func (s Seed) EntraConfigured() bool {
	return s.GraphTenantID != "" || s.GraphClientID != "" || s.GraphClientSecret != ""
}

// SeedFiles locates the seed file, the optional overlay, and the optional
// Entra demo file. `seed validate` reads only these, so it runs with no
// passwords and no network.
type SeedFiles struct {
	File          string `env:"SEED_FILE" default:"seed/directory/noord.yaml"`
	OverlayFile   string `env:"SEED_OVERLAY_FILE" default:"seed/directory/overlay.local.yaml"`
	EntraDemoFile string `env:"SEED_ENTRA_DEMO_FILE" default:"seed/entra/demo.local.yaml"`
	HelpdeskDir   string `env:"SEED_HELPDESK_DIR" default:"seed/helpdesk"`
}

// SeedHelpdesk is what `seed helpdesk` reads (spec 0006): the directory seed
// settings for the svc-zammad bind check, the Zammad API settings for the
// token it installs, and the local Zammad admin only cmd/seed uses.
type SeedHelpdesk struct {
	Seed
	Zammad

	ZammadAdminEmail    string        `env:"SEED_ZAMMAD_ADMIN_EMAIL" default:"zammad-admin@noord.example"`
	ZammadAdminPassword string        `env:"SEED_ZAMMAD_ADMIN_PASSWORD" required:"true"`
	ZammadWaitTimeout   time.Duration `env:"SEED_ZAMMAD_WAIT_TIMEOUT" default:"10m"`
}

// SlackConfigured reports whether the Slack tokens are set. LoadAgent has
// already refused exactly one of them.
func (a Agent) SlackConfigured() bool {
	return a.SlackBotToken != "" && a.SlackAppToken != ""
}

// LoadAgent reads and validates the agent's settings.
func LoadAgent(lookup LookupFunc) (Agent, error) {
	var cfg Agent
	err := load(&cfg, lookup)
	err = errors.Join(err, checkSlack(cfg))
	if err != nil {
		return Agent{}, fmt.Errorf("agent config: %w", err)
	}
	return cfg, nil
}

// checkSlack accepts both Slack tokens or neither (spec 0006, AC-13), and
// checks each token's prefix. Errors name the setting, never the value.
func checkSlack(cfg Agent) error {
	if cfg.SlackBotToken == "" && cfg.SlackAppToken == "" {
		return nil
	}
	errs := missing(map[string]string{
		"SLACK_BOT_TOKEN": cfg.SlackBotToken,
		"SLACK_APP_TOKEN": cfg.SlackAppToken,
	})
	if cfg.SlackBotToken != "" && !strings.HasPrefix(cfg.SlackBotToken, "xoxb-") {
		errs = append(errs, errors.New("SLACK_BOT_TOKEN must be a bot token starting xoxb-"))
	}
	if cfg.SlackAppToken != "" && !strings.HasPrefix(cfg.SlackAppToken, "xapp-") {
		errs = append(errs, errors.New("SLACK_APP_TOKEN must be an app level token starting xapp-"))
	}
	return errors.Join(errs...)
}

// LoadToolServer reads and validates the tool server's settings.
func LoadToolServer(lookup LookupFunc) (ToolServer, error) {
	var cfg ToolServer
	err := load(&cfg, lookup)
	err = errors.Join(err, checkZammadToken(cfg.ZammadToken))
	if err != nil {
		return ToolServer{}, fmt.Errorf("toolserver config: %w", err)
	}
	return cfg, nil
}

// minZammadTokenLen is the shortest ZAMMAD_TOKEN accepted (spec 0006).
const minZammadTokenLen = 32

// checkZammadToken accepts an unset token, or one of at least 32 characters
// from A-Za-z0-9_- (spec 0006). The error never holds the value.
func checkZammadToken(token string) error {
	if token == "" {
		return nil
	}
	valid := len(token) >= minZammadTokenLen && strings.IndexFunc(token, func(r rune) bool {
		return !isTokenRune(r)
	}) < 0
	if !valid {
		return fmt.Errorf("ZAMMAD_TOKEN must be at least %d characters from A-Za-z0-9_-", minZammadTokenLen)
	}
	return nil
}

func isTokenRune(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-'
}

// LoadSeed reads and validates cmd/seed's settings.
func LoadSeed(lookup LookupFunc) (Seed, error) {
	var cfg Seed
	err := load(&cfg, lookup)
	err = errors.Join(err, checkSeedEntra(cfg))
	if err != nil {
		return Seed{}, fmt.Errorf("seed config: %w", err)
	}
	return cfg, nil
}

// checkSeedEntra refuses a partial SEED_GRAPH_* set and, once Entra is
// configured, a missing domain or out of range limits.
func checkSeedEntra(cfg Seed) error {
	if !cfg.EntraConfigured() {
		return nil
	}
	errs := missing(map[string]string{
		"SEED_GRAPH_TENANT_ID":     cfg.GraphTenantID,
		"SEED_GRAPH_CLIENT_ID":     cfg.GraphClientID,
		"SEED_GRAPH_CLIENT_SECRET": cfg.GraphClientSecret,
		"SEED_ENTRA_DOMAIN":        cfg.EntraDomain,
	})
	if cfg.EntraDeleteLimit < 0 {
		errs = append(errs, errors.New("SEED_ENTRA_DELETE_LIMIT must not be negative"))
	}
	if cfg.GraphMaxRetries < 0 {
		errs = append(errs, errors.New("SEED_GRAPH_MAX_RETRIES must not be negative"))
	}
	if cfg.GraphTimeout <= 0 {
		errs = append(errs, errors.New("SEED_GRAPH_TIMEOUT must be positive"))
	}
	return errors.Join(errs...)
}

// LoadSeedHelpdesk reads and validates the settings for `seed helpdesk`. It
// requires the Zammad API settings and the svc-zammad password on top of
// LoadSeed's checks.
func LoadSeedHelpdesk(lookup LookupFunc) (SeedHelpdesk, error) {
	var cfg SeedHelpdesk
	err := load(&cfg, lookup)
	err = errors.Join(err, checkSeedEntra(cfg.Seed), checkZammadToken(cfg.ZammadToken))
	errs := missing(map[string]string{
		"SEED_ZAMMAD_BIND_PASSWORD": cfg.ZammadBindPassword,
		"ZAMMAD_URL":                cfg.ZammadURL,
		"ZAMMAD_TOKEN":              cfg.ZammadToken,
		"ZAMMAD_DEFAULT_GROUP":      cfg.ZammadDefaultGroup,
	})
	if cfg.ZammadWaitTimeout <= 0 {
		errs = append(errs, errors.New("SEED_ZAMMAD_WAIT_TIMEOUT must be positive"))
	}
	if cfg.ZammadTimeout <= 0 {
		errs = append(errs, errors.New("ZAMMAD_TIMEOUT must be positive"))
	}
	err = errors.Join(err, errors.Join(errs...))
	if err != nil {
		return SeedHelpdesk{}, fmt.Errorf("seed config: %w", err)
	}
	return cfg, nil
}

// LoadGraph reads the tool server's Graph app and requires all of it. The
// tool server itself boots without it; callers that talk to Entra use this.
func LoadGraph(lookup LookupFunc) (Graph, error) {
	var cfg Graph
	err := load(&cfg, lookup)
	err = errors.Join(err, checkGraph(cfg))
	if err != nil {
		return Graph{}, fmt.Errorf("graph config: %w", err)
	}
	return cfg, nil
}

// LoadSeedEntraMFA reads the settings for `seed entra mfa`.
func LoadSeedEntraMFA(lookup LookupFunc) (SeedEntraMFA, error) {
	var cfg SeedEntraMFA
	err := load(&cfg, lookup)
	err = errors.Join(err, checkGraph(cfg.Graph))
	if cfg.ClearWait <= 0 {
		err = errors.Join(err, errors.New("SEED_ENTRA_CLEAR_WAIT must be positive"))
	}
	if err != nil {
		return SeedEntraMFA{}, fmt.Errorf("seed config: %w", err)
	}
	return cfg, nil
}

func checkGraph(cfg Graph) error {
	errs := missing(map[string]string{
		"GRAPH_TENANT_ID":     cfg.GraphTenantID,
		"GRAPH_CLIENT_ID":     cfg.GraphClientID,
		"GRAPH_CLIENT_SECRET": cfg.GraphClientSecret,
	})
	if cfg.GraphMaxRetries < 0 {
		errs = append(errs, errors.New("GRAPH_MAX_RETRIES must not be negative"))
	}
	if cfg.GraphTimeout <= 0 {
		errs = append(errs, errors.New("GRAPH_TIMEOUT must be positive"))
	}
	return errors.Join(errs...)
}

// missing returns one error per blank value, sorted by setting name.
func missing(values map[string]string) []error {
	var errs []error
	for _, name := range slices.Sorted(maps.Keys(values)) {
		if values[name] == "" {
			errs = append(errs, fmt.Errorf("%s is required but not set", name))
		}
	}
	return errs
}

// LoadSeedFiles reads only the seed file locations.
func LoadSeedFiles(lookup LookupFunc) (SeedFiles, error) {
	var cfg SeedFiles
	if err := load(&cfg, lookup); err != nil {
		return SeedFiles{}, fmt.Errorf("seed config: %w", err)
	}
	return cfg, nil
}

// Names lists every setting the binaries, migrations, and the seeder know, sorted. The .env.example
// drift test compares against it.
func Names() []string {
	var names []string
	for _, t := range []reflect.Type{reflect.TypeFor[Agent](), reflect.TypeFor[ToolServer](), reflect.TypeFor[Migrate](), reflect.TypeFor[Seed](), reflect.TypeFor[SeedEntraMFA](), reflect.TypeFor[SeedHelpdesk]()} {
		walkFields(t, func(f reflect.StructField) {
			names = append(names, f.Tag.Get("env"))
		})
	}
	slices.Sort(names)
	return slices.Compact(names)
}

// load fills the struct pointed to by dst and joins every problem into one
// error, so an operator sees all missing settings in a single start attempt.
func load(dst any, lookup LookupFunc) error {
	v := reflect.ValueOf(dst).Elem()
	var errs []error
	walkValues(v, func(f reflect.StructField, field reflect.Value) {
		if err := setField(f, field, lookup); err != nil {
			errs = append(errs, err)
		}
	})
	return errors.Join(errs...)
}

func setField(f reflect.StructField, field reflect.Value, lookup LookupFunc) error {
	name := f.Tag.Get("env")
	raw, _ := lookup(name)
	raw = strings.TrimSpace(raw)
	if raw == "" {
		if f.Tag.Get("required") == "true" {
			return fmt.Errorf("%s is required but not set", name)
		}
		raw = f.Tag.Get("default")
	}
	if raw == "" {
		return nil
	}
	if allowed := f.Tag.Get("oneof"); allowed != "" && !slices.Contains(strings.Split(allowed, "|"), raw) {
		return fmt.Errorf("%s must be one of %s, got %q", name, allowed, raw)
	}
	if err := parseInto(field, raw); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}

var durationType = reflect.TypeFor[time.Duration]()

func parseInto(field reflect.Value, raw string) error {
	switch {
	case field.Type() == durationType:
		d, err := time.ParseDuration(raw)
		if err != nil {
			return fmt.Errorf("not a duration such as 20s: %q", raw)
		}
		field.SetInt(int64(d))
	case field.Kind() == reflect.String:
		field.SetString(raw)
	case field.Kind() == reflect.Int:
		n, err := strconv.Atoi(raw)
		if err != nil {
			return fmt.Errorf("not a whole number: %q", raw)
		}
		field.SetInt(int64(n))
	case field.Kind() == reflect.Bool:
		b, err := strconv.ParseBool(raw)
		if err != nil {
			return fmt.Errorf("not true or false: %q", raw)
		}
		field.SetBool(b)
	case field.Kind() == reflect.Slice && field.Type().Elem().Kind() == reflect.String:
		field.Set(reflect.ValueOf(splitList(raw)))
	default:
		return fmt.Errorf("unsupported setting type %s", field.Type())
	}
	return nil
}

func splitList(raw string) []string {
	var out []string
	for part := range strings.SplitSeq(raw, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// walkFields visits every tagged field, descending into embedded structs.
func walkFields(t reflect.Type, visit func(reflect.StructField)) {
	for i := range t.NumField() {
		f := t.Field(i)
		if f.Anonymous {
			walkFields(f.Type, visit)
			continue
		}
		if f.Tag.Get("env") != "" {
			visit(f)
		}
	}
}

func walkValues(v reflect.Value, visit func(reflect.StructField, reflect.Value)) {
	t := v.Type()
	for i := range t.NumField() {
		f := t.Field(i)
		if f.Anonymous {
			walkValues(v.Field(i), visit)
			continue
		}
		if f.Tag.Get("env") != "" {
			visit(f, v.Field(i))
		}
	}
}
