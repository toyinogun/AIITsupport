package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// covers: AC-18 (spec 0007). The tool server binary links no code from
// internal/seed, which binds as the domain Administrator. Test files may
// use the seeder's helpers; the binary may not.
func TestToolServerDoesNotLinkTheSeeder(t *testing.T) {
	cmd := exec.CommandContext(t.Context(), "go", "list", "-deps", ".")
	cmd.Env = append(os.Environ(), "GOFLAGS=-mod=readonly", "GOWORK=off")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list -deps: %v", err)
	}
	const seed = "github.com/toyinogun/AIITsupport/internal/seed"
	var linked []string
	for dep := range strings.FieldsSeq(string(out)) {
		if dep == seed || strings.HasPrefix(dep, seed+"/") {
			linked = append(linked, dep)
		}
	}
	if len(linked) > 0 {
		t.Errorf("cmd/toolserver links %v", linked)
	}
	// The check can see the tool layer, so an empty result is not a blind spot.
	if !strings.Contains(string(out), "github.com/toyinogun/AIITsupport/internal/tools\n") {
		t.Error("go list did not report internal/tools; the check is not looking at the binary")
	}
}
