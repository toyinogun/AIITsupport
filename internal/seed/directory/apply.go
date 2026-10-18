package directory

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"slices"
	"unicode/utf16"

	"github.com/go-ldap/ldap/v3"
)

// ErrNeedsReset means the directory already holds seeded content that is not
// exactly this seed: an earlier seed file, or a run that stopped partway.
var ErrNeedsReset = errors.New("the directory holds an earlier or partial seed; run make directory-reset")

// wrongPasswordSuffix turns the dev password into one that can never be valid,
// for locking users by failed binds.
const wrongPasswordSuffix = "-not-the-password"

// WrongPassword returns a password that is never valid for a seeded user.
func WrongPassword(userPassword string) string { return userPassword + wrongPasswordSuffix }

// Options are the inputs Apply needs beyond the model.
type Options struct {
	// UserPassword is set on every seeded user and admin account.
	UserPassword string
	// Dialer is used to lock users by failed binds as themselves.
	Dialer Dialer
}

// Result counts what Apply wrote.
type Result struct {
	AlreadySeeded bool
	Departments   int
	Users         int
	Admins        int
	Groups        int
	Locked        int
}

// Apply fills a freshly provisioned directory from the model. conn must be
// bound as the domain Administrator. If the directory already carries this
// model's marker, Apply writes nothing; if it holds any other seeded content,
// it returns ErrNeedsReset. The marker is written last, only after every
// object and every lock succeeded.
func Apply(ctx context.Context, conn ldap.Client, m Model, opts Options) (Result, error) {
	if opts.UserPassword == "" {
		return Result{}, errors.New("no user password; set SEED_USER_PASSWORD")
	}
	base, err := BaseDN(conn)
	if err != nil {
		return Result{}, err
	}
	seeded, err := checkSeedState(conn, base, m.Marker)
	if err != nil || seeded {
		return Result{AlreadySeeded: seeded}, err
	}

	a := applier{ctx: ctx, conn: conn, m: m, base: base, opts: opts}
	steps := []func() error{a.departments, a.users, a.managers, a.admins, a.groups, a.memberships, a.nesting, a.locks}
	for _, step := range steps {
		if err := step(); err != nil {
			return a.res, err
		}
	}
	if err := a.replace(NoordDN(base), "description", m.Marker); err != nil {
		return a.res, fmt.Errorf("write seed marker: %w", err)
	}
	return a.res, nil
}

// checkSeedState reports true when the marker matches, and ErrNeedsReset when
// the directory holds seeded content under any other marker or none.
func checkSeedState(conn ldap.Client, base, want string) (bool, error) {
	top, err := readOne(conn, NoordDN(base), "description")
	if err != nil {
		return false, err
	}
	if top == nil {
		return false, fmt.Errorf("%s is missing; the directory was not provisioned by our image", NoordDN(base))
	}
	got := top.GetAttributeValue("description")
	if got == want {
		return true, nil
	}
	for _, ou := range []string{UsersDN(base), GroupsDN(base), AdminsDN(base)} {
		has, err := hasChildren(conn, ou)
		if err != nil {
			return false, err
		}
		if has {
			return false, ErrNeedsReset
		}
	}
	if got != "" {
		return false, ErrNeedsReset
	}
	return false, nil
}

func hasChildren(conn ldap.Client, dn string) (bool, error) {
	res, err := conn.Search(ldap.NewSearchRequest(dn, ldap.ScopeSingleLevel, ldap.NeverDerefAliases,
		1, 0, false, "(objectClass=*)", []string{"1.1"}, nil))
	if ldap.IsErrorWithCode(err, ldap.LDAPResultSizeLimitExceeded) {
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("look for seeded content in %s: %w", dn, err)
	}
	return len(res.Entries) > 0, nil
}

type applier struct {
	ctx  context.Context
	conn ldap.Client
	m    Model
	base string
	opts Options
	res  Result
}

func (a *applier) departments() error {
	for _, d := range a.m.Departments {
		if err := a.add(DepartmentDN(a.base, d), [][2]any{
			{"objectClass", []string{"top", "organizationalUnit"}},
			{"ou", d},
		}); err != nil {
			return err
		}
		a.res.Departments++
	}
	return nil
}

// users adds every staff user disabled, sets the password, then enables them.
// Managers come in a second pass, once every user exists.
func (a *applier) users() error {
	for _, u := range a.m.Users {
		dn, _ := a.m.UserDN(a.base, u.Username)
		attrs := [][2]any{
			{"objectClass", []string{"top", "person", "organizationalPerson", "user"}},
			{"cn", u.DisplayName()},
			{"sAMAccountName", u.Username},
			{"userPrincipalName", u.Username + "@" + a.m.MailDomain},
			{"mail", u.Mail},
			{"givenName", u.GivenName},
			{"sn", u.Surname},
			{"displayName", u.DisplayName()},
			{"department", u.Department},
			{"title", u.Title},
			{"userAccountControl", uacDisabled},
		}
		if u.Description != "" {
			attrs = append(attrs, [2]any{"description", u.Description})
		}
		if err := a.addAccount(dn, attrs, uacEnabled); err != nil {
			return fmt.Errorf("user %s: %w", u.Username, err)
		}
		a.res.Users++
	}
	return nil
}

func (a *applier) managers() error {
	for _, u := range a.m.Users {
		if u.Manager == "" {
			continue
		}
		dn, _ := a.m.UserDN(a.base, u.Username)
		managerDN, _ := a.m.UserDN(a.base, u.Manager)
		if err := a.replace(dn, "manager", managerDN); err != nil {
			return fmt.Errorf("user %s: set manager: %w", u.Username, err)
		}
	}
	return nil
}

func (a *applier) admins() error {
	for _, adm := range a.m.Admins {
		dn, _ := a.m.UserDN(a.base, adm.Username)
		person, _ := a.m.User(adm.Person)
		attrs := [][2]any{
			{"objectClass", []string{"top", "person", "organizationalPerson", "user"}},
			{"cn", adm.Username},
			{"sAMAccountName", adm.Username},
			{"userPrincipalName", adm.Username + "@" + a.m.MailDomain},
			{"displayName", person.DisplayName() + " (admin)"},
			{"userAccountControl", uacDisabled},
		}
		if err := a.addAccount(dn, attrs, uacEnabledNeverExpires); err != nil {
			return fmt.Errorf("admin %s: %w", adm.Username, err)
		}
		a.res.Admins++
	}
	return nil
}

// addAccount adds a disabled account, sets its password over LDAPS, then
// enables it with the given userAccountControl.
func (a *applier) addAccount(dn string, attrs [][2]any, enabledUAC string) error {
	if err := a.add(dn, attrs); err != nil {
		return err
	}
	req := ldap.NewModifyRequest(dn, nil)
	req.Replace("unicodePwd", []string{encodePassword(a.opts.UserPassword)})
	if err := a.conn.Modify(req); err != nil {
		if ldap.IsErrorAnyOf(err, ldap.LDAPResultConstraintViolation, ldap.LDAPResultUnwillingToPerform) {
			return fmt.Errorf("set password: the domain rejected SEED_USER_PASSWORD (check its complexity and that it holds no staff name): %w", err)
		}
		return fmt.Errorf("set password: %w", err)
	}
	return a.replace(dn, "userAccountControl", enabledUAC)
}

// groups adds every group without members, so members can refer to groups
// added later.
func (a *applier) groups() error {
	for _, g := range a.m.Groups {
		dn, _ := a.m.GroupDN(a.base, g.Name)
		groupType := groupTypeGlobalSecurity
		if g.Kind == KindDistribution {
			groupType = groupTypeGlobalDistribution
		}
		attrs := [][2]any{
			{"objectClass", []string{"top", "group"}},
			{"cn", g.Name},
			{"sAMAccountName", g.Name},
			{"groupType", groupType},
		}
		if g.Kind == KindDistribution {
			attrs = append(attrs, [2]any{"mail", g.Name + "@" + a.m.MailDomain})
		}
		if g.Description != "" {
			attrs = append(attrs, [2]any{"description", g.Description})
		}
		if g.Owner != "" {
			ownerDN, _ := a.m.UserDN(a.base, g.Owner)
			attrs = append(attrs, [2]any{"managedBy", ownerDN})
		}
		if err := a.add(dn, attrs); err != nil {
			return fmt.Errorf("group %s: %w", g.Name, err)
		}
		a.res.Groups++
	}
	return nil
}

func (a *applier) memberships() error {
	for _, g := range a.m.Groups {
		if len(g.Members) == 0 {
			continue
		}
		dn, _ := a.m.GroupDN(a.base, g.Name)
		members := make([]string, 0, len(g.Members))
		for _, name := range g.Members {
			memberDN, _ := a.m.memberDN(a.base, name)
			members = append(members, memberDN)
		}
		slices.Sort(members)
		req := ldap.NewModifyRequest(dn, nil)
		req.Add("member", members)
		if err := a.modify(req); err != nil {
			return fmt.Errorf("group %s: add members: %w", g.Name, err)
		}
	}
	return nil
}

// nesting puts privileged groups into the built in groups they name, such as
// Domain Admins, found by sAMAccountName.
func (a *applier) nesting() error {
	for _, g := range a.m.Groups {
		dn, _ := a.m.GroupDN(a.base, g.Name)
		for _, builtin := range g.NestedInto {
			builtinDN, err := findBySAMAccountName(a.conn, a.base, builtin)
			if err != nil {
				return fmt.Errorf("group %s: nested_into %s: %w", g.Name, builtin, err)
			}
			req := ldap.NewModifyRequest(builtinDN, nil)
			req.Add("member", []string{dn})
			if err := a.modify(req); err != nil {
				return fmt.Errorf("group %s: add to %s: %w", g.Name, builtin, err)
			}
		}
	}
	return nil
}

// locks locks every user marked locked by failed binds as that user, since
// AD lets nobody set lockoutTime directly (AC-8).
func (a *applier) locks() error {
	var targets []string
	for _, u := range a.m.Users {
		if u.Locked {
			targets = append(targets, u.Username)
		}
	}
	if len(targets) == 0 {
		return nil
	}
	threshold, err := LockoutThreshold(a.conn, a.base)
	if err != nil {
		return err
	}
	for _, username := range targets {
		dn, _ := a.m.UserDN(a.base, username)
		if err := LockAndConfirm(a.ctx, a.conn, a.opts.Dialer, dn, WrongPassword(a.opts.UserPassword), threshold); err != nil {
			return fmt.Errorf("lock %s: %w", username, err)
		}
		a.res.Locked++
	}
	return nil
}

func (a *applier) add(dn string, attrs [][2]any) error {
	req := ldap.NewAddRequest(dn, nil)
	for _, kv := range attrs {
		name, _ := kv[0].(string)
		switch v := kv[1].(type) {
		case string:
			req.Attribute(name, []string{v})
		case []string:
			req.Attribute(name, v)
		}
	}
	if err := a.ctx.Err(); err != nil {
		return fmt.Errorf("add %s: %w", dn, err)
	}
	if err := a.conn.Add(req); err != nil {
		return fmt.Errorf("add %s: %w", dn, err)
	}
	return nil
}

func (a *applier) replace(dn, attr, value string) error {
	req := ldap.NewModifyRequest(dn, nil)
	req.Replace(attr, []string{value})
	if err := a.modify(req); err != nil {
		return fmt.Errorf("set %s on %s: %w", attr, dn, err)
	}
	return nil
}

func (a *applier) modify(req *ldap.ModifyRequest) error {
	if err := a.ctx.Err(); err != nil {
		return err
	}
	if err := a.conn.Modify(req); err != nil {
		return fmt.Errorf("modify %s: %w", req.DN, err)
	}
	return nil
}

func findBySAMAccountName(conn ldap.Client, base, name string) (string, error) {
	res, err := conn.Search(ldap.NewSearchRequest(base, ldap.ScopeWholeSubtree, ldap.NeverDerefAliases,
		2, 0, false, "(sAMAccountName="+ldap.EscapeFilter(name)+")", []string{"1.1"}, nil))
	if err != nil {
		return "", fmt.Errorf("find %s: %w", name, err)
	}
	if len(res.Entries) != 1 {
		return "", fmt.Errorf("find %s: got %d matches, want 1", name, len(res.Entries))
	}
	return res.Entries[0].DN, nil
}

// encodePassword is AD's unicodePwd format: the password in double quotes,
// encoded as UTF-16LE.
func encodePassword(password string) string {
	units := utf16.Encode([]rune(`"` + password + `"`))
	out := make([]byte, 0, 2*len(units))
	for _, u := range units {
		out = binary.LittleEndian.AppendUint16(out, u)
	}
	return string(out)
}
