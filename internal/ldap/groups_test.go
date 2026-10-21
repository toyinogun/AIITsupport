package ldap

import (
	"slices"
	"testing"
)

// covers: spec 0013 AC-3. Only the exact tag form parses; anything else
// hides the group (no departments).
func TestParseDepartments(t *testing.T) {
	cases := map[string][]string{
		"noord-departments: Finance":                  {"Finance"},
		"noord-departments: Finance, Management":      {"Finance", "Management"},
		"noord-departments:  Customer Service ,Sales": {"Customer Service", "Sales"},
		"noord-departments: Finance,":                 nil,
		"noord-departments: ":                         nil,
		"noord-departments:Finance":                   nil,
		"Noord-departments: Finance":                  nil,
		" noord-departments: Finance":                 nil,
		"Finance":                                     nil,
		"":                                            nil,
		"notes first\nnoord-departments: Finance":     nil,
		"noord-departments: Finance, , Management":    nil,
	}
	for info, want := range cases {
		if got := ParseDepartments(info); !slices.Equal(got, want) {
			t.Errorf("ParseDepartments(%q) = %q, want %q", info, got, want)
		}
	}
}
