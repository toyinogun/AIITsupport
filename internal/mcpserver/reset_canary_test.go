package mcpserver_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/toyinogun/AIITsupport/internal/audit"
	"github.com/toyinogun/AIITsupport/internal/canary"
	"github.com/toyinogun/AIITsupport/internal/ldap"
	"github.com/toyinogun/AIITsupport/internal/logging"
	"github.com/toyinogun/AIITsupport/internal/mcpclient"
	"github.com/toyinogun/AIITsupport/internal/mcpserver"
	"github.com/toyinogun/AIITsupport/internal/resetlink"
	"github.com/toyinogun/AIITsupport/internal/resetpage"
	"github.com/toyinogun/AIITsupport/internal/server"
	"github.com/toyinogun/AIITsupport/internal/telemetry"
	"github.com/toyinogun/AIITsupport/internal/tools"
	"github.com/toyinogun/AIITsupport/internal/tools/toolstest"
	"github.com/toyinogun/AIITsupport/internal/zammad"
	"github.com/toyinogun/AIITsupport/internal/zammad/zammadtest"
)

// memLinks is an in memory reset_links store that, like the real one,
// keeps only sha256(token). It records every hash it stored.
type memLinks struct {
	mu     sync.Mutex
	rows   map[string]*resetlink.Link // hex hash -> link
	status map[string]string
	hashes [][]byte
}

func (m *memLinks) Issue(_ context.Context, in resetlink.Issue) (resetlink.Issued, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	token := resetlink.NewToken()
	hash := resetlink.Hash(token)
	key := hex.EncodeToString(hash)
	m.rows[key] = &resetlink.Link{
		ID: uuid.New(), Account: in.Account, RequesterSlackID: in.RequesterSlackID, ChannelID: in.ChannelID,
		ThreadTS: in.ThreadTS, TicketID: in.TicketID, IssueCallID: in.IssueCallID, ExpiresAt: time.Now().Add(in.TTL),
	}
	m.status[key] = "live"
	m.hashes = append(m.hashes, hash)
	return resetlink.Issued{Link: *m.rows[key], Token: token}, nil
}

func (m *memLinks) CountRecent(context.Context, string, time.Duration) (int64, error) { return 0, nil }

func (m *memLinks) Valid(_ context.Context, token string) (resetlink.Link, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := hex.EncodeToString(resetlink.Hash(token))
	if m.status[key] != "live" {
		return resetlink.Link{}, resetlink.ErrNotValid
	}
	return *m.rows[key], nil
}

func (m *memLinks) Claim(ctx context.Context, token string) (resetlink.Link, error) {
	l, err := m.Valid(ctx, token)
	if err != nil {
		return l, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.status[hex.EncodeToString(resetlink.Hash(token))] = "used"
	l.UsedAt = time.Now()
	return l, nil
}

func (m *memLinks) Revert(context.Context, resetlink.Link) (bool, error) { return false, nil }

// pageDir is the fake directory plus the page's reads and the reset write.
type pageDir struct {
	*toolstest.Directory
	mu     sync.Mutex
	writes int
}

func (d *pageDir) FindUserByAccount(_ context.Context, name string) (ldap.User, bool, error) {
	for _, u := range d.Users {
		if u.Username == name {
			return u, true, nil
		}
	}
	return ldap.User{}, false, nil
}

func (d *pageDir) PasswordPolicy(context.Context) (ldap.PasswordPolicy, error) {
	return ldap.PasswordPolicy{MinLength: 7, Complex: true}, nil
}

func (d *pageDir) PasswordLastSet(context.Context, string) (time.Time, error) {
	return time.Time{}, nil
}

func (d *pageDir) SetPassword(context.Context, string, string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.writes++
	return nil
}

// covers: AC-19 (spec 0011). Issue over MCP, then GET and POST on the reset
// page, logged through logging.New and exported through
// NewRedactingExporter: no log line or span holds the token, its hash, the
// password, or a seed name or username, and no audit row holds the token,
// its hash, or the password.
func TestCanaryResetLink(t *testing.T) {
	const (
		password = "Canary-Pass-Word-7Q"
		title    = "Forgotten password after the holiday"
		body     = "Requester forgot the password; sending a reset link."
	)
	hunt, err := canary.Seed(filepath.Join("..", "..", "seed", "directory", "noord.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	hunt.AnyCase(requester().SlackID)
	for _, s := range []string{title, body, password} {
		hunt.Text(s)
	}

	exported := tracetest.NewInMemoryExporter()
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSyncer(telemetry.NewRedactingExporter(exported))))
	t.Cleanup(func() { otel.SetTracerProvider(prev) })

	logs := &bytes.Buffer{}
	logger := logging.New(logs, "debug", "toolserver")
	store := &toolstest.MemStore{}
	rec, err := audit.NewRecorder(store, audit.Config{WriteTimeout: time.Second, MaxPayloadBytes: 4096}, logger)
	if err != nil {
		t.Fatal(err)
	}
	fake := zammadtest.New(t)
	fake.AddUser("eva.peters")
	zc, err := zammad.New(zammad.Options{BaseURL: fake.URL(), Auth: zammad.TokenAuth(zammadtest.Token), Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	dir := &pageDir{Directory: toolstest.NewDirectory()}
	links := &memLinks{rows: map[string]*resetlink.Link{}, status: map[string]string{}}
	helpdesk := tools.NewHelpdesk(zc, "IT Support", "")
	killSwitch := tools.NewKillSwitch("", false)
	denied := []string{"SG-IT-Admins", "APP-Helpdesk-Agents"}

	reg, err := tools.NewRegistry(tools.WithPasswordReset())
	if err != nil {
		t.Fatal(err)
	}
	d, err := tools.NewDispatcher(reg, tools.Config{
		BaseDN: toolstest.Base, UserOUs: []string{"OU=Users,OU=Noord"}, GroupOUs: []string{"OU=Groups,OU=Noord"},
		CallTimeout: 5 * time.Second, WriteLimit: 3, WriteWindow: time.Hour, TicketCreateLimit: 5, TicketMaxPending: 720 * time.Hour,
		PrivilegedWindow: 24 * time.Hour,
	}, tools.Systems{
		Directory: dir, Writer: dir, Helpdesk: helpdesk, Recorder: rec, KillSwitch: killSwitch,
		Reset: &tools.ResetSystem{Links: links, DeniedGroups: denied, Settings: tools.ResetSettings{
			PublicURL: "https://reset.noord.test", TTL: 15 * time.Minute, LinkLimit: 3, Window: 24 * time.Hour,
		}},
	}, logger)
	if err != nil {
		t.Fatal(err)
	}
	h, err := mcpserver.Handler(d, mcpserver.Options{Token: token, MaxRequestBytes: 4096, Logger: logger})
	if err != nil {
		t.Fatal(err)
	}
	mux := server.NewMux()
	mux.Handle(mcpserver.Path, h)
	mcpSrv := httptest.NewServer(mux)
	t.Cleanup(mcpSrv.Close)

	page, err := resetpage.New(resetpage.Config{
		Links: links, Directory: dir, Writer: dir, Tickets: helpdesk, Accounts: tools.NewAccountFence(dir, denied, logger),
		Recorder: rec, KillSwitch: killSwitch, BaseDN: toolstest.Base, UserOUs: []string{"OU=Users,OU=Noord"},
		CallTimeout: 5 * time.Second, MaxInflight: 2, SecureHSTS: true, Logger: logger,
	})
	if err != nil {
		t.Fatal(err)
	}
	pageSrv := httptest.NewServer(page.Handler(server.NewMux()))
	t.Cleanup(pageSrv.Close)

	// Issue over MCP.
	c, err := mcpclient.New(mcpSrv.URL+mcpserver.Path, token)
	if err != nil {
		t.Fatal(err)
	}
	r := requester()
	created, err := c.Call(t.Context(), r, tools.ToolTicketCreate, map[string]any{"title": title, "body": body})
	if err != nil || created.IsError {
		t.Fatalf("ticket_create = %+v, %v", created, err)
	}
	r.TicketID = int64(created.Data["ticket_id"].(float64))
	issued, err := c.Call(t.Context(), r, tools.ToolPasswordResetLink, map[string]any{})
	if err != nil || issued.IsError {
		t.Fatalf("password_reset_link = %+v, %v", issued, err)
	}
	link, _ := issued.Data[tools.ResetURLKey].(string)
	token := link[strings.LastIndex(link, "/")+1:]
	if !resetlink.WellFormed(token) {
		t.Fatalf("reset_url = %q", link)
	}
	hunt.Text(token)
	for _, hash := range links.hashes {
		hunt.Text(hex.EncodeToString(hash))
		hunt.Text(base64.StdEncoding.EncodeToString(hash))
	}

	// GET twice, a field error, then the real POST.
	path := pageSrv.URL + "/reset/" + token
	for range 2 {
		if status := do(t, http.MethodGet, path, nil); status != http.StatusOK {
			t.Fatalf("GET = %d", status)
		}
	}
	if status := do(t, http.MethodPost, path, url.Values{"password": {password}, "confirm": {"different"}}); status != http.StatusUnprocessableEntity {
		t.Fatalf("POST with a mismatch = %d", status)
	}
	if status := do(t, http.MethodPost, path, url.Values{"password": {password}, "confirm": {password}}); status != http.StatusOK || dir.writes != 1 {
		t.Fatalf("POST = %d with %d writes", status, dir.writes)
	}

	out := logs.String()
	for _, want := range []string{`"tool":"password_reset_link"`, `"msg":"reset_submit"`, `"outcome":"ok"`} {
		if !strings.Contains(out, want) {
			t.Errorf("the flow did not run: no %s in\n%s", want, out)
		}
	}
	spans := exported.GetSpans()
	names := map[string]bool{}
	for _, s := range spans {
		names[s.Name] = true
	}
	if !names["reset.submit"] || !names["tool.call"] {
		t.Errorf("spans = %v, want tool.call and reset.submit", names)
	}
	values := append(canary.LogValues(logs.Bytes()), canary.SpanValues(spans)...)
	if leaks := hunt.Leaks(values); len(leaks) > 0 {
		t.Errorf("logs or spans hold the token, its hash, the password, personal data, or [redacted]:\n%s\nlogs:\n%s", strings.Join(leaks, "\n"), out)
	}

	secrets := []string{token, password}
	for _, hash := range links.hashes {
		secrets = append(secrets, hex.EncodeToString(hash), base64.StdEncoding.EncodeToString(hash))
	}
	for _, row := range store.All() {
		raw := string(row.Arguments) + string(row.Result) + row.Error.String + row.Target.String
		for _, s := range secrets {
			if strings.Contains(raw, s) {
				t.Errorf("audit row %d (%s %s) holds a secret", row.ID, row.ToolName, row.Kind)
			}
		}
	}
}

// do sends one request to the reset page and returns its status.
func do(t *testing.T, method, target string, form url.Values) int {
	t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequestWithContext(t.Context(), method, target, body)
	if err != nil {
		t.Fatal(err)
	}
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}
