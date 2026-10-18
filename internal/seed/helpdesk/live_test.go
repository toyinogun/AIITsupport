package helpdesk

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/toyinogun/AIITsupport/internal/config"
	"github.com/toyinogun/AIITsupport/internal/seed/certs"
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
	cfg       config.SeedHelpdesk
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
	return liveEnv{cfg: cfg, content: c, model: m, runner: ComposeRunner{Dir: repoRoot, Limit: cfg.ZammadWaitTimeout}, admin: admin, assistant: assistant}
}

type report struct {
	Marker     string   `json:"marker"`
	OrgMembers int      `json:"org_members"`
	LDAPUsers  int      `json:"ldap_users"`
	RubenRoles []string `json:"ruben_roles"`
	Tickets    []struct {
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

	// The user search index lags a fresh import by a few seconds.
	var users []zammad.User
	for range 30 {
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
		c, err := zammad.New(zammad.Options{BaseURL: e.cfg.ZammadURL, Timeout: e.cfg.ZammadTimeout, Auth: zammad.BasicAuth{Login: login, Password: e.cfg.UserPassword}})
		if err != nil {
			return err
		}
		_, err = c.Me(ctx)
		return err
	}
	if err := signIn("eva.peters"); err != nil {
		t.Errorf("eva.peters with the right password: %v", err)
	}

	pool, err := certs.Pool(e.cfg.CACert)
	if err != nil {
		t.Fatal(err)
	}
	d := directory.Dialer{URL: e.cfg.LDAPURL, RootCAs: pool}
	admin, err := d.Bind(e.cfg.AdminUser, e.cfg.AdminPassword)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	base, err := directory.BaseDN(admin)
	if err != nil {
		t.Fatal(err)
	}
	dn, _ := e.model.UserDN(base, "yara.schouten")
	threshold, err := directory.LockoutThreshold(admin, base)
	if err != nil {
		t.Fatal(err)
	}
	state, err := directory.State(admin, dn)
	if err != nil {
		t.Fatal(err)
	}
	if !state.Locked {
		if err := directory.LockAndConfirm(ctx, admin, d, dn, directory.WrongPassword(e.cfg.UserPassword), threshold); err != nil {
			t.Fatalf("lock yara.schouten: %v", err)
		}
	}
	if err := signIn("yara.schouten"); !zammad.IsUnauthorized(err) {
		t.Errorf("locked yara.schouten with the right password: %v, want 401", err)
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
