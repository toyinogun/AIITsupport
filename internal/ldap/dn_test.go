package ldap_test

import (
	"testing"

	noordldap "github.com/toyinogun/AIITsupport/internal/ldap"
)

// covers: AC-13, AC-16. Under parses both DNs and compares RDNs, never a raw suffix.
func TestUnder(t *testing.T) {
	const base = "DC=ad,DC=noord,DC=example"
	for _, tc := range []struct {
		dn, ou string
		want   bool
	}{
		{"CN=Eva Peters,OU=Sales,OU=Users,OU=Noord," + base, "OU=Users,OU=Noord", true},
		{"cn=eva peters,ou=sales,ou=users,ou=noord,dc=AD,dc=noord,dc=example", "OU=Users,OU=Noord", true},
		{"CN=SG-Finance,OU=Groups,OU=Noord," + base, "OU=Users,OU=Noord", false},
		{"CN=adm.ruben.mulder,OU=Admin Accounts,OU=Noord," + base, "OU=Users,OU=Noord", false},
		{"OU=Users,OU=Noord," + base, "OU=Users,OU=Noord", false},
		// A crafted RDN that ends in the OU's text is not under it.
		{`CN=x\,OU=Users,OU=Noord,` + base, "OU=Users,OU=Noord", false},
		{"CN=x,OU=FakeUsers,OU=Noord," + base, "OU=Users,OU=Noord", false},
		{"CN=x,OU=Users,OU=Noord,DC=evil,DC=example", "OU=Users,OU=Noord", false},
	} {
		got, err := noordldap.Under(tc.dn, tc.ou, base)
		if err != nil || got != tc.want {
			t.Errorf("Under(%q, %q) = %v, %v; want %v", tc.dn, tc.ou, got, err, tc.want)
		}
	}
	if _, err := noordldap.Under("not a dn", "OU=Users", base); err == nil {
		t.Error("Under accepted a malformed DN")
	}
}

// covers: AC-16
func TestCheckRelativeOU(t *testing.T) {
	for ou, ok := range map[string]bool{
		"OU=Users,OU=Noord":       true,
		"OU=Groups,OU=Noord":      true,
		"":                        false,
		"   ":                     false,
		"OU=Users,OU=Noord,DC=ad": false,
		"OU=Users,dc=noord":       false,
		"not a dn":                false,
		"OU=Users,,OU=Noord":      false,
	} {
		if err := noordldap.CheckRelativeOU(ou); (err == nil) != ok {
			t.Errorf("CheckRelativeOU(%q) = %v, want ok %v", ou, err, ok)
		}
	}
}

// covers: AC-13. UnderAny is true when any OU holds the DN, and a bad OU is an
// error, never a silent "not under".
func TestUnderAny(t *testing.T) {
	const base = "DC=ad,DC=noord,DC=example"
	ous := []string{"OU=Groups,OU=Noord", "OU=Users,OU=Noord"}
	eva := "CN=Eva Peters,OU=Sales,OU=Users,OU=Noord," + base

	if got, err := noordldap.UnderAny(eva, ous, base); err != nil || !got {
		t.Errorf("UnderAny(eva, groups+users) = %v, %v; want true", got, err)
	}
	if got, err := noordldap.UnderAny(eva, ous[:1], base); err != nil || got {
		t.Errorf("UnderAny(eva, groups) = %v, %v; want false", got, err)
	}
	if got, err := noordldap.UnderAny(eva, nil, base); err != nil || got {
		t.Errorf("UnderAny(eva, none) = %v, %v; want false", got, err)
	}
	if _, err := noordldap.UnderAny("not a dn", ous, base); err == nil {
		t.Error("UnderAny accepted a malformed DN")
	}
}

func TestOUHelpers(t *testing.T) {
	const base = "DC=ad,DC=noord,DC=example"
	for got, want := range map[string]string{
		noordldap.NoordDN(base):  "OU=Noord," + base,
		noordldap.UsersDN(base):  "OU=Users,OU=Noord," + base,
		noordldap.GroupsDN(base): "OU=Groups,OU=Noord," + base,
		noordldap.AdminsDN(base): "OU=Admin Accounts,OU=Noord," + base,
	} {
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	}
}

// A department name is escaped, so a comma cannot add an RDN and move the OU.
func TestDepartmentDNEscapesTheName(t *testing.T) {
	const base = "DC=ad,DC=noord,DC=example"

	got := noordldap.DepartmentDN(base, "Sales, OU=Admin Accounts")

	under, err := noordldap.Under("CN=x,"+got, "OU=Users,OU=Noord", base)
	if err != nil || !under {
		t.Errorf("DepartmentDN = %q; a user in it is under Users: %v, %v", got, under, err)
	}
	if want := `OU=Sales\, OU=Admin Accounts,OU=Users,OU=Noord,` + base; got != want {
		t.Errorf("DepartmentDN = %q, want %q", got, want)
	}
}
