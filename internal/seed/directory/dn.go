package directory

import "github.com/go-ldap/ldap/v3"

// Skeleton OUs the Samba entrypoint creates, relative to the base DN.
const (
	noordRDN  = "OU=Noord"
	usersRDN  = "OU=Users," + noordRDN
	groupsRDN = "OU=Groups," + noordRDN
	adminsRDN = "OU=Admin Accounts," + noordRDN
)

// NoordDN is the top OU, which carries the seed marker.
func NoordDN(base string) string { return noordRDN + "," + base }

// UsersDN holds one OU per department.
func UsersDN(base string) string { return usersRDN + "," + base }

// GroupsDN holds every non privileged group.
func GroupsDN(base string) string { return groupsRDN + "," + base }

// AdminsDN holds admin accounts and privileged groups.
func AdminsDN(base string) string { return adminsRDN + "," + base }

// DepartmentDN is a department's OU under OU=Users.
func DepartmentDN(base, department string) string {
	return "OU=" + ldap.EscapeDN(department) + "," + UsersDN(base)
}

// UserDN returns the DN of a staff user or an admin account.
func (m Model) UserDN(base, username string) (string, bool) {
	if u, ok := m.User(username); ok {
		return "CN=" + ldap.EscapeDN(u.DisplayName()) + "," + DepartmentDN(base, u.Department), true
	}
	if _, ok := m.Admin(username); ok {
		return "CN=" + ldap.EscapeDN(username) + "," + AdminsDN(base), true
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
	parent := GroupsDN(base)
	if g.Kind == KindPrivileged {
		parent = AdminsDN(base)
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
