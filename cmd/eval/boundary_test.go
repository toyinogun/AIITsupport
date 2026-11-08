package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const (
	evalresetPkg = "github.com/toyinogun/AIITsupport/internal/seed/evalreset"
	modulePrefix = "github.com/toyinogun/AIITsupport/"
)

// harnessPkgs are the harness packages no other binary may pull in.
var harnessPkgs = []string{
	modulePrefix + "eval/score",
	modulePrefix + "eval/report",
	modulePrefix + "eval/runner",
	modulePrefix + "eval/runstore",
	modulePrefix + "eval/env",
	evalresetPkg,
}

// binaryDeps lists every package one binary under cmd/ pulls in.
func binaryDeps(t *testing.T, dir string) []string {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "go", "list", "-deps", "-e", ".")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOFLAGS=-mod=readonly", "GOWORK=off", "GOTOOLCHAIN=local", "GOPROXY=off")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list -deps in %s: %v", dir, err)
	}
	return strings.Fields(string(out))
}

// covers: AC-19 (spec 0018). cmd/eval is the only binary that may import
// internal/seed/evalreset (the Administrator undo), and no other binary
// pulls in any harness package. A new binary under cmd/ is checked too.
func TestOnlyTheHarnessBinaryReachesTheHarness(t *testing.T) {
	dirs, err := filepath.Glob(filepath.Join("..", "*"))
	if err != nil {
		t.Fatal(err)
	}
	var checked int
	for _, dir := range dirs {
		if info, err := os.Stat(dir); err != nil || !info.IsDir() {
			continue
		}
		name := filepath.Base(dir)
		deps := binaryDeps(t, dir)
		if name == "eval" {
			if !slices.Contains(deps, evalresetPkg) {
				t.Errorf("cmd/eval no longer imports %s; this check would pass for the wrong reason", evalresetPkg)
			}
			continue
		}
		checked++
		for _, p := range harnessPkgs {
			if slices.Contains(deps, p) {
				t.Errorf("cmd/%s pulls in the harness package %s", name, p)
			}
		}
	}
	if checked < 3 {
		t.Errorf("checked %d other binaries, want at least agent, toolserver, and seed", checked)
	}
}
