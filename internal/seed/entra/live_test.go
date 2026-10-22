package entra

import (
	"errors"
	"log/slog"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/go-ldap/ldap/v3"

	"github.com/toyinogun/AIITsupport/internal/config"
	"github.com/toyinogun/AIITsupport/internal/graph"
	noordldap "github.com/toyinogun/AIITsupport/internal/ldap"
	"github.com/toyinogun/AIITsupport/internal/seed/directory"
	"github.com/toyinogun/AIITsupport/internal/seed/directory/sambatest"
)

// The live tests change a real internet tenant. They run only on your
// laptop with TEST_ENTRA=1, TEST_SAMBA=1, and the .env settings loaded,
// never in CI (AC-16).

const sambaBuildContext = "../../../seed/directory/samba"

type live struct {
	seed, tool *graph.Client
	syncer     Syncer
	scope      []ADUser
	opts       Options
}

func newLive(t *testing.T) *live {
	t.Helper()
	if os.Getenv("TEST_ENTRA") == "" {
		t.Skip("set TEST_ENTRA=1 (with TEST_SAMBA=1 and .env loaded) to run the live Entra tests")
	}
	s := sambatest.New(t, sambaBuildContext)
	lookup := func(key string) (string, bool) {
		switch key {
		case "SEED_ADMIN_PASSWORD":
			return sambatest.AdminPassword, true
		case "SEED_USER_PASSWORD":
			return sambatest.UserPassword, true
		}
		return os.LookupEnv(key)
	}
	cfg, err := config.LoadSeed(lookup)
	if err != nil {
		t.Fatalf("LoadSeed: %v", err)
	}
	if !cfg.EntraConfigured() {
		t.Fatal("TEST_ENTRA is set but SEED_GRAPH_* is not; load .env first")
	}
	toolCfg, err := config.LoadGraph(os.LookupEnv)
	if err != nil {
		t.Fatalf("LoadGraph: %v", err)
	}

	m := loadNoord(t)
	dialer := noordldap.Dialer{URL: s.URL, RootCAs: s.RootCAs}
	admin, err := dialer.WaitAndBind(t.Context(), sambatest.AdminDN, sambatest.AdminPassword, time.Minute)
	if err != nil {
		t.Fatalf("bind as Administrator: %v", err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	if _, err := directory.Apply(t.Context(), admin, m, directory.Options{UserPassword: sambatest.UserPassword, Dialer: dialer}); err != nil {
		t.Fatalf("seed Samba: %v", err)
	}
	scope, err := ReadScope(admin, m)
	if err != nil {
		t.Fatalf("ReadScope: %v", err)
	}

	seedClient, err := graph.NewWithCredentials(
		graph.Credentials{TenantID: cfg.GraphTenantID, ClientID: cfg.GraphClientID, ClientSecret: cfg.GraphClientSecret},
		graph.Options{MaxRetries: cfg.GraphMaxRetries, Timeout: cfg.GraphTimeout, ClientIDSetting: "SEED_GRAPH_CLIENT_ID"})
	if err != nil {
		t.Fatal(err)
	}
	toolClient, err := graph.NewWithCredentials(
		graph.Credentials{TenantID: toolCfg.GraphTenantID, ClientID: toolCfg.GraphClientID, ClientSecret: toolCfg.GraphClientSecret, CertFile: toolCfg.GraphClientCert},
		graph.Options{MaxRetries: toolCfg.GraphMaxRetries, Timeout: toolCfg.GraphTimeout, ClientIDSetting: "GRAPH_CLIENT_ID"})
	if err != nil {
		t.Fatal(err)
	}
	return &live{
		seed: seedClient, tool: toolClient, scope: scope,
		syncer: Syncer{Client: seedClient, Logger: slog.New(slog.DiscardHandler), Password: func(string) string { return RandomPassword() }},
		opts:   Options{Domain: cfg.EntraDomain, Phones: mustPhones(t, m, Demo{}), DeleteLimit: cfg.EntraDeleteLimit},
	}
}

func (l *live) sync(t *testing.T) Result {
	t.Helper()
	start := time.Now()
	res, err := l.syncer.Run(t.Context(), l.scope, l.opts)
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	t.Logf("sync took %s: %+v", time.Since(start).Round(time.Millisecond), res)
	return res
}

// covers: AC-2, AC-3, AC-5, AC-6, AC-9, AC-10, AC-11. The whole thread on a
// real tenant: sync, rerun, drift, the MFA clear, the fence, least privilege.
func TestLiveSync(t *testing.T) {
	l := newLive(t)
	l.sync(t)

	m := loadNoord(t)
	for _, u := range m.Users {
		found, err := l.seed.UsersByNickname(t.Context(), u.Username)
		if err != nil {
			t.Fatal(err)
		}
		if (len(found) == 1) != u.Synced {
			t.Errorf("%s in Entra = %t, want %t", u.Username, len(found) == 1, u.Synced)
		}
	}

	if res := l.sync(t); res.Writes != 0 {
		t.Errorf("rerun wrote %d, want 0", res.Writes)
	}

	t.Run("drift", func(t *testing.T) { liveDrift(t, l) })
	t.Run("mfa clear", func(t *testing.T) { liveMFAClear(t, l) })
	t.Run("fence and least privilege", func(t *testing.T) { liveFence(t, l) })
}

func liveDrift(t *testing.T, l *live) {
	eva, err := l.seed.UserByUsername(t.Context(), "eva.peters")
	if err != nil {
		t.Fatal(err)
	}
	if err := l.seed.Do(t.Context(), graph.Request{Method: http.MethodPatch, Path: graph.UserPath(eva.ID()), Body: map[string]any{"department": "Marketing"}}); err != nil {
		t.Fatal(err)
	}
	bas, err := l.seed.UserByUsername(t.Context(), "bas.kramer")
	if err != nil {
		t.Fatal(err)
	}
	if err := l.seed.Do(t.Context(), graph.Request{Method: http.MethodDelete, Path: graph.UserPath(bas.ID())}); err != nil {
		t.Fatal(err)
	}

	total := l.syncUntilSettled(t)
	if total.Restored != 1 || total.Created != 0 || total.Updated < 1 {
		t.Errorf("total over the settling runs = %+v, want bas.kramer restored (not created) and eva.peters updated", total)
	}
	back, err := l.seed.UserByUsername(t.Context(), "bas.kramer")
	if err != nil || back.ID() != bas.ID() {
		t.Errorf("bas.kramer after the sync = %v, %v; want the same object id", back.ID(), err)
	}
	if got := l.mustUser(t, "eva.peters").User().Department; got == nil || *got != "Sales" {
		t.Errorf("eva.peters department = %v, want Sales", deref(got))
	}
}

// syncUntilSettled syncs until a run makes no writes (at most 4 runs, 15s
// apart) and returns the summed result. Graph is eventually consistent:
// for a while after a write, some reads still return the old state, so the
// first run after a drift may not see all of it. Each later run fixes what
// it sees, and none recreates or duplicates anything (AC-6, AC-12).
func (l *live) syncUntilSettled(t *testing.T) Result {
	t.Helper()
	var total Result
	for run := 1; run <= 4; run++ {
		res := l.sync(t)
		total.Created += res.Created
		total.Updated += res.Updated
		total.Restored += res.Restored
		total.Deleted += res.Deleted
		total.PhonesSet += res.PhonesSet
		total.Writes += res.Writes
		if res.Writes == 0 && run > 1 {
			return total
		}
		time.Sleep(15 * time.Second)
	}
	t.Fatalf("still writing after 4 runs: %+v", total)
	return total
}

func liveMFAClear(t *testing.T, l *live) {
	u, err := l.tool.UserByUsername(t.Context(), "eva.peters")
	if err != nil {
		t.Fatal(err)
	}
	phones, err := l.tool.PhoneMethods(t.Context(), u)
	if err != nil {
		t.Fatalf("PhoneMethods with the tool server app: %v", err)
	}
	for _, p := range phones {
		if err := l.tool.DeletePhoneMethod(t.Context(), u, p.ID); err != nil {
			t.Fatalf("DeletePhoneMethod with the tool server app: %v", err)
		}
	}
	if total := l.syncUntilSettled(t); total.PhonesSet != 1 {
		t.Errorf("total over the settling runs = %+v, want the mobile restored once", total)
	}
}

func liveFence(t *testing.T, l *live) {
	users, err := graph.ListAll[graph.User](t.Context(), l.seed, "/users"+graph.Query("$select", graph.UserSelect))
	if err != nil {
		t.Fatal(err)
	}
	var admin graph.User
	for _, u := range users {
		if u.EmployeeType != graph.ManagedMarker {
			admin = u
			break
		}
	}
	if admin.ID == "" {
		t.Fatal("no unmanaged user (your admin) in the tenant")
	}
	if _, err := l.tool.UserByUsername(t.Context(), admin.MailNickname); !errors.Is(err, graph.ErrNotManaged) {
		t.Errorf("UserByUsername(your admin) = %v, want ErrNotManaged", err)
	}
	if _, err := l.tool.UserByUsername(t.Context(), "made.up.user"); !errors.Is(err, graph.ErrNotFound) {
		t.Errorf("UserByUsername(made.up.user) = %v, want ErrNotFound", err)
	}

	eva := l.mustUser(t, "eva.peters")
	err = l.tool.Do(t.Context(), graph.Request{Method: http.MethodPost, Path: "/users", Body: map[string]any{
		"accountEnabled": false, "displayName": "Probe", "mailNickname": "probe.user",
		"userPrincipalName": "probe.user@" + l.opts.Domain,
		"passwordProfile":   map[string]any{"password": RandomPassword()},
	}})
	if !graph.IsForbidden(err) {
		t.Errorf("POST /users with the tool server app = %v, want 403", err)
	}
	err = l.tool.Do(t.Context(), graph.Request{
		Method: http.MethodPost,
		Path:   graph.UserPath(eva.ID()) + "/authentication/temporaryAccessPassMethods", Body: map[string]any{"isUsableOnce": true},
	})
	if !graph.IsForbidden(err) {
		t.Errorf("Temporary Access Pass POST with the tool server app = %v, want 403", err)
	}

	// The admin probe: what Graph itself allows on your admin. The fence
	// above stops our code either way; record this in the journal.
	var page struct{ Value []graph.PhoneMethod }
	err = l.tool.Do(t.Context(), graph.Request{Method: http.MethodGet, Path: graph.UserPath(admin.ID) + "/authentication/phoneMethods", Out: &page})
	t.Logf("admin probe: reading your admin's phone methods with the tool server app gives status %d (0 means allowed): %v", graph.Status(err), err)
}

func (l *live) mustUser(t *testing.T, username string) graph.ManagedUser {
	t.Helper()
	u, err := l.seed.UserByUsername(t.Context(), username)
	if err != nil {
		t.Fatalf("UserByUsername(%s): %v", username, err)
	}
	return u
}

// covers: AC-3. Against a real seeded Samba (TEST_SAMBA=1, no Entra needed):
// the scope is exactly the synced users with their managers, and an admin
// account added to the group trips the scope guard.
func TestLiveReadScope(t *testing.T) {
	s := sambatest.New(t, sambaBuildContext)
	m := loadNoord(t)
	dialer := noordldap.Dialer{URL: s.URL, RootCAs: s.RootCAs}
	admin, err := dialer.WaitAndBind(t.Context(), sambatest.AdminDN, sambatest.AdminPassword, time.Minute)
	if err != nil {
		t.Fatalf("bind as Administrator: %v", err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	if _, err := directory.Apply(t.Context(), admin, m, directory.Options{UserPassword: sambatest.UserPassword, Dialer: dialer}); err != nil {
		t.Fatalf("seed Samba: %v", err)
	}

	scope, err := ReadScope(admin, m)
	if err != nil {
		t.Fatalf("ReadScope: %v", err)
	}
	want := scopeOf(m)
	slices.SortFunc(want, func(a, b ADUser) int { return strings.Compare(a.Username, b.Username) })
	if len(scope) != len(want) {
		t.Fatalf("scope has %d members, want %d synced users", len(scope), len(want))
	}
	for i, u := range scope {
		w := want[i]
		if u.Outside || u.Username != w.Username || u.Manager != w.Manager || u.DisplayName != w.DisplayName ||
			u.Department != w.Department || u.Title != w.Title || u.Disabled {
			t.Errorf("scope[%d] = %+v, want %+v", i, u, w)
		}
	}

	groupDN, _ := m.GroupDN(sambatest.BaseDN, directory.SyncGroup)
	admDN, _ := m.UserDN(sambatest.BaseDN, "adm.ruben.mulder")
	req := ldap.NewModifyRequest(groupDN, nil)
	req.Add("member", []string{admDN})
	if err := admin.Modify(req); err != nil {
		t.Fatalf("add the admin account to %s: %v", directory.SyncGroup, err)
	}
	scope, err = ReadScope(admin, m)
	if err != nil {
		t.Fatalf("ReadScope: %v", err)
	}
	_, err = MakePlan(Inputs{Scope: scope, Domain: testDomain, Phones: mustPhones(t, m, Demo{}), DeleteLimit: 10})
	if err == nil || !strings.Contains(err.Error(), "OU=Admin Accounts") {
		t.Errorf("MakePlan = %v, want the scope guard naming the admin account", err)
	}
}
