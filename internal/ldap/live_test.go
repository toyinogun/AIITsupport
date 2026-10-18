package ldap_test

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	goldap "github.com/go-ldap/ldap/v3"

	noordldap "github.com/toyinogun/AIITsupport/internal/ldap"
	"github.com/toyinogun/AIITsupport/internal/seed/directory"
	"github.com/toyinogun/AIITsupport/internal/seed/directory/sambatest"
)

const (
	buildContext = "../../seed/directory/samba"
	noordSeed    = "../../seed/directory/noord.yaml"
	base         = sambatest.BaseDN
)

// shared is the one seeded container every live test here uses.
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
	d := noordldap.Dialer{URL: s.URL, RootCAs: s.RootCAs}
	admin, err := d.WaitAndBind(ctx, sambatest.AdminDN, sambatest.AdminPassword, time.Minute)
	if err != nil {
		return err
	}
	defer admin.Close()
	_, err = directory.Apply(ctx, admin, m, directory.Options{UserPassword: sambatest.UserPassword, Dialer: d})
	return err
}

func requireShared(t *testing.T) {
	t.Helper()
	if shared == nil {
		t.Skip("set TEST_SAMBA=1 to run Samba integration tests (needs Docker)")
	}
}

// svcConfig binds as svc-itsupport, the tool server's account.
func svcConfig() noordldap.Config {
	return noordldap.Config{
		Dialer:   noordldap.Dialer{URL: shared.URL, RootCAs: shared.RootCAs, Timeout: 5 * time.Second},
		BindDN:   sambatest.SvcDN,
		Password: sambatest.SvcPassword,
		BaseDN:   base,
	}
}

func adminConn(t *testing.T) *goldap.Conn {
	t.Helper()
	conn, err := noordldap.Dialer{URL: shared.URL, RootCAs: shared.RootCAs}.Bind(sambatest.AdminDN, sambatest.AdminPassword)
	if err != nil {
		t.Fatalf("bind as Administrator: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// covers: AC-4. The identity search finds one enabled user by mail.
func TestLiveFindUsersByMail(t *testing.T) {
	requireShared(t)
	c := noordldap.NewClient(svcConfig())

	users, err := c.FindUsersByMail(t.Context(), "eva.peters@noord.example")
	if err != nil {
		t.Fatalf("FindUsersByMail: %v", err)
	}
	if len(users) != 1 || users[0].Username != "eva.peters" || users[0].Department != "Sales" || !users[0].Enabled {
		t.Fatalf("users = %+v, want eva.peters in Sales, enabled", users)
	}
	under, err := noordldap.Under(users[0].DN, "OU=Users,OU=Noord", base)
	if err != nil || !under {
		t.Errorf("Under(%s) = %v, %v; want true", users[0].DN, under, err)
	}

	// Case differs: the directory matches, the caller still compares exactly.
	upper, err := c.FindUsersByMail(t.Context(), "EVA.PETERS@noord.example")
	if err != nil || len(upper) != 1 {
		t.Errorf("FindUsersByMail with upper case = %v, %v; want one match", upper, err)
	}
	// A filter character is escaped, so it matches nobody.
	none, err := c.FindUsersByMail(t.Context(), "*")
	if err != nil || len(none) != 0 {
		t.Errorf("FindUsersByMail(*) = %v, %v; want no match", none, err)
	}
}

// covers: AC-6, AC-7. The two Samba facts the design leans on: svc-itsupport
// reads the computed lock flag, and its lockoutTime write unlocks.
func TestLiveLockAndClearAsTheServiceAccount(t *testing.T) {
	requireShared(t)
	admin := adminConn(t)
	d := noordldap.Dialer{URL: shared.URL, RootCAs: shared.RootCAs}
	c := noordldap.NewClient(svcConfig())

	users, err := c.FindUsersByMail(t.Context(), "fleur.dijkstra@noord.example")
	if err != nil || len(users) != 1 {
		t.Fatalf("find fleur.dijkstra = %v, %v", users, err)
	}
	dn := users[0].DN
	threshold, err := directory.LockoutThreshold(admin, base)
	if err != nil {
		t.Fatal(err)
	}
	if err := directory.LockAndConfirm(t.Context(), admin, d, dn, directory.WrongPassword(sambatest.UserPassword), threshold); err != nil {
		t.Fatalf("lock: %v", err)
	}

	before := time.Now()
	lock, err := c.Lock(t.Context(), dn)
	if err != nil {
		t.Fatalf("Lock as svc-itsupport: %v", err)
	}
	if !lock.Locked {
		t.Fatal("svc-itsupport reads the account as unlocked; it cannot see the computed flag")
	}
	// The image locks for 15 minutes.
	if lock.EndsAt.Before(before.Add(10*time.Minute)) || lock.EndsAt.After(before.Add(16*time.Minute)) {
		t.Errorf("EndsAt = %s, want about 15 minutes after %s", lock.EndsAt, before)
	}

	if err := noordldap.NewWriter(svcConfig()).ClearLockout(t.Context(), dn); err != nil {
		t.Fatalf("ClearLockout as svc-itsupport: %v", err)
	}
	after, err := c.Lock(t.Context(), dn)
	if err != nil || after.Locked || !after.EndsAt.IsZero() {
		t.Fatalf("after ClearLockout: %+v, %v; want unlocked", after, err)
	}
}

// covers: AC-7. AD itself stops svc-itsupport unlocking an admin account.
func TestLiveServiceAccountCannotUnlockAnAdmin(t *testing.T) {
	requireShared(t)
	dn := "CN=adm.ruben.mulder," + noordldap.AdminsDN(base)
	err := noordldap.NewWriter(svcConfig()).ClearLockout(t.Context(), dn)
	if !goldap.IsErrorWithCode(err, goldap.LDAPResultInsufficientAccessRights) {
		t.Fatalf("ClearLockout on an admin = %v, want insufficient access", err)
	}
}

// covers: AC-13. Samba honors the in chain matching rule, so nesting at any
// depth is one search per denied group.
func TestLiveGroupsAndNesting(t *testing.T) {
	requireShared(t)
	c := noordldap.NewClient(svcConfig())
	admin := adminConn(t)

	groups, err := c.GroupsByName(t.Context(), []string{"SG-IT-Admins", "Domain Admins", "SG-Finance", "No-Such-Group"})
	if err != nil {
		t.Fatalf("GroupsByName: %v", err)
	}
	if len(groups) != 3 || groups["No-Such-Group"].DN != "" {
		t.Fatalf("groups = %v, want three found and no missing one", groups)
	}

	// A test group, two levels below Domain Admins: test -> SG-IT-Admins -> Domain Admins.
	name := "T-Nested-Live"
	dn := "CN=" + name + "," + noordldap.GroupsDN(base)
	add := goldap.NewAddRequest(dn, nil)
	add.Attribute("objectClass", []string{"group"})
	add.Attribute("sAMAccountName", []string{name})
	if err := admin.Add(add); err != nil {
		t.Fatalf("add test group: %v", err)
	}
	t.Cleanup(func() { _ = admin.Del(goldap.NewDelRequest(dn, nil)) })
	mod := goldap.NewModifyRequest(groups["SG-IT-Admins"].DN, nil)
	mod.Add("member", []string{dn})
	if err := admin.Modify(mod); err != nil {
		t.Fatalf("nest the test group: %v", err)
	}
	t.Cleanup(func() {
		undo := goldap.NewModifyRequest(groups["SG-IT-Admins"].DN, nil)
		undo.Delete("member", []string{dn})
		_ = admin.Modify(undo)
	})

	for _, tc := range []struct {
		group   string
		parents []string
		want    bool
	}{
		{name, []string{groups["SG-IT-Admins"].DN}, true},
		{name, []string{groups["Domain Admins"].DN}, true},
		{"SG-Finance", []string{groups["SG-IT-Admins"].DN, groups["Domain Admins"].DN}, false},
	} {
		got, err := c.InChainMemberOf(t.Context(), tc.group, tc.parents)
		if err != nil || got != tc.want {
			t.Errorf("InChainMemberOf(%s, %v) = %v, %v; want %v", tc.group, tc.parents, got, err, tc.want)
		}
	}
}

// covers: AC-19. A cancelled context ends the call instead of waiting on the directory.
func TestLiveCallEndsWithItsContext(t *testing.T) {
	requireShared(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := noordldap.NewClient(svcConfig()).FindUsersByMail(ctx, "eva.peters@noord.example"); err == nil {
		t.Fatal("FindUsersByMail with a cancelled context succeeded")
	}
}
