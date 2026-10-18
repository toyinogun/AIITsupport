package entra

import (
	"slices"
	"testing"

	"github.com/toyinogun/AIITsupport/internal/graph"
)

func ptr(s string) *string { return &s }

// managedUser is the Entra user a clean sync leaves for an AD user.
func managedUser(id string, u ADUser) graph.User {
	return graph.User{
		ID: id, MailNickname: u.Username, UserPrincipalName: upnFor(u.Username, testDomain),
		DisplayName: u.DisplayName, GivenName: u.GivenName, Surname: u.Surname,
		Department: ptr(u.Department), JobTitle: ptr(u.Title), AccountEnabled: !u.Disabled,
		CompanyName: CompanyName, EmployeeType: graph.ManagedMarker, UsageLocation: UsageLocation,
	}
}

// covers: AC-2, AC-6. The plan orders actions restore, create, update,
// delete, phone, manager, and an in sync user plans nothing.
func TestMakePlanOrderAndNoop(t *testing.T) {
	boss := ADUser{DN: "b", Username: "boss", DisplayName: "B O", GivenName: "B", Surname: "O", Department: "IT", Title: "Lead"}
	kept := ADUser{DN: "k", Username: "kept", DisplayName: "K P", GivenName: "K", Surname: "P", Department: "IT", Title: "Dev", Manager: "boss"}
	fresh := ADUser{DN: "f", Username: "fresh", DisplayName: "F R", GivenName: "F", Surname: "R", Manager: "boss"}
	back := ADUser{DN: "r", Username: "back", DisplayName: "R B", GivenName: "R", Surname: "B"}

	bossU := managedUser("id-boss", boss)
	keptU := managedUser("id-kept", kept)
	keptU.Manager = &graph.ObjectRef{ID: "id-boss"}
	stale := graph.User{ID: "id-stale", MailNickname: "stale", EmployeeType: graph.ManagedMarker}
	phone := func(id, n string) []graph.PhoneMethod {
		return []graph.PhoneMethod{{ID: "m-" + id, PhoneNumber: n, PhoneType: graph.PhoneMobile}}
	}
	in := Inputs{
		Scope: []ADUser{back, boss, fresh, kept},
		Entra: State{
			Users:   []graph.User{bossU, keptU, stale},
			Deleted: []graph.DeletedUser{{ID: "id-back", MailNickname: "back", EmployeeType: graph.ManagedMarker}},
			Phones:  map[string][]graph.PhoneMethod{"id-boss": phone("boss", "+1 2025550101"), "id-kept": phone("kept", "+1 2025550102")},
		},
		Domain:      testDomain,
		Phones:      map[string]string{"boss": "+1 2025550101", "kept": "+1 2025550102", "fresh": "+1 2025550103", "back": "+1 2025550104"},
		DeleteLimit: 10,
	}
	p, err := MakePlan(in)
	if err != nil {
		t.Fatalf("MakePlan: %v", err)
	}
	var got []string
	for _, a := range p.Actions {
		got = append(got, a.Kind.String()+" "+a.Username)
	}
	want := []string{
		"restore back", "create fresh", "delete stale",
		"set phone back", "set phone fresh",
		"set manager fresh", "clear manager back",
	}
	if !slices.Equal(got, want) {
		t.Errorf("plan =\n%v\nwant\n%v", got, want)
	}
}

// covers: AC-2. A blank AD department or title maps to null; a set one to
// its value; UPN compares case insensitively.
func TestDiffNullRule(t *testing.T) {
	u := ADUser{Username: "eva.peters", DisplayName: "Eva Peters", GivenName: "Eva", Surname: "Peters", Department: "", Title: "Account Manager"}
	cur := managedUser("x", u)
	cur.Department = ptr("Sales")
	cur.UserPrincipalName = "EVA.PETERS@" + testDomain

	changed := diff(cur, desired(u, testDomain))
	if len(changed) != 1 {
		t.Fatalf("changed = %v, want only department", changed)
	}
	if v, ok := changed["department"]; !ok || v != nil {
		t.Errorf("department = %v, want null", v)
	}

	cur.Department = nil
	if changed := diff(cur, desired(u, testDomain)); len(changed) != 0 {
		t.Errorf("changed = %v, want none once department is null", changed)
	}
}
