package helpdesk

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/go-ldap/ldap/v3"

	"github.com/toyinogun/AIITsupport/internal/config"
	noordldap "github.com/toyinogun/AIITsupport/internal/ldap"
	"github.com/toyinogun/AIITsupport/internal/seed/directory"
	"github.com/toyinogun/AIITsupport/internal/zammad"
)

// The live tests run only with TEST_ZAMMAD=1, on your laptop, against the
// running Compose stack right after `make helpdesk-reset`, with .env loaded:
//
//	set -a && . ./.env && set +a && TEST_ZAMMAD=1 go test -race -run Live ./internal/seed/helpdesk/
//
// One of them adds and then deletes a ticket; another runs a second LDAP
// import. Neither changes the seeded content.

type liveEnv struct {
	cfg config.SeedHelpdesk
	// seed has the domain Administrator, which only the lock test needs;
	// seed helpdesk itself never reads it.
	seed      config.Seed
	content   Content
	model     directory.Model
	runner    ComposeRunner
	admin     *zammad.Client
	assistant *zammad.Client
}

func live(t *testing.T) liveEnv {
	t.Helper()
	if os.Getenv("TEST_ZAMMAD") != "1" {
		t.Skip("set TEST_ZAMMAD=1 to run against the running Zammad (after make helpdesk-reset)")
	}
	// The package runs from internal/seed/helpdesk; point the files at the repo.
	t.Setenv("SEED_FILE", noordSeed)
	t.Setenv("SEED_OVERLAY_FILE", repoRoot+"/seed/directory/overlay.local.yaml")
	t.Setenv("SEED_HELPDESK_DIR", helpdeskDir)
	t.Setenv("SEED_CA_CERT", repoRoot+"/.dev/certs/ca.pem")
	cfg, err := config.LoadSeedHelpdesk(os.LookupEnv)
	if err != nil {
		t.Fatalf("load .env settings: %v", err)
	}
	seedCfg, err := config.LoadSeed(os.LookupEnv)
	if err != nil {
		t.Fatalf("load .env settings: %v", err)
	}
	m, err := directory.Load(cfg.File, cfg.OverlayFile)
	if err != nil {
		t.Fatal(err)
	}
	c, err := Load(cfg.HelpdeskDir, m)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := zammad.New(zammad.Options{BaseURL: cfg.ZammadURL, Timeout: cfg.ZammadTimeout, Auth: zammad.BasicAuth{Login: cfg.ZammadAdminEmail, Password: cfg.ZammadAdminPassword}})
	if err != nil {
		t.Fatal(err)
	}
	assistant, err := zammad.New(zammad.Options{BaseURL: cfg.ZammadURL, Timeout: cfg.ZammadTimeout, Auth: zammad.TokenAuth(cfg.ZammadToken)})
	if err != nil {
		t.Fatal(err)
	}
	return liveEnv{cfg: cfg, seed: seedCfg, content: c, model: m, runner: ComposeRunner{Dir: repoRoot, Limit: cfg.ZammadWaitTimeout}, admin: admin, assistant: assistant}
}

type report struct {
	Marker     string   `json:"marker"`
	OrgMembers int      `json:"org_members"`
	LDAPUsers  int      `json:"ldap_users"`
	RubenRoles []string `json:"ruben_roles"`
	// The AC-17 side doors: both settings, reset tokens, and every login
	// that did not come from LDAP (a signup has source "signup").
	UserCreateAccount bool     `json:"user_create_account"`
	UserLostPassword  bool     `json:"user_lost_password"`
	ResetTokens       int      `json:"reset_tokens"`
	LocalUsers        []string `json:"local_users"`
	Tickets           []struct {
		Tags     []string   `json:"tags"`
		Title    string     `json:"title"`
		State    string     `json:"state"`
		Owner    string     `json:"owner"`
		Customer string     `json:"customer"`
		Group    string     `json:"group"`
		Created  time.Time  `json:"created_at"`
		CloseAt  *time.Time `json:"close_at"`
		Articles []struct {
			From     string    `json:"from"`
			Sender   string    `json:"sender"`
			Type     string    `json:"type"`
			Internal bool      `json:"internal"`
			At       time.Time `json:"at"`
		} `json:"articles"`
	} `json:"tickets"`
	Answers []struct {
		Title     string    `json:"title"`
		Category  string    `json:"category"`
		Created   time.Time `json:"created_at"`
		Updated   time.Time `json:"updated_at"`
		Published bool      `json:"published"`
	} `json:"answers"`
	Categories []struct {
		Title string `json:"title"`
		Icon  string `json:"icon"`
	} `json:"categories"`
}

func (e liveEnv) report(t *testing.T) report {
	t.Helper()
	raw, err := runStep(t.Context(), e.runner, "report", map[string]string{}, nil)
	if err != nil {
		t.Fatalf("report: %v", err)
	}
	var r report
	if err := json.Unmarshal(raw, &r); err != nil {
		t.Fatalf("report: %v", err)
	}
	return r
}

// covers: AC-4, AC-7, AC-8, AC-9. What Zammad holds equals the seed files,
// so two resets in a row give the same content.
func TestLiveContentMatchesFiles(t *testing.T) {
	e := live(t)
	r := e.report(t)
	want, err := Marker(repoRoot, e.cfg.HelpdeskDir, e.cfg.File, e.cfg.CACert)
	if err != nil {
		t.Fatal(err)
	}
	if r.Marker != want {
		t.Fatalf("marker = %q, want %q (run make helpdesk-reset first)", r.Marker, want)
	}
	if r.OrgMembers != len(e.model.Users) || r.LDAPUsers != len(e.model.Users) {
		t.Errorf("org members %d, LDAP users %d, want %d", r.OrgMembers, r.LDAPUsers, len(e.model.Users))
	}
	if !slices.Equal(r.RubenRoles, []string{"Admin", "Agent"}) {
		t.Errorf("ruben roles = %v", r.RubenRoles)
	}

	if len(r.Tickets) != len(e.content.Tickets) {
		t.Fatalf("%d tickets in Zammad, %d in the file", len(r.Tickets), len(e.content.Tickets))
	}
	zstate := map[string]string{StateClosed: "closed", StateOpen: "open", StatePending: zammadPending}
	for i, ft := range e.content.Tickets {
		zt := r.Tickets[i]
		if !slices.Equal(zt.Tags, []string{ft.Key}) || zt.Title != ft.Title || zt.Customer != ft.Customer ||
			zt.State != zstate[ft.State] || zt.Group != e.cfg.ZammadDefaultGroup || !zt.Created.Equal(ft.Created) {
			t.Errorf("ticket %s: zammad %+v", ft.Key, zt)
		}
		wantOwner := "-"
		if ft.State != StateOpen {
			wantOwner = Agent
		}
		if zt.Owner != wantOwner {
			t.Errorf("ticket %s owner = %q, want %q", ft.Key, zt.Owner, wantOwner)
		}
		if (ft.Closed == nil) != (zt.CloseAt == nil) || (ft.Closed != nil && !ft.Closed.Equal(*zt.CloseAt)) {
			t.Errorf("ticket %s close_at = %v, want %v", ft.Key, zt.CloseAt, ft.Closed)
		}
		if len(zt.Articles) != len(ft.Articles) {
			t.Errorf("ticket %s has %d articles, want %d", ft.Key, len(zt.Articles), len(ft.Articles))
			continue
		}
		for j, fa := range ft.Articles {
			za := zt.Articles[j]
			wantType, wantSender := "web", "Customer"
			if fa.From == Agent {
				wantType, wantSender = "note", "Agent"
			}
			if za.From != fa.From || !za.At.Equal(fa.At) || za.Type != wantType || za.Sender != wantSender || za.Internal {
				t.Errorf("ticket %s article %d: zammad %+v, file from %s at %v", ft.Key, j+1, za, fa.From, fa.At)
			}
		}
	}

	if len(r.Answers) != len(e.content.Articles) || len(r.Categories) != len(e.content.Categories) {
		t.Fatalf("%d answers, %d categories, want %d and %d", len(r.Answers), len(r.Categories), len(e.content.Articles), len(e.content.Categories))
	}
	for i, fa := range e.content.Articles {
		za := r.Answers[i]
		day := func(t time.Time) string { return t.In(Amsterdam).Format(dateLayout) }
		if za.Title != fa.Title || za.Category != fa.Category || !za.Published ||
			day(za.Created) != day(fa.Created) || day(za.Updated) != day(fa.Updated) {
			t.Errorf("answer %s: zammad %+v", fa.Slug, za)
		}
	}
	for i, fc := range e.content.Categories {
		if r.Categories[i].Title != fc.Name || r.Categories[i].Icon != fc.Icon {
			t.Errorf("category %d = %+v, want %+v", i, r.Categories[i], fc)
		}
	}
}

// covers: AC-6. The assistant token reads, works tickets, and is refused
// settings and KB writes.
func TestLiveAssistantRights(t *testing.T) {
	e := live(t)
	ctx := t.Context()
	me, err := e.assistant.Me(ctx)
	if err != nil || me.Login != AssistantLogin {
		t.Fatalf("Me = %+v, %v", me, err)
	}
	if err := e.assistant.Do(ctx, zammad.Request{Method: http.MethodGet, Path: "/api/v1/settings"}); !zammad.IsForbidden(err) {
		t.Errorf("settings: %v, want 403", err)
	}
	if _, err := e.assistant.CreateCategory(ctx, 1, NewCategoryForTest()); !zammad.IsForbidden(err) {
		t.Errorf("KB category write: %v, want 403", err)
	}

	var kb map[string]map[string]json.RawMessage
	if err := e.assistant.Do(ctx, zammad.Request{Method: http.MethodPost, Path: "/api/v1/knowledge_bases/init", Out: &kb}); err != nil {
		t.Fatalf("KB init: %v", err)
	}
	answers := kb["KnowledgeBaseAnswer"]
	if len(answers) != len(e.content.Articles) {
		t.Errorf("assistant sees %d answers, want %d", len(answers), len(e.content.Articles))
	}
	for id := range answers {
		var n int
		_, _ = fmt.Sscan(id, &n)
		if _, err := e.assistant.GetAnswer(ctx, 1, n); err != nil {
			t.Errorf("read answer %s: %v", id, err)
		}
		if err := e.assistant.Do(ctx, zammad.Request{Method: http.MethodPut, Path: "/api/v1/knowledge_bases/1/answers/" + id, Body: map[string]any{"promoted": true}}); !zammad.IsForbidden(err) {
			t.Errorf("edit answer %s: %v, want 403", id, err)
		}
		break
	}

	// The user search index lags a fresh import, by more than a minute right
	// after make helpdesk-reset, so wait up to 3 minutes.
	var users []zammad.User
	for range 90 {
		users, err = e.admin.SearchUsers(ctx, "eva.peters", 5)
		if err != nil || len(users) > 0 {
			break
		}
		time.Sleep(2 * time.Second)
	}
	if err != nil || len(users) == 0 {
		t.Fatalf("search eva.peters: %v, %v", users, err)
	}
	tk, err := e.assistant.CreateTicket(ctx, zammad.NewTicket{
		Title: "live test ticket", Group: e.cfg.ZammadDefaultGroup, CustomerID: users[0].ID, Tags: []string{"live-test"},
		Article: zammad.NewArticle{Body: "Created by TestLiveAssistantRights; deleted right after.", ContentType: "text/plain", Type: zammad.ArticleNote, Sender: zammad.SenderAgent, Internal: true},
	}, "")
	if err != nil {
		t.Fatalf("assistant create ticket: %v", err)
	}
	t.Cleanup(func() {
		if err := e.admin.Do(context.Background(), zammad.Request{Method: http.MethodDelete, Path: fmt.Sprintf("/api/v1/tickets/%d", tk.ID)}); err != nil {
			t.Errorf("delete the live test ticket %d: %v", tk.ID, err)
		}
	})
	if _, err := e.assistant.UpdateTicket(ctx, tk.ID, zammad.TicketUpdate{State: "open"}, ""); err != nil {
		t.Errorf("assistant update ticket: %v", err)
	}
}

// NewCategoryForTest is a throwaway category the assistant must not create.
func NewCategoryForTest() zammad.NewCategory {
	return zammad.NewCategory{Title: "live test", Icon: "f000", KBLocaleID: 1}
}

// covers: AC-5. Zammad checks the password against AD: the right password
// works, and a locked AD account is refused even with the right password.
func TestLiveSignInFollowsAD(t *testing.T) {
	e := live(t)
	ctx := t.Context()
	signIn := func(login string) error {
		c, err := zammad.New(zammad.Options{BaseURL: e.cfg.ZammadURL, Timeout: e.cfg.ZammadTimeout, Auth: zammad.BasicAuth{Login: login, Password: e.seed.UserPassword}})
		if err != nil {
			return err
		}
		_, err = c.Me(ctx)
		return err
	}
	if err := signIn("eva.peters"); err != nil {
		t.Errorf("eva.peters with the right password: %v", err)
	}

	pool, err := noordldap.LoadCA(e.cfg.CACert)
	if err != nil {
		t.Fatal(err)
	}
	d := noordldap.Dialer{URL: e.cfg.LDAPURL, RootCAs: pool}
	admin, err := d.Bind(e.seed.AdminUser, e.seed.AdminPassword)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	base, err := noordldap.BaseDN(admin)
	if err != nil {
		t.Fatal(err)
	}
	dn, _ := e.model.UserDN(base, "yara.schouten")
	threshold, err := directory.LockoutThreshold(admin, base)
	if err != nil {
		t.Fatal(err)
	}
	state, err := noordldap.State(admin, dn)
	if err != nil {
		t.Fatal(err)
	}
	if !state.Locked {
		if err := directory.LockAndConfirm(ctx, admin, d, dn, directory.WrongPassword(e.seed.UserPassword), threshold); err != nil {
			t.Fatalf("lock yara.schouten: %v", err)
		}
	}
	if err := signIn("yara.schouten"); !zammad.IsUnauthorized(err) {
		t.Errorf("locked yara.schouten with the right password: %v, want 401", err)
	}
}

// covers: AC-17. Zammad offers no way around AD sign in: self signup and
// password reset are off in the settings and refused on the server. The
// requests go through a plain net/http client, because internal/zammad
// refuses any non GET under /api/v1/users.
func TestLiveSideDoorsClosed(t *testing.T) {
	e := live(t)
	before := e.report(t)
	if before.UserCreateAccount || before.UserLostPassword {
		t.Errorf("settings user_create_account=%v user_lost_password=%v, want both false", before.UserCreateAccount, before.UserLostPassword)
	}

	signupEmail := "signup.probe@noord.example"
	status, body := e.anonymousPost(t, "/api/v1/users", map[string]any{
		"signup": true, "firstname": "Signup", "lastname": "Probe", "email": signupEmail,
		"password": fmt.Sprintf("Np7!%dQz", time.Now().UnixNano()),
	})
	t.Logf("signup: %d %s", status, body)
	if accepted(status, body) {
		t.Errorf("signup accepted: %d %s", status, body)
	}
	status, body = e.anonymousPost(t, "/api/v1/users/password_reset", map[string]any{"username": "eva.peters"})
	t.Logf("password reset: %d %s", status, body)
	if accepted(status, body) {
		t.Errorf("password reset accepted: %d %s", status, body)
	}

	after := e.report(t)
	if slices.Contains(after.LocalUsers, signupEmail) {
		t.Errorf("signup made the local user %s", signupEmail)
	}
	if after.ResetTokens != before.ResetTokens {
		t.Errorf("reset tokens %d, then %d: a reset token was made", before.ResetTokens, after.ResetTokens)
	}
}

// accepted is true when Zammad accepted the request: a 2xx status without
// a failure body.
func accepted(status int, body string) bool {
	return status/100 == 2 && !strings.Contains(body, `"failed"`)
}

// anonymousPost sends a JSON POST with no credentials, the way the login page
// does: it first takes a CSRF token and session cookie from /api/v1/signshow,
// so a refusal comes from Zammad's settings, not from the CSRF check.
func (e liveEnv) anonymousPost(t *testing.T, path string, payload map[string]any) (int, string) {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar, Timeout: e.cfg.ZammadTimeout}
	send := func(method, path string, body []byte, csrf string) (int, http.Header, string) {
		req, err := http.NewRequestWithContext(t.Context(), method, e.cfg.ZammadURL+path, bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")
		if csrf != "" {
			req.Header.Set("X-CSRF-Token", csrf)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		defer resp.Body.Close()
		raw, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatalf("%s %s: read body: %v", method, path, err)
		}
		return resp.StatusCode, resp.Header, string(raw)
	}
	_, header, _ := send(http.MethodGet, "/api/v1/signshow", nil, "")
	csrf := header.Get("CSRF-Token")
	if csrf == "" {
		t.Fatal("signshow gave no CSRF token")
	}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	status, _, raw := send(http.MethodPost, path, body, csrf)
	return status, raw
}

// covers: AC-2. svc-zammad binds over LDAPS and reads Noord's people and
// groups, Ruben is the one member of APP-Helpdesk-Agents, and every write
// as svc-zammad (a user change, or adding someone to the agents group) is
// refused.
func TestLiveZammadBindReadsOnly(t *testing.T) {
	e := live(t)
	pool, err := noordldap.LoadCA(e.cfg.CACert)
	if err != nil {
		t.Fatal(err)
	}
	svc, err := noordldap.Dialer{URL: e.cfg.LDAPURL, RootCAs: pool}.Bind(ZammadBindDN, e.cfg.ZammadBindPassword)
	if err != nil {
		t.Fatalf("bind as svc-zammad: %v", err)
	}
	defer svc.Close()
	base, err := noordldap.BaseDN(svc)
	if err != nil {
		t.Fatal(err)
	}

	eva, _ := e.model.UserDN(base, "eva.peters")
	res, err := svc.Search(ldap.NewSearchRequest(noordldap.NoordDN(base), ldap.ScopeWholeSubtree, ldap.NeverDerefAliases, 0, 0, false,
		"(&(objectCategory=person)(objectClass=user)(department=*)(sAMAccountName=eva.peters))", []string{"department"}, nil))
	if err != nil || len(res.Entries) != 1 || !strings.EqualFold(res.Entries[0].DN, eva) {
		t.Fatalf("svc-zammad search for eva.peters: %v, %v", res, err)
	}

	agents, ok := e.model.GroupDN(base, "APP-Helpdesk-Agents")
	if !ok {
		t.Fatal("APP-Helpdesk-Agents is not in noord.yaml")
	}
	ruben, _ := e.model.UserDN(base, "ruben.mulder")
	group, err := svc.Search(ldap.NewSearchRequest(agents, ldap.ScopeBaseObject, ldap.NeverDerefAliases, 0, 0, false,
		"(objectClass=group)", []string{"member"}, nil))
	if err != nil || len(group.Entries) != 1 {
		t.Fatalf("read APP-Helpdesk-Agents: %v", err)
	}
	if members := group.Entries[0].GetAttributeValues("member"); len(members) != 1 || !strings.EqualFold(members[0], ruben) {
		t.Errorf("APP-Helpdesk-Agents members = %v, want only %s", members, ruben)
	}

	refused := map[string]*ldap.ModifyRequest{
		"change eva.peters's title":             ldap.NewModifyRequest(eva, nil),
		"add eva.peters to the helpdesk agents": ldap.NewModifyRequest(agents, nil),
	}
	refused["change eva.peters's title"].Replace("title", []string{"Helpdesk Admin"})
	refused["add eva.peters to the helpdesk agents"].Add("member", []string{eva})
	for name, req := range refused {
		if err := svc.Modify(req); !ldap.IsErrorWithCode(err, ldap.LDAPResultInsufficientAccessRights) {
			t.Errorf("%s as svc-zammad: %v, want insufficient access", name, err)
		}
	}
}

// covers: AC-3. A reset leaves a finished Zammad with our settings, none of
// its stock content, no way to send mail, one group, and one organization.
// Read as the seed admin, which proves that account too.
func TestLiveResetLeavesACleanZammad(t *testing.T) {
	e := live(t)
	ctx := t.Context()
	me, err := e.admin.Me(ctx)
	if err != nil || !strings.EqualFold(me.Email, e.cfg.ZammadAdminEmail) {
		t.Fatalf("seed admin Me = %+v, %v", me, err)
	}

	var settings []struct {
		Name  string `json:"name"`
		State struct {
			Value any `json:"value"`
		} `json:"state_current"`
	}
	if err := e.admin.Do(ctx, zammad.Request{Method: http.MethodGet, Path: "/api/v1/settings", Out: &settings}); err != nil {
		t.Fatalf("settings: %v", err)
	}
	got := map[string]any{}
	for _, s := range settings {
		got[s.Name] = s.State.Value
	}
	want := map[string]any{
		"system_init_done": true, "fqdn": "127.0.0.1:58080", "http_type": "http",
		"locale_default": "en-us", "timezone_default": "Europe/Amsterdam",
	}
	for name, v := range want {
		if got[name] != v {
			t.Errorf("setting %s = %v, want %v", name, got[name], v)
		}
	}

	var email struct {
		AccountChannelIDs []int `json:"account_channel_ids"`
		Assets            struct {
			Channel map[string]struct {
				Area   string `json:"area"`
				Active bool   `json:"active"`
			} `json:"Channel"`
		} `json:"assets"`
	}
	if err := e.admin.Do(ctx, zammad.Request{Method: http.MethodGet, Path: "/api/v1/channels_email", Out: &email}); err != nil {
		t.Fatalf("email channels: %v", err)
	}
	notifications := 0
	for id, ch := range email.Assets.Channel {
		switch {
		case ch.Area == "Email::Notification":
			notifications++
			if ch.Active {
				t.Errorf("notification channel %s is active, so mail can leave", id)
			}
		case strings.HasPrefix(ch.Area, "Email::"):
			t.Errorf("channel %s has area %s, want no email account", id, ch.Area)
		}
	}
	if notifications != 2 || len(email.AccountChannelIDs) != 0 {
		t.Errorf("%d notification channels and account channels %v, want 2 and none", notifications, email.AccountChannelIDs)
	}

	var groups, orgs []struct {
		Name string `json:"name"`
	}
	if err := e.admin.Do(ctx, zammad.Request{Method: http.MethodGet, Path: "/api/v1/groups", Out: &groups}); err != nil {
		t.Fatal(err)
	}
	if len(groups) != 1 || groups[0].Name != e.cfg.ZammadDefaultGroup {
		t.Errorf("groups = %v, want only %s (Zammad's Users group removed)", groups, e.cfg.ZammadDefaultGroup)
	}
	if err := e.admin.Do(ctx, zammad.Request{Method: http.MethodGet, Path: "/api/v1/organizations", Out: &orgs}); err != nil {
		t.Fatal(err)
	}
	if len(orgs) != 1 || orgs[0].Name != "Noord Logistics" {
		t.Errorf("organizations = %v, want only Noord Logistics (the demo organization removed)", orgs)
	}

	seen := 0
	for page := 1; ; page++ {
		var users []zammad.User
		if err := e.admin.Do(ctx, zammad.Request{Method: http.MethodGet, Path: fmt.Sprintf("/api/v1/users?per_page=100&page=%d", page), Out: &users}); err != nil {
			t.Fatal(err)
		}
		if len(users) == 0 {
			break
		}
		seen += len(users)
		for _, u := range users {
			if strings.EqualFold(u.Email, "nicole.braun@zammad.org") {
				t.Errorf("Zammad's demo customer %s is still there", u.Email)
			}
		}
	}
	if seen < len(e.model.Users) {
		t.Errorf("listed %d Zammad users, want at least the %d in noord.yaml", seen, len(e.model.Users))
	}
}

// covers: AC-16. A later LDAP sync leaves the organization, Ruben's roles,
// and the counts alone.
func TestLiveSecondLDAPImport(t *testing.T) {
	e := live(t)
	before := e.report(t)
	raw, err := runStep(t.Context(), e.runner, "ldapsync", map[string]string{}, nil)
	if err != nil {
		t.Fatalf("ldapsync: %v", err)
	}
	if !strings.Contains(string(raw), `"failed":0`) {
		t.Errorf("import result %s", raw)
	}
	after := e.report(t)
	if after.Marker != before.Marker || after.OrgMembers != before.OrgMembers || after.LDAPUsers != before.LDAPUsers ||
		!slices.Equal(after.RubenRoles, before.RubenRoles) || len(after.Tickets) != len(before.Tickets) {
		t.Errorf("before %+v\nafter %+v", summary(before), summary(after))
	}
}

func summary(r report) string {
	return fmt.Sprintf("marker=%s org=%d ldap=%d ruben=%v tickets=%d", r.Marker, r.OrgMembers, r.LDAPUsers, r.RubenRoles, len(r.Tickets))
}
