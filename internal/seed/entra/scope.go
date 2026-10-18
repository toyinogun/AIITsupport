package entra

import (
	"cmp"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/go-ldap/ldap/v3"

	noordldap "github.com/toyinogun/AIITsupport/internal/ldap"
	"github.com/toyinogun/AIITsupport/internal/seed/directory"
)

// uacDisabledBit is userAccountControl's ACCOUNTDISABLE flag.
const uacDisabledBit = 0x2

const scopePageSize = 500

// ADUser is one direct member of SYNC-Entra-Users as AD holds it.
type ADUser struct {
	DN          string
	Username    string // sAMAccountName; empty for a member that is not a user
	DisplayName string
	GivenName   string
	Surname     string
	Department  string
	Title       string
	Manager     string // the manager's username; empty when none
	Disabled    bool
	// Outside is true for a member outside OU=Users,OU=Noord (an admin
	// account, a group). The scope guard refuses a sync while any exists.
	Outside bool
}

var adAttributes = []string{
	"sAMAccountName", "displayName", "givenName", "sn", "department", "title", "manager", "userAccountControl",
}

// ReadScope reads the direct members of SYNC-Entra-Users and their
// attributes. conn must be bound as the domain Administrator.
func ReadScope(conn ldap.Client, m directory.Model) ([]ADUser, error) {
	base, err := noordldap.BaseDN(conn)
	if err != nil {
		return nil, err
	}
	groupDN, _ := m.GroupDN(base, directory.SyncGroup)
	res, err := conn.Search(ldap.NewSearchRequest(groupDN, ldap.ScopeBaseObject, ldap.NeverDerefAliases,
		0, 0, false, "(objectClass=group)", []string{"member"}, nil))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", groupDN, err)
	}
	if len(res.Entries) != 1 {
		return nil, fmt.Errorf("read %s: got %d entries; is the directory seeded?", groupDN, len(res.Entries))
	}
	members := res.Entries[0].GetAttributeValues("member")

	people, err := conn.SearchWithPaging(ldap.NewSearchRequest(base, ldap.ScopeWholeSubtree, ldap.NeverDerefAliases,
		0, 0, false, "(&(objectClass=user)(objectCategory=person))", adAttributes, nil), scopePageSize)
	if err != nil {
		return nil, fmt.Errorf("read the directory's users: %w", err)
	}
	return BuildScope(base, members, people.Entries), nil
}

// BuildScope turns the group's member DNs and the directory's user entries
// into the sync scope, sorted by username, flagging members outside
// OU=Users,OU=Noord.
func BuildScope(base string, members []string, entries []*ldap.Entry) []ADUser {
	byDN := make(map[string]*ldap.Entry, len(entries))
	for _, e := range entries {
		byDN[strings.ToLower(e.DN)] = e
	}
	usersOU, _ := ldap.ParseDN(noordldap.UsersDN(base))

	out := make([]ADUser, 0, len(members))
	for _, dn := range members {
		e, isUser := byDN[strings.ToLower(dn)]
		parsed, err := ldap.ParseDN(dn)
		inside := err == nil && usersOU.AncestorOfFold(parsed)
		if !isUser {
			out = append(out, ADUser{DN: dn, Outside: true})
			continue
		}
		u := ADUser{
			DN:          dn,
			Username:    e.GetAttributeValue("sAMAccountName"),
			DisplayName: e.GetAttributeValue("displayName"),
			GivenName:   e.GetAttributeValue("givenName"),
			Surname:     e.GetAttributeValue("sn"),
			Department:  e.GetAttributeValue("department"),
			Title:       e.GetAttributeValue("title"),
			Disabled:    disabled(e.GetAttributeValue("userAccountControl")),
			Outside:     !inside,
		}
		if mgr, ok := byDN[strings.ToLower(e.GetAttributeValue("manager"))]; ok {
			u.Manager = mgr.GetAttributeValue("sAMAccountName")
		}
		out = append(out, u)
	}
	slices.SortFunc(out, func(a, b ADUser) int {
		return cmp.Or(strings.Compare(a.Username, b.Username), strings.Compare(a.DN, b.DN))
	})
	return out
}

func disabled(uac string) bool {
	n, err := strconv.Atoi(uac)
	return err == nil && n&uacDisabledBit != 0
}
