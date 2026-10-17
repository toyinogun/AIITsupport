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
	DatabaseURL         string `env:"DATABASE_URL" required:"true"`
	ToolServerToken     string `env:"TOOLSERVER_TOKEN" required:"true"`
	OTelEndpoint        string `env:"OTEL_EXPORTER_OTLP_ENDPOINT"`
	TicketRetentionDays int    `env:"TICKET_RETENTION_DAYS"` // default owned by feature #12
}

// Agent holds the Slack facing agent's settings. It never receives client
// system credentials.
type Agent struct {
	Common

	HTTPAddr string `env:"AGENT_HTTP_ADDR" default:":8080"`

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
}

// ToolServer holds the MCP tool server's settings. It alone holds the
// credentials for Samba AD, Entra, and Zammad.
type ToolServer struct {
	Common

	HTTPAddr string `env:"TOOLSERVER_HTTP_ADDR" default:":8080"`

	LDAPURL          string `env:"LDAP_URL"`
	LDAPBaseDN       string `env:"LDAP_BASE_DN"`
	LDAPBindDN       string `env:"LDAP_BIND_DN"`
	LDAPBindPassword string `env:"LDAP_BIND_PASSWORD"`
	LDAPCACert       string `env:"LDAP_CA_CERT"`

	GraphTenantID     string `env:"GRAPH_TENANT_ID"`
	GraphClientID     string `env:"GRAPH_CLIENT_ID"`
	GraphClientSecret string `env:"GRAPH_CLIENT_SECRET"`

	ZammadURL          string `env:"ZAMMAD_URL"`
	ZammadToken        string `env:"ZAMMAD_TOKEN"`
	ZammadDefaultGroup string `env:"ZAMMAD_DEFAULT_GROUP"` // feature #8

	// ReadOnly is only the boot default for the kill switch; the mounted
	// KillSwitchFile is read on every write call (feature #9).
	ReadOnly        bool          `env:"READ_ONLY" default:"true"`
	KillSwitchFile  string        `env:"KILL_SWITCH_FILE"`
	ApprovalTimeout time.Duration `env:"APPROVAL_TIMEOUT"` // feature #14
}

// LoadAgent reads and validates the agent's settings.
func LoadAgent(lookup LookupFunc) (Agent, error) {
	var cfg Agent
	if err := load(&cfg, lookup); err != nil {
		return Agent{}, fmt.Errorf("agent config: %w", err)
	}
	return cfg, nil
}

// LoadToolServer reads and validates the tool server's settings.
func LoadToolServer(lookup LookupFunc) (ToolServer, error) {
	var cfg ToolServer
	if err := load(&cfg, lookup); err != nil {
		return ToolServer{}, fmt.Errorf("toolserver config: %w", err)
	}
	return cfg, nil
}

// Names lists every setting either binary knows, sorted. The .env.example
// drift test compares against it.
func Names() []string {
	var names []string
	for _, t := range []reflect.Type{reflect.TypeFor[Agent](), reflect.TypeFor[ToolServer]()} {
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
