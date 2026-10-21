package helpdesk

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/toyinogun/AIITsupport/internal/seed/directory"
	"github.com/toyinogun/AIITsupport/internal/zammad"
)

// Helpdesk states bootstrap.rb reports.
const (
	stateFresh         = "fresh"
	stateAlreadySeeded = "already_seeded"
	stateNeedsReset    = "needs_reset"
)

const (
	// Organization holds every synced Noord user.
	Organization = "Noord Logistics"
	// AssistantLogin is the local user behind ZAMMAD_TOKEN.
	AssistantLogin = "noord-assistant"
	// defaultLDAPHost is the Compose service Samba runs as.
	defaultLDAPHost = "samba"
	// localFQDN is where make helpdesk-up publishes Zammad.
	localFQDN = "127.0.0.1:58080"
	// pendingFor sets a pending ticket's reminder after the seed run, so
	// Zammad's scheduler never flips it back to open.
	pendingFor = 30 * 24 * time.Hour
	// Zammad's state for a pending ticket.
	zammadPending = "pending reminder"
)

// ErrNeedsReset means Zammad holds content without the matching marker: a
// changed seed, new certs, or a run that stopped halfway.
var ErrNeedsReset = errors.New("the helpdesk holds content from another seed or a run that stopped halfway; run make helpdesk-reset")

// Secrets are the values the Rails steps need. They travel only as
// environment variables of the runner.
type Secrets struct {
	AdminEmail     string
	AdminPassword  string
	BindPassword   string
	AssistantToken string
}

// Seeder fills a fresh Zammad.
type Seeder struct {
	Runner Runner
	// Admin is the REST client as the seed admin (basic auth).
	Admin *zammad.Client
	// Assistant is the REST client with ZAMMAD_TOKEN, for the drift check.
	Assistant *zammad.Client
	Content   Content
	Model     directory.Model
	Marker    string
	Group     string
	Secrets   Secrets
	// Now is the run time (for the pending reminder); defaults to time.Now.
	Now func() time.Time
	// RetentionDays is TICKET_RETENTION_DAYS, the noord-retention window.
	RetentionDays int
	// LDAPHost is the LDAPS host Zammad's LDAP source uses
	// (SEED_ZAMMAD_LDAP_HOST); empty means samba, the Compose service.
	LDAPHost string
	// PublicURL is Zammad's browser URL (ZAMMAD_PUBLIC_URL); bootstrap sets
	// the fqdn and http_type settings from it. Empty keeps the local stack's
	// http://127.0.0.1:58080.
	PublicURL string
}

// Result says what the run did.
type Result struct {
	AlreadySeeded bool
	Users         int
	Tickets       int
	Articles      int
	Categories    int
	Answers       int
	// RetentionDays and RetentionMatches are the noord-retention
	// scheduler's window and how many tickets it would delete now.
	RetentionDays    int
	RetentionMatches int
}

// bootstrapResult is bootstrap.rb's NOORD_RESULT.
type bootstrapResult struct {
	State          string                `json:"state"`
	GroupID        int                   `json:"group_id"`
	AdminID        int                   `json:"admin_id"`
	AssistantID    int                   `json:"assistant_id"`
	KBID           int                   `json:"kb_id"`
	KBLocaleID     int                   `json:"kb_locale_id"`
	OrganizationID int                   `json:"organization_id"`
	Roles          map[string]int        `json:"roles"`
	Users          map[string]syncedUser `json:"users"`
}

type syncedUser struct {
	ID           int      `json:"id"`
	Active       bool     `json:"active"`
	Organization string   `json:"organization"`
	Roles        []string `json:"roles"`
}

// Run bootstraps, checks the synced users, fills tickets and the KB, then
// backdates and writes the marker last. On already_seeded it only checks
// that ZAMMAD_TOKEN still belongs to the assistant.
func (s Seeder) Run(ctx context.Context) (Result, error) {
	boot, err := s.bootstrap(ctx)
	if err != nil {
		return Result{}, err
	}
	switch boot.State {
	case stateAlreadySeeded:
		if err := s.checkToken(ctx); err != nil {
			return Result{}, err
		}
		res := Result{AlreadySeeded: true}
		return res, s.retention(ctx, &res)
	case stateNeedsReset:
		return Result{}, ErrNeedsReset
	case stateFresh:
	default:
		return Result{}, fmt.Errorf("bootstrap reported unknown state %q", boot.State)
	}

	if err := s.checkUsers(boot.Users); err != nil {
		return Result{}, err
	}
	f := filler{s: s, boot: boot, now: s.now()}
	if err := f.tickets(ctx); err != nil {
		return Result{}, err
	}
	if err := f.knowledgeBase(ctx); err != nil {
		return Result{}, err
	}
	if err := s.backdate(ctx, f.payload); err != nil {
		return Result{}, err
	}
	res := Result{
		Users: len(boot.Users), Tickets: len(f.payload.Tickets), Articles: f.articles,
		Categories: f.categories, Answers: len(f.payload.Answers),
	}
	return res, s.retention(ctx, &res)
}

// retentionResult is retention.rb's NOORD_RESULT.
type retentionResult struct {
	Days    int `json:"days"`
	Matches int `json:"matches"`
}

// retention upserts the noord-retention scheduler with RetentionDays and
// records its window and current match count in res (spec 0010, AC-9).
func (s Seeder) retention(ctx context.Context, res *Result) error {
	env := map[string]string{"NOORD_RETENTION_DAYS": strconv.Itoa(s.RetentionDays)}
	raw, err := runStep(ctx, s.Runner, StepRetention, env, nil)
	if err != nil {
		return fmt.Errorf("retention scheduler: %w", err)
	}
	var r retentionResult
	if err := json.Unmarshal(raw, &r); err != nil {
		return fmt.Errorf("retention result: %w", err)
	}
	if r.Days != s.RetentionDays {
		return fmt.Errorf("retention scheduler: set %d days, want %d", r.Days, s.RetentionDays)
	}
	res.RetentionDays, res.RetentionMatches = r.Days, r.Matches
	return nil
}

func (s Seeder) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s Seeder) ldapHost() string {
	if s.LDAPHost != "" {
		return s.LDAPHost
	}
	return defaultLDAPHost
}

// site is the fqdn and http_type Zammad builds its links from.
func (s Seeder) site() (fqdn, httpType string, err error) {
	if s.PublicURL == "" {
		return localFQDN, "http", nil
	}
	u, err := url.Parse(s.PublicURL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "", "", errors.New("ZAMMAD_PUBLIC_URL must be an http or https URL with a host")
	}
	return u.Host, u.Scheme, nil
}

func (s Seeder) bootstrap(ctx context.Context) (bootstrapResult, error) {
	fqdn, httpType, err := s.site()
	if err != nil {
		return bootstrapResult{}, err
	}
	env := map[string]string{
		"NOORD_LDAP_HOST":       s.ldapHost(),
		"NOORD_FQDN":            fqdn,
		"NOORD_HTTP_TYPE":       httpType,
		"NOORD_MARKER":          s.Marker,
		"NOORD_ADMIN_EMAIL":     s.Secrets.AdminEmail,
		"NOORD_ADMIN_PASSWORD":  s.Secrets.AdminPassword,
		"NOORD_BIND_PASSWORD":   s.Secrets.BindPassword,
		"NOORD_ASSISTANT_TOKEN": s.Secrets.AssistantToken,
		"NOORD_GROUP":           s.Group,
	}
	raw, err := runStep(ctx, s.Runner, StepBootstrap, env, nil)
	if err != nil {
		var serr *StepError
		if errors.As(err, &serr) && strings.HasPrefix(serr.Output, "ldap") {
			return bootstrapResult{}, fmt.Errorf("zammad could not import from ldaps://%s:636: %w", s.ldapHost(), err)
		}
		return bootstrapResult{}, err
	}
	var res bootstrapResult
	if err := json.Unmarshal(raw, &res); err != nil {
		return bootstrapResult{}, fmt.Errorf("bootstrap result: %w", err)
	}
	return res, nil
}

// checkToken makes sure ZAMMAD_TOKEN still belongs to the assistant.
func (s Seeder) checkToken(ctx context.Context) error {
	me, err := s.Assistant.Me(ctx)
	if err != nil || me.Login != AssistantLogin {
		return fmt.Errorf("ZAMMAD_TOKEN is not the %s token (it changed since the seed); run make helpdesk-reset", AssistantLogin)
	}
	return nil
}

// checkUsers compares bootstrap's login map with the users: list before any
// write, listing every problem.
func (s Seeder) checkUsers(synced map[string]syncedUser) error {
	var problems []string
	for _, u := range s.Model.Users {
		zu, ok := synced[u.Username]
		switch {
		case !ok:
			problems = append(problems, u.Username+" is missing")
			continue
		case !zu.Active:
			problems = append(problems, u.Username+" is inactive")
		case zu.Organization != Organization:
			problems = append(problems, fmt.Sprintf("%s is in organization %q", u.Username, zu.Organization))
		}
		want := []string{"Customer"}
		if u.Username == Agent {
			want = []string{"Admin", "Agent"}
		}
		if !slices.Equal(zu.Roles, want) {
			problems = append(problems, fmt.Sprintf("%s has roles %v, want %v", u.Username, zu.Roles, want))
		}
	}
	for login := range synced {
		if _, ok := s.Model.User(login); !ok {
			problems = append(problems, login+" was synced but is not a users: entry (an admin or service account?)")
		}
	}
	if len(problems) > 0 {
		slices.Sort(problems)
		return fmt.Errorf("the LDAP sync does not match %s: %s; run make helpdesk-reset after fixing it",
			"the users: list", strings.Join(problems, "; "))
	}
	return nil
}

func (s Seeder) backdate(ctx context.Context, p backdatePayload) error {
	p.Marker = s.Marker
	data, err := json.Marshal(p)
	if err != nil {
		return fmt.Errorf("backdate payload: %w", err)
	}
	_, err = runStep(ctx, s.Runner, StepBackdate, map[string]string{"NOORD_MARKER": s.Marker}, data)
	return err
}
