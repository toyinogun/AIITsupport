package tools

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"

	"github.com/toyinogun/AIITsupport/internal/ldap"
)

// FloorDeniedGroups is the code floor: groups no tool may ever change, which
// no setting can remove. Names this directory lacks are skipped.
var FloorDeniedGroups = []string{
	"Domain Admins", "Enterprise Admins", "Schema Admins", "Administrators",
	"Account Operators", "Backup Operators", "Server Operators", "Print Operators",
	"Group Policy Creator Owners", "DnsAdmins", "Cert Publishers", "Key Admins",
	"Enterprise Key Admins",
}

// ErrGroupProtected is the fence's refusal: the group is outside what the
// assistant may change.
var ErrGroupProtected = errors.New("group is protected")

// Fence guards group targets (spec 0007, AC-13). #15's group tools call
// CheckGroup for one group, and group_search AllowedGroups for many.
type Fence struct {
	dir      Directory
	groupOUs []string
	base     string
	denied   []string // TOOL_DENIED_GROUPS
	logger   *slog.Logger
}

// AccountFence guards accounts a reset link may target (spec 0011, AC-3 and
// AC-10): an account that is a member, at any depth, of a code floor group
// or a RESET_DENIED_GROUPS group is protected.
type AccountFence struct {
	dir    Directory
	denied []string // RESET_DENIED_GROUPS
	logger *slog.Logger
}

// NewAccountFence guards with the code floor plus denied.
func NewAccountFence(dir Directory, denied []string, logger *slog.Logger) *AccountFence {
	return &AccountFence{dir: dir, denied: slices.Clone(denied), logger: logger}
}

// CheckAccount returns nil when the user at dn is in no protected group, and
// ErrGroupProtected when it is. A configured name that does not resolve
// fails every check closed and logs denied_group_unresolved. Directory
// errors come back classified.
func (a *AccountFence) CheckAccount(ctx context.Context, dn string) error {
	denied := append(slices.Clone(FloorDeniedGroups), a.denied...)
	found, err := a.dir.GroupsByName(ctx, denied)
	if err != nil {
		return directoryFailure(err)
	}
	groups := make(map[string]ldap.Group, len(found))
	for _, g := range found {
		groups[strings.ToLower(g.Name)] = g
	}
	for _, d := range a.denied {
		if _, ok := groups[strings.ToLower(d)]; !ok {
			a.logger.ErrorContext(ctx, "denied_group_unresolved")
			return ErrGroupProtected
		}
	}
	parents := make([]string, 0, len(groups))
	for _, g := range groups {
		parents = append(parents, g.DN)
	}
	slices.Sort(parents)
	in, err := a.dir.UserInChainMemberOf(ctx, dn, parents)
	if err != nil {
		return directoryFailure(err)
	}
	if in {
		return ErrGroupProtected
	}
	return nil
}

// DeniedName reports whether name is on the denied name list (the code floor
// or TOOL_DENIED_GROUPS), ignoring case. It reads nothing.
func (f *Fence) DeniedName(name string) bool {
	match := func(d string) bool { return strings.EqualFold(d, name) }
	return slices.ContainsFunc(FloorDeniedGroups, match) || slices.ContainsFunc(f.denied, match)
}

// CheckGroup returns nil when a tool may change the group named name (a
// sAMAccountName). It returns ErrGroupProtected when the group does not
// exist, sits outside the allowed group OUs, is denied (the floor or
// TOOL_DENIED_GROUPS), or is nested at any depth into a denied group. A
// configured denied name that does not resolve fails every check closed.
// Directory errors come back classified (directory unavailable or failed).
func (f *Fence) CheckGroup(ctx context.Context, name string) error {
	denied := append(slices.Clone(FloorDeniedGroups), f.denied...)
	if slices.ContainsFunc(denied, func(d string) bool { return strings.EqualFold(d, name) }) {
		return ErrGroupProtected
	}
	found, err := f.dir.GroupsByName(ctx, append([]string{name}, denied...))
	if err != nil {
		return directoryFailure(err)
	}
	// AD names are case insensitive: compare them folded.
	groups := make(map[string]ldap.Group, len(found))
	for _, g := range found {
		groups[strings.ToLower(g.Name)] = g
	}
	for _, d := range f.denied {
		if _, ok := groups[strings.ToLower(d)]; !ok {
			f.logger.ErrorContext(ctx, "denied_group_unresolved")
			return ErrGroupProtected
		}
	}
	target, ok := groups[strings.ToLower(name)]
	if !ok {
		return ErrGroupProtected
	}
	if under, err := ldap.UnderAny(target.DN, f.groupOUs, f.base); err != nil || !under {
		return ErrGroupProtected
	}
	var deniedParents []string
	for _, d := range denied {
		if g, ok := groups[strings.ToLower(d)]; ok {
			deniedParents = append(deniedParents, g.DN)
		}
	}
	nested, err := f.dir.InChainMemberOf(ctx, name, deniedParents)
	if err != nil {
		return directoryFailure(err)
	}
	if nested {
		return ErrGroupProtected
	}
	return nil
}

// AllowedGroups is CheckGroup for many groups at once (spec 0013, AC-2): it
// returns, in their order, the groups a tool may change, with two directory
// reads however many groups there are. A configured denied name that does
// not resolve allows none. Directory errors come back classified.
func (f *Fence) AllowedGroups(ctx context.Context, groups []ldap.GroupInfo) ([]ldap.GroupInfo, error) {
	if len(groups) == 0 {
		return nil, nil
	}
	denied := append(slices.Clone(FloorDeniedGroups), f.denied...)
	found, err := f.dir.GroupsByName(ctx, denied)
	if err != nil {
		return nil, directoryFailure(err)
	}
	resolved := make(map[string]ldap.Group, len(found))
	for _, g := range found {
		resolved[strings.ToLower(g.Name)] = g
	}
	for _, d := range f.denied {
		if _, ok := resolved[strings.ToLower(d)]; !ok {
			f.logger.ErrorContext(ctx, "denied_group_unresolved")
			return nil, nil
		}
	}
	parents := make([]string, 0, len(resolved))
	for _, g := range resolved {
		parents = append(parents, g.DN)
	}
	slices.Sort(parents)
	nested, err := f.dir.GroupsNestedUnder(ctx, parents, f.groupOUs)
	if err != nil {
		return nil, directoryFailure(err)
	}
	isNested := func(name string) bool {
		return slices.ContainsFunc(nested, func(n string) bool { return strings.EqualFold(n, name) })
	}
	var allowed []ldap.GroupInfo
	for _, g := range groups {
		if f.DeniedName(g.Name) || isNested(g.Name) {
			continue
		}
		if under, err := ldap.UnderAny(g.DN, f.groupOUs, f.base); err != nil || !under {
			continue
		}
		allowed = append(allowed, g)
	}
	return allowed, nil
}
