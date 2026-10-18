package ldap

import (
	"fmt"
	"strings"

	goldap "github.com/go-ldap/ldap/v3"
)

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
	return "OU=" + goldap.EscapeDN(department) + "," + UsersDN(base)
}

// CheckRelativeOU accepts an OU DN written relative to the base DN (a
// TOOL_USER_OUS or TOOL_GROUP_OUS entry): not empty, parseable, and with no
// DC= component.
func CheckRelativeOU(ou string) error {
	if strings.TrimSpace(ou) == "" {
		return fmt.Errorf("empty entry")
	}
	dn, err := goldap.ParseDN(ou)
	if err != nil {
		return fmt.Errorf("not a DN: %w", err)
	}
	if len(dn.RDNs) == 0 {
		return fmt.Errorf("empty entry")
	}
	for _, rdn := range dn.RDNs {
		for _, attr := range rdn.Attributes {
			if strings.EqualFold(attr.Type, "DC") {
				return fmt.Errorf("must be relative to LDAP_BASE_DN, with no DC= component")
			}
		}
	}
	return nil
}

// Under reports whether dn sits below the OU ou (relative to base). Both are
// parsed and compared RDN by RDN, attribute types and values case
// insensitively; never as a raw string suffix. The OU itself is not under
// itself.
func Under(dn, ou, base string) (bool, error) {
	obj, err := goldap.ParseDN(dn)
	if err != nil {
		return false, fmt.Errorf("parse object DN: %w", err)
	}
	parent, err := goldap.ParseDN(ou + "," + base)
	if err != nil {
		return false, fmt.Errorf("parse OU DN: %w", err)
	}
	return parent.AncestorOfFold(obj), nil
}

// UnderAny reports whether dn sits below any of the OUs.
func UnderAny(dn string, ous []string, base string) (bool, error) {
	for _, ou := range ous {
		under, err := Under(dn, ou, base)
		if err != nil {
			return false, err
		}
		if under {
			return true, nil
		}
	}
	return false, nil
}
