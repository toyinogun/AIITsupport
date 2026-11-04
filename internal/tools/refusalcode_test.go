package tools

import (
	"os"
	"regexp"
	"testing"
)

// covers: AC-8 (spec 0017). Every refusal in refusal.go is reachable through
// CodeForReason, so a new refusal that is not added to allRefusals fails here.
func TestAllRefusalsListsEveryRefusalVariable(t *testing.T) {
	src, err := os.ReadFile("refusal.go")
	if err != nil {
		t.Fatalf("read refusal.go: %v", err)
	}
	declared := len(regexp.MustCompile(`(?m)^\s+refuse\w+\s+=\s+refusal\{`).FindAll(src, -1))
	if declared != len(allRefusals) {
		t.Errorf("refusal.go declares %d refusals, allRefusals lists %d", declared, len(allRefusals))
	}
}

// covers: AC-8 (spec 0017).
func TestCodeForReason(t *testing.T) {
	for _, r := range allRefusals {
		got, ok := CodeForReason(r.reason)
		if !ok || got != r.code {
			t.Errorf("CodeForReason(%q) = %q, %v; want %q, true", r.reason, got, ok, r.code)
		}
	}
	if code, ok := CodeForReason("action failed"); ok {
		t.Errorf("a failure reason is not a refusal, got code %q", code)
	}
	if _, ok := CodeForReason(""); ok {
		t.Error("an empty reason has no code")
	}
}

// covers: AC-8 (spec 0017).
func TestRefusalCodesAreUniqueAndComplete(t *testing.T) {
	codes := RefusalCodes()
	seen := map[Code]bool{}
	for _, c := range codes {
		if seen[c] {
			t.Errorf("code %q listed twice", c)
		}
		seen[c] = true
	}
	for _, want := range []Code{CodeBlocked, CodeProtected, CodeNotYours, CodeAlreadyMember, CodeNoOwner, CodeNotInEntra} {
		if !seen[want] {
			t.Errorf("RefusalCodes is missing %q", want)
		}
	}
}
