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
//	sep:";"             the list separator for a []string (default a comma)
//	secret:"true"       a credential (token, password, key, database URL)
//	private:"true"      not a credential, but kept out of the public repo
//	                    (tenant, client and channel ids, public URLs, a
//	                    client's own hosts and DNs)
//
// In a cluster every secret and private setting a workload reads arrives in
// that workload's own sealed Secret (spec 0015, AC-5); everything else is a
// chart value.
//
// Later features flip a setting to required when their code starts using it.
package config

import (
	"errors"
	"fmt"
	"maps"
	"net/url"
	"reflect"
	"regexp"
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
	LogLevel        string `env:"LOG_LEVEL" default:"info" oneof:"debug|info|warn|error"`
	ToolServerToken string `env:"TOOLSERVER_TOKEN" required:"true" secret:"true"`
	OTelEndpoint    string `env:"OTEL_EXPORTER_OTLP_ENDPOINT"`
	Retention
}

// Retention is the one retention window (spec 0010): the days after a
// conversation or an assistant ticket ends before its text is purged. It is
// in Common for both binaries and in SeedHelpdesk for the Zammad scheduler,
// which seed helpdesk installs; checkRetention guards every loader.
type Retention struct {
	TicketRetentionDays int `env:"TICKET_RETENTION_DAYS" default:"30"`
}

// The range TICKET_RETENTION_DAYS must fall in.
const (
	minRetentionDays = 1
	maxRetentionDays = 365
)

// checkRetention refuses a retention window outside 1 to 365 days.
func checkRetention(r Retention) error {
	if r.TicketRetentionDays < minRetentionDays || r.TicketRetentionDays > maxRetentionDays {
		return fmt.Errorf("TICKET_RETENTION_DAYS must be between %d and %d", minRetentionDays, maxRetentionDays)
	}
	return nil
}

// Agent holds the Slack facing agent's settings. It never receives client
// system credentials.
type Agent struct {
	Common

	HTTPAddr    string `env:"AGENT_HTTP_ADDR" default:":8080"`
	DatabaseURL string `env:"AGENT_DATABASE_URL" required:"true" secret:"true"` // connects as noord_agent

	LLMProvider        string `env:"LLM_PROVIDER" default:"anthropic" oneof:"anthropic|bedrock"`
	LLMModel           string `env:"LLM_MODEL" default:"claude-sonnet-5-5"`
	AnthropicAPIKey    string `env:"ANTHROPIC_API_KEY" secret:"true"`
	AWSRegion          string `env:"AWS_REGION"`
	AWSAccessKeyID     string `env:"AWS_ACCESS_KEY_ID" secret:"true"`
	AWSSecretAccessKey string `env:"AWS_SECRET_ACCESS_KEY" secret:"true"`

	MaxTurns           int           `env:"AGENT_MAX_TURNS" default:"8"`
	ToolTimeout        time.Duration `env:"AGENT_TOOL_TIMEOUT" default:"20s"`
	MessageTimeout     time.Duration `env:"AGENT_MESSAGE_TIMEOUT" default:"45s"`
	MaxRetries         int           `env:"AGENT_MAX_RETRIES" default:"2"`
	MaxTokensPerTicket int           `env:"AGENT_MAX_TOKENS_PER_TICKET" default:"30000"`

	// feature #10 (spec 0008): the agent loop's own bounds.
	MaxConcurrent        int           `env:"AGENT_MAX_CONCURRENT" default:"4"`
	MaxResponseTokens    int           `env:"AGENT_MAX_RESPONSE_TOKENS" default:"1024"`
	ShutdownGrace        time.Duration `env:"AGENT_SHUTDOWN_GRACE" default:"25s"`
	ConversationsPerHour int           `env:"AGENT_CONVERSATIONS_PER_HOUR" default:"10"`

	SlackBotToken  string `env:"SLACK_BOT_TOKEN" secret:"true"`
	SlackAppToken  string `env:"SLACK_APP_TOKEN" secret:"true"`
	SlackITChannel string `env:"SLACK_IT_CHANNEL" private:"true"` // required with both Slack tokens (spec 0009)

	// The approval tick and the decision call (spec 0012, AC-21). Keep the
	// decide timeout above the tool server's APPROVAL_EXECUTE_TIMEOUT.
	ApprovalPollInterval  time.Duration `env:"APPROVAL_POLL_INTERVAL" default:"30s"`
	ApprovalDecideTimeout time.Duration `env:"APPROVAL_DECIDE_TIMEOUT" default:"90s"`
	ApprovalWindow                      // APPROVAL_TIMEOUT, shared with the tool server: the tick's adopt window

	ToolServerURL string `env:"TOOLSERVER_URL" required:"true"`

	// SlackEventDedupTTL is how long seen Slack event ids are kept (spec 0003).
	SlackEventDedupTTL time.Duration `env:"SLACK_EVENT_DEDUP_TTL" default:"24h"`
}

// ToolServer holds the MCP tool server's settings. It alone holds the
// credentials for Samba AD, Entra, and Zammad.
type ToolServer struct {
	Common

	HTTPAddr    string `env:"TOOLSERVER_HTTP_ADDR" default:":8080"`
	DatabaseURL string `env:"TOOLSERVER_DATABASE_URL" required:"true" secret:"true"` // connects as noord_toolserver

	// feature #9: svc-itsupport over LDAPS.
	LDAPURL          string        `env:"LDAP_URL" required:"true" private:"true"`
	LDAPBaseDN       string        `env:"LDAP_BASE_DN" required:"true" private:"true"`
	LDAPBindDN       string        `env:"LDAP_BIND_DN" required:"true" private:"true"`
	LDAPBindPassword string        `env:"LDAP_BIND_PASSWORD" required:"true" secret:"true"`
	LDAPCACert       string        `env:"LDAP_CA_CERT" required:"true"`
	LDAPTimeout      time.Duration `env:"LDAP_TIMEOUT" default:"5s"`

	Graph
	Zammad // required here by LoadToolServer (feature #9)

	// ReadOnly decides the kill switch only when KillSwitchFile is unset (local
	// dev and tests). Otherwise the file is read on every account change and
	// only the exact content read_write allows one (feature #9).
	ReadOnly       bool   `env:"READ_ONLY" default:"true"`
	KillSwitchFile string `env:"KILL_SWITCH_FILE"`

	Approvals

	// The tool layer (feature #9, spec 0007). The OU lists are DNs relative
	// to LDAP_BASE_DN; they hold commas, so they are separated by ";".
	ToolCallTimeout   time.Duration `env:"TOOL_CALL_TIMEOUT" default:"15s"`
	MaxRequestBytes   int           `env:"TOOLSERVER_MAX_REQUEST_BYTES" default:"65536"`
	ToolUserOUs       []string      `env:"TOOL_USER_OUS" default:"OU=Users,OU=Noord" sep:";"`
	ToolGroupOUs      []string      `env:"TOOL_GROUP_OUS" default:"OU=Groups,OU=Noord" sep:";"`
	ToolDeniedGroups  []string      `env:"TOOL_DENIED_GROUPS" default:"SG-IT-Admins,APP-Helpdesk-Agents,SYNC-Entra-Users"`
	ToolWriteLimit    int           `env:"TOOL_WRITE_LIMIT" default:"3"`
	ToolWriteWindow   time.Duration `env:"TOOL_WRITE_WINDOW" default:"1h"`
	TicketCreateLimit int           `env:"TICKET_CREATE_LIMIT" default:"5"`
	TicketMaxPending  time.Duration `env:"TICKET_MAX_PENDING" default:"720h"`

	// Escalation (feature #11, spec 0009): the window bounds the blocked
	// privileged attempt count. Ticket links use Zammad's ZammadPublicURL.
	PrivilegedWindow time.Duration `env:"PRIVILEGED_ATTEMPT_WINDOW" default:"24h"`

	KB

	Reset

	// Audit log writes (spec 0003). internal/audit validates the ranges.
	AuditOutcomeRetries  int           `env:"AUDIT_OUTCOME_RETRIES" default:"3"`
	AuditWriteTimeout    time.Duration `env:"AUDIT_WRITE_TIMEOUT" default:"5s"`
	AuditMaxPayloadBytes int           `env:"AUDIT_MAX_PAYLOAD_BYTES" default:"4096"`
}

// Approvals are the medium tier settings (spec 0012, AC-21): who may approve,
// how long they have, and how an approved call runs. mfa_clear registers only
// with the GRAPH_* settings complete and SLACK_IT_APPROVERS set.
type Approvals struct {
	// SlackITApprovers are the Slack user ids allowed to approve an IT
	// approval, snapshot into each approval when it is requested.
	SlackITApprovers []string `env:"SLACK_IT_APPROVERS" private:"true"`
	ApprovalWindow
	ApprovalExecuteTimeout time.Duration `env:"APPROVAL_EXECUTE_TIMEOUT" default:"60s"`
	// ApprovalExecuteGrace is when an approved call with no confirmed
	// execution is abandoned as failed; at least twice the execute timeout.
	ApprovalExecuteGrace time.Duration `env:"APPROVAL_EXECUTE_GRACE" default:"5m"`
	// MFADeniedGroups protect their members, at any depth, from an MFA clear
	// on top of the code floor. Separate from RESET_DENIED_GROUPS and
	// TOOL_DENIED_GROUPS (journal 0037).
	MFADeniedGroups []string `env:"MFA_DENIED_GROUPS" default:"SG-IT-Admins,APP-Helpdesk-Agents"`
	// AccessMaxPending is how many access requests one person may have
	// waiting for an owner at once (spec 0013, AC-20).
	AccessMaxPending int `env:"ACCESS_MAX_PENDING" default:"3"`
}

// ApprovalWindow is how long IT has to decide an approval, and a group owner
// an access request (spec 0013, AC-20). It is embedded in Approvals (the
// tool server sets each expiry from it) and in Agent (the approval tick
// adopts approvals created within the longer one plus an hour, spec 0012
// AC-14), so both binaries read one value.
type ApprovalWindow struct {
	ApprovalTimeout       time.Duration `env:"APPROVAL_TIMEOUT" default:"30m"`
	AccessApprovalTimeout time.Duration `env:"ACCESS_APPROVAL_TIMEOUT" default:"8h"`
}

// checkApprovalWindow refuses an APPROVAL_TIMEOUT or ACCESS_APPROVAL_TIMEOUT
// outside 5m to 24h.
func checkApprovalWindow(w ApprovalWindow) error {
	var errs []error
	if w.ApprovalTimeout < minApprovalTimeout || w.ApprovalTimeout > maxApprovalTimeout {
		errs = append(errs, errors.New("APPROVAL_TIMEOUT must be between 5m and 24h"))
	}
	if w.AccessApprovalTimeout < minApprovalTimeout || w.AccessApprovalTimeout > maxApprovalTimeout {
		errs = append(errs, errors.New("ACCESS_APPROVAL_TIMEOUT must be between 5m and 24h"))
	}
	return errors.Join(errs...)
}

// Range bounds for the approval settings (spec 0012, AC-21).
const (
	minApprovalTimeout        = 5 * time.Minute
	maxApprovalTimeout        = 24 * time.Hour
	minApprovalExecuteTimeout = 15 * time.Second
	maxApprovalExecuteTimeout = 5 * time.Minute
	minApprovalPollInterval   = 10 * time.Second
	maxApprovalPollInterval   = 5 * time.Minute
	minApprovalDecideTimeout  = 30 * time.Second
	maxApprovalDecideTimeout  = 10 * time.Minute
	minAccessMaxPending       = 1
	maxAccessMaxPending       = 10
)

// slackApproverID is an approver's Slack user id (spec 0012, AC-21).
var slackApproverID = regexp.MustCompile(`^[UW][A-Z0-9]{8,11}$`)

// checkApprovals checks the approver ids and the ranges. Errors name the
// setting and the entry's position, never its value.
func checkApprovals(a Approvals) error {
	var errs []error
	for i, id := range a.SlackITApprovers {
		if !slackApproverID.MatchString(id) {
			errs = append(errs, fmt.Errorf("SLACK_IT_APPROVERS entry %d must be a Slack user id (U or W, then 8 to 11 of A-Z0-9)", i+1))
		}
	}
	if err := checkApprovalWindow(a.ApprovalWindow); err != nil {
		errs = append(errs, err)
	}
	if a.ApprovalExecuteTimeout < minApprovalExecuteTimeout || a.ApprovalExecuteTimeout > maxApprovalExecuteTimeout {
		errs = append(errs, errors.New("APPROVAL_EXECUTE_TIMEOUT must be between 15s and 5m"))
	}
	if a.ApprovalExecuteGrace < 2*a.ApprovalExecuteTimeout {
		errs = append(errs, errors.New("APPROVAL_EXECUTE_GRACE must be at least twice APPROVAL_EXECUTE_TIMEOUT"))
	}
	if a.AccessMaxPending < minAccessMaxPending || a.AccessMaxPending > maxAccessMaxPending {
		errs = append(errs, errors.New("ACCESS_MAX_PENDING must be between 1 and 10"))
	}
	return errors.Join(errs...)
}

// checkAgentApprovals checks the agent's approval tick and decision call ranges.
func checkAgentApprovals(cfg Agent) error {
	var errs []error
	if cfg.ApprovalPollInterval < minApprovalPollInterval || cfg.ApprovalPollInterval > maxApprovalPollInterval {
		errs = append(errs, errors.New("APPROVAL_POLL_INTERVAL must be between 10s and 5m"))
	}
	if cfg.ApprovalDecideTimeout < minApprovalDecideTimeout || cfg.ApprovalDecideTimeout > maxApprovalDecideTimeout {
		errs = append(errs, errors.New("APPROVAL_DECIDE_TIMEOUT must be between 30s and 10m"))
	}
	if err := checkApprovalWindow(cfg.ApprovalWindow); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// MFAEnabled reports whether mfa_clear registers (spec 0012, AC-2): the
// tool server's Entra app is fully set and someone can approve.
func (cfg ToolServer) MFAEnabled() bool {
	return cfg.GraphTenantID != "" && cfg.GraphClientID != "" && cfg.hasGraphCredential() && len(cfg.SlackITApprovers) > 0
}

// KB is kb_search (spec 0014, AC-16): how many articles one search returns,
// how many of Zammad's matches it reads and ranks, when an article counts as
// stale, and how much of a body the model reads. ZAMMAD_PUBLIC_URL, when
// set, is the base of each article's link.
type KB struct {
	KBSearchLimit      int `env:"KB_SEARCH_LIMIT" default:"3"`
	KBSearchCandidates int `env:"KB_SEARCH_CANDIDATES" default:"10"`
	KBStaleAfterDays   int `env:"KB_STALE_AFTER_DAYS" default:"730"`
	KBBodyMaxRunes     int `env:"KB_BODY_MAX_RUNES" default:"2000"`
	// KBStaleAsOf pins "today" for the stale flag (YYYY-MM-DD), for the
	// eval and the live verify; unset in production.
	KBStaleAsOf string `env:"KB_STALE_AS_OF"`
}

const (
	minKBSearchLimit    = 1
	maxKBSearchLimit    = 5
	maxKBCandidates     = 20 // the least is KB_SEARCH_LIMIT
	minKBStaleAfterDays = 30
	maxKBStaleAfterDays = 3650
	minKBBodyMaxRunes   = 200
	maxKBBodyMaxRunes   = 8000
)

// StaleAsOf is KB_STALE_AS_OF as a date; ok is false when it is unset.
// LoadToolServer has already refused a malformed one.
func (k KB) StaleAsOf() (day time.Time, ok bool) {
	if k.KBStaleAsOf == "" {
		return time.Time{}, false
	}
	day, err := time.Parse(time.DateOnly, k.KBStaleAsOf)
	return day, err == nil
}

// checkKB refuses KB values out of range or a malformed KB_STALE_AS_OF.
func checkKB(k KB) error {
	var errs []error
	ranges := []struct {
		name          string
		value, lo, hi int
	}{
		{"KB_SEARCH_LIMIT", k.KBSearchLimit, minKBSearchLimit, maxKBSearchLimit},
		{"KB_SEARCH_CANDIDATES", k.KBSearchCandidates, max(k.KBSearchLimit, minKBSearchLimit), maxKBCandidates},
		{"KB_STALE_AFTER_DAYS", k.KBStaleAfterDays, minKBStaleAfterDays, maxKBStaleAfterDays},
		{"KB_BODY_MAX_RUNES", k.KBBodyMaxRunes, minKBBodyMaxRunes, maxKBBodyMaxRunes},
	}
	for _, r := range ranges {
		if r.value < r.lo || r.value > r.hi {
			errs = append(errs, fmt.Errorf("%s must be between %d and %d", r.name, r.lo, r.hi))
		}
	}
	if k.KBStaleAsOf != "" {
		if _, err := time.Parse(time.DateOnly, k.KBStaleAsOf); err != nil {
			errs = append(errs, errors.New("KB_STALE_AS_OF must be a date in the form YYYY-MM-DD"))
		}
	}
	return errors.Join(errs...)
}

// Reset is the password reset link feature (spec 0011). The first four are
// one set: all unset turns the feature off (no tool, no listener), and a
// partial set is an error naming each missing setting.
type Reset struct {
	ResetPublicURL        string        `env:"RESET_PUBLIC_URL" private:"true"` // browser base URL in links
	ResetHTTPAddr         string        `env:"RESET_HTTP_ADDR"`                 // the reset page's own listener
	ResetLDAPBindDN       string        `env:"RESET_LDAP_BIND_DN" private:"true"`
	ResetLDAPBindPassword string        `env:"RESET_LDAP_BIND_PASSWORD" secret:"true"` // svc-pwreset
	ResetLinkTTL          time.Duration `env:"RESET_LINK_TTL" default:"15m"`
	ResetLinkLimit        int           `env:"RESET_LINK_LIMIT" default:"3"`
	ResetLinkWindow       time.Duration `env:"RESET_LINK_WINDOW" default:"24h"`
	ResetMaxInflight      int           `env:"RESET_MAX_INFLIGHT" default:"4"`
	// ResetDeniedGroups protect their members, at any depth, from a reset on
	// top of the code floor. Not TOOL_DENIED_GROUPS, which also fences
	// SYNC-Entra-Users, a group nearly every employee is in.
	ResetDeniedGroups []string `env:"RESET_DENIED_GROUPS" default:"SG-IT-Admins,APP-Helpdesk-Agents"`
}

// ResetEnabled reports whether the reset link feature is configured.
func (r Reset) ResetEnabled() bool { return r.ResetPublicURL != "" }

// Range bounds for the reset settings (spec 0011, AC-17).
const (
	minResetLinkTTL    = 5 * time.Minute
	maxResetLinkTTL    = time.Hour
	minResetLinkWindow = time.Hour
)

// checkReset applies the all or none rule, the https rule, and the ranges.
func checkReset(r Reset, retention Retention) error {
	set := map[string]string{
		"RESET_PUBLIC_URL":         r.ResetPublicURL,
		"RESET_HTTP_ADDR":          r.ResetHTTPAddr,
		"RESET_LDAP_BIND_DN":       r.ResetLDAPBindDN,
		"RESET_LDAP_BIND_PASSWORD": r.ResetLDAPBindPassword,
	}
	if !slices.ContainsFunc(slices.Collect(maps.Values(set)), func(v string) bool { return v != "" }) {
		return nil
	}
	errs := missing(set)
	if r.ResetPublicURL != "" && !isResetURL(r.ResetPublicURL) {
		errs = append(errs, errors.New("RESET_PUBLIC_URL must be an https URL with a host and no credentials, query, or fragment (http only for localhost, 127.0.0.1, or [::1])"))
	}
	if r.ResetLinkTTL < minResetLinkTTL || r.ResetLinkTTL > maxResetLinkTTL {
		errs = append(errs, errors.New("RESET_LINK_TTL must be between 5m and 60m"))
	}
	maxWindow := time.Duration(retention.TicketRetentionDays) * 24 * time.Hour
	if r.ResetLinkWindow < minResetLinkWindow || r.ResetLinkWindow > maxWindow {
		errs = append(errs, errors.New("RESET_LINK_WINDOW must be at least 1h and no longer than TICKET_RETENTION_DAYS"))
	}
	for name, v := range map[string]int{"RESET_LINK_LIMIT": r.ResetLinkLimit, "RESET_MAX_INFLIGHT": r.ResetMaxInflight} {
		if v < 1 {
			errs = append(errs, fmt.Errorf("%s must be at least 1", name))
		}
	}
	return errors.Join(errs...)
}

// isResetURL is isPublicURL, https unless the host is a loopback name.
func isResetURL(raw string) bool {
	if !isPublicURL(raw) {
		return false
	}
	u, _ := url.Parse(raw) // isPublicURL parsed it
	switch u.Hostname() {
	case "localhost", "127.0.0.1", "::1":
		return true
	}
	return u.Scheme == "https"
}

// Graph is the tool server's Entra app registration (spec 0005). It may read
// users and their MFA methods and change only phone methods. The tool server
// boots with it empty (the AD only fallback); LoadGraph requires it.
type Graph struct {
	GraphTenantID     string `env:"GRAPH_TENANT_ID" private:"true"`
	GraphClientID     string `env:"GRAPH_CLIENT_ID" private:"true"`
	GraphClientSecret string `env:"GRAPH_CLIENT_SECRET" secret:"true"`
	// GraphClientCert is the path of a PEM holding the app's certificate and
	// its key, in place of the secret (spec 0015, AC-24). The cluster mounts
	// it; the path itself is no secret.
	GraphClientCert string        `env:"GRAPH_CLIENT_CERT"`
	GraphMaxRetries int           `env:"GRAPH_MAX_RETRIES" default:"5"`
	GraphTimeout    time.Duration `env:"GRAPH_TIMEOUT" default:"30s"`
}

// Zammad is the helpdesk's REST API as the noord-assistant user (spec 0006).
// The tool server holds it; cmd/seed holds it too, to install the token.
type Zammad struct {
	ZammadURL   string `env:"ZAMMAD_URL" private:"true"`
	ZammadToken string `env:"ZAMMAD_TOKEN" secret:"true"`
	// ZammadPublicURL is Zammad's browser URL, optional: the tool server's
	// ticket and article links (spec 0009), and the fqdn seed helpdesk sets
	// (spec 0015).
	ZammadPublicURL    string        `env:"ZAMMAD_PUBLIC_URL" private:"true"`
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
// `make migrate` reads MIGRATE_DATABASE_URL from the shell; `seed migrate`,
// the cluster's migration Job (spec 0015, AC-9), loads it with LoadMigrate.
type Migrate struct {
	DatabaseURL string `env:"MIGRATE_DATABASE_URL" required:"true" secret:"true"` // connects as noord_owner
	// WaitTimeout bounds how long `seed migrate` waits for the database to
	// accept the owner and for the managed roles to exist.
	WaitTimeout time.Duration `env:"MIGRATE_WAIT_TIMEOUT" default:"5m"`
}

// The range MIGRATE_WAIT_TIMEOUT must fall in.
const (
	minMigrateWait = 10 * time.Second
	maxMigrateWait = 30 * time.Minute
)

// LoadMigrate reads and validates the settings for `seed migrate`.
func LoadMigrate(lookup LookupFunc) (Migrate, error) {
	var cfg Migrate
	err := load(&cfg, lookup)
	if cfg.WaitTimeout < minMigrateWait || cfg.WaitTimeout > maxMigrateWait {
		err = errors.Join(err, errors.New("MIGRATE_WAIT_TIMEOUT must be between 10s and 30m"))
	}
	if err != nil {
		return Migrate{}, fmt.Errorf("migrate config: %w", err)
	}
	return cfg, nil
}

// Install is what `seed install` and its siblings read from a cluster's
// deploy/clusters/<name>/secrets.env (spec 0015). Never a workload's
// setting: the workloads get their values from the sealed Secrets the
// installer writes. The PG_* passwords are generated once (AC-5) and become
// the CNPG role Secrets and the per role database URLs.
type Install struct {
	KubeContext    string        `env:"KUBE_CONTEXT" required:"true"`
	Tailnet        string        `env:"TAILNET" private:"true"`
	InstallTimeout time.Duration `env:"INSTALL_TIMEOUT" default:"30m"`
	GraphCertFile  string        `env:"GRAPH_CERT_FILE"` // a local path, read and sealed (AC-24)

	BackupS3Endpoint        string `env:"BACKUP_S3_ENDPOINT" private:"true"`
	BackupS3Bucket          string `env:"BACKUP_S3_BUCKET" private:"true"`
	BackupS3Region          string `env:"BACKUP_S3_REGION" default:"auto"`
	BackupS3AccessKeyID     string `env:"BACKUP_S3_ACCESS_KEY_ID" secret:"true"`
	BackupS3SecretAccessKey string `env:"BACKUP_S3_SECRET_ACCESS_KEY" secret:"true"`

	TSOAuthClientID     string `env:"TS_OAUTH_CLIENT_ID" secret:"true"`
	TSOAuthClientSecret string `env:"TS_OAUTH_CLIENT_SECRET" secret:"true"`

	PGOwnerPassword      string `env:"PG_OWNER_PASSWORD" secret:"true"`
	PGAgentPassword      string `env:"PG_AGENT_PASSWORD" secret:"true"`
	PGToolServerPassword string `env:"PG_TOOLSERVER_PASSWORD" secret:"true"`
	PGEvalPassword       string `env:"PG_EVAL_PASSWORD" secret:"true"`

	// The demo Zammad's own Postgres and Redis (deploy only, generated):
	// the chart reads them from noord-zammad, the helpdesk Job from its
	// own Secret (AC-6, AC-11).
	ZammadPostgresPassword string `env:"ZAMMAD_POSTGRES_PASSWORD" secret:"true"`
	ZammadRedisPassword    string `env:"ZAMMAD_REDIS_PASSWORD" secret:"true"`
}

// The range INSTALL_TIMEOUT must fall in.
const (
	minInstallTimeout = 5 * time.Minute
	maxInstallTimeout = 3 * time.Hour
)

// LoadInstall reads and validates the installer's own settings. Settings an
// enabled component needs are checked by the installer's preflight, which
// knows which components are on.
func LoadInstall(lookup LookupFunc) (Install, error) {
	var cfg Install
	err := load(&cfg, lookup)
	if cfg.InstallTimeout < minInstallTimeout || cfg.InstallTimeout > maxInstallTimeout {
		err = errors.Join(err, errors.New("INSTALL_TIMEOUT must be between 5m and 3h"))
	}
	if err != nil {
		return Install{}, fmt.Errorf("install config: %w", err)
	}
	return cfg, nil
}

// Seed holds the settings for cmd/seed, which builds Noord's directory as the
// domain Administrator (spec 0004). Only cmd/seed reads it; the Administrator
// password never reaches the agent or the tool server.
type Seed struct {
	SeedFiles
	SeedLDAP

	AdminUser     string        `env:"SEED_ADMIN_USER" default:"CN=Administrator,CN=Users,DC=ad,DC=noord,DC=example" private:"true"`
	AdminPassword string        `env:"SEED_ADMIN_PASSWORD" required:"true" secret:"true"`
	UserPassword  string        `env:"SEED_USER_PASSWORD" required:"true" secret:"true"`
	WaitTimeout   time.Duration `env:"SEED_WAIT_TIMEOUT" default:"90s"`

	// The seed app registration (spec 0005), which may create, update,
	// delete, and restore users. All three unset means Entra is not
	// configured and `seed entra sync` skips; partly set is an error.
	GraphTenantID     string        `env:"SEED_GRAPH_TENANT_ID" private:"true"`
	GraphClientID     string        `env:"SEED_GRAPH_CLIENT_ID" private:"true"`
	GraphClientSecret string        `env:"SEED_GRAPH_CLIENT_SECRET" secret:"true"`
	GraphMaxRetries   int           `env:"SEED_GRAPH_MAX_RETRIES" default:"5"`
	GraphTimeout      time.Duration `env:"SEED_GRAPH_TIMEOUT" default:"30s"`

	EntraDomain       string `env:"SEED_ENTRA_DOMAIN" private:"true"`
	EntraDemoPassword string `env:"SEED_ENTRA_DEMO_PASSWORD" secret:"true"`
	EntraDeleteLimit  int    `env:"SEED_ENTRA_DELETE_LIMIT" default:"10"`
}

// EntraConfigured reports whether any SEED_GRAPH_* credential is set.
// LoadSeed has already refused a partial set.
func (s Seed) EntraConfigured() bool {
	return s.GraphTenantID != "" || s.GraphClientID != "" || s.GraphClientSecret != ""
}

// SeedLDAP is where the seeded directory answers and svc-zammad's password:
// the part of Seed that seed helpdesk also reads, so the helpdesk Job never
// holds the domain Administrator password (spec 0015).
type SeedLDAP struct {
	LDAPURL string `env:"SEED_LDAP_URL" default:"ldaps://localhost:55636"`
	CACert  string `env:"SEED_CA_CERT" default:".dev/certs/ca.pem"`

	// ZammadBindPassword is svc-zammad's AD password (spec 0006). Optional
	// here so LoadSeed callers that never touch Zammad keep working; `seed
	// directory` and `seed helpdesk` each refuse to run without it.
	ZammadBindPassword string `env:"SEED_ZAMMAD_BIND_PASSWORD" secret:"true"`
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

// SeedHelpdesk is what `seed helpdesk` reads (spec 0006): the seed files,
// the directory address for the svc-zammad bind check, the Zammad API
// settings for the token it installs, and the local Zammad admin only
// cmd/seed uses. It holds no directory admin password (spec 0015).
type SeedHelpdesk struct {
	SeedFiles
	SeedLDAP
	Zammad
	Retention

	ZammadAdminEmail    string        `env:"SEED_ZAMMAD_ADMIN_EMAIL" default:"zammad-admin@noord.example" private:"true"`
	ZammadAdminPassword string        `env:"SEED_ZAMMAD_ADMIN_PASSWORD" required:"true" secret:"true"`
	ZammadWaitTimeout   time.Duration `env:"SEED_ZAMMAD_WAIT_TIMEOUT" default:"10m"`
	// ZammadLDAPHost is the LDAPS host Zammad's LDAP source uses: samba in
	// Compose, the Samba Service FQDN in the cluster (spec 0015, AC-11).
	ZammadLDAPHost string `env:"SEED_ZAMMAD_LDAP_HOST" default:"samba"`
	// ZammadRunner picks how the Rails steps run: docker compose exec, or
	// in process inside a Zammad image (the cluster's helpdesk Job).
	ZammadRunner string `env:"SEED_ZAMMAD_RUNNER" default:"compose" oneof:"compose|local"`
}

// errZammadPublicURL is the one ZAMMAD_PUBLIC_URL rule both loaders apply.
var errZammadPublicURL = errors.New("ZAMMAD_PUBLIC_URL must be an absolute http or https URL with a host and no credentials, query, or fragment")

// ZammadRunnerLocal runs the Rails steps in process (spec 0015, AC-11).
const ZammadRunnerLocal = "local"

// SlackConfigured reports whether the Slack tokens are set. LoadAgent has
// already refused exactly one of them.
func (a Agent) SlackConfigured() bool {
	return a.SlackBotToken != "" && a.SlackAppToken != ""
}

// LoadAgent reads and validates the agent's settings.
func LoadAgent(lookup LookupFunc) (Agent, error) {
	var cfg Agent
	err := load(&cfg, lookup)
	err = errors.Join(err, checkSlack(cfg), checkToolServerToken(cfg.ToolServerToken), checkAgentLoop(cfg), checkRetention(cfg.Retention),
		checkAgentApprovals(cfg))
	if err != nil {
		return Agent{}, fmt.Errorf("agent config: %w", err)
	}
	return cfg, nil
}

// minResponseTokens is the smallest AGENT_MAX_RESPONSE_TOKENS accepted (spec 0008).
const minResponseTokens = 256

// checkAgentLoop checks the agent loop's settings, only when Slack is
// configured: without Slack the agent serves health only (spec 0008, AC-16).
// The provider and price checks that need other packages run in cmd/agent.
func checkAgentLoop(cfg Agent) error {
	if !cfg.SlackConfigured() {
		return nil
	}
	var errs []error
	if cfg.LLMProvider == "anthropic" && cfg.AnthropicAPIKey == "" {
		errs = append(errs, errors.New("ANTHROPIC_API_KEY is required when SLACK_BOT_TOKEN and SLACK_APP_TOKEN are set and LLM_PROVIDER is anthropic"))
	}
	positive := map[string]time.Duration{
		"AGENT_TOOL_TIMEOUT":    cfg.ToolTimeout,
		"AGENT_MESSAGE_TIMEOUT": cfg.MessageTimeout,
		"AGENT_SHUTDOWN_GRACE":  cfg.ShutdownGrace,
	}
	for _, name := range slices.Sorted(maps.Keys(positive)) {
		if positive[name] <= 0 {
			errs = append(errs, fmt.Errorf("%s must be positive", name))
		}
	}
	atLeastOne := map[string]int{
		"AGENT_MAX_CONCURRENT":         cfg.MaxConcurrent,
		"AGENT_MAX_TURNS":              cfg.MaxTurns,
		"AGENT_MAX_TOKENS_PER_TICKET":  cfg.MaxTokensPerTicket,
		"AGENT_CONVERSATIONS_PER_HOUR": cfg.ConversationsPerHour,
	}
	for _, name := range slices.Sorted(maps.Keys(atLeastOne)) {
		if atLeastOne[name] < 1 {
			errs = append(errs, fmt.Errorf("%s must be at least 1", name))
		}
	}
	if cfg.MaxResponseTokens < minResponseTokens {
		errs = append(errs, fmt.Errorf("AGENT_MAX_RESPONSE_TOKENS must be at least %d", minResponseTokens))
	}
	if cfg.MaxRetries < 0 {
		errs = append(errs, errors.New("AGENT_MAX_RETRIES must not be negative"))
	}
	return errors.Join(errs...)
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
	if !slackChannelID.MatchString(cfg.SlackITChannel) {
		errs = append(errs, errors.New("SLACK_IT_CHANNEL must be a Slack channel id (C or G, then 2 to 20 of A-Z0-9) when the Slack tokens are set"))
	}
	return errors.Join(errs...)
}

// slackChannelID is a public or private channel id, the IT channel's format
// (spec 0009, AC-13). Startup checks the format only, never membership.
var slackChannelID = regexp.MustCompile(`^[CG][A-Z0-9]{2,20}$`)

// LoadToolServer reads and validates the tool server's settings. It checks
// configuration only, never reachability. The OU lists are parsed as DNs by
// the tool layer at startup (internal/tools), because DN parsing needs the
// LDAP package, which this package must not import: the agent imports it.
func LoadToolServer(lookup LookupFunc) (ToolServer, error) {
	var cfg ToolServer
	err := load(&cfg, lookup)
	err = errors.Join(err, checkZammadToken(cfg.ZammadToken), checkToolServerToken(cfg.ToolServerToken), checkToolLayer(cfg), checkRetention(cfg.Retention),
		checkReset(cfg.Reset, cfg.Retention), checkApprovals(cfg.Approvals), checkKB(cfg.KB), checkGraphCredential(cfg.Graph))
	if err != nil {
		return ToolServer{}, fmt.Errorf("toolserver config: %w", err)
	}
	cfg.ZammadPublicURL = strings.TrimRight(cfg.ZammadPublicURL, "/")
	cfg.ResetPublicURL = strings.TrimRight(cfg.ResetPublicURL, "/")
	return cfg, nil
}

// minToolServerTokenLen is the shortest TOOLSERVER_TOKEN either binary accepts (spec 0007).
const minToolServerTokenLen = 32

// checkToolServerToken accepts an unset token (load already reported it) or
// one of at least 32 characters. The error never holds the value.
func checkToolServerToken(token string) error {
	if token == "" || len(token) >= minToolServerTokenLen {
		return nil
	}
	return fmt.Errorf("TOOLSERVER_TOKEN must be at least %d characters", minToolServerTokenLen)
}

// checkToolLayer requires the Zammad settings and checks the tool layer's ranges.
func checkToolLayer(cfg ToolServer) error {
	errs := missing(map[string]string{
		"ZAMMAD_URL":           cfg.ZammadURL,
		"ZAMMAD_TOKEN":         cfg.ZammadToken,
		"ZAMMAD_DEFAULT_GROUP": cfg.ZammadDefaultGroup,
	})
	positive := map[string]time.Duration{
		"LDAP_TIMEOUT":       cfg.LDAPTimeout,
		"TOOL_CALL_TIMEOUT":  cfg.ToolCallTimeout,
		"TOOL_WRITE_WINDOW":  cfg.ToolWriteWindow,
		"TICKET_MAX_PENDING": cfg.TicketMaxPending,
		"ZAMMAD_TIMEOUT":     cfg.ZammadTimeout,
	}
	for _, name := range slices.Sorted(maps.Keys(positive)) {
		if positive[name] <= 0 {
			errs = append(errs, fmt.Errorf("%s must be positive", name))
		}
	}
	atLeastOne := map[string]int{
		"TOOL_WRITE_LIMIT":             cfg.ToolWriteLimit,
		"TICKET_CREATE_LIMIT":          cfg.TicketCreateLimit,
		"TOOLSERVER_MAX_REQUEST_BYTES": cfg.MaxRequestBytes,
	}
	for _, name := range slices.Sorted(maps.Keys(atLeastOne)) {
		if atLeastOne[name] < 1 {
			errs = append(errs, fmt.Errorf("%s must be at least 1", name))
		}
	}
	if cfg.LDAPURL != "" && !isLDAPS(cfg.LDAPURL) {
		errs = append(errs, errors.New("LDAP_URL must be an ldaps:// URL with a host"))
	}
	if len(cfg.ToolUserOUs) == 0 {
		errs = append(errs, errors.New("TOOL_USER_OUS must name at least one OU"))
	}
	if cfg.PrivilegedWindow < time.Hour {
		errs = append(errs, errors.New("PRIVILEGED_ATTEMPT_WINDOW must be at least 1h"))
	}
	if cfg.ZammadPublicURL != "" && !isPublicURL(cfg.ZammadPublicURL) {
		errs = append(errs, errZammadPublicURL)
	}
	if len(cfg.ToolGroupOUs) == 0 {
		errs = append(errs, errors.New("TOOL_GROUP_OUS must name at least one OU"))
	}
	return errors.Join(errs...)
}

// isPublicURL reports whether raw is an absolute http or https URL with a
// host and no credentials, query, or fragment, the base of a ticket link
// (spec 0009). Ticket links are posted to Slack, so userinfo is refused.
func isPublicURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" && u.User == nil &&
		u.RawQuery == "" && !u.ForceQuery && u.Fragment == "" && !strings.Contains(raw, "#")
}

// isLDAPS reports whether raw parses as an ldaps URL with a host, the only
// kind the LDAP dialer opens.
func isLDAPS(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "ldaps" && u.Host != ""
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
	err = errors.Join(err, checkZammadToken(cfg.ZammadToken), checkRetention(cfg.Retention))
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
	if cfg.ZammadPublicURL != "" && !isPublicURL(cfg.ZammadPublicURL) {
		errs = append(errs, errZammadPublicURL)
	}
	// The fqdn is set once, on the fresh seed: in the cluster a blank URL
	// would leave Zammad's links on the Compose address for good.
	if cfg.ZammadRunner == ZammadRunnerLocal && cfg.ZammadPublicURL == "" {
		errs = append(errs, errors.New("ZAMMAD_PUBLIC_URL is required with SEED_ZAMMAD_RUNNER=local"))
	}
	err = errors.Join(err, errors.Join(errs...))
	if err != nil {
		return SeedHelpdesk{}, fmt.Errorf("seed config: %w", err)
	}
	cfg.ZammadPublicURL = strings.TrimRight(cfg.ZammadPublicURL, "/")
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
		"GRAPH_TENANT_ID": cfg.GraphTenantID,
		"GRAPH_CLIENT_ID": cfg.GraphClientID,
	})
	if !cfg.hasGraphCredential() {
		errs = append(errs, errors.New("GRAPH_CLIENT_SECRET or GRAPH_CLIENT_CERT is required but not set"))
	}
	if err := checkGraphCredential(cfg); err != nil {
		errs = append(errs, err)
	}
	if cfg.GraphMaxRetries < 0 {
		errs = append(errs, errors.New("GRAPH_MAX_RETRIES must not be negative"))
	}
	if cfg.GraphTimeout <= 0 {
		errs = append(errs, errors.New("GRAPH_TIMEOUT must be positive"))
	}
	return errors.Join(errs...)
}

// hasGraphCredential reports whether the app has a secret or a certificate.
func (cfg Graph) hasGraphCredential() bool {
	return cfg.GraphClientSecret != "" || cfg.GraphClientCert != ""
}

// checkGraphCredential refuses a secret and a certificate at once (AC-24):
// the app authenticates one way, so a leftover secret is a mistake.
func checkGraphCredential(cfg Graph) error {
	if cfg.GraphClientSecret != "" && cfg.GraphClientCert != "" {
		return errors.New("set GRAPH_CLIENT_SECRET or GRAPH_CLIENT_CERT, not both")
	}
	return nil
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
	for _, t := range []reflect.Type{
		reflect.TypeFor[Agent](), reflect.TypeFor[ToolServer](), reflect.TypeFor[Migrate](), reflect.TypeFor[Seed](),
		reflect.TypeFor[SeedEntraMFA](), reflect.TypeFor[SeedHelpdesk](), reflect.TypeFor[Install](),
	} {
		walkFields(t, func(f reflect.StructField) {
			names = append(names, f.Tag.Get("env"))
		})
	}
	slices.Sort(names)
	return slices.Compact(names)
}

// Fields lists every setting the struct pointed to by cfg declares, sorted.
func Fields(cfg any) []string {
	var names []string
	walkFields(reflect.TypeOf(cfg).Elem(), func(f reflect.StructField) {
		names = append(names, f.Tag.Get("env"))
	})
	slices.Sort(names)
	return slices.Compact(names)
}

// Sealed lists the secret and private settings the struct pointed to by cfg
// declares, sorted: the keys that struct's workload reads from its sealed
// Secret in a cluster (spec 0015, AC-7). Pass a pointer to a zero value,
// such as &Agent{}.
func Sealed(cfg any) []string {
	var names []string
	walkFields(reflect.TypeOf(cfg).Elem(), func(f reflect.StructField) {
		if f.Tag.Get("secret") == "true" || f.Tag.Get("private") == "true" {
			names = append(names, f.Tag.Get("env"))
		}
	})
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
	if err := parseInto(field, raw, f.Tag.Get("sep")); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}

var durationType = reflect.TypeFor[time.Duration]()

func parseInto(field reflect.Value, raw, sep string) error {
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
		field.Set(reflect.ValueOf(splitList(raw, sep)))
	default:
		return fmt.Errorf("unsupported setting type %s", field.Type())
	}
	return nil
}

// splitList splits on commas, dropping blank entries. With a sep tag it
// splits on sep instead and keeps blank entries, so the setting's own check
// can refuse them (TOOL_USER_OUS, TOOL_GROUP_OUS).
func splitList(raw, sep string) []string {
	var out []string
	if sep == "" {
		for part := range strings.SplitSeq(raw, ",") {
			if p := strings.TrimSpace(part); p != "" {
				out = append(out, p)
			}
		}
		return out
	}
	for part := range strings.SplitSeq(raw, sep) {
		out = append(out, strings.TrimSpace(part))
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
