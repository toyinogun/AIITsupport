package evalreset

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/go-ldap/ldap/v3"

	"github.com/toyinogun/AIITsupport/internal/graph/graphtest"
	noordldap "github.com/toyinogun/AIITsupport/internal/ldap"
	"github.com/toyinogun/AIITsupport/internal/seed/directory"
	"github.com/toyinogun/AIITsupport/internal/zammad"
)

// ErrDirty means the throwaway environment differs from the seed after an
// attempt was undone (spec 0018, AC-7). The error text lists the differences.
var ErrDirty = errors.New("the environment differs from the seed")

const ticketPage = 100

// Env is the throwaway environment the harness started: the directory, the
// helpdesk (read and delete as the seed admin), and the fake Entra.
type Env struct {
	Dir   Directory
	Model directory.Model
	// Zammad is the seed admin's client; the assistant token cannot delete.
	Zammad *zammad.Client
	Graph  *graphtest.Fake
	// EntraIDs maps a username to its fake object id.
	EntraIDs map[string]string
}

// Snapshot is the outside state an attempt can change, read at one moment.
type Snapshot struct {
	// Locked maps a username to its AD lock flag.
	Locked map[string]bool
	// Members maps a group name to its sorted, lower case member DNs.
	Members map[string][]string
	// MaxTicket is the highest Zammad ticket id.
	MaxTicket int
	// Authenticators and Phones are the fake Entra MFA methods per username.
	Authenticators map[string][]string
	Phones         map[string][]graphtest.Phone
}

// Take reads the outside state.
func Take(ctx context.Context, e Env) (Snapshot, error) {
	s := Snapshot{Locked: map[string]bool{}, Members: map[string][]string{}}
	if err := e.readDirectory(ctx, &s); err != nil {
		return Snapshot{}, err
	}
	tickets, err := allTickets(ctx, e.Zammad)
	if err != nil {
		return Snapshot{}, err
	}
	for _, t := range tickets {
		s.MaxTicket = max(s.MaxTicket, t.ID)
	}
	s.Authenticators, s.Phones = map[string][]string{}, map[string][]graphtest.Phone{}
	for name, id := range e.EntraIDs {
		s.Authenticators[name] = sorted(e.Graph.Authenticators(id))
		s.Phones[name] = e.Graph.Phones(id)
	}
	return s, nil
}

func (e Env) readDirectory(ctx context.Context, s *Snapshot) error {
	admin, err := e.Dir.dialer().WaitAndBind(ctx, e.Dir.AdminDN, e.Dir.AdminPassword, bindWait)
	if err != nil {
		return fmt.Errorf("bind to the throwaway directory: %w", err)
	}
	defer admin.Close()
	base, err := noordldap.BaseDN(admin)
	if err != nil {
		return err
	}
	for _, u := range e.Model.Users {
		dn, _ := e.Model.UserDN(base, u.Username)
		st, err := noordldap.State(admin, dn)
		if err != nil {
			return err
		}
		s.Locked[u.Username] = st.Locked
	}
	for _, g := range e.Model.Groups {
		dn, _ := e.Model.GroupDN(base, g.Name)
		entry, err := noordldap.ReadOne(admin, dn, "member")
		if err != nil {
			return err
		}
		var members []string
		if entry != nil {
			members = entry.GetAttributeValues("member")
		}
		s.Members[g.Name] = lowered(members)
	}
	return nil
}

func allTickets(ctx context.Context, c *zammad.Client) ([]zammad.Ticket, error) {
	var all []zammad.Ticket
	for page := 1; ; page++ {
		batch, err := c.ListTickets(ctx, page, ticketPage)
		if err != nil {
			return nil, fmt.Errorf("list tickets: %w", err)
		}
		all = append(all, batch...)
		if len(batch) < ticketPage {
			return all, nil
		}
	}
}

// Undo puts the outside state back to base: locks and group members in AD,
// tickets an attempt left in Zammad, and the fake Entra's MFA methods.
func Undo(ctx context.Context, e Env, base Snapshot) error {
	now, err := Take(ctx, e)
	if err != nil {
		return err
	}
	return errors.Join(
		e.undoDirectory(ctx, base, now),
		e.undoTickets(ctx, base),
		e.undoGraph(base, now),
	)
}

func (e Env) undoDirectory(ctx context.Context, base, now Snapshot) error {
	lockDiff, memberDiff := diffLocks(base, now), diffMembers(base, now)
	if len(lockDiff) == 0 && len(memberDiff) == 0 {
		return nil
	}
	admin, err := e.Dir.dialer().WaitAndBind(ctx, e.Dir.AdminDN, e.Dir.AdminPassword, bindWait)
	if err != nil {
		return fmt.Errorf("bind to the throwaway directory: %w", err)
	}
	defer admin.Close()
	adminBase, err := noordldap.BaseDN(admin)
	if err != nil {
		return err
	}
	var errs []error
	for _, user := range lockDiff {
		errs = append(errs, e.fixLock(ctx, admin, adminBase, user, base.Locked[user]))
	}
	for _, group := range memberDiff {
		errs = append(errs, e.fixMembers(admin, adminBase, group, base.Members[group], now.Members[group]))
	}
	return errors.Join(errs...)
}

func (e Env) fixLock(ctx context.Context, admin ldap.Client, base, user string, wantLocked bool) error {
	dn, _ := e.Model.UserDN(base, user)
	if wantLocked {
		threshold, err := directory.LockoutThreshold(admin, base)
		if err != nil {
			return err
		}
		return directory.LockAndConfirm(ctx, admin, e.Dir.dialer(), dn, directory.WrongPassword(e.Dir.UserPassword), threshold)
	}
	req := ldap.NewModifyRequest(dn, nil)
	req.Replace("lockoutTime", []string{"0"})
	if err := admin.Modify(req); err != nil {
		return fmt.Errorf("unlock %s: %w", user, err)
	}
	return nil
}

func (e Env) fixMembers(admin ldap.Client, base, group string, want, have []string) error {
	dn, _ := e.Model.GroupDN(base, group)
	// The DNs read back are lower case; the groups' real members are found by
	// their original spelling through the model.
	add, remove := difference(want, have), difference(have, want)
	if len(add) > 0 {
		req := ldap.NewModifyRequest(dn, nil)
		req.Add("member", add)
		if err := admin.Modify(req); err != nil {
			return fmt.Errorf("restore members of %s: %w", group, err)
		}
	}
	if len(remove) > 0 {
		req := ldap.NewModifyRequest(dn, nil)
		req.Delete("member", remove)
		if err := admin.Modify(req); err != nil {
			return fmt.Errorf("remove members of %s: %w", group, err)
		}
	}
	return nil
}

func (e Env) undoTickets(ctx context.Context, base Snapshot) error {
	tickets, err := allTickets(ctx, e.Zammad)
	if err != nil {
		return err
	}
	var errs []error
	for _, t := range tickets {
		if t.ID > base.MaxTicket {
			if err := e.Zammad.DeleteTicket(ctx, t.ID); err != nil {
				errs = append(errs, fmt.Errorf("delete ticket %d: %w", t.ID, err))
			}
		}
	}
	return errors.Join(errs...)
}

func (e Env) undoGraph(base, now Snapshot) error {
	for _, user := range diffGraph(base, now) {
		id := e.EntraIDs[user]
		e.Graph.SetAuthenticators(id, base.Authenticators[user]...)
		e.Graph.SetPhones(id, base.Phones[user])
	}
	return nil
}

// Verify reads the state again and returns ErrDirty, with every difference
// named, when it is not the baseline.
func Verify(ctx context.Context, e Env, base Snapshot) error {
	now, err := Take(ctx, e)
	if err != nil {
		return err
	}
	if diffs := Diff(base, now); len(diffs) > 0 {
		return fmt.Errorf("%w: %s", ErrDirty, strings.Join(diffs, "; "))
	}
	return nil
}

// Diff names every difference between two snapshots; it is empty when they match.
func Diff(base, now Snapshot) []string {
	var out []string
	for _, u := range diffLocks(base, now) {
		out = append(out, fmt.Sprintf("lock state of %s", u))
	}
	for _, g := range diffMembers(base, now) {
		out = append(out, fmt.Sprintf("members of group %s", g))
	}
	if now.MaxTicket != base.MaxTicket {
		out = append(out, fmt.Sprintf("helpdesk tickets (highest id %d, was %d)", now.MaxTicket, base.MaxTicket))
	}
	for _, u := range diffGraph(base, now) {
		out = append(out, fmt.Sprintf("MFA methods of %s", u))
	}
	return out
}

func diffLocks(base, now Snapshot) []string {
	var out []string
	for u, was := range base.Locked {
		if now.Locked[u] != was {
			out = append(out, u)
		}
	}
	slices.Sort(out)
	return out
}

func diffMembers(base, now Snapshot) []string {
	var out []string
	for g, was := range base.Members {
		if !slices.Equal(was, now.Members[g]) {
			out = append(out, g)
		}
	}
	slices.Sort(out)
	return out
}

func diffGraph(base, now Snapshot) []string {
	var out []string
	for u, was := range base.Authenticators {
		if !slices.Equal(was, now.Authenticators[u]) || !slices.Equal(base.Phones[u], now.Phones[u]) {
			out = append(out, u)
		}
	}
	slices.Sort(out)
	return out
}

func lowered(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		out = append(out, strings.ToLower(s))
	}
	slices.Sort(out)
	return out
}

func sorted(in []string) []string {
	out := slices.Clone(in)
	slices.Sort(out)
	return out
}

// difference returns the entries of a that are not in b.
func difference(a, b []string) []string {
	var out []string
	for _, s := range a {
		if !slices.Contains(b, s) {
			out = append(out, s)
		}
	}
	return out
}
