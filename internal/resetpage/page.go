// Package resetpage is the password reset page the tool server serves on its
// own listener (spec 0011): GET /reset/{token} shows a form, POST sets the
// password as svc-pwreset inside audit.Recorder.Run and closes the ticket.
// It holds no session and no cookie; the token in the path is the only key.
// Only the tool server imports it.
package resetpage

import (
	"context"
	"embed"
	"errors"
	"html/template"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/toyinogun/AIITsupport/internal/audit"
	"github.com/toyinogun/AIITsupport/internal/ldap"
	"github.com/toyinogun/AIITsupport/internal/resetlink"
)

// Route is the reset page's one path pattern; the span is named after it,
// never the path, which holds the token.
const Route = "/reset/{token}"

// Fixed page texts (spec 0011, the reset page table).
const (
	textRulesGeneric = "Use a long password you haven't used here before. If it is refused, the page will say so."
	textFields       = "The two passwords must match and follow the rules below."
	textPolicy       = "The directory refused that password. Please choose another one that follows the rules below."
	textDone         = "Your password is changed. You can sign in now."
	textInvalid      = "This link is no longer valid. Send the IT assistant a new message in Slack to get a fresh one."
	textPaused       = "Password changes are paused right now. Please contact IT."
	textBlocked      = "This link can't be used for this account. Please contact IT."
	textFailed       = "The password could not be changed. Please try again, or contact IT."
	textTryLater     = "The service is busy. Please try again in a minute."
)

// Form limits (AC-8).
const (
	maxBodyBytes     = 4 << 10
	maxPasswordRunes = 256
	policyCacheTTL   = 5 * time.Minute
)

//go:embed templates/*.html
var templateFS embed.FS

var (
	formPage    = template.Must(template.ParseFS(templateFS, "templates/layout.html", "templates/form.html"))
	messagePage = template.Must(template.ParseFS(templateFS, "templates/layout.html", "templates/message.html"))
)

// Links is the reset_links store. *resetlink.Store satisfies it.
type Links interface {
	Valid(ctx context.Context, token string) (resetlink.Link, error)
	Claim(ctx context.Context, token string) (resetlink.Link, error)
	Revert(ctx context.Context, l resetlink.Link) (bool, error)
}

// Directory is the reader, bound as svc-itsupport. *ldap.Client satisfies it.
type Directory interface {
	FindUserByAccount(ctx context.Context, name string) (ldap.User, bool, error)
	Lock(ctx context.Context, dn string) (ldap.Lock, error)
	PasswordPolicy(ctx context.Context) (ldap.PasswordPolicy, error)
	PasswordLastSet(ctx context.Context, dn string) (time.Time, error)
}

// PasswordWriter is the reset write, bound as svc-pwreset. *ldap.PasswordWriter satisfies it.
type PasswordWriter interface {
	SetPassword(ctx context.Context, dn, password string) error
}

// Tickets closes the link's ticket. *tools.Helpdesk satisfies it.
type Tickets interface {
	CloseWithNote(ctx context.Context, id int, body string) (bool, error)
}

// Accounts is the protected group check. *tools.AccountFence satisfies it.
type Accounts interface {
	CheckAccount(ctx context.Context, dn string) error
}

// KillSwitch says whether account changes may run. tools.KillSwitch satisfies it.
type KillSwitch interface {
	AccountChangesAllowed() bool
}

// Config is the page's dependencies and settings.
type Config struct {
	Links       Links
	Directory   Directory
	Writer      PasswordWriter
	Tickets     Tickets
	Accounts    Accounts
	Recorder    *audit.Recorder
	KillSwitch  KillSwitch
	BaseDN      string        // LDAP_BASE_DN
	UserOUs     []string      // TOOL_USER_OUS
	CallTimeout time.Duration // TOOL_CALL_TIMEOUT, the POST's one deadline
	MaxInflight int           // RESET_MAX_INFLIGHT
	SecureHSTS  bool          // RESET_PUBLIC_URL is https
	Logger      *slog.Logger
}

// Page serves GET and POST on Route.
type Page struct {
	cfg    Config
	slots  chan struct{}
	policy policyCache
	now    func() time.Time
}

// New checks cfg and returns the page.
func New(cfg Config) (*Page, error) {
	if cfg.Links == nil || cfg.Directory == nil || cfg.Writer == nil || cfg.Tickets == nil || cfg.Accounts == nil || cfg.Recorder == nil ||
		cfg.KillSwitch == nil || cfg.Logger == nil || cfg.CallTimeout <= 0 || cfg.MaxInflight < 1 {
		return nil, errors.New("reset page: needs every dependency, a positive call timeout, and at least one in flight slot")
	}
	return &Page{cfg: cfg, slots: make(chan struct{}, cfg.MaxInflight), now: time.Now}, nil
}

// Handler is the reset listener's whole mux: Route and GET /healthz, and
// nothing else (AC-16). Every response carries the security headers.
func (p *Page) Handler(mux *http.ServeMux) http.Handler {
	mux.HandleFunc("GET "+Route, p.serveForm)
	mux.HandleFunc("POST "+Route, p.serveSubmit)
	return securityHeaders(mux, p.cfg.SecureHSTS)
}

// serveForm is AC-7: one indexed read, at most one LDAP read, no writes.
func (p *Page) serveForm(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	if !resetlink.WellFormed(token) {
		p.message(w, http.StatusGone, textInvalid)
		return
	}
	link, err := p.cfg.Links.Valid(r.Context(), token)
	if err != nil {
		p.message(w, http.StatusGone, textInvalid)
		return
	}
	p.form(r.Context(), w, http.StatusOK, link.Account, "")
}

// form renders the form with the rules and an optional message.
func (p *Page) form(ctx context.Context, w http.ResponseWriter, status int, account, message string) {
	render(w, status, formPage, formData{Account: account, Message: message, Rules: p.rules(ctx)})
}

func (p *Page) message(w http.ResponseWriter, status int, text string) {
	render(w, status, messagePage, messageData{Message: text})
}

type formData struct {
	Account string
	Message string
	Rules   []string
}

type messageData struct {
	Message string
}

func render(w http.ResponseWriter, status int, t *template.Template, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_ = t.ExecuteTemplate(w, "layout", data) // a failed write means the client is gone
}

// rules is the AC-7 rules text. Samba ignores the password policy hints
// control on a reset (spec 0011 build step 1, journal), so the history rule
// is not enforced here and the page does not claim it.
func (p *Page) rules(ctx context.Context) []string {
	pol, ok := p.policy.get(ctx, p.cfg.Directory, p.now())
	if !ok {
		return []string{textRulesGeneric}
	}
	out := []string{"At least " + strconv.Itoa(pol.MinLength) + " characters."}
	if pol.Complex {
		out = append(out, "Use three of: capital letters, small letters, numbers, symbols. Don't include your username.")
	}
	return out
}

// policyCache holds the domain's password rules for up to 5 minutes. A
// failed read is not cached.
type policyCache struct {
	mu     sync.Mutex
	policy ldap.PasswordPolicy
	at     time.Time
}

func (c *policyCache) get(ctx context.Context, dir Directory, now time.Time) (ldap.PasswordPolicy, bool) {
	c.mu.Lock()
	pol, at := c.policy, c.at
	c.mu.Unlock()
	if !at.IsZero() && now.Sub(at) < policyCacheTTL {
		return pol, true
	}
	fresh, err := dir.PasswordPolicy(ctx)
	if err != nil {
		return pol, !at.IsZero() // a stale value beats none
	}
	c.mu.Lock()
	c.policy, c.at = fresh, now
	c.mu.Unlock()
	return fresh, true
}

// minLength is the cached minPwdLength, or 0 when unknown.
func (c *policyCache) minLength() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.at.IsZero() {
		return 0
	}
	return c.policy.MinLength
}

// securityHeaders sets the AC-16 headers on every response.
func securityHeaders(next http.Handler, hsts bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Cache-Control", "no-store")
		h.Set("Pragma", "no-cache")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Content-Security-Policy",
			"default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'")
		if hsts {
			h.Set("Strict-Transport-Security", "max-age=31536000")
		}
		next.ServeHTTP(w, r)
	})
}
