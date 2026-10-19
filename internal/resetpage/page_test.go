package resetpage

import (
	"bytes"
	"context"
	"errors"
	"html"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	goldap "github.com/go-ldap/ldap/v3"
	"github.com/google/uuid"

	"github.com/toyinogun/AIITsupport/internal/audit"
	"github.com/toyinogun/AIITsupport/internal/ldap"
	"github.com/toyinogun/AIITsupport/internal/resetlink"
	"github.com/toyinogun/AIITsupport/internal/server"
	"github.com/toyinogun/AIITsupport/internal/tools"
	"github.com/toyinogun/AIITsupport/internal/tools/toolstest"
)

const (
	base    = "DC=ad,DC=noord,DC=example"
	evaDN   = "CN=Eva Peters,OU=Sales,OU=Users,OU=Noord," + base
	goodPwd = "Correct-Horse-7x"
	// protectedDN is a staff account the fake fence protects.
	protectedDN = "CN=Ruben Mulder,OU=IT,OU=Users,OU=Noord," + base
)

// memLinks is an in memory link store with one link per token.
type memLinks struct {
	mu        sync.Mutex
	links     map[string]*resetlink.Link
	status    map[string]string
	revertErr error
	reverts   int
	reads     int // Valid and Claim calls
}

func newMemLinks() (*memLinks, string) {
	token := resetlink.NewToken()
	m := &memLinks{links: map[string]*resetlink.Link{}, status: map[string]string{}}
	m.links[token] = &resetlink.Link{
		ID: uuid.New(), Account: "eva.peters", RequesterSlackID: "U01EVA", ChannelID: "D01EVA",
		ThreadTS: "1.0", TicketID: 42, ExpiresAt: time.Now().Add(15 * time.Minute),
	}
	m.status[token] = "live"
	return m, token
}

// add stores one more live link for eva.peters and returns its token.
func (m *memLinks) add() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	token := resetlink.NewToken()
	m.links[token] = &resetlink.Link{
		ID: uuid.New(), Account: "eva.peters", RequesterSlackID: "U01EVA", ChannelID: "D01EVA",
		ThreadTS: "1.0", TicketID: 42, ExpiresAt: time.Now().Add(15 * time.Minute),
	}
	m.status[token] = "live"
	return token
}

func (m *memLinks) readCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.reads
}

func (m *memLinks) Valid(_ context.Context, token string) (resetlink.Link, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reads++
	if m.status[token] != "live" {
		return resetlink.Link{}, resetlink.ErrNotValid
	}
	return *m.links[token], nil
}

func (m *memLinks) Claim(_ context.Context, token string) (resetlink.Link, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reads++
	if m.status[token] != "live" {
		return resetlink.Link{}, resetlink.ErrNotValid
	}
	m.status[token] = "used"
	l := *m.links[token]
	l.UsedAt = time.Now()
	return l, nil
}

func (m *memLinks) Revert(_ context.Context, l resetlink.Link) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reverts++
	if m.revertErr != nil {
		return false, m.revertErr
	}
	for tok, link := range m.links {
		if link.ID == l.ID && m.status[tok] == "used" {
			m.status[tok] = "live"
			return true, nil
		}
	}
	return false, nil
}

func (m *memLinks) state(token string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.status[token]
}

// fakeDir is the reader plus the writer.
type fakeDir struct {
	mu        sync.Mutex
	user      ldap.User
	found     bool
	locked    bool
	policyErr error
	writeErr  error
	writes    []string
	lastSet   time.Time
	policies  int // PasswordPolicy calls
	// hold, when set, makes SetPassword report on entered and then wait
	// until hold is closed, so a test can keep a write in flight.
	hold    chan struct{}
	entered chan struct{}
}

func newFakeDir() *fakeDir {
	return &fakeDir{
		user:  ldap.User{DN: evaDN, Username: "eva.peters", Mail: "eva.peters@noord.example", Enabled: true},
		found: true, locked: true,
	}
}

func (d *fakeDir) FindUserByAccount(context.Context, string) (ldap.User, bool, error) {
	return d.user, d.found, nil
}

func (d *fakeDir) Lock(context.Context, string) (ldap.Lock, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return ldap.Lock{Locked: d.locked}, nil
}

func (d *fakeDir) PasswordPolicy(context.Context) (ldap.PasswordPolicy, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.policies++
	if d.policyErr != nil {
		return ldap.PasswordPolicy{}, d.policyErr
	}
	return ldap.PasswordPolicy{MinLength: 7, Complex: true, HistoryLength: 24}, nil
}

func (d *fakeDir) PasswordLastSet(context.Context, string) (time.Time, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.lastSet, nil
}

func (d *fakeDir) policyReads() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.policies
}

func (d *fakeDir) writeCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.writes)
}

func (d *fakeDir) SetPassword(_ context.Context, dn, _ string) error {
	if d.hold != nil {
		d.entered <- struct{}{}
		<-d.hold
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.writeErr != nil {
		return d.writeErr
	}
	d.writes = append(d.writes, dn)
	d.locked = false
	d.lastSet = time.Now()
	return nil
}

type fakeTickets struct {
	mu     sync.Mutex
	closed []int
	notes  []string
	err    error
}

func (f *fakeTickets) CloseWithNote(_ context.Context, id int, body string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return false, f.err
	}
	f.closed, f.notes = append(f.closed, id), append(f.notes, body)
	return true, nil
}

// fakeAccounts protects the DNs in protected.
type fakeAccounts struct {
	protected map[string]bool
}

func (f *fakeAccounts) CheckAccount(_ context.Context, dn string) error {
	if f.protected[dn] {
		return tools.ErrGroupProtected
	}
	return nil
}

type flipSwitch struct {
	mu      sync.Mutex
	allowed []bool // answers in order; the last one repeats
}

func (s *flipSwitch) AccountChangesAllowed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	a := s.allowed[0]
	if len(s.allowed) > 1 {
		s.allowed = s.allowed[1:]
	}
	return a
}

type pageHarness struct {
	t       *testing.T
	srv     *httptest.Server
	links   *memLinks
	token   string
	dir     *fakeDir
	tickets *fakeTickets
	acct    *fakeAccounts
	ks      *flipSwitch
	store   *toolstest.MemStore
	logs    *bytes.Buffer
	page    *Page
	skew    atomic.Int64 // how far the page's clock runs ahead, in nanoseconds
}

func newPageHarness(t *testing.T) *pageHarness {
	t.Helper()
	return newPageHarnessWith(t, nil)
}

// newPageHarnessWith is newPageHarness with change applied to the page's
// Config before New.
func newPageHarnessWith(t *testing.T, change func(*Config)) *pageHarness {
	t.Helper()
	links, token := newMemLinks()
	h := &pageHarness{
		t: t, links: links, token: token, dir: newFakeDir(), tickets: &fakeTickets{}, acct: &fakeAccounts{protected: map[string]bool{}},
		ks: &flipSwitch{allowed: []bool{true}}, store: &toolstest.MemStore{}, logs: &bytes.Buffer{},
	}
	logger := slog.New(slog.NewJSONHandler(h.logs, nil))
	rec, err := audit.NewRecorder(h.store, audit.Config{OutcomeRetries: 0, WriteTimeout: time.Second, MaxPayloadBytes: 4096}, logger)
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{
		Links: links, Directory: h.dir, Writer: h.dir, Tickets: h.tickets, Accounts: h.acct, Recorder: rec, KillSwitch: h.ks,
		BaseDN: base, UserOUs: []string{"OU=Users,OU=Noord"}, CallTimeout: 5 * time.Second, MaxInflight: 2,
		SecureHSTS: true, Logger: logger,
	}
	if change != nil {
		change(&cfg)
	}
	page, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	page.now = func() time.Time { return time.Now().Add(time.Duration(h.skew.Load())) }
	h.page = page
	mux := http.NewServeMux()
	h.srv = httptest.NewServer(page.Handler(mux))
	t.Cleanup(h.srv.Close)
	return h
}

// reply is one response, read and closed.
type reply struct {
	StatusCode int
	Header     http.Header
}

func (h *pageHarness) get(path string) (reply, string) {
	h.t.Helper()
	req, err := http.NewRequestWithContext(h.t.Context(), http.MethodGet, h.srv.URL+path, nil)
	if err != nil {
		h.t.Fatal(err)
	}
	return h.do(req)
}

func (h *pageHarness) post(password, confirm string) (reply, string) {
	h.t.Helper()
	return h.postTo(h.token, password, confirm)
}

// postTo posts the form to another token's link.
func (h *pageHarness) postTo(token, password, confirm string) (reply, string) {
	h.t.Helper()
	form := url.Values{"password": {password}, "confirm": {confirm}}
	return h.postRaw("/reset/"+token, form.Encode())
}

// postRaw posts body as a form to path, which may carry a query string.
func (h *pageHarness) postRaw(path, body string) (reply, string) {
	h.t.Helper()
	req, err := http.NewRequestWithContext(h.t.Context(), http.MethodPost, h.srv.URL+path, strings.NewReader(body))
	if err != nil {
		h.t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return h.do(req)
}

func (h *pageHarness) do(req *http.Request) (reply, string) {
	h.t.Helper()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		h.t.Fatal(err)
	}
	return reply{StatusCode: resp.StatusCode, Header: resp.Header}, string(b)
}

// shows reports whether body holds text as html/template escapes it.
func shows(body, text string) bool { return strings.Contains(body, html.EscapeString(text)) }

func (h *pageHarness) outcomes() map[string]string {
	out := map[string]string{}
	for _, r := range h.store.All() {
		if r.Kind == "outcome" {
			out[r.Outcome.String] = r.Error.String
		}
	}
	return out
}

// covers: spec 0011 AC-7, AC-16. GET shows the form, changes nothing, and
// sets every header; a bad token gets the invalid page.
func TestGetForm(t *testing.T) {
	h := newPageHarness(t)
	resp, body := h.get("/reset/" + h.token)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "Set a new password for eva.peters") ||
		!strings.Contains(body, "At least 7 characters.") || strings.Contains(body, "<script") {
		t.Fatalf("GET = %d %s", resp.StatusCode, body)
	}
	if strings.Contains(body, "last 24 passwords") {
		t.Error("the rules claim a history rule Samba does not enforce on a reset")
	}
	for k, want := range map[string]string{
		"Cache-Control": "no-store", "Pragma": "no-cache", "Referrer-Policy": "no-referrer",
		"X-Content-Type-Options": "nosniff", "Strict-Transport-Security": "max-age=31536000",
		"Content-Security-Policy": "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'",
	} {
		if got := resp.Header.Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	if h.links.state(h.token) != "live" || len(h.store.All()) != 0 {
		t.Error("GET changed the link or wrote an audit row")
	}
	for _, path := range []string{"/reset/short", "/reset/" + resetlink.NewToken()} {
		if resp, body := h.get(path); resp.StatusCode != http.StatusGone || !shows(body, textInvalid) {
			t.Errorf("GET %s = %d", path, resp.StatusCode)
		}
	}
	if resp, _ := h.get("/mcp"); resp.StatusCode != http.StatusNotFound {
		t.Errorf("/mcp on the reset listener = %d, want 404", resp.StatusCode)
	}
}

// covers: spec 0011 AC-7. Without the policy the page shows the generic line.
func TestGetFormGenericRules(t *testing.T) {
	h := newPageHarness(t)
	h.dir.policyErr = errors.New("ldap down")
	if resp, body := h.get("/reset/" + h.token); resp.StatusCode != http.StatusOK || !shows(body, "If it is refused, the page will say so.") {
		t.Fatalf("GET = %d %s", resp.StatusCode, body)
	}
}

// covers: spec 0011 AC-11. Two preview GETs, then a POST: the password is
// set, the lock cleared, the ticket closed with both notes, one audit pair,
// and the link is used.
func TestPostHappyPath(t *testing.T) {
	h := newPageHarness(t)
	h.get("/reset/" + h.token)
	h.get("/reset/" + h.token)
	resp, body := h.post(goodPwd, goodPwd)
	if resp.StatusCode != http.StatusOK || !shows(body, textDone) {
		t.Fatalf("POST = %d %s", resp.StatusCode, body)
	}
	if len(h.dir.writes) != 1 || h.dir.writes[0] != evaDN {
		t.Errorf("writes = %v", h.dir.writes)
	}
	if len(h.tickets.closed) != 1 || h.tickets.closed[0] != 42 || h.tickets.notes[0] != noteReset+"\n"+noteUnlocked {
		t.Errorf("tickets = %+v", h.tickets)
	}
	if h.links.state(h.token) != "used" {
		t.Errorf("link = %s, want used", h.links.state(h.token))
	}
	rows := h.store.All()
	if len(rows) != 2 || rows[1].ToolName != ToolPasswordReset || rows[1].Outcome.String != "ok" ||
		rows[1].RequesterAccount.String != "eva.peters" || rows[1].ZammadTicketID.Int64 != 42 {
		t.Fatalf("audit rows = %+v", rows)
	}
	if strings.Contains(string(rows[0].Arguments)+string(rows[1].Result), goodPwd) {
		t.Error("the password reached the audit log")
	}
	if !strings.Contains(h.logs.String(), `"msg":"reset_submit"`) || strings.Contains(h.logs.String(), goodPwd) {
		t.Errorf("logs = %s", h.logs.String())
	}
	if resp, _ := h.post(goodPwd, goodPwd); resp.StatusCode != http.StatusGone {
		t.Errorf("second POST = %d, want 410", resp.StatusCode)
	}
}

// covers: spec 0011 AC-8. Field errors give the form again and touch nothing.
func TestPostFieldErrors(t *testing.T) {
	h := newPageHarness(t)
	h.get("/reset/" + h.token) // caches minPwdLength 7
	for _, c := range [][2]string{{"", ""}, {goodPwd, goodPwd + "x"}, {"Ab1-", "Ab1-"}, {strings.Repeat("a", 257), strings.Repeat("a", 257)}} {
		resp, body := h.post(c[0], c[1])
		if resp.StatusCode != http.StatusUnprocessableEntity || !shows(body, textFields) {
			t.Errorf("POST %q = %d", c[0], resp.StatusCode)
		}
	}
	if h.links.state(h.token) != "live" || len(h.store.All()) != 0 || len(h.dir.writes) != 0 {
		t.Error("a field error changed something")
	}
}

// covers: spec 0011 AC-12. A policy refusal gives the form with the policy
// message, reverts the claim, and a retry works.
func TestPostPolicyRefusalReverts(t *testing.T) {
	h := newPageHarness(t)
	h.dir.writeErr = goldap.NewError(goldap.LDAPResultConstraintViolation, errors.New("password too simple"))
	resp, body := h.post(goodPwd, goodPwd)
	if resp.StatusCode != http.StatusUnprocessableEntity || !shows(body, textPolicy) {
		t.Fatalf("POST = %d %s", resp.StatusCode, body)
	}
	if h.links.state(h.token) != "live" {
		t.Fatalf("link = %s, want live after the revert", h.links.state(h.token))
	}
	if got := h.outcomes()["error"]; got != reasonPolicy {
		t.Errorf("audit error reason = %q", got)
	}
	h.dir.writeErr = nil
	if resp, _ := h.post(goodPwd, goodPwd); resp.StatusCode != http.StatusOK {
		t.Errorf("retry = %d, want 200", resp.StatusCode)
	}
}

// covers: spec 0011 AC-9. Paused before the claim: 503, a read_only refusal,
// and the link stays live; paused after the claim reverts it.
func TestPostPaused(t *testing.T) {
	h := newPageHarness(t)
	h.ks.allowed = []bool{false}
	if resp, body := h.post(goodPwd, goodPwd); resp.StatusCode != http.StatusServiceUnavailable || !shows(body, textPaused) {
		t.Fatalf("POST = %d %s", resp.StatusCode, body)
	}
	if h.links.state(h.token) != "live" || h.outcomes()["read_only"] != reasonPaused {
		t.Errorf("link %s, outcomes %v", h.links.state(h.token), h.outcomes())
	}

	h.ks.allowed = []bool{true, false}
	if resp, _ := h.post(goodPwd, goodPwd); resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("POST flipped after the claim = %d", resp.StatusCode)
	}
	if h.links.state(h.token) != "live" || len(h.dir.writes) != 0 {
		t.Errorf("link %s, writes %v", h.links.state(h.token), h.dir.writes)
	}
}

// covers: spec 0011 AC-10. A disabled, missing, or out of OU account gets
// blocked with its audit refusal; the link stays live.
func TestPostAccountRecheck(t *testing.T) {
	cases := []struct {
		name    string
		change  func(d *fakeDir)
		outcome string
		reason  string
	}{
		{"disabled", func(d *fakeDir) { d.user.Enabled = false }, "denied_policy", reasonDisabled},
		{"missing", func(d *fakeDir) { d.found = false }, "denied_identity", reasonNotMatched},
		{"protected", func(d *fakeDir) { d.user.DN = protectedDN }, "denied_policy", reasonProtected},
		{"outside the OUs", func(d *fakeDir) { d.user.DN = "CN=adm,OU=Admin Accounts,OU=Noord," + base }, "denied_policy", reasonProtected},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newPageHarness(t)
			h.acct.protected[protectedDN] = true
			c.change(h.dir)
			if resp, body := h.post(goodPwd, goodPwd); resp.StatusCode != http.StatusForbidden || !shows(body, textBlocked) {
				t.Fatalf("POST = %d %s", resp.StatusCode, body)
			}
			if h.links.state(h.token) != "live" || h.outcomes()[c.outcome] != c.reason {
				t.Errorf("link %s, outcomes %v", h.links.state(h.token), h.outcomes())
			}
		})
	}
}

// covers: spec 0011 AC-12. A timeout whose write landed (pwdLastSet newer
// than the claim) counts as success; a failed revert leaves the link used.
func TestPostAmbiguousAndRevertFailure(t *testing.T) {
	h := newPageHarness(t)
	h.dir.writeErr = context.DeadlineExceeded
	h.dir.lastSet = time.Now().Add(time.Hour)
	if resp, _ := h.post(goodPwd, goodPwd); resp.StatusCode != http.StatusOK {
		t.Fatalf("POST with a landed write = %d, want 200", resp.StatusCode)
	}

	h2 := newPageHarness(t)
	h2.dir.writeErr = goldap.NewError(goldap.LDAPResultInsufficientAccessRights, errors.New("no"))
	h2.links.revertErr = errors.New("db down")
	if resp, body := h2.post(goodPwd, goodPwd); resp.StatusCode != http.StatusBadGateway || !shows(body, textFailed) {
		t.Fatalf("POST = %d %s", resp.StatusCode, body)
	}
	if h2.links.state(h2.token) != "used" || !strings.Contains(h2.logs.String(), `"outcome":"revert_failed"`) {
		t.Errorf("link %s, logs %s", h2.links.state(h2.token), h2.logs.String())
	}
}

// covers: spec 0011 AC-14. Zammad down after the write: done, ticket_closed false.
func TestPostTicketCloseFails(t *testing.T) {
	h := newPageHarness(t)
	h.tickets.err = errors.New("zammad 502")
	if resp, body := h.post(goodPwd, goodPwd); resp.StatusCode != http.StatusOK || !shows(body, textDone) {
		t.Fatalf("POST = %d %s", resp.StatusCode, body)
	}
	for _, r := range h.store.All() {
		if r.Kind == "outcome" && !strings.Contains(string(r.Result), `"ticket_closed":false`) {
			t.Errorf("result = %s", r.Result)
		}
	}
}

// tryPost posts a valid form to token from any goroutine: it never calls
// t.Fatal, so a failure comes back as an error.
func (h *pageHarness) tryPost(token string) (int, error) {
	form := url.Values{"password": {goodPwd}, "confirm": {goodPwd}}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, h.srv.URL+"/reset/"+token, strings.NewReader(form.Encode()))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode, nil
}

// holdWrites makes every SetPassword wait until the returned release runs.
func (h *pageHarness) holdWrites() (release func()) {
	h.dir.hold, h.dir.entered = make(chan struct{}), make(chan struct{}, 8)
	return sync.OnceFunc(func() { close(h.dir.hold) })
}

// waitEntered waits until n writes are in flight.
func (h *pageHarness) waitEntered(n int) {
	h.t.Helper()
	for range n {
		select {
		case <-h.dir.entered:
		case <-time.After(5 * time.Second):
			h.t.Fatal("a write never started")
		}
	}
}

// covers: spec 0011 AC-16. With RESET_MAX_INFLIGHT POSTs already writing,
// one more gets try_later (503) and its link stays live; the held ones finish.
func TestPostInflightCap(t *testing.T) {
	h := newPageHarness(t) // MaxInflight 2
	release := h.holdWrites()
	defer release()
	held := []string{h.token, h.links.add()}
	extra := h.links.add()

	var wg sync.WaitGroup
	statuses := make([]int, len(held))
	for i, tok := range held {
		wg.Go(func() {
			code, err := h.tryPost(tok)
			if err != nil {
				t.Errorf("held POST: %v", err)
			}
			statuses[i] = code
		})
	}
	h.waitEntered(len(held))

	resp, body := h.postTo(extra, goodPwd, goodPwd)
	if resp.StatusCode != http.StatusServiceUnavailable || !shows(body, textTryLater) {
		t.Errorf("POST past the cap = %d, want 503 try_later", resp.StatusCode)
	}
	if h.links.state(extra) != "live" {
		t.Errorf("the refused link is %s, want live", h.links.state(extra))
	}

	release()
	wg.Wait()
	for i, code := range statuses {
		if code != http.StatusOK {
			t.Errorf("held POST %d = %d, want 200", i, code)
		}
	}
	if h.dir.writeCount() != len(held) {
		t.Errorf("writes = %d, want %d", h.dir.writeCount(), len(held))
	}
}

// covers: spec 0011 AC-13. Two POSTs on one link at once: the second gets
// the invalid page (410) while the first is writing, and AD is written once.
func TestPostSameLinkTwiceAtOnce(t *testing.T) {
	h := newPageHarness(t)
	release := h.holdWrites()
	defer release()

	var wg sync.WaitGroup
	var first int
	wg.Go(func() {
		code, err := h.tryPost(h.token)
		if err != nil {
			t.Errorf("first POST: %v", err)
		}
		first = code
	})
	h.waitEntered(1)

	if resp, body := h.post(goodPwd, goodPwd); resp.StatusCode != http.StatusGone || !shows(body, textInvalid) {
		t.Errorf("second POST = %d, want 410 invalid", resp.StatusCode)
	}
	release()
	wg.Wait()
	if first != http.StatusOK {
		t.Errorf("first POST = %d, want 200", first)
	}
	if h.dir.writeCount() != 1 {
		t.Errorf("writes = %d, want exactly 1", h.dir.writeCount())
	}
}

// covers: spec 0011 AC-12. Every failure short of success reverts the claim,
// so the link still works: the audit log down (try_later 503), a timeout
// whose write did not land (failed 502), and any other directory error
// (failed 502).
func TestPostFailuresRevert(t *testing.T) {
	cases := []struct {
		name    string
		breakIt func(h *pageHarness)
		status  int
		text    string
	}{
		{"audit log down", func(h *pageHarness) { h.store.IntentErr = errors.New("db down") }, http.StatusServiceUnavailable, textTryLater},
		{"timeout, write not landed", func(h *pageHarness) { h.dir.writeErr = context.DeadlineExceeded }, http.StatusBadGateway, textFailed},
		{"insufficient access", func(h *pageHarness) {
			h.dir.writeErr = goldap.NewError(goldap.LDAPResultInsufficientAccessRights, errors.New("no"))
		}, http.StatusBadGateway, textFailed},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newPageHarness(t)
			c.breakIt(h)
			resp, body := h.post(goodPwd, goodPwd)
			if resp.StatusCode != c.status || !shows(body, c.text) {
				t.Fatalf("POST = %d %s", resp.StatusCode, body)
			}
			if h.links.state(h.token) != "live" {
				t.Errorf("link = %s, want live after the revert", h.links.state(h.token))
			}
			if h.dir.writeCount() != 0 || len(h.tickets.closed) != 0 {
				t.Errorf("writes %d, tickets closed %v; want none", h.dir.writeCount(), h.tickets.closed)
			}
		})
	}
}

// covers: spec 0011 AC-9, AC-12. A refusal whose audit row cannot be written
// gets try_later (503), not the refusal page.
func TestPostRefusalWithAuditDown(t *testing.T) {
	h := newPageHarness(t)
	h.ks.allowed = []bool{false}
	h.store.IntentErr = errors.New("db down")
	if resp, body := h.post(goodPwd, goodPwd); resp.StatusCode != http.StatusServiceUnavailable || !shows(body, textTryLater) {
		t.Fatalf("POST = %d %s", resp.StatusCode, body)
	}
	if h.links.state(h.token) != "live" {
		t.Errorf("link = %s, want live", h.links.state(h.token))
	}
}

// covers: spec 0011 AC-8. The POST reads only the form body: a password in
// the query string does not count, and a body over 4 KiB is a field error.
// Neither writes anything.
func TestPostReadsOnlyACappedBody(t *testing.T) {
	h := newPageHarness(t)
	q := url.Values{"password": {goodPwd}, "confirm": {goodPwd}}.Encode()
	if resp, body := h.postRaw("/reset/"+h.token+"?"+q, ""); resp.StatusCode != http.StatusUnprocessableEntity || !shows(body, textFields) {
		t.Errorf("query string POST = %d, want 422 fields", resp.StatusCode)
	}
	big := url.Values{"password": {goodPwd}, "confirm": {goodPwd}, "pad": {strings.Repeat("x", 5<<10)}}.Encode()
	if resp, body := h.postRaw("/reset/"+h.token, big); resp.StatusCode != http.StatusUnprocessableEntity || !shows(body, textFields) {
		t.Errorf("oversized POST = %d, want 422 fields", resp.StatusCode)
	}
	if h.links.state(h.token) != "live" || len(h.store.All()) != 0 || h.dir.writeCount() != 0 {
		t.Error("a rejected body changed something")
	}
}

// covers: spec 0011 AC-8. Before the page has read the domain rules, it
// does not guess a minimum length: a short password goes to the directory,
// which enforces it.
func TestPostLengthCheckWaitsForTheRules(t *testing.T) {
	h := newPageHarness(t) // no GET, so nothing is cached
	if resp, _ := h.post("Ab1-x", "Ab1-x"); resp.StatusCode != http.StatusOK {
		t.Fatalf("POST = %d, want the directory to decide", resp.StatusCode)
	}
	if h.dir.writeCount() != 1 {
		t.Errorf("writes = %d, want 1", h.dir.writeCount())
	}
}

// covers: spec 0011 AC-7. The rules are read once and cached for up to 5
// minutes, then read again.
func TestRulesAreCachedForFiveMinutes(t *testing.T) {
	h := newPageHarness(t)
	h.get("/reset/" + h.token)
	h.get("/reset/" + h.token)
	if n := h.dir.policyReads(); n != 1 {
		t.Errorf("policy reads after two GETs = %d, want 1", n)
	}
	h.skew.Store(int64(6 * time.Minute))
	if resp, body := h.get("/reset/" + h.token); resp.StatusCode != http.StatusOK || !strings.Contains(body, "At least 7 characters.") {
		t.Fatalf("GET = %d %s", resp.StatusCode, body)
	}
	if n := h.dir.policyReads(); n != 2 {
		t.Errorf("policy reads after the cache aged out = %d, want 2", n)
	}
}

// covers: spec 0011 AC-7. A malformed token gets the invalid page without a
// database read, on GET and on POST.
func TestMalformedTokenSkipsTheDatabase(t *testing.T) {
	h := newPageHarness(t)
	for _, tok := range []string{"short", strings.Repeat("+", resetlink.TokenLen), h.token[:resetlink.TokenLen-1] + "="} {
		if resp, body := h.get("/reset/" + url.PathEscape(tok)); resp.StatusCode != http.StatusGone || !shows(body, textInvalid) {
			t.Errorf("GET %q = %d, want 410", tok, resp.StatusCode)
		}
		if resp, _ := h.postTo(url.PathEscape(tok), goodPwd, goodPwd); resp.StatusCode != http.StatusGone {
			t.Errorf("POST %q = %d, want 410", tok, resp.StatusCode)
		}
	}
	if n := h.links.readCount(); n != 0 {
		t.Errorf("database reads = %d, want 0", n)
	}
}

// covers: spec 0011 AC-16. On the real reset mux the listener answers
// /healthz with the security headers, refuses other methods on the route,
// and has no /mcp.
func TestResetListenerRoutes(t *testing.T) {
	h := newPageHarness(t)
	srv := httptest.NewServer(h.page.Handler(server.NewMux()))
	t.Cleanup(srv.Close)
	h.srv = srv

	resp, _ := h.get("/healthz")
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Cache-Control") != "no-store" || resp.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("GET /healthz = %d, headers %v", resp.StatusCode, resp.Header)
	}
	if resp, _ := h.get("/mcp"); resp.StatusCode != http.StatusNotFound {
		t.Errorf("GET /mcp = %d, want 404", resp.StatusCode)
	}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPut, srv.URL+"/reset/"+h.token, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp, _ := h.do(req); resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("PUT on the route = %d, want 405", resp.StatusCode)
	}
	if h.links.state(h.token) != "live" {
		t.Error("a non GET/POST request touched the link")
	}
}

// covers: spec 0011 AC-16. HSTS is sent only when RESET_PUBLIC_URL is https.
func TestNoHSTSWithoutHTTPS(t *testing.T) {
	h := newPageHarnessWith(t, func(c *Config) { c.SecureHSTS = false })
	resp, _ := h.get("/reset/" + h.token)
	if got := resp.Header.Get("Strict-Transport-Security"); got != "" {
		t.Errorf("Strict-Transport-Security = %q, want none over http", got)
	}
	if resp.Header.Get("Content-Security-Policy") == "" {
		t.Error("the other headers went missing too")
	}
}

// covers: spec 0011 AC-11, AC-19. An account that was not locked gets only
// the reset note; the audit pair carries the link's Slack ids, thread,
// ticket, and account; no log line holds the token or the account name.
func TestPostNotLockedAndAuditFields(t *testing.T) {
	h := newPageHarness(t)
	h.dir.locked = false
	if resp, _ := h.post(goodPwd, goodPwd); resp.StatusCode != http.StatusOK {
		t.Fatalf("POST = %d", resp.StatusCode)
	}
	if len(h.tickets.notes) != 1 || h.tickets.notes[0] != noteReset {
		t.Errorf("notes = %q, want only the reset note", h.tickets.notes)
	}
	var out bool
	for _, r := range h.store.All() {
		if r.Kind != "outcome" {
			continue
		}
		out = true
		if r.RequesterSlackID != "U01EVA" || r.SlackChannelID.String != "D01EVA" || r.SlackThreadTs.String != "1.0" ||
			r.ZammadTicketID.Int64 != 42 || r.Target.String != "eva.peters" || r.RequesterAccount.String != "eva.peters" {
			t.Errorf("outcome row = %+v", r)
		}
		if !strings.Contains(string(r.Result), `"was_locked":false`) || !strings.Contains(string(r.Result), `"ticket_closed":true`) {
			t.Errorf("result = %s", r.Result)
		}
	}
	if !out {
		t.Fatal("no outcome row")
	}
	for _, secret := range []string{h.token, "eva.peters", goodPwd} {
		if strings.Contains(h.logs.String(), secret) {
			t.Errorf("a log line holds %q", secret)
		}
	}
}
