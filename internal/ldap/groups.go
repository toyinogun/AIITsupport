package ldap

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	goldap "github.com/go-ldap/ldap/v3"
)

// Group attributes the access request reads (spec 0013).
const (
	attrMember      = "member"
	attrGroupType   = "groupType"
	attrManagedBy   = "managedBy"
	attrInfo        = "info"
	attrDescription = "description"
)

// groupTypeSecurity is the security bit of groupType; a group without it is
// a distribution list.
const groupTypeSecurity = 0x80000000

// groupPageSize is the paged search's page size for ListGroups.
const groupPageSize = 500

// DepartmentsPrefix starts the one value of a group's info attribute that
// tags it with departments (spec 0013, AC-3).
const DepartmentsPrefix = "noord-departments: "

// ErrAlreadyMember means AddMember found the user already in the group.
var ErrAlreadyMember = errors.New("ldap: already a member of the group")

var groupAttrs = []string{"sAMAccountName", attrDescription, attrGroupType, attrManagedBy, attrInfo}

// GroupInfo is a group as the access request sees it (spec 0013).
type GroupInfo struct {
	DN          string
	Name        string // sAMAccountName, AD's own spelling
	Description string
	Security    bool     // groupType has the security bit
	ManagedBy   string   // the owner's DN, empty when unset
	Departments []string // from info by ParseDepartments; none when it does not parse
}

// ListGroups reads every group under the OUs (relative to the base DN), one
// paged search per OU. Filtering by department, type, name, or fence is the
// caller's, so no user text ever enters this filter.
func (c *Client) ListGroups(ctx context.Context, ous []string) ([]GroupInfo, error) {
	var groups []GroupInfo
	err := c.session(ctx, func(conn *goldap.Conn) error {
		for _, ou := range ous {
			res, err := conn.SearchWithPaging(goldap.NewSearchRequest(ou+","+c.cfg.BaseDN, goldap.ScopeWholeSubtree,
				goldap.NeverDerefAliases, 0, 0, false, "(objectClass=group)", groupAttrs, nil), groupPageSize)
			if err != nil {
				return fmt.Errorf("list groups: %w", err)
			}
			for _, e := range res.Entries {
				groups = append(groups, toGroupInfo(e))
			}
		}
		return nil
	})
	return groups, err
}

// GroupByName reads the one group whose sAMAccountName is name, searched
// from the base DN; ok is false when there is none. More than one match is
// ErrTooManyMatches.
func (c *Client) GroupByName(ctx context.Context, name string) (g GroupInfo, ok bool, err error) {
	err = c.session(ctx, func(conn *goldap.Conn) error {
		filter := "(&(objectClass=group)(sAMAccountName=" + goldap.EscapeFilter(name) + "))"
		res, err := conn.Search(goldap.NewSearchRequest(c.cfg.BaseDN, goldap.ScopeWholeSubtree, goldap.NeverDerefAliases,
			2, 0, false, filter, groupAttrs, nil))
		if goldap.IsErrorWithCode(err, goldap.LDAPResultSizeLimitExceeded) || (err == nil && len(res.Entries) > 1) {
			return ErrTooManyMatches
		}
		if err != nil {
			return fmt.Errorf("search group by name: %w", err)
		}
		if len(res.Entries) == 1 {
			g, ok = toGroupInfo(res.Entries[0]), true
		}
		return nil
	})
	return g, ok, err
}

// GroupNestedInto lists, sorted, the names of the security groups under the
// OUs that hold the group at groupDN at any depth of nesting: what an add to
// it also gives (spec 0013, AC-10). A distribution list gives no access, so
// it is left out. One in chain search per OU.
func (c *Client) GroupNestedInto(ctx context.Context, groupDN string, ous []string) ([]string, error) {
	return c.securityGroupsHolding(ctx, groupDN, ous)
}

// UserGroups lists, sorted, the names of the security groups under the OUs
// that hold the user at userDN at any depth of nesting: a search's
// requester_is_member for every group in one read (spec 0013, AC-2).
func (c *Client) UserGroups(ctx context.Context, userDN string, ous []string) ([]string, error) {
	return c.securityGroupsHolding(ctx, userDN, ous)
}

func (c *Client) securityGroupsHolding(ctx context.Context, memberDN string, ous []string) ([]string, error) {
	var names []string
	err := c.session(ctx, func(conn *goldap.Conn) error {
		filter := fmt.Sprintf("(&(objectClass=group)(%s:%s:=%d)(member:%s:=%s))",
			attrGroupType, ruleBitAnd, groupTypeSecurity, ruleInChain, goldap.EscapeFilter(memberDN))
		for _, ou := range ous {
			res, err := conn.SearchWithPaging(goldap.NewSearchRequest(ou+","+c.cfg.BaseDN, goldap.ScopeWholeSubtree,
				goldap.NeverDerefAliases, 0, 0, false, filter, []string{"sAMAccountName"}, nil), groupPageSize)
			if err != nil {
				return fmt.Errorf("search nesting parents: %w", err)
			}
			for _, e := range res.Entries {
				names = append(names, e.GetAttributeValue("sAMAccountName"))
			}
		}
		return nil
	})
	slices.Sort(names)
	return slices.Compact(names), err
}

// GroupsNestedUnder lists, sorted, the names of the groups under the OUs
// that are members, at any depth of nesting, of any group in parents (DNs):
// the fence's nesting check for many groups in one read (spec 0013, AC-2).
// No parents reads nothing.
func (c *Client) GroupsNestedUnder(ctx context.Context, parents, ous []string) ([]string, error) {
	if len(parents) == 0 {
		return nil, nil
	}
	var names []string
	err := c.session(ctx, func(conn *goldap.Conn) error {
		filter := "(&(objectClass=group)(|"
		for _, p := range parents {
			filter += fmt.Sprintf("(memberOf:%s:=%s)", ruleInChain, goldap.EscapeFilter(p))
		}
		filter += "))"
		for _, ou := range ous {
			res, err := conn.SearchWithPaging(goldap.NewSearchRequest(ou+","+c.cfg.BaseDN, goldap.ScopeWholeSubtree,
				goldap.NeverDerefAliases, 0, 0, false, filter, []string{"sAMAccountName"}, nil), groupPageSize)
			if err != nil {
				return fmt.Errorf("search nested groups: %w", err)
			}
			for _, e := range res.Entries {
				names = append(names, e.GetAttributeValue("sAMAccountName"))
			}
		}
		return nil
	})
	slices.Sort(names)
	return slices.Compact(names), err
}

// GroupHasMember reports whether the user at userDN is a direct member of
// the group at groupDN: the read back after an add (spec 0013, AC-14).
func (c *Client) GroupHasMember(ctx context.Context, groupDN, userDN string) (bool, error) {
	var found bool
	err := c.session(ctx, func(conn *goldap.Conn) error {
		filter := "(" + attrMember + "=" + goldap.EscapeFilter(userDN) + ")"
		res, err := conn.Search(goldap.NewSearchRequest(groupDN, goldap.ScopeBaseObject, goldap.NeverDerefAliases,
			1, 0, false, filter, []string{"1.1"}, nil))
		if err != nil {
			return fmt.Errorf("read group members: %w", err)
		}
		found = len(res.Entries) > 0
		return nil
	})
	return found, err
}

// ParseDepartments reads a group's info attribute (spec 0013, AC-3). Only
// exactly DepartmentsPrefix followed by names joined with ", " counts; each
// name is trimmed and kept as written. Anything else, or an empty name,
// returns none, so a broken tag hides the group (fail closed).
func ParseDepartments(info string) []string {
	rest, ok := strings.CutPrefix(info, DepartmentsPrefix)
	if !ok {
		return nil
	}
	var out []string
	for name := range strings.SplitSeq(rest, ",") {
		name = strings.TrimSpace(name)
		if name == "" {
			return nil
		}
		out = append(out, name)
	}
	return out
}

func toGroupInfo(e *goldap.Entry) GroupInfo {
	gt, _ := strconv.ParseInt(e.GetAttributeValue(attrGroupType), 10, 64) // a missing type reads as not security
	g := GroupInfo{
		DN:          e.DN,
		Name:        e.GetAttributeValue("sAMAccountName"),
		Description: e.GetAttributeValue(attrDescription),
		Security:    uint32(gt)&groupTypeSecurity != 0, //nolint:gosec // G115: groupType is a signed 32 bit flag word
		ManagedBy:   e.GetAttributeValue(attrManagedBy),
	}
	// A multi valued info is not the tag's form.
	if infos := e.GetAttributeValues(attrInfo); len(infos) == 1 {
		g.Departments = ParseDepartments(infos[0])
	}
	return g
}

// AddMember adds the user at userDN to the group at groupDN, as
// svc-itsupport: the access request's one write (spec 0013, AC-14). A user
// already in the group is ErrAlreadyMember (LDAP 20, or 68 as Windows AD
// answers).
func (w *Writer) AddMember(ctx context.Context, groupDN, userDN string) error {
	return session(ctx, w.cfg, func(conn *goldap.Conn) error {
		req := goldap.NewModifyRequest(groupDN, nil)
		req.Add(attrMember, []string{userDN})
		err := conn.Modify(req)
		if goldap.IsErrorAnyOf(err, goldap.LDAPResultAttributeOrValueExists, goldap.LDAPResultEntryAlreadyExists) {
			return ErrAlreadyMember
		}
		if err != nil {
			return fmt.Errorf("add group member: %w", err)
		}
		return nil
	})
}
