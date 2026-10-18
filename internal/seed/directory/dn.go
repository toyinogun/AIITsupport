package directory

import (
	"github.com/go-ldap/ldap/v3"

	noordldap "github.com/toyinogun/AIITsupport/internal/ldap"
)

// UserDN returns the DN of a staff user or an admin account.
func (m Model) UserDN(base, username string) (string, bool) {
	if u, ok := m.User(username); ok {
		return "CN=" + ldap.EscapeDN(u.DisplayName()) + "," + noordldap.DepartmentDN(base, u.Department), true
	}
	if _, ok := m.Admin(username); ok {
		return "CN=" + ldap.EscapeDN(username) + "," + noordldap.AdminsDN(base), true
	}
	return "", false
}

// GroupDN returns the DN of a group: privileged groups sit with the admin
// accounts, every other group in OU=Groups.
func (m Model) GroupDN(base, name string) (string, bool) {
	g, ok := m.Group(name)
	if !ok {
		return "", false
	}
	parent := noordldap.GroupsDN(base)
	if g.Kind == KindPrivileged {
		parent = noordldap.AdminsDN(base)
	}
	return "CN=" + ldap.EscapeDN(name) + "," + parent, true
}

// memberDN resolves a group member, which may be a user, an admin, or a group.
func (m Model) memberDN(base, name string) (string, bool) {
	if dn, ok := m.UserDN(base, name); ok {
		return dn, true
	}
	return m.GroupDN(base, name)
}
