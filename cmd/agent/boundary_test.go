package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// deniedImports are the client system packages only the tool server may
// import (AGENTS.md, Rules). Keep this list in sync with the
// agent-credential-boundary rule in .golangci.yml. depguard sees only direct
// imports; this test also catches one pulled in through a shared package.
var deniedImports = []string{
	"github.com/toyinogun/AIITsupport/internal/ldap",
	"github.com/toyinogun/AIITsupport/internal/graph",
	"github.com/toyinogun/AIITsupport/internal/zammad",
	"github.com/toyinogun/AIITsupport/internal/seed",
	"github.com/toyinogun/AIITsupport/internal/tools",
	"github.com/toyinogun/AIITsupport/internal/mcpserver",
	"github.com/toyinogun/AIITsupport/internal/resetlink",
	"github.com/toyinogun/AIITsupport/internal/resetpage",
	"github.com/toyinogun/AIITsupport/internal/approvalapi",
	"github.com/go-ldap/ldap",
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity",
}

func TestAgentDoesNotImportClientSystems(t *testing.T) {
	if bad := deniedDeps(listDeps(t, ".")); len(bad) > 0 {
		t.Errorf("cmd/agent pulls in client system packages: %v", bad)
	}
}

// covers: AC-18 (spec 0008). The agent loop and the model client import no
// client system package, directly or through a shared one.
func TestAgentLoopPackagesDoNotImportClientSystems(t *testing.T) {
	for _, dir := range []string{"../../internal/agent", "../../internal/agent/agenttest", "../../internal/llm"} {
		if bad := deniedDeps(listDeps(t, dir)); len(bad) > 0 {
			t.Errorf("%s pulls in client system packages: %v", dir, bad)
		}
	}
}

// The real packages don't exist yet, so this proves the check can fail: a
// stub binary imports a shared package that imports a denied one.
func TestBoundaryCheckCatchesTransitiveImport(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module boundarystub\n\ngo 1.26\n")
	writeFile(t, filepath.Join(dir, "main.go"),
		"package main\n\nimport _ \"boundarystub/shared\"\n\nfunc main() {}\n")
	writeFile(t, filepath.Join(dir, "shared", "shared.go"),
		"package shared\n\nimport _ \"github.com/go-ldap/ldap/v3\"\n")

	bad := deniedDeps(listDeps(t, dir))
	if !slices.Equal(bad, []string{"github.com/go-ldap/ldap/v3"}) {
		t.Errorf("deniedDeps = %v, want [github.com/go-ldap/ldap/v3]", bad)
	}
}

// covers: DM-2. The two deny lists are kept in sync by hand (spec 0002,
// Consequences); this fails the moment one changes without the other.
func TestDeniedImportsMatchTheDepguardRule(t *testing.T) {
	config, err := os.ReadFile(filepath.Join("..", "..", ".golangci.yml"))
	if err != nil {
		t.Fatalf("read .golangci.yml: %v", err)
	}

	got := depguardDenyList(string(config), "agent-credential-boundary")
	if len(got) == 0 {
		t.Fatal("no agent-credential-boundary deny entries found in .golangci.yml")
	}
	want := slices.Sorted(slices.Values(deniedImports))
	if !slices.Equal(got, want) {
		t.Errorf("depguard denies %v, boundary test denies %v; keep both lists in sync", got, want)
	}
}

// depguardDenyList returns the sorted `pkg:` values listed under the named
// depguard rule. It reads the YAML by indentation, which is enough for the
// one file it checks and avoids a YAML dependency.
func depguardDenyList(config, rule string) []string {
	var pkgs []string
	ruleIndent := -1
	for line := range strings.Lines(config) {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		indent := len(line) - len(strings.TrimLeft(line, " "))
		if ruleIndent < 0 {
			if trimmed == rule+":" {
				ruleIndent = indent
			}
			continue
		}
		if indent <= ruleIndent {
			break
		}
		if pkg, ok := strings.CutPrefix(trimmed, "- pkg:"); ok {
			pkgs = append(pkgs, strings.Trim(strings.TrimSpace(pkg), `"'`))
		}
	}
	slices.Sort(pkgs)
	return pkgs
}

// listDeps returns every package the package in dir imports, directly or
// not. -e keeps packages that can't be resolved (the stub's denied import).
func listDeps(t *testing.T, dir string) []string {
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

func deniedDeps(deps []string) []string {
	var bad []string
	for _, dep := range deps {
		for _, denied := range deniedImports {
			if dep == denied || strings.HasPrefix(dep, denied+"/") {
				bad = append(bad, dep)
			}
		}
	}
	return bad
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatalf("make dir for %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
