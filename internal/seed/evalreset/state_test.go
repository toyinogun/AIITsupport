package evalreset

import (
	"slices"
	"testing"

	"github.com/toyinogun/AIITsupport/internal/graph/graphtest"
)

func baseSnap() Snapshot {
	return Snapshot{
		Locked:         map[string]bool{"a": false, "b": true},
		Members:        map[string][]string{"g": {"cn=a", "cn=b"}},
		MaxTicket:      10,
		Authenticators: map[string][]string{"a": {"m1"}},
		Phones:         map[string][]graphtest.Phone{"a": nil},
	}
}

// covers: AC-7 (spec 0018)
func TestDiff(t *testing.T) {
	if d := Diff(baseSnap(), baseSnap()); len(d) != 0 {
		t.Fatalf("identical snapshots differ: %v", d)
	}
	now := baseSnap()
	now.Locked["a"] = true
	now.Members["g"] = []string{"cn=a"}
	now.MaxTicket = 12
	now.Authenticators["a"] = nil
	got := Diff(baseSnap(), now)
	if len(got) != 4 {
		t.Fatalf("diff = %v, want 4 differences", got)
	}
	for _, want := range []string{"lock state of a", "members of group g", "MFA methods of a"} {
		if !slices.Contains(got, want) {
			t.Errorf("diff %v misses %q", got, want)
		}
	}
}

func TestDifference(t *testing.T) {
	if got := difference([]string{"a", "b", "c"}, []string{"b"}); !slices.Equal(got, []string{"a", "c"}) {
		t.Fatalf("difference = %v", got)
	}
}
