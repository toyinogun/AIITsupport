package entra

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/toyinogun/AIITsupport/internal/graph"
)

// Constants every managed user carries (spec 0005 data model).
const (
	CompanyName   = "Noord Logistics"
	UsageLocation = "NL"
)

// Kind is one step of a reconcile plan. The plan runs kinds in this order.
type Kind int

// Plan kinds, in run order.
const (
	Restore Kind = iota
	Create
	Update
	Delete
	SetPhone
	SetManager
	ClearManager
)

var kindNames = [...]string{"restore", "create", "update", "delete", "set phone", "set manager", "clear manager"}

func (k Kind) String() string { return kindNames[k] }

// Action is one planned Graph change for one user.
type Action struct {
	Kind     Kind
	Username string
	// ID is the Entra id when the user already exists (live or deleted).
	ID string
	// Fields are every mapped field for create and restore, and only the
	// changed fields for update.
	Fields map[string]any
	// Phone is the expected mobile number; PhoneMethodID the mobile method
	// to PATCH, empty to POST. PhoneUnknown means the user was restored, so
	// its methods are read again before writing.
	Phone         string
	PhoneMethodID string
	PhoneUnknown  bool
	// Manager is the manager's username for SetManager.
	Manager string
}

// Skip is a manager link the plan left out, and why.
type Skip struct {
	Username, Manager, Reason string
}

// Plan is the ordered list of actions plus the managers it skipped.
type Plan struct {
	Actions []Action
	Skipped []Skip
}

// Count returns how many actions of a kind the plan holds.
func (p Plan) Count(k Kind) int {
	n := 0
	for _, a := range p.Actions {
		if a.Kind == k {
			n++
		}
	}
	return n
}

// State is what Entra holds at the start of a run.
type State struct {
	// Users is every live user, managed or not, with its manager expanded.
	Users []graph.User
	// Deleted is every user in the recycle bin.
	Deleted []graph.DeletedUser
	// Phones holds the phone methods of the managed users in scope, by id.
	Phones map[string][]graph.PhoneMethod
}

// Inputs are everything a plan is made from.
type Inputs struct {
	Scope       []ADUser
	Entra       State
	Domain      string
	Phones      map[string]string // expected mobile number by username
	DeleteLimit int
	// Only restricts the plan to one user: no deletes, no delete limit, the
	// guards on that user only, and a manager set only if the manager
	// already exists in Entra.
	Only string
}

// Plan reasons for skipped managers.
const (
	ReasonNotInScope = "manager not in scope"
	ReasonNotInEntra = "manager not in Entra yet"
)

// MakePlan diffs AD against Entra. Every guard runs here, so a refused plan
// means no Graph write happened.
func MakePlan(in Inputs) (Plan, error) {
	scope, err := planScope(in.Scope, in.Only)
	if err != nil {
		return Plan{}, err
	}
	idx := indexEntra(in.Entra)
	if err := guardEntra(scope, idx, in.Domain); err != nil {
		return Plan{}, err
	}

	p := planner{in: in, idx: idx, inScope: map[string]bool{}, synced: map[string]bool{}, fresh: map[string]bool{}, restored: map[string]bool{}}
	for _, u := range scope {
		p.inScope[strings.ToLower(u.Username)] = true
	}
	for _, u := range in.Scope {
		if !u.Outside {
			p.synced[strings.ToLower(u.Username)] = true
		}
	}
	for _, u := range scope {
		if err := p.user(u); err != nil {
			return Plan{}, err
		}
	}
	if in.Only == "" {
		if err := p.deletes(); err != nil {
			return Plan{}, err
		}
	}
	for _, u := range scope {
		p.manager(u)
	}
	slices.SortStableFunc(p.plan.Actions, func(a, b Action) int { return int(a.Kind) - int(b.Kind) })
	return p.plan, nil
}

// planScope applies the scope guard (AC-3) and the --only filter.
func planScope(scope []ADUser, only string) ([]ADUser, error) {
	if only != "" {
		i := slices.IndexFunc(scope, func(u ADUser) bool { return u.Username == only })
		if i < 0 {
			return nil, fmt.Errorf("%s is not a member of SYNC-Entra-Users", only)
		}
		scope = scope[i : i+1]
	}
	var outside []string
	for _, u := range scope {
		if u.Outside {
			outside = append(outside, u.DN)
		}
	}
	if len(outside) > 0 {
		return nil, fmt.Errorf("SYNC-Entra-Users holds members outside OU=Users,OU=Noord, refusing to sync: %s",
			strings.Join(outside, "; "))
	}
	for _, u := range scope {
		if u.DisplayName == "" || u.GivenName == "" || u.Surname == "" {
			return nil, fmt.Errorf("%s has no displayName, givenName, or sn in AD", u.Username)
		}
	}
	return scope, nil
}

// entraIndex finds users by lower cased mailNickname.
type entraIndex struct {
	managed        map[string][]graph.User
	managedDeleted map[string][]graph.DeletedUser
	unmanaged      []graph.User
	unmanagedGone  []graph.DeletedUser
}

func indexEntra(s State) entraIndex {
	idx := entraIndex{managed: map[string][]graph.User{}, managedDeleted: map[string][]graph.DeletedUser{}}
	for _, u := range s.Users {
		if u.EmployeeType == graph.ManagedMarker {
			key := strings.ToLower(u.MailNickname)
			idx.managed[key] = append(idx.managed[key], u)
		} else {
			idx.unmanaged = append(idx.unmanaged, u)
		}
	}
	for _, u := range s.Deleted {
		if u.EmployeeType == graph.ManagedMarker {
			key := strings.ToLower(u.MailNickname)
			idx.managedDeleted[key] = append(idx.managedDeleted[key], u)
		} else {
			idx.unmanagedGone = append(idx.unmanagedGone, u)
		}
	}
	return idx
}

// guardEntra refuses duplicate managed nicknames and unmanaged users that
// hold an in scope nickname or UPN (AC-7).
func guardEntra(scope []ADUser, idx entraIndex, domain string) error {
	var problems []error
	nicknames := map[string]bool{}
	for _, u := range scope {
		nicknames[strings.ToLower(u.Username)] = true
	}
	for nick := range nicknames {
		if n := len(idx.managed[nick]) + len(idx.managedDeleted[nick]); n > 1 {
			problems = append(problems, fmt.Errorf("%d managed Entra users (live or in the recycle bin) share mailNickname %s", n, nick))
		}
	}
	for _, u := range scope {
		upn := upnFor(u.Username, domain)
		for _, x := range idx.unmanaged {
			if strings.EqualFold(x.MailNickname, u.Username) || strings.EqualFold(x.UserPrincipalName, upn) {
				problems = append(problems, fmt.Errorf("unmanaged Entra user %s holds %s's mailNickname or UPN", x.UserPrincipalName, u.Username))
			}
		}
		for _, x := range idx.unmanagedGone {
			if strings.EqualFold(x.MailNickname, u.Username) || strings.EqualFold(x.UserPrincipalName, upn) {
				problems = append(problems, fmt.Errorf("unmanaged Entra user %s in the recycle bin holds %s's mailNickname or UPN", x.UserPrincipalName, u.Username))
			}
		}
	}
	slices.SortFunc(problems, func(a, b error) int { return strings.Compare(a.Error(), b.Error()) })
	return errors.Join(problems...)
}

type planner struct {
	in  Inputs
	idx entraIndex
	// inScope is what this run plans (one user with --only); synced is the
	// whole of SYNC-Entra-Users, which decides "manager not in scope".
	inScope map[string]bool
	synced  map[string]bool
	// fresh holds the users this run creates or restores, whose manager
	// link is unknown; restored is the subset that may come back with one.
	fresh    map[string]bool
	restored map[string]bool
	plan     Plan
}

func (p *planner) add(a Action) { p.plan.Actions = append(p.plan.Actions, a) }

func (p *planner) user(u ADUser) error {
	want := desired(u, p.in.Domain)
	phone, ok := p.in.Phones[u.Username]
	if !ok {
		return fmt.Errorf("%s has no expected phone number (not a user in the seed file)", u.Username)
	}
	key := strings.ToLower(u.Username)

	if live := p.idx.managed[key]; len(live) == 1 {
		cur := live[0]
		if changed := diff(cur, want); len(changed) > 0 {
			p.add(Action{Kind: Update, Username: u.Username, ID: cur.ID, Fields: changed})
		}
		p.phone(u.Username, cur.ID, phone)
		return nil
	}
	if gone := p.idx.managedDeleted[key]; len(gone) == 1 {
		p.add(Action{Kind: Restore, Username: u.Username, ID: gone[0].ID, Fields: want})
		p.add(Action{Kind: SetPhone, Username: u.Username, ID: gone[0].ID, Phone: phone, PhoneUnknown: true})
		p.fresh[key] = true
		p.restored[key] = true
		return nil
	}
	p.add(Action{Kind: Create, Username: u.Username, Fields: want})
	p.add(Action{Kind: SetPhone, Username: u.Username, Phone: phone})
	p.fresh[key] = true
	return nil
}

// phone plans the mobile method of a live user: POST when missing, PATCH a
// wrong number, nothing when right. Other phone types are never touched.
func (p *planner) phone(username, id, want string) {
	for _, m := range p.in.Entra.Phones[id] {
		if m.PhoneType != graph.PhoneMobile {
			continue
		}
		if !samePhone(m.PhoneNumber, want) {
			p.add(Action{Kind: SetPhone, Username: username, ID: id, Phone: want, PhoneMethodID: m.ID})
		}
		return
	}
	p.add(Action{Kind: SetPhone, Username: username, ID: id, Phone: want})
}

// deletes plans a soft delete for every managed user no longer in scope and
// refuses a plan over the delete limit (AC-7).
func (p *planner) deletes() error {
	var dels []Action
	for _, key := range slices.Sorted(maps.Keys(p.idx.managed)) {
		if p.inScope[key] {
			continue
		}
		for _, u := range p.idx.managed[key] {
			dels = append(dels, Action{Kind: Delete, Username: u.MailNickname, ID: u.ID})
		}
	}
	if len(dels) > p.in.DeleteLimit {
		return fmt.Errorf("the sync would delete %d managed users, more than SEED_ENTRA_DELETE_LIMIT (%d); nothing was written",
			len(dels), p.in.DeleteLimit)
	}
	p.plan.Actions = append(p.plan.Actions, dels...)
	return nil
}

// manager plans the manager link once every user exists (AC-4).
func (p *planner) manager(u ADUser) {
	key := strings.ToLower(u.Username)
	var current string
	if live := p.idx.managed[key]; len(live) == 1 && live[0].Manager != nil {
		current = live[0].Manager.ID
	}
	fresh := p.fresh[key]
	// A restored user may come back with its old link, so it is cleared
	// too; the run treats "no manager to clear" as done.
	clearIfSet := func() {
		if current != "" || p.restored[key] {
			p.add(Action{Kind: ClearManager, Username: u.Username, ID: p.idOf(key)})
		}
	}

	if u.Manager == "" {
		clearIfSet()
		return
	}
	mkey := strings.ToLower(u.Manager)
	if !p.synced[mkey] {
		p.plan.Skipped = append(p.plan.Skipped, Skip{Username: u.Username, Manager: u.Manager, Reason: ReasonNotInScope})
		clearIfSet()
		return
	}
	managerID := p.idOf(mkey)
	if p.in.Only != "" && len(p.idx.managed[mkey]) != 1 {
		p.plan.Skipped = append(p.plan.Skipped, Skip{Username: u.Username, Manager: u.Manager, Reason: ReasonNotInEntra})
		return
	}
	if !fresh && !p.fresh[mkey] && managerID != "" && current == managerID {
		return
	}
	p.add(Action{Kind: SetManager, Username: u.Username, ID: p.idOf(key), Manager: u.Manager})
}

// idOf is the Entra id of a managed user that exists now (live or in the
// recycle bin), or "" for one this run creates.
func (p *planner) idOf(key string) string {
	if live := p.idx.managed[key]; len(live) == 1 {
		return live[0].ID
	}
	if gone := p.idx.managedDeleted[key]; len(gone) == 1 {
		return gone[0].ID
	}
	return ""
}

func upnFor(username, domain string) string { return username + "@" + domain }

// desired is every compared field of the Entra user an AD user maps to.
func desired(u ADUser, domain string) map[string]any {
	return map[string]any{
		"mailNickname":      u.Username,
		"userPrincipalName": upnFor(u.Username, domain),
		"displayName":       u.DisplayName,
		"givenName":         u.GivenName,
		"surname":           u.Surname,
		"department":        nullable(u.Department),
		"jobTitle":          nullable(u.Title),
		"accountEnabled":    !u.Disabled,
		"companyName":       CompanyName,
		"employeeType":      graph.ManagedMarker,
		"usageLocation":     UsageLocation,
	}
}

// nullable maps an AD blank to JSON null.
func nullable(s string) any {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return s
}

// diff returns the desired fields that differ from the current user.
func diff(cur graph.User, want map[string]any) map[string]any {
	current := map[string]any{
		"mailNickname":   cur.MailNickname,
		"displayName":    cur.DisplayName,
		"givenName":      cur.GivenName,
		"surname":        cur.Surname,
		"department":     deref(cur.Department),
		"jobTitle":       deref(cur.JobTitle),
		"accountEnabled": cur.AccountEnabled,
		"companyName":    cur.CompanyName,
		"employeeType":   cur.EmployeeType,
		"usageLocation":  cur.UsageLocation,
	}
	changed := map[string]any{}
	for key, w := range want {
		if key == "userPrincipalName" {
			if !strings.EqualFold(cur.UserPrincipalName, w.(string)) {
				changed[key] = w
			}
			continue
		}
		if current[key] != w {
			changed[key] = w
		}
	}
	return changed
}

func deref(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}
