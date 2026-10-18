package directory_test

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/go-ldap/ldap/v3"

	"github.com/toyinogun/AIITsupport/internal/seed/directory"
	"github.com/toyinogun/AIITsupport/internal/seed/directory/sambatest"
)

const (
	buildContext = "../../../seed/directory/samba"
	noordSeed    = "../../../seed/directory/noord.yaml"
	base         = sambatest.BaseDN
	// LDAP_MATCHING_RULE_IN_CHAIN: membership through any depth of nesting.
	inChain = "1.2.840.113556.1.4.1941"
)

// shared is the one seeded container every live test in this package uses.
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
	if err := seed(ctx, s, loadNoord()); err != nil {
		fmt.Fprintf(os.Stderr, "seed the shared Samba: %v\n", err)
		return 1
	}
	shared = s
	return m.Run()
}

func loadNoord() directory.Model {
	m, err := directory.Load(noordSeed, "")
	if err != nil {
		panic(fmt.Sprintf("load %s: %v", noordSeed, err))
	}
	return m
}

func dialer(s *sambatest.Samba) directory.Dialer {
	return directory.Dialer{URL: s.URL, RootCAs: s.RootCAs}
}

func seed(ctx context.Context, s *sambatest.Samba, m directory.Model) error {
	admin, err := dialer(s).WaitAndBind(ctx, sambatest.AdminDN, sambatest.AdminPassword, time.Minute)
	if err != nil {
		return err
	}
	defer admin.Close()
	_, err = directory.Apply(ctx, admin, m, directory.Options{UserPassword: sambatest.UserPassword, Dialer: dialer(s)})
	return err
}

func bind(t *testing.T, s *sambatest.Samba, dn, password string) *ldap.Conn {
	t.Helper()
	conn, err := dialer(s).Bind(dn, password)
	if err != nil {
		t.Fatalf("bind as %s: %v", dn, err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func search(t *testing.T, conn ldap.Client, baseDN string, scope int, filter string, attrs ...string) []*ldap.Entry {
	t.Helper()
	res, err := conn.SearchWithPaging(ldap.NewSearchRequest(baseDN, scope, ldap.NeverDerefAliases,
		0, 0, false, filter, attrs, nil), 500)
	if err != nil {
		t.Fatalf("search %s %s: %v", baseDN, filter, err)
	}
	return res.Entries
}

func requireShared(t *testing.T) {
	t.Helper()
	if shared == nil {
		t.Skip("set TEST_SAMBA=1 to run Samba integration tests (needs Docker)")
	}
}

// TestLiveDirectory runs in a fixed order, because later steps change state
// (locks, unlocks, a group edit) that earlier checks compare.
func TestLiveDirectory(t *testing.T) {
	requireShared(t)
	m := loadNoord()
	admin := bind(t, shared, sambatest.AdminDN, sambatest.AdminPassword)

	t.Run("seeded content matches the YAML", func(t *testing.T) { checkContent(t, admin, m) })
	t.Run("privileged access", func(t *testing.T) { checkPrivileged(t, admin, m) })
	t.Run("domain policy", func(t *testing.T) { checkPolicy(t, admin) })
	t.Run("identical rebuilds", func(t *testing.T) { checkIdenticalRebuild(t, admin, m) })
	t.Run("rerun is safe", func(t *testing.T) { checkRerun(t, admin, m) })
	t.Run("lockout", func(t *testing.T) { checkLockout(t, admin, m) })
	t.Run("permission wall", func(t *testing.T) { checkPermissionWall(t, admin, m) })
	t.Run("transport", checkTransport)
	t.Run("no added capabilities", checkCapabilities)
}

// covers: AC-3, AC-4, AC-5, AC-6, AC-10
func checkContent(t *testing.T, admin ldap.Client, m directory.Model) {
	for _, dept := range m.Departments {
		var want int
		for _, u := range m.Users {
			if u.Department == dept {
				want++
			}
		}
		got := search(t, admin, directory.DepartmentDN(base, dept), ldap.ScopeSingleLevel, "(objectClass=user)", "1.1")
		if len(got) != want {
			t.Errorf("OU %s holds %d users, want %d", dept, len(got), want)
		}
	}

	users := search(t, admin, directory.UsersDN(base), ldap.ScopeWholeSubtree, "(objectClass=user)",
		"sAMAccountName", "userPrincipalName", "mail", "manager", "userAccountControl", "title", "description")
	byName := map[string]*ldap.Entry{}
	for _, e := range users {
		byName[e.GetAttributeValue("sAMAccountName")] = e
	}
	for _, u := range m.Users {
		e, ok := byName[u.Username]
		if !ok {
			t.Errorf("user %s missing", u.Username)
			continue
		}
		if e.GetAttributeValue("userPrincipalName") != u.Username+"@noord.example" || e.GetAttributeValue("mail") != u.Mail {
			t.Errorf("user %s UPN/mail = %s / %s", u.Username, e.GetAttributeValue("userPrincipalName"), e.GetAttributeValue("mail"))
		}
		if (u.Manager == "") != (e.GetAttributeValue("manager") == "") {
			t.Errorf("user %s manager = %q, want set iff the YAML names one", u.Username, e.GetAttributeValue("manager"))
		}
		if u.Left != nil && e.GetAttributeValue("userAccountControl") != "512" {
			t.Errorf("leaver %s userAccountControl = %s, want 512 (still enabled)", u.Username, e.GetAttributeValue("userAccountControl"))
		}
	}

	jan1, _ := m.UserDN(base, "jan.devries")
	jan2, _ := m.UserDN(base, "jan.devries2")
	if jan1 == jan2 || !strings.HasPrefix(jan1, "CN=Jan de Vries,") || !strings.HasPrefix(jan2, "CN=Jan de Vries,") {
		t.Errorf("look alike DNs = %s and %s", jan1, jan2)
	}

	groups := search(t, admin, directory.GroupsDN(base), ldap.ScopeSingleLevel, "(objectClass=group)",
		"cn", "groupType", "mail", "managedBy")
	var unowned int
	for _, e := range groups {
		g, ok := m.Group(e.GetAttributeValue("cn"))
		if !ok {
			t.Errorf("unexpected group %s", e.DN)
			continue
		}
		wantType, wantMail := "-2147483646", ""
		if g.Kind == directory.KindDistribution {
			wantType, wantMail = "2", g.Name+"@noord.example"
		}
		if e.GetAttributeValue("groupType") != wantType || e.GetAttributeValue("mail") != wantMail {
			t.Errorf("group %s groupType/mail = %s / %q, want %s / %q", g.Name, e.GetAttributeValue("groupType"), e.GetAttributeValue("mail"), wantType, wantMail)
		}
		if e.GetAttributeValue("managedBy") == "" {
			unowned++
		}
	}
	if len(groups) < 18 || unowned < 4 {
		t.Errorf("%d groups in OU=Groups, %d unowned; want about 20 and at least 4", len(groups), unowned)
	}

	syncDN, _ := m.GroupDN(base, directory.SyncGroup)
	members := search(t, admin, base, ldap.ScopeWholeSubtree, "(memberOf="+ldap.EscapeFilter(syncDN)+")", "sAMAccountName")
	var synced int
	for _, u := range m.Users {
		if u.Synced {
			synced++
		}
	}
	if len(members) != synced {
		t.Errorf("%s has %d members, want %d", directory.SyncGroup, len(members), synced)
	}
}

// covers: AC-7
func checkPrivileged(t *testing.T, admin ldap.Client, m directory.Model) {
	itAdmins, _ := m.GroupDN(base, "SG-IT-Admins")
	domainAdmins := "CN=Domain Admins,CN=Users," + base
	got := search(t, admin, itAdmins, ldap.ScopeBaseObject, "(objectClass=group)", "memberOf", "member")
	if len(got) != 1 {
		t.Fatalf("SG-IT-Admins not found at %s", itAdmins)
	}
	if !slices.Contains(got[0].GetAttributeValues("memberOf"), domainAdmins) {
		t.Errorf("SG-IT-Admins memberOf = %v, want Domain Admins", got[0].GetAttributeValues("memberOf"))
	}
	admDN, _ := m.UserDN(base, "adm.ruben.mulder")
	if !slices.Contains(got[0].GetAttributeValues("member"), admDN) {
		t.Errorf("SG-IT-Admins members = %v, want %s", got[0].GetAttributeValues("member"), admDN)
	}

	for _, privileged := range []string{domainAdmins, itAdmins, "CN=Administrators,CN=Builtin," + base} {
		nested := search(t, admin, directory.GroupsDN(base), ldap.ScopeWholeSubtree,
			"(memberOf:"+inChain+":="+ldap.EscapeFilter(privileged)+")", "1.1")
		if len(nested) != 0 {
			t.Errorf("%d objects in OU=Groups are members of %s, want none", len(nested), privileged)
		}
	}
}

// covers: AC-8, AC-11 (machine account quota)
func checkPolicy(t *testing.T, admin ldap.Client) {
	root := search(t, admin, base, ldap.ScopeBaseObject, "(objectClass=*)",
		"lockoutThreshold", "lockoutDuration", "lockOutObservationWindow", "ms-DS-MachineAccountQuota")[0]
	fifteenMinutes := fmt.Sprint(-int64(15 * time.Minute / 100))
	want := map[string]string{
		"lockoutThreshold":          "5",
		"lockoutDuration":           fifteenMinutes,
		"lockOutObservationWindow":  fifteenMinutes,
		"ms-DS-MachineAccountQuota": "0",
	}
	for attr, w := range want {
		if got := root.GetAttributeValue(attr); got != w {
			t.Errorf("%s = %s, want %s", attr, got, w)
		}
	}
}

// covers: AC-2
func checkIdenticalRebuild(t *testing.T, admin ldap.Client, m directory.Model) {
	second := sambatest.New(t, buildContext)
	if err := seed(t.Context(), second, m); err != nil {
		t.Fatalf("seed the second container: %v", err)
	}
	secondAdmin := bind(t, second, sambatest.AdminDN, sambatest.AdminPassword)

	a, b := snapshot(t, admin), snapshot(t, secondAdmin)
	for dn, want := range a {
		got, ok := b[dn]
		if !ok {
			t.Errorf("%s exists in the first rebuild only", dn)
			continue
		}
		if got != want {
			t.Errorf("%s differs:\nfirst:  %s\nsecond: %s", dn, want, got)
		}
	}
	for dn := range b {
		if _, ok := a[dn]; !ok {
			t.Errorf("%s exists in the second rebuild only", dn)
		}
	}
}

// compareAttrs is AC-2's fixed list; GUIDs, SIDs, timestamps, USNs,
// pwdLastSet, and raw lockoutTime are left out on purpose.
var compareAttrs = []string{
	"cn", "sAMAccountName", "userPrincipalName", "mail", "givenName", "sn", "displayName",
	"department", "title", "description", "manager", "managedBy", "member", "groupType", "userAccountControl",
}

func snapshot(t *testing.T, admin ldap.Client) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, e := range search(t, admin, directory.NoordDN(base), ldap.ScopeWholeSubtree, "(objectClass=*)", compareAttrs...) {
		var parts []string
		for _, attr := range compareAttrs {
			values := slices.Clone(e.GetAttributeValues(attr))
			slices.Sort(values)
			parts = append(parts, attr+"="+strings.Join(values, "|"))
		}
		// Only accounts carry the computed lock flag; State fails on anything else.
		if state, err := directory.State(admin, e.DN); err == nil {
			parts = append(parts, fmt.Sprintf("locked=%t", state.Locked))
		}
		out[strings.ToLower(e.DN)] = strings.Join(parts, "; ")
	}
	return out
}

// covers: AC-13
func checkRerun(t *testing.T, admin ldap.Client, m directory.Model) {
	opts := directory.Options{UserPassword: sambatest.UserPassword, Dialer: dialer(shared)}

	res, err := directory.Apply(t.Context(), admin, m, opts)
	if err != nil || !res.AlreadySeeded {
		t.Fatalf("rerun with the same files: res=%+v err=%v, want already seeded", res, err)
	}

	edited, err := directory.Parse(append(mustRead(t, noordSeed), []byte("\n# edited\n")...), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := directory.Apply(t.Context(), admin, edited, opts); !errors.Is(err, directory.ErrNeedsReset) {
		t.Errorf("rerun with an edited YAML: err = %v, want ErrNeedsReset", err)
	}

	// A seed killed partway leaves content but no marker, since the marker is written last.
	setMarker(t, admin, nil)
	if _, err := directory.Apply(t.Context(), admin, m, opts); !errors.Is(err, directory.ErrNeedsReset) {
		t.Errorf("rerun after a partial seed: err = %v, want ErrNeedsReset", err)
	}
	setMarker(t, admin, []string{m.Marker})
}

func setMarker(t *testing.T, admin ldap.Client, values []string) {
	t.Helper()
	req := ldap.NewModifyRequest(directory.NoordDN(base), nil)
	req.Replace("description", values)
	if err := admin.Modify(req); err != nil {
		t.Fatalf("set marker: %v", err)
	}
}

// covers: AC-8, AC-9
func checkLockout(t *testing.T, admin ldap.Client, m directory.Model) {
	for _, u := range m.Users {
		dn, _ := m.UserDN(base, u.Username)
		state, err := directory.State(admin, dn)
		if err != nil {
			t.Fatal(err)
		}
		if state.Locked != u.Locked {
			t.Errorf("user %s locked = %t, want %t", u.Username, state.Locked, u.Locked)
		}
	}

	eva, _ := m.UserDN(base, "eva.peters")
	if err := directory.LockAndConfirm(t.Context(), admin, dialer(shared), eva, directory.WrongPassword(sambatest.UserPassword), 5); err != nil {
		t.Fatalf("lock eva.peters: %v", err)
	}
	if conn, err := dialer(shared).Bind(eva, sambatest.UserPassword); err == nil {
		_ = conn.Close()
		t.Error("eva.peters bound with the right password while locked")
	}
	daan, _ := m.UserDN(base, "daan.bos")
	bind(t, shared, daan, sambatest.UserPassword)
}

// covers: AC-11
func checkPermissionWall(t *testing.T, admin ldap.Client, m directory.Model) {
	svc := bind(t, shared, sambatest.SvcDN, sambatest.SvcPassword)
	dn := func(name string) string {
		if d, ok := m.UserDN(base, name); ok {
			return d
		}
		d, _ := m.GroupDN(base, name)
		return d
	}
	eva := dn("eva.peters")

	allowed := []struct {
		name string
		req  *ldap.ModifyRequest
	}{
		{"clear lockoutTime on a staff user", modify(eva, "replace", "lockoutTime", "0")},
		{"add a member to SG-Finance", modify(dn("SG-Finance"), "add", "member", eva)},
	}
	for _, c := range allowed {
		if err := svc.Modify(c.req); err != nil {
			t.Errorf("%s: %v, want allowed", c.name, err)
		}
	}
	if state, err := directory.State(admin, eva); err != nil || state.Locked {
		t.Errorf("eva.peters after the svc unlock: %+v, %v; want unlocked", state, err)
	}

	refused := []struct {
		name string
		do   func() error
	}{
		{"clear lockoutTime on an admin account", func() error {
			return svc.Modify(modify(dn("adm.ruben.mulder"), "replace", "lockoutTime", "0"))
		}},
		{"add a member to SG-IT-Admins", func() error {
			return svc.Modify(modify(dn("SG-IT-Admins"), "add", "member", eva))
		}},
		{"add a member to Domain Admins", func() error {
			return svc.Modify(modify("CN=Domain Admins,CN=Users,"+base, "add", "member", eva))
		}},
		{"reset a password", func() error {
			return svc.Modify(modify(dn("daan.bos"), "replace", "unicodePwd", "\"Other-Pass-9z\""))
		}},
		{"edit another attribute", func() error {
			return svc.Modify(modify(eva, "replace", "title", "Managing Director"))
		}},
		{"create a user under OU=Users", func() error {
			req := ldap.NewAddRequest("CN=Intruder,"+directory.DepartmentDN(base, "Sales"), nil)
			req.Attribute("objectClass", []string{"user"})
			req.Attribute("sAMAccountName", []string{"intruder"})
			return svc.Add(req)
		}},
		{"create a computer in CN=Computers", func() error {
			req := ldap.NewAddRequest("CN=ROGUEPC,CN=Computers,"+base, nil)
			req.Attribute("objectClass", []string{"computer"})
			req.Attribute("sAMAccountName", []string{"ROGUEPC$"})
			return svc.Add(req)
		}},
		{"delete a user", func() error { return svc.Del(ldap.NewDelRequest(dn("daan.bos"), nil)) }},
	}
	for _, c := range refused {
		err := c.do()
		if !ldap.IsErrorWithCode(err, ldap.LDAPResultInsufficientAccessRights) {
			t.Errorf("%s: err = %v, want insufficient access", c.name, err)
		}
	}
}

func modify(dn, op, attr, value string) *ldap.ModifyRequest {
	req := ldap.NewModifyRequest(dn, nil)
	switch op {
	case "add":
		req.Add(attr, []string{value})
	default:
		req.Replace(attr, []string{value})
	}
	return req
}

// covers: AC-12
func checkTransport(t *testing.T) {
	stranger := directory.Dialer{URL: shared.URL, RootCAs: x509.NewCertPool()}
	if conn, err := stranger.Dial(); err == nil {
		_ = conn.Close()
		t.Error("connected without trusting our CA")
	}
	if _, err := (directory.Dialer{URL: strings.Replace(shared.URL, "ldaps://", "ldap://", 1), RootCAs: shared.RootCAs}).Dial(); err == nil {
		t.Error("the dialer accepted a plain ldap:// URL")
	}
	if _, err := shared.Container.MappedPort(t.Context(), "389/tcp"); err == nil {
		t.Error("port 389 is published")
	}
	conn, err := dialer(shared).Dial()
	if err != nil {
		t.Fatalf("verified connect: %v", err)
	}
	_ = conn.Close()
}

// covers: AC-17
func checkCapabilities(t *testing.T) {
	info, err := shared.Container.Inspect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(info.HostConfig.CapAdd) != 0 || info.HostConfig.Privileged {
		t.Errorf("CapAdd = %v, privileged = %t; want none", info.HostConfig.CapAdd, info.HostConfig.Privileged)
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path) //nolint:gosec // G304: a fixed repo file named by the test
	if err != nil {
		t.Fatal(err)
	}
	return b
}
