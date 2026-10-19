package mcpserver_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	goldap "github.com/go-ldap/ldap/v3"
	"github.com/jackc/pgx/v5/pgtype"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/toyinogun/AIITsupport/internal/audit"
	"github.com/toyinogun/AIITsupport/internal/db/dbtest"
	"github.com/toyinogun/AIITsupport/internal/db/queries"
	"github.com/toyinogun/AIITsupport/internal/ldap"
	"github.com/toyinogun/AIITsupport/internal/mcpclient"
	"github.com/toyinogun/AIITsupport/internal/mcpserver"
	"github.com/toyinogun/AIITsupport/internal/seed/directory"
	"github.com/toyinogun/AIITsupport/internal/seed/directory/sambatest"
	"github.com/toyinogun/AIITsupport/internal/server"
	"github.com/toyinogun/AIITsupport/internal/tools"
	"github.com/toyinogun/AIITsupport/internal/zammad"
	"github.com/toyinogun/AIITsupport/internal/zammad/zammadtest"
)

// These tests run the whole pipe: mcpclient, the tool server on httptest,
// live Samba bound as svc-itsupport, real Postgres as noord_toolserver, and
// the fake Zammad. They need TEST_SAMBA=1 and TEST_DATABASE_URL.

const (
	buildContext = "../../seed/directory/samba"
	noordSeed    = "../../seed/directory/noord.yaml"
	base         = sambatest.BaseDN
)

var shared *sambatest.Samba

func TestMain(m *testing.M) {
	os.Exit(runTests(m))
}

func runTests(m *testing.M) int {
	if !sambatest.Enabled() {
		return m.Run()
	}
	ctx := context.Background()
	s, err := sambatest.Start(ctx, buildContext)
	if err != nil {
		fmt.Fprintf(os.Stderr, "TEST_SAMBA is set but Samba did not start (is Docker running?): %v\n", err)
		return 1
	}
	defer func() {
		if err := s.Terminate(); err != nil {
			fmt.Fprintf(os.Stderr, "stop Samba: %v\n", err)
		}
	}()
	if err := seed(ctx, s); err != nil {
		fmt.Fprintf(os.Stderr, "seed the shared Samba: %v\n", err)
		return 1
	}
	shared = s
	return m.Run()
}

func seed(ctx context.Context, s *sambatest.Samba) error {
	m, err := directory.Load(noordSeed, "")
	if err != nil {
		return err
	}
	d := ldap.Dialer{URL: s.URL, RootCAs: s.RootCAs}
	admin, err := d.WaitAndBind(ctx, sambatest.AdminDN, sambatest.AdminPassword, time.Minute)
	if err != nil {
		return err
	}
	defer admin.Close()
	_, err = directory.Apply(ctx, admin, m, directory.Options{UserPassword: sambatest.UserPassword, Dialer: d})
	return err
}

type live struct {
	client  *mcpclient.Client
	queries *queries.Queries
	fence   *tools.Fence
	zammad  *zammadtest.Fake
	logs    *bytes.Buffer
	ks      string // the kill switch file
}

// startLive serves the production tool layer against live systems. ldapURL
// lets a test point it at a dead port.
func startLive(t *testing.T, ldapURL string) *live {
	t.Helper()
	requireShared(t)
	tdb := dbtest.New(t)
	l := &live{queries: queries.New(tdb.ToolServer), zammad: zammadtest.New(t), logs: &bytes.Buffer{}}
	l.zammad.AddUser("eva.peters")
	l.ks = filepath.Join(t.TempDir(), "kill-switch")
	writeFile(t, l.ks, "read_write")

	logger := slog.New(slog.NewJSONHandler(l.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	rec, err := audit.NewRecorder(audit.NewPgStore(tdb.ToolServer), audit.Config{OutcomeRetries: 1, WriteTimeout: 5 * time.Second, MaxPayloadBytes: 4096}, logger)
	if err != nil {
		t.Fatal(err)
	}
	zc, err := zammad.New(zammad.Options{BaseURL: l.zammad.URL(), Auth: zammad.TokenAuth(zammadtest.Token), Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	dir := ldap.Config{
		Dialer:   ldap.Dialer{URL: ldapURL, RootCAs: shared.RootCAs, Timeout: 5 * time.Second},
		BindDN:   sambatest.SvcDN,
		Password: sambatest.SvcPassword,
		BaseDN:   base,
	}
	reg, err := tools.NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	d, err := tools.NewDispatcher(reg, tools.Config{
		BaseDN: base, UserOUs: []string{"OU=Users,OU=Noord"}, GroupOUs: []string{"OU=Groups,OU=Noord"},
		DeniedGroups: []string{"SG-IT-Admins", "APP-Helpdesk-Agents", "SYNC-Entra-Users"},
		CallTimeout:  15 * time.Second, WriteLimit: 3, WriteWindow: time.Hour,
		TicketCreateLimit: 5, TicketMaxPending: 720 * time.Hour, PrivilegedWindow: 24 * time.Hour,
	}, tools.Systems{
		Directory: ldap.NewClient(dir), Writer: ldap.NewWriter(dir),
		Helpdesk: tools.NewHelpdesk(zc, "IT Support", ""), Recorder: rec,
		KillSwitch: tools.NewKillSwitch(l.ks, true),
	}, logger)
	if err != nil {
		t.Fatal(err)
	}
	l.fence = d.Fence()
	h, err := mcpserver.Handler(d, mcpserver.Options{Token: token, MaxRequestBytes: 65536, Logger: logger})
	if err != nil {
		t.Fatal(err)
	}
	mux := server.NewMux()
	mux.Handle(mcpserver.Path, h)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	if l.client, err = mcpclient.New(srv.URL+mcpserver.Path, token); err != nil {
		t.Fatal(err)
	}
	return l
}

func requireShared(t *testing.T) {
	t.Helper()
	if shared == nil {
		t.Skip("set TEST_SAMBA=1 to run Samba integration tests (needs Docker)")
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func adminConn(t *testing.T) *goldap.Conn {
	t.Helper()
	conn, err := ldap.Dialer{URL: shared.URL, RootCAs: shared.RootCAs}.Bind(sambatest.AdminDN, sambatest.AdminPassword)
	if err != nil {
		t.Fatalf("bind as Administrator: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// lockUser locks a seeded user by failed binds and returns its DN.
func lockUser(t *testing.T, admin *goldap.Conn, username string) string {
	t.Helper()
	m, err := directory.Load(noordSeed, "")
	if err != nil {
		t.Fatal(err)
	}
	dn, ok := m.UserDN(base, username)
	if !ok {
		t.Fatalf("%s is not seeded", username)
	}
	threshold, err := directory.LockoutThreshold(admin, base)
	if err != nil {
		t.Fatal(err)
	}
	d := ldap.Dialer{URL: shared.URL, RootCAs: shared.RootCAs}
	if err := directory.LockAndConfirm(t.Context(), admin, d, dn, directory.WrongPassword(sambatest.UserPassword), threshold); err != nil {
		t.Fatalf("lock %s: %v", username, err)
	}
	return dn
}

func setMail(t *testing.T, admin *goldap.Conn, dn, mail string) {
	t.Helper()
	req := goldap.NewModifyRequest(dn, nil)
	req.Replace("mail", []string{mail})
	if err := admin.Modify(req); err != nil {
		t.Fatalf("set mail on %s: %v", dn, err)
	}
}

func liveRequester(username, thread string) mcpclient.Requester {
	return mcpclient.Requester{SlackID: "U01LIVE", Email: username + "@noord.example", ChannelID: "C01HELP", ThreadTS: thread}
}

// covers: AC-3, AC-6, AC-7, AC-10, AC-11, AC-17, AC-20. The locked out user,
// end to end: four calls, four audit pairs, with nothing personal in a log
// line or a span attribute.
func TestLiveLockedOutUserEndToEnd(t *testing.T) {
	requireShared(t)
	spans := tracetest.NewSpanRecorder()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans)))
	l := startLive(t, shared.URL)
	admin := adminConn(t)
	dn := lockUser(t, admin, "eva.peters")
	r := liveRequester("eva.peters", "1760000100.000100")

	res, err := l.client.Call(t.Context(), r, tools.ToolUserLookup, nil)
	if err != nil || res.IsError || res.Data["locked"] != true || res.Data["username"] != "eva.peters" {
		t.Fatalf("user_lookup = %+v, %v", res, err)
	}
	if ends, ok := res.Data["lock_ends_at"].(string); !ok {
		t.Errorf("lock_ends_at = %v, want a time", res.Data["lock_ends_at"])
	} else if at, err := time.Parse(time.RFC3339, ends); err != nil || at.Before(time.Now()) {
		t.Errorf("lock_ends_at = %q, want a future RFC 3339 time", ends)
	}

	res, err = l.client.Call(t.Context(), r, tools.ToolAccountUnlock, nil)
	if err != nil || res.IsError || res.Data["was_locked"] != true || res.Data["locked"] != false {
		t.Fatalf("account_unlock = %+v, %v", res, err)
	}
	if state, err := ldap.State(admin, dn); err != nil || state.Locked {
		t.Fatalf("the Administrator sees %+v, %v; want unlocked", state, err)
	}

	res, err = l.client.Call(t.Context(), r, tools.ToolTicketCreate, map[string]any{"title": "Locked out", "body": "Locked out; unlocked by the assistant."})
	if err != nil || res.IsError {
		t.Fatalf("ticket_create = %+v, %v", res, err)
	}
	r.TicketID = int64(res.Data["ticket_id"].(float64))
	before := len(l.zammad.Requests())
	res, err = l.client.Call(t.Context(), r, tools.ToolTicketUpdate, map[string]any{"note": "Unlocked and confirmed.", "state": "closed"})
	if err != nil || res.IsError || res.Data["state"] != "closed" {
		t.Fatalf("ticket_update = %+v, %v", res, err)
	}
	puts := 0
	for _, s := range l.zammad.Requests()[before:] {
		if s.Method == "PUT" {
			puts++
		}
	}
	if puts != 1 {
		t.Errorf("ticket_update sent %d updates, want one", puts)
	}

	rows, err := l.queries.ListAuditByThread(t.Context(), queries.ListAuditByThreadParams{
		SlackChannelID: text(r.ChannelID), SlackThreadTs: text(r.ThreadTS),
	})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, row := range rows {
		got = append(got, row.Kind+":"+row.ToolName+":"+row.RiskTier+":"+row.Outcome.String+":"+row.RequesterAccount.String)
	}
	want := []string{
		"intent:user_lookup:read::eva.peters", "outcome:user_lookup:read:ok:eva.peters",
		"intent:account_unlock:low::eva.peters", "outcome:account_unlock:low:ok:eva.peters",
		"intent:ticket_create:low::eva.peters", "outcome:ticket_create:low:ok:eva.peters",
		"intent:ticket_update:low::eva.peters", "outcome:ticket_update:low:ok:eva.peters",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("audit rows:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}

	// AC-20: one span per call with tool, tier, and outcome; nothing personal.
	var calls int
	for _, s := range spans.Ended() {
		if s.Name() != "tool.call" {
			continue
		}
		calls++
		for _, a := range s.Attributes() {
			if k := string(a.Key); k != "tool" && k != "tier" && k != "outcome" {
				t.Errorf("span attribute %s", k)
			}
		}
	}
	if calls != 4 {
		t.Errorf("tool.call spans = %d, want 4", calls)
	}
	for _, leak := range []string{"eva", "U01LIVE", "noord.example", "Locked out", "Unlocked and confirmed", "C01HELP"} {
		if strings.Contains(l.logs.String(), leak) {
			t.Errorf("logs hold %q:\n%s", leak, l.logs)
		}
	}
	if !strings.Contains(l.logs.String(), `"trace_id":"`) || strings.Contains(l.logs.String(), `"trace_id":""`) {
		t.Errorf("tool_call lines lack a trace id:\n%s", l.logs)
	}
}

// covers: AC-8. The kill switch file pauses unlocks without a restart, and AD is untouched.
func TestLiveKillSwitch(t *testing.T) {
	requireShared(t)
	l := startLive(t, shared.URL)
	admin := adminConn(t)
	dn := lockUser(t, admin, "sophie.koster")
	l.zammad.AddUser("sophie.koster")
	r := liveRequester("sophie.koster", "1760000200.000100")

	for _, content := range []string{"read_only", ""} {
		writeFile(t, l.ks, content)
		res, err := l.client.Call(t.Context(), r, tools.ToolAccountUnlock, nil)
		if err != nil || res.Code != "read_only" {
			t.Fatalf("with %q: %+v, %v", content, res, err)
		}
		if state, _ := ldap.State(admin, dn); !state.Locked {
			t.Fatal("the account was unlocked while paused")
		}
	}
	if res, err := l.client.Call(t.Context(), r, tools.ToolTicketCreate, map[string]any{"title": "t", "body": "b"}); err != nil || res.IsError {
		t.Fatalf("ticket_create while paused = %+v, %v", res, err)
	}
	writeFile(t, l.ks, "read_write\n")
	if res, err := l.client.Call(t.Context(), r, tools.ToolAccountUnlock, nil); err != nil || res.Data["was_locked"] != true {
		t.Fatalf("unlock after flipping = %+v, %v", res, err)
	}
}

// covers: AC-4. No match, two users on one address, and an admin account
// given a mail are all refused as identity, with no write.
func TestLiveIdentity(t *testing.T) {
	requireShared(t)
	l := startLive(t, shared.URL)
	admin := adminConn(t)
	m, err := directory.Load(noordSeed, "")
	if err != nil {
		t.Fatal(err)
	}
	admDN, _ := m.UserDN(base, "adm.ruben.mulder")
	milanDN, _ := m.UserDN(base, "milan.vanderberg")
	setMail(t, admin, admDN, "adm.ruben.mulder@noord.example")
	setMail(t, admin, milanDN, "lars.brouwer@noord.example")
	t.Cleanup(func() {
		_ = admin.Modify(func() *goldap.ModifyRequest {
			req := goldap.NewModifyRequest(admDN, nil)
			req.Delete("mail", nil)
			return req
		}())
		setMail(t, admin, milanDN, "milan.vanderberg@noord.example")
	})

	for username, code := range map[string]string{
		"nobody":           "not_matched",
		"adm.ruben.mulder": "not_matched",
		"lars.brouwer":     "ambiguous",
	} {
		res, err := l.client.Call(t.Context(), liveRequester(username, "1760000300.000100"), tools.ToolAccountUnlock, nil)
		if err != nil || res.Code != code {
			t.Errorf("%s: %+v, %v; want %s", username, res, err, code)
		}
	}
}

// covers: AC-13. The fence against the seeded directory.
func TestLiveFence(t *testing.T) {
	requireShared(t)
	l := startLive(t, shared.URL)
	admin := adminConn(t)

	name := "T-Nested-Fence"
	dn := "CN=" + name + "," + ldap.GroupsDN(base)
	add := goldap.NewAddRequest(dn, nil)
	add.Attribute("objectClass", []string{"group"})
	add.Attribute("sAMAccountName", []string{name})
	if err := admin.Add(add); err != nil {
		t.Fatalf("add test group: %v", err)
	}
	t.Cleanup(func() { _ = admin.Del(goldap.NewDelRequest(dn, nil)) })
	itAdmins := "CN=SG-IT-Admins," + ldap.AdminsDN(base)
	mod := goldap.NewModifyRequest(itAdmins, nil)
	mod.Add("member", []string{dn})
	if err := admin.Modify(mod); err != nil {
		t.Fatalf("nest the test group: %v", err)
	}
	t.Cleanup(func() {
		undo := goldap.NewModifyRequest(itAdmins, nil)
		undo.Delete("member", []string{dn})
		_ = admin.Modify(undo)
	})

	for _, group := range []string{"SG-IT-Admins", "Domain Admins", "APP-Helpdesk-Agents", "SYNC-Entra-Users", "No-Such-Group", name} {
		if err := l.fence.CheckGroup(t.Context(), group); !errors.Is(err, tools.ErrGroupProtected) {
			t.Errorf("CheckGroup(%s) = %v, want ErrGroupProtected", group, err)
		}
	}
	if err := l.fence.CheckGroup(t.Context(), "SG-Finance"); err != nil {
		t.Errorf("CheckGroup(SG-Finance) = %v, want allowed", err)
	}
}

// covers: AC-12, AC-19. With the directory unreachable, the blocked tool is
// still "blocked" and every other call is "directory unavailable".
func TestLiveDirectoryDown(t *testing.T) {
	requireShared(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	dead := "ldaps://" + ln.Addr().String()
	_ = ln.Close()
	l := startLive(t, dead)
	r := liveRequester("eva.peters", "1760000400.000100")

	res, err := l.client.Call(t.Context(), r, tools.ToolPrivilegedAccessChange, map[string]any{"summary": "make me Domain Admin"})
	if err != nil || res.Code != "blocked" {
		t.Fatalf("privileged_access_change = %+v, %v", res, err)
	}
	res, err = l.client.Call(t.Context(), r, tools.ToolUserLookup, nil)
	if err != nil || res.Code != "unavailable" || res.Text != "unavailable: The directory cannot be reached right now." {
		t.Fatalf("user_lookup = %+v, %v", res, err)
	}
}

func text(s string) pgtype.Text { return pgtype.Text{String: s, Valid: s != ""} }
