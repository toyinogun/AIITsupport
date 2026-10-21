package ldap_test

import (
	"errors"
	"slices"
	"strings"
	"testing"

	goldap "github.com/go-ldap/ldap/v3"

	noordldap "github.com/toyinogun/AIITsupport/internal/ldap"
)

var groupOUs = []string{"OU=Groups,OU=Noord"}

// covers: spec 0013 AC-2, AC-3, AC-10. As svc-itsupport, the seeded groups
// read back with their type, owner, description, and department tags; the
// privileged groups sit outside the group OUs; a lookup by name gives AD's
// own spelling; nesting lists only security groups.
func TestLiveGroupReads(t *testing.T) {
	requireShared(t)
	c := noordldap.NewClient(svcConfig())
	list, err := c.ListGroups(t.Context(), groupOUs)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]noordldap.GroupInfo{}
	for _, g := range list {
		byName[g.Name] = g
	}
	fin, ok := byName["SG-Finance"]
	if !ok || !fin.Security || fin.Description != "Finance department" || !strings.HasPrefix(fin.ManagedBy, "CN=Ilse Smit,") ||
		!slices.Equal(fin.Departments, []string{"Finance"}) {
		t.Errorf("SG-Finance = %+v", fin)
	}
	if ops := byName["SHR-Operations-R"]; !slices.Equal(ops.Departments, []string{"Warehouse", "Transport", "Management"}) || ops.ManagedBy != "" {
		t.Errorf("SHR-Operations-R = %+v", ops)
	}
	if all := byName["allstaff"]; all.Security || len(all.Departments) != 0 {
		t.Errorf("allstaff = %+v", all)
	}
	if ro := byName["fin-readonly"]; ro.Description != "" || !slices.Equal(ro.Departments, []string{"Finance"}) {
		t.Errorf("fin-readonly = %+v", ro)
	}
	if helpdesk := byName["APP-Helpdesk-Agents"]; len(helpdesk.Departments) != 0 {
		t.Errorf("APP-Helpdesk-Agents is tagged: %v", helpdesk.Departments)
	}
	if _, ok := byName["SG-IT-Admins"]; ok {
		t.Error("SG-IT-Admins is under the group OUs")
	}

	g, ok, err := c.GroupByName(t.Context(), "sg-finance")
	if err != nil || !ok || g.Name != "SG-Finance" || g.DN != fin.DN {
		t.Errorf("GroupByName = %+v, %v, %v", g, ok, err)
	}
	if _, ok, err := c.GroupByName(t.Context(), "SG-Finanse"); err != nil || ok {
		t.Errorf("a missing group = %v, %v", ok, err)
	}
	nested, err := c.GroupNestedInto(t.Context(), fin.DN, groupOUs)
	if err != nil || !slices.Equal(nested, []string{"APP-ERP-Finance", "SHR-Finance-RW"}) {
		t.Errorf("nested into = %v, %v", nested, err)
	}
	noor, ok, err := c.FindUserByAccount(t.Context(), "noor.elamrani")
	if err != nil || !ok || noor.Department != "Finance" {
		t.Fatalf("noor = %+v, %v, %v", noor, ok, err)
	}
	if in, err := c.UserInChainMemberOf(t.Context(), noor.DN, []string{fin.DN}); err != nil || in {
		t.Errorf("noor in SG-Finance = %v, %v; the seed takes her out", in, err)
	}
}

// covers: spec 0013 AC-14. svc-itsupport adds a member under OU=Groups and
// reads it back; a second add is ErrAlreadyMember; a privileged group
// outside OU=Groups refuses the write.
func TestLiveAddMember(t *testing.T) {
	requireShared(t)
	c := noordldap.NewClient(svcConfig())
	w := noordldap.NewWriter(svcConfig())
	g, _, err := c.GroupByName(t.Context(), "fin-readonly")
	if err != nil {
		t.Fatal(err)
	}
	noor, _, err := c.FindUserByAccount(t.Context(), "noor.elamrani")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		undo := goldap.NewModifyRequest(g.DN, nil)
		undo.Delete("member", []string{noor.DN})
		_ = adminConn(t).Modify(undo)
	})
	if err := w.AddMember(t.Context(), g.DN, noor.DN); err != nil {
		t.Fatalf("AddMember: %v", err)
	}
	if in, err := c.GroupHasMember(t.Context(), g.DN, noor.DN); err != nil || !in {
		t.Errorf("read back = %v, %v", in, err)
	}
	if err := w.AddMember(t.Context(), g.DN, noor.DN); !errors.Is(err, noordldap.ErrAlreadyMember) {
		t.Errorf("second add = %v, want ErrAlreadyMember", err)
	}
	admins, ok, err := c.GroupByName(t.Context(), "SG-IT-Admins")
	if err != nil || !ok {
		t.Fatalf("SG-IT-Admins = %v, %v", ok, err)
	}
	if err := w.AddMember(t.Context(), admins.DN, noor.DN); err == nil || errors.Is(err, noordldap.ErrAlreadyMember) {
		t.Errorf("an add to SG-IT-Admins = %v, want refused", err)
		undo := goldap.NewModifyRequest(admins.DN, nil)
		undo.Delete("member", []string{noor.DN})
		_ = adminConn(t).Modify(undo)
	}
}
