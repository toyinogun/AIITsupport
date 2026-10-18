package entra

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/toyinogun/AIITsupport/internal/graph"
)

// Result counts what a run changed. Writes is every successful Graph write.
type Result struct {
	Created, Updated, Restored, Deleted int
	PhonesSet                           int
	ManagersSet, ManagersCleared        int
	ManagersSkipped                     int
	Writes                              int
}

// Syncer applies plans with the seed app's client.
type Syncer struct {
	Client *graph.Client
	Logger *slog.Logger
	// Password returns the password a new user gets at create: the demo
	// password for demo users, a fresh random one for everyone else.
	Password func(username string) string
}

// Options are one run's inputs besides the AD scope.
type Options struct {
	Domain      string
	Phones      map[string]string
	DeleteLimit int
	Only        string
}

const (
	usersListPath   = "/users"
	deletedListPath = "/directory/deletedItems/microsoft.graph.user"
	deletedSelect   = "id,mailNickname,userPrincipalName,employeeType"
)

// Run reads Entra, plans against the AD scope, and applies the plan. A plan
// refused by a guard returns before any write.
func (s Syncer) Run(ctx context.Context, scope []ADUser, opts Options) (Result, error) {
	state, err := s.ReadState(ctx, scope, opts.Only)
	if err != nil {
		return Result{}, err
	}
	plan, err := MakePlan(Inputs{
		Scope: scope, Entra: state, Domain: opts.Domain, Phones: opts.Phones,
		DeleteLimit: opts.DeleteLimit, Only: opts.Only,
	})
	if err != nil {
		return Result{}, err
	}
	for _, skip := range plan.Skipped {
		if skip.Reason == ReasonNotInEntra {
			s.Logger.InfoContext(ctx, ReasonNotInEntra, "user", skip.Username, "manager", skip.Manager)
			continue
		}
		s.Logger.InfoContext(ctx, ReasonNotInScope, "user", skip.Username, "manager", skip.Manager)
	}
	r := run{Syncer: s, ids: knownIDs(state), res: Result{ManagersSkipped: len(plan.Skipped)}}
	for _, a := range plan.Actions {
		if err := r.apply(ctx, a); err != nil {
			return r.res, fmt.Errorf("%s %s: %w", a.Kind, a.Username, err)
		}
	}
	return r.res, nil
}

// ReadState reads every live and deleted user, then the phone methods of
// the managed users in scope (with --only, that one user).
func (s Syncer) ReadState(ctx context.Context, scope []ADUser, only string) (State, error) {
	users, err := graph.ListAll[graph.User](ctx, s.Client,
		usersListPath+graph.Query("$select", graph.UserSelect, "$expand", "manager($select=id)"))
	if err != nil {
		return State{}, fmt.Errorf("list Entra users: %w", err)
	}
	deleted, err := graph.ListAll[graph.DeletedUser](ctx, s.Client, deletedListPath+graph.Query("$select", deletedSelect))
	if err != nil {
		return State{}, fmt.Errorf("list deleted Entra users: %w", err)
	}
	wanted := map[string]bool{}
	for _, u := range scope {
		if only == "" || u.Username == only {
			wanted[strings.ToLower(u.Username)] = true
		}
	}
	phones := map[string][]graph.PhoneMethod{}
	live := make([]graph.User, 0, len(users))
	for _, u := range users {
		mu, err := graph.Managed(u)
		if !wanted[strings.ToLower(u.MailNickname)] || err != nil {
			live = append(live, u) // unmanaged: the plan's guard reports any clash
			continue
		}
		methods, err := s.Client.PhoneMethods(ctx, mu)
		if graph.IsNotFound(err) {
			// Graph still listed a user that was just deleted (replication
			// lag, AC-12). Treat it as gone for this run.
			s.Logger.InfoContext(ctx, "listed user is gone, skipping it this run", "user", u.MailNickname)
			continue
		}
		if err != nil {
			return State{}, err
		}
		phones[u.ID] = methods
		live = append(live, u)
	}
	return State{Users: live, Deleted: deleted, Phones: phones}, nil
}

func knownIDs(s State) map[string]string {
	ids := map[string]string{}
	for _, u := range s.Deleted {
		if u.EmployeeType == graph.ManagedMarker {
			ids[strings.ToLower(u.MailNickname)] = u.ID
		}
	}
	for _, u := range s.Users {
		if u.EmployeeType == graph.ManagedMarker {
			ids[strings.ToLower(u.MailNickname)] = u.ID
		}
	}
	return ids
}

// run is one sync's execution state: the Entra id of every managed user,
// filled in as creates land, and the counts.
type run struct {
	Syncer
	ids map[string]string
	// unknownPhones are users resolved through a create conflict, whose
	// phone methods must be read before writing.
	unknownPhones map[string]bool
	res           Result
}

func (r *run) apply(ctx context.Context, a Action) error {
	switch a.Kind {
	case Restore:
		return r.restore(ctx, a.Username, a.ID, a.Fields)
	case Create:
		return r.create(ctx, a)
	case Update:
		if err := r.write(ctx, graph.Request{Method: http.MethodPatch, Path: graph.UserPath(a.ID), Body: a.Fields}); err != nil {
			return err
		}
		r.res.Updated++
		r.Logger.DebugContext(ctx, "user updated", "user", a.Username, "fields", strings.Join(sortedKeys(a.Fields), ","))
		return nil
	case Delete:
		if err := r.write(ctx, graph.Request{Method: http.MethodDelete, Path: graph.UserPath(a.ID)}); err != nil {
			return err
		}
		r.res.Deleted++
		r.Logger.InfoContext(ctx, "user deleted into the recycle bin", "user", a.Username)
		return nil
	case SetPhone:
		return r.phone(ctx, a)
	case SetManager:
		return r.setManager(ctx, a)
	case ClearManager:
		return r.clearManager(ctx, a)
	}
	return fmt.Errorf("unknown action %d", a.Kind)
}

func (r *run) write(ctx context.Context, req graph.Request) error {
	if err := r.Client.Do(ctx, req); err != nil {
		return err
	}
	r.res.Writes++
	return nil
}

func (r *run) restore(ctx context.Context, username, id string, fields map[string]any) error {
	path := "/directory/deletedItems/" + url.PathEscape(id) + "/restore"
	if err := r.write(ctx, graph.Request{Method: http.MethodPost, Path: path}); err != nil {
		return err
	}
	if err := r.write(ctx, graph.Request{Method: http.MethodPatch, Path: graph.UserPath(id), Body: fields, RetryNotFound: true}); err != nil {
		return fmt.Errorf("update after restore: %w", err)
	}
	r.ids[strings.ToLower(username)] = id
	r.res.Restored++
	r.Logger.InfoContext(ctx, "user restored from the recycle bin", "user", username)
	return nil
}

func (r *run) create(ctx context.Context, a Action) error {
	body := map[string]any{}
	for k, v := range a.Fields {
		if v != nil {
			body[k] = v
		}
	}
	body["passwordProfile"] = map[string]any{
		"password":                      r.Password(a.Username),
		"forceChangePasswordNextSignIn": false,
	}
	var created graph.ObjectRef
	err := r.Client.Do(ctx, graph.Request{Method: http.MethodPost, Path: usersListPath, Body: body, Out: &created})
	if graph.IsConflict(err) {
		return r.resolveConflict(ctx, a, err)
	}
	if err != nil {
		return err
	}
	r.res.Writes++
	r.ids[strings.ToLower(a.Username)] = created.ID
	r.res.Created++
	r.Logger.DebugContext(ctx, "user created", "user", a.Username)
	return nil
}

// resolveConflict handles a create that clashed because the listing was
// stale: a managed holder continues as update or restore, an unmanaged one
// stops the run (AC-7, AC-12).
func (r *run) resolveConflict(ctx context.Context, a Action, conflict error) error {
	live, err := r.Client.UsersByNickname(ctx, a.Username)
	if err != nil {
		return err
	}
	if len(live) > 1 {
		return fmt.Errorf("create clashed and %d Entra users share mailNickname %s: %w", len(live), a.Username, conflict)
	}
	if len(live) == 1 {
		u, err := graph.Managed(live[0])
		if err != nil {
			return fmt.Errorf("create clashed with unmanaged Entra user %s: %w", live[0].UserPrincipalName, err)
		}
		if err := r.write(ctx, graph.Request{Method: http.MethodPatch, Path: graph.UserPath(u.ID()), Body: a.Fields}); err != nil {
			return err
		}
		r.adopt(a.Username, u.ID())
		r.res.Updated++
		return nil
	}
	deleted, err := graph.ListAll[graph.DeletedUser](ctx, r.Client, deletedListPath+graph.Query("$select", deletedSelect))
	if err != nil {
		return err
	}
	upn := strings.ToLower(fmt.Sprint(a.Fields["userPrincipalName"]))
	for _, d := range deleted {
		if !strings.EqualFold(d.MailNickname, a.Username) && strings.ToLower(d.UserPrincipalName) != upn {
			continue
		}
		if d.EmployeeType != graph.ManagedMarker {
			return fmt.Errorf("create clashed with unmanaged Entra user %s in the recycle bin: %w", d.UserPrincipalName, graph.ErrNotManaged)
		}
		if err := r.restore(ctx, a.Username, d.ID, a.Fields); err != nil {
			return err
		}
		r.adopt(a.Username, d.ID)
		return nil
	}
	return conflict
}

func (r *run) adopt(username, id string) {
	key := strings.ToLower(username)
	r.ids[key] = id
	if r.unknownPhones == nil {
		r.unknownPhones = map[string]bool{}
	}
	r.unknownPhones[key] = true
}

// phone puts the expected mobile number in place; other phone types and
// other method types are never touched (AC-5).
func (r *run) phone(ctx context.Context, a Action) error {
	id := r.ids[strings.ToLower(a.Username)]
	if id == "" {
		return errors.New("no Entra id after create")
	}
	methodID := a.PhoneMethodID
	if a.PhoneUnknown || r.unknownPhones[strings.ToLower(a.Username)] {
		current, done, err := r.currentMobile(ctx, id, a.Phone)
		if err != nil || done {
			return err
		}
		methodID = current
	}
	if methodID == "" {
		err := r.write(ctx, graph.Request{
			Method: http.MethodPost, Path: phonesPath(id), RetryNotFound: true,
			Body: map[string]string{"phoneNumber": a.Phone, "phoneType": graph.PhoneMobile},
		})
		if !graph.IsConflict(err) {
			return r.phoneSet(ctx, a.Username, err)
		}
		current, done, err := r.currentMobile(ctx, id, a.Phone)
		if err != nil || done {
			return err
		}
		methodID = current
	}
	err := r.write(ctx, graph.Request{
		Method: http.MethodPatch, Path: phonesPath(id) + "/" + url.PathEscape(methodID), RetryNotFound: true,
		Body: map[string]string{"phoneNumber": a.Phone},
	})
	return r.phoneSet(ctx, a.Username, err)
}

func (r *run) phoneSet(ctx context.Context, username string, err error) error {
	if err != nil {
		return err
	}
	r.res.PhonesSet++
	r.Logger.DebugContext(ctx, "phone set", "user", username)
	return nil
}

// currentMobile reads a user's phone methods: the mobile method id to
// PATCH, or done when the number is already right.
func (r *run) currentMobile(ctx context.Context, id, want string) (string, bool, error) {
	var page struct {
		Value []graph.PhoneMethod `json:"value"`
	}
	if err := r.Client.Do(ctx, graph.Request{Method: http.MethodGet, Path: phonesPath(id), Out: &page, RetryNotFound: true}); err != nil {
		return "", false, fmt.Errorf("read phone methods: %w", err)
	}
	for _, m := range page.Value {
		if m.PhoneType == graph.PhoneMobile {
			return m.ID, samePhone(m.PhoneNumber, want), nil
		}
	}
	return "", false, nil
}

func (r *run) setManager(ctx context.Context, a Action) error {
	id := r.ids[strings.ToLower(a.Username)]
	managerID := r.ids[strings.ToLower(a.Manager)]
	if id == "" || managerID == "" {
		return fmt.Errorf("no Entra id for %s or its manager %s", a.Username, a.Manager)
	}
	body := map[string]string{"@odata.id": r.Client.Base() + graph.UserPath(managerID)}
	if err := r.write(ctx, graph.Request{Method: http.MethodPut, Path: graph.UserPath(id) + "/manager/$ref", Body: body, RetryNotFound: true}); err != nil {
		return err
	}
	r.res.ManagersSet++
	return nil
}

// clearManager removes a manager link; one already gone counts as done.
func (r *run) clearManager(ctx context.Context, a Action) error {
	id := r.ids[strings.ToLower(a.Username)]
	err := r.Client.Do(ctx, graph.Request{Method: http.MethodDelete, Path: graph.UserPath(id) + "/manager/$ref"})
	if graph.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	r.res.Writes++
	r.res.ManagersCleared++
	return nil
}

func phonesPath(id string) string { return graph.UserPath(id) + "/authentication/phoneMethods" }

func sortedKeys(m map[string]any) []string { return slices.Sorted(maps.Keys(m)) }
