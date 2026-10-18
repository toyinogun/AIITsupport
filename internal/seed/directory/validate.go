package directory

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

const maxUsernameLen = 20

var usernamePattern = regexp.MustCompile(`^[a-z][a-z0-9.]*[a-z0-9]$`)

// reservedNames already exist in every provisioned domain or come from the
// entrypoint's skeleton, so the seed file may not reuse them.
var reservedNames = []string{
	"administrator", "guest", "krbtgt", "svc-itsupport", "svc-zammad",
	"domain admins", "domain users", "domain guests", "domain computers",
	"domain controllers", "enterprise admins", "schema admins", "administrators",
	"account operators", "server operators", "backup operators", "print operators",
	"group policy creator owners", "dnsadmins", "cert publishers", "protected users",
}

// sAMAccountName may not hold any of these (AD's own rule).
const illegalNameChars = `"/\[]:;|=,+*?<>`

var validKinds = []string{KindDepartment, KindApp, KindShare, KindDistribution, KindPrivileged}

// validate runs every AC-14 rule and returns all problems found.
func validate(m Model) []error {
	v := &validator{m: m}
	v.departments()
	v.names()
	v.users()
	v.managers()
	v.admins()
	v.groups()
	v.groupCycles()
	return v.problems
}

type validator struct {
	m        Model
	problems []error
}

func (v *validator) add(format string, args ...any) {
	v.problems = append(v.problems, fmt.Errorf(format, args...))
}

func (v *validator) departments() {
	if len(v.m.Departments) == 0 {
		v.add("departments is empty")
	}
	seen := map[string]bool{}
	for _, d := range v.m.Departments {
		key := strings.ToLower(d)
		if strings.TrimSpace(d) == "" {
			v.add("departments: an entry is empty")
		}
		if seen[key] {
			v.add("departments: %q is listed twice", d)
		}
		seen[key] = true
	}
}

// names checks that every sAMAccountName (and so every UPN) is unique across
// users, admins, and groups, ignoring case.
func (v *validator) names() {
	owner := map[string]string{}
	for _, r := range reservedNames {
		owner[r] = "a built in or skeleton account"
	}
	claim := func(name, what string) {
		key := strings.ToLower(name)
		if prev, ok := owner[key]; ok {
			v.add("name %q (%s) is already used by %s", name, what, prev)
			return
		}
		owner[key] = what + " " + name
	}
	for _, u := range v.m.Users {
		claim(u.Username, "user")
	}
	for _, a := range v.m.Admins {
		claim(a.Username, "admin")
	}
	for _, g := range v.m.Groups {
		claim(g.Name, "group")
	}
}

func (v *validator) checkUsername(what, name string) {
	switch {
	case len(name) > maxUsernameLen:
		v.add("%s %q is %d characters, the limit is %d", what, name, len(name), maxUsernameLen)
	case !usernamePattern.MatchString(name):
		v.add("%s %q must match %s", what, name, usernamePattern)
	}
}

func (v *validator) users() {
	displayInOU := map[string]string{}
	for _, u := range v.m.Users {
		v.checkUsername("username", u.Username)
		for _, f := range []struct{ name, value string }{
			{"given_name", u.GivenName}, {"surname", u.Surname}, {"department", u.Department}, {"title", u.Title},
		} {
			if strings.TrimSpace(f.value) == "" {
				v.add("user %s: %s is empty", u.Username, f.name)
			}
		}
		if u.Department != "" && !slices.Contains(v.m.Departments, u.Department) {
			v.add("user %s: unknown department %q", u.Username, u.Department)
		}
		key := strings.ToLower(u.Department + "/" + u.DisplayName())
		if prev, ok := displayInOU[key]; ok {
			v.add("users %s and %s share the display name %q in department %s", prev, u.Username, u.DisplayName(), u.Department)
		}
		displayInOU[key] = u.Username
	}
}

// managers checks that every manager exists, exactly one user has none, and
// no chain of managers loops.
func (v *validator) managers() {
	var tops []string
	for _, u := range v.m.Users {
		if u.Manager == "" {
			tops = append(tops, u.Username)
			continue
		}
		if _, ok := v.m.User(u.Manager); !ok {
			v.add("user %s: manager %q does not exist", u.Username, u.Manager)
		}
	}
	if len(v.m.Users) > 0 && len(tops) != 1 {
		v.add("exactly one user may have no manager, found %d: %s", len(tops), strings.Join(tops, ", "))
	}

	for _, u := range v.m.Users {
		chain := []string{u.Username}
		for cur := u.Manager; cur != ""; {
			if cur == u.Username {
				// Report each cycle once, from its alphabetically first member.
				if slices.Min(chain) == u.Username {
					v.add("manager cycle: %s -> %s", strings.Join(chain, " -> "), u.Username)
				}
				break
			}
			if slices.Contains(chain, cur) {
				break // a cycle further up; reported from its own members
			}
			chain = append(chain, cur)
			next, ok := v.m.User(cur)
			if !ok {
				break
			}
			cur = next.Manager
		}
	}
}

func (v *validator) admins() {
	for _, a := range v.m.Admins {
		v.checkUsername("admin username", a.Username)
		if _, ok := v.m.User(a.Person); !ok {
			v.add("admin %s: person %q does not exist", a.Username, a.Person)
		}
	}
}

func (v *validator) groups() {
	for _, g := range v.m.Groups {
		switch {
		case strings.TrimSpace(g.Name) == "":
			v.add("a group has no name")
		case strings.ContainsAny(g.Name, illegalNameChars):
			v.add("group %q: the name may not contain any of %s", g.Name, illegalNameChars)
		}
		if !slices.Contains(validKinds, g.Kind) {
			v.add("group %s: unknown kind %q, want one of %s", g.Name, g.Kind, strings.Join(validKinds, ", "))
		}
		if g.Owner != "" {
			if _, ok := v.m.User(g.Owner); !ok {
				v.add("group %s: owner %q is not a staff user", g.Name, g.Owner)
			}
		}
		if len(g.NestedInto) > 0 && g.Kind != KindPrivileged {
			v.add("group %s: only privileged groups may use nested_into", g.Name)
		}
		for _, b := range g.NestedInto {
			if strings.TrimSpace(b) == "" {
				v.add("group %s: nested_into has an empty entry", g.Name)
			}
		}
		v.groupMembers(g)
	}
}

func (v *validator) groupMembers(g Group) {
	seen := map[string]bool{}
	for _, member := range g.Members {
		if seen[member] {
			v.add("group %s: member %q is listed twice", g.Name, member)
		}
		seen[member] = true

		if member == g.Name {
			v.add("group %s: contains itself", g.Name)
			continue
		}
		if inner, ok := v.m.Group(member); ok {
			if g.Kind == KindPrivileged && inner.Kind != KindPrivileged {
				v.add("group %s: non privileged group %s may not be nested into a privileged group", g.Name, inner.Name)
			}
			continue
		}
		_, isUser := v.m.User(member)
		_, isAdmin := v.m.Admin(member)
		if !isUser && !isAdmin {
			v.add("group %s: member %q does not exist", g.Name, member)
		}
	}
}

// groupCycles finds loops of groups nested inside each other.
func (v *validator) groupCycles() {
	const (
		unvisited = iota
		visiting
		done
	)
	state := map[string]int{}
	var path []string
	var visit func(name string)
	visit = func(name string) {
		state[name] = visiting
		path = append(path, name)
		g, _ := v.m.Group(name)
		for _, member := range g.Members {
			if _, isGroup := v.m.Group(member); !isGroup || member == name {
				continue // self membership is reported by groupMembers
			}
			switch state[member] {
			case visiting:
				start := slices.Index(path, member)
				v.add("group membership cycle: %s -> %s", strings.Join(path[start:], " -> "), member)
			case unvisited:
				visit(member)
			}
		}
		path = path[:len(path)-1]
		state[name] = done
	}
	for _, g := range v.m.Groups {
		if state[g.Name] == unvisited {
			visit(g.Name)
		}
	}
}
