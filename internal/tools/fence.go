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

// Fence guards group targets (spec 0007, AC-13). #9 has no tool that takes a
// group; #15's group tools call CheckGroup.
type Fence struct {
	dir      Directory
	groupOUs []string
	base     string
	denied   []string // TOOL_DENIED_GROUPS
	logger   *slog.Logger
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
