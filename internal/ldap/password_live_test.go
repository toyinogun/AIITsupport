package ldap_test

import (
	"testing"

	goldap "github.com/go-ldap/ldap/v3"

	noordldap "github.com/toyinogun/AIITsupport/internal/ldap"
	"github.com/toyinogun/AIITsupport/internal/seed/directory"
	"github.com/toyinogun/AIITsupport/internal/seed/directory/sambatest"
)

// resetConfig binds as svc-pwreset, the reset page's account (spec 0011).
func resetConfig() noordldap.Config {
	cfg := svcConfig()
	cfg.BindDN, cfg.Password = sambatest.PwresetDN, sambatest.PwresetPassword
	return cfg
}

func staffDN(t *testing.T, mail string) string {
	t.Helper()
	users, err := noordldap.NewClient(svcConfig()).FindUsersByMail(t.Context(), mail)
	if err != nil || len(users) != 1 {
		t.Fatalf("find %s = %v, %v", mail, users, err)
	}
	return users[0].DN
}

// covers: spec 0011 AC-11, AC-15. The build step 1 probe: svc-pwreset resets
// a locked staff user's password with the policy hints control, which also
// clears the lock; the new password binds; and reusing it is refused.
func TestLiveResetPasswordAsTheResetAccount(t *testing.T) {
	requireShared(t)
	admin := adminConn(t)
	d := noordldap.Dialer{URL: shared.URL, RootCAs: shared.RootCAs}
	dn := staffDN(t, "erwin.kok@noord.example")
	threshold, err := directory.LockoutThreshold(admin, base)
	if err != nil {
		t.Fatal(err)
	}
	if err := directory.LockAndConfirm(t.Context(), admin, d, dn, directory.WrongPassword(sambatest.UserPassword), threshold); err != nil {
		t.Fatalf("lock: %v", err)
	}

	c := noordldap.NewClient(svcConfig())
	before, err := c.PasswordLastSet(t.Context(), dn)
	if err != nil {
		t.Fatalf("PasswordLastSet: %v", err)
	}
	w := noordldap.NewPasswordWriter(resetConfig())
	const newPassword = "Reset-Probe-7f3a-Q9"
	if err := w.SetPassword(t.Context(), dn, newPassword); err != nil {
		t.Fatalf("SetPassword as svc-pwreset: %v", err)
	}

	lock, err := c.Lock(t.Context(), dn)
	if err != nil || lock.Locked {
		t.Fatalf("after the reset: %+v, %v; want unlocked", lock, err)
	}
	state, err := noordldap.State(admin, dn)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("badPwdCount after the reset: %d", state.BadPwdCount)
	after, err := c.PasswordLastSet(t.Context(), dn)
	if err != nil || !after.After(before) {
		t.Errorf("pwdLastSet = %s (was %s), %v; want newer", after, before, err)
	}
	conn, err := d.Bind(dn, newPassword)
	if err != nil {
		t.Fatalf("bind with the new password: %v", err)
	}
	_ = conn.Close()
	// The image sets old password allowed period = 0, so the replaced
	// password stops working at once (journal 0038).
	if old, err := d.Bind(dn, sambatest.UserPassword); err == nil {
		_ = old.Close()
		t.Error("the old password still binds right after the reset")
	} else if !goldap.IsErrorWithCode(err, goldap.LDAPResultInvalidCredentials) {
		t.Errorf("bind with the old password = %v, want invalid credentials", err)
	}

	// Windows AD holds a reset with the hints control to the history rule;
	// Samba ignores the control, so the page shows no history line. If this
	// starts failing, Samba honours it now: restore the line (spec 0011 AC-11).
	if err := w.SetPassword(t.Context(), dn, newPassword); err != nil {
		t.Errorf("reusing the current password = %v; Samba used to accept it on a reset", err)
	}
	err = w.SetPassword(t.Context(), dn, "short")
	if !noordldap.IsPolicyRefusal(err) {
		t.Errorf("a too short password = %v, want a policy refusal", err)
	}
}

// covers: spec 0011 AC-7. The tool server's reader sees the domain rules.
func TestLivePasswordPolicy(t *testing.T) {
	requireShared(t)
	p, err := noordldap.NewClient(svcConfig()).PasswordPolicy(t.Context())
	if err != nil {
		t.Fatalf("PasswordPolicy: %v", err)
	}
	if p.MinLength < 1 || !p.Complex || p.HistoryLength < 1 {
		t.Errorf("policy = %+v, want Samba's provisioned rules (length, complexity, history)", p)
	}
}

// covers: spec 0011 AC-15. svc-pwreset can do nothing else: AD refuses it an
// admin account, any other attribute, a group change, and a create, and
// svc-itsupport still cannot reset a password.
func TestLiveResetAccountLeastPrivilege(t *testing.T) {
	requireShared(t)
	adminDN := "CN=adm.ruben.mulder," + noordldap.AdminsDN(base)
	staff := staffDN(t, "lucas.dekker@noord.example")
	insufficient := func(name string, err error) {
		t.Helper()
		if !goldap.IsErrorWithCode(err, goldap.LDAPResultInsufficientAccessRights) {
			t.Errorf("%s = %v, want insufficient access", name, err)
		}
	}

	insufficient("reset an admin account", noordldap.NewPasswordWriter(resetConfig()).SetPassword(t.Context(), adminDN, "Admin-Probe-2c9d-Q9"))
	insufficient("svc-itsupport resets a password", noordldap.NewPasswordWriter(svcConfig()).SetPassword(t.Context(), staff, "Svc-Probe-5e1b-Q9"))

	conn, err := noordldap.Dialer{URL: shared.URL, RootCAs: shared.RootCAs}.Bind(sambatest.PwresetDN, sambatest.PwresetPassword)
	if err != nil {
		t.Fatalf("bind as svc-pwreset: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	other := goldap.NewModifyRequest(staff, nil)
	other.Replace("description", []string{"probe"})
	insufficient("write another attribute", conn.Modify(other))

	group := goldap.NewModifyRequest("CN=SG-Finance,"+noordldap.GroupsDN(base), nil)
	group.Add("member", []string{staff})
	insufficient("change a group", conn.Modify(group))

	add := goldap.NewAddRequest("CN=probe-user,"+noordldap.UsersDN(base), nil)
	add.Attribute("objectClass", []string{"user"})
	add.Attribute("sAMAccountName", []string{"probe-user"})
	insufficient("create a user", conn.Add(add))
}

// covers: spec 0011 AC-3, AC-10. The user membership check follows nesting:
// the admin account is in Domain Admins through SG-IT-Admins.
func TestLiveUserInChainMemberOf(t *testing.T) {
	requireShared(t)
	c := noordldap.NewClient(svcConfig())
	groups, err := c.GroupsByName(t.Context(), []string{"Domain Admins"})
	if err != nil || groups["Domain Admins"].DN == "" {
		t.Fatalf("GroupsByName = %v, %v", groups, err)
	}
	parents := []string{groups["Domain Admins"].DN}
	adminDN := "CN=adm.ruben.mulder," + noordldap.AdminsDN(base)
	if in, err := c.UserInChainMemberOf(t.Context(), adminDN, parents); err != nil || !in {
		t.Errorf("admin in Domain Admins = %v, %v; want true", in, err)
	}
	staff := staffDN(t, "eva.peters@noord.example")
	if in, err := c.UserInChainMemberOf(t.Context(), staff, parents); err != nil || in {
		t.Errorf("staff in Domain Admins = %v, %v; want false", in, err)
	}
}
