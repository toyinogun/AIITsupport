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
	"github.com/go-ldap/ldap",
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity",
}

func TestAgentDoesNotImportClientSystems(t *testing.T) {
	if bad := deniedDeps(listDeps(t, ".")); len(bad) > 0 {
		t.Errorf("cmd/agent pulls in client system packages: %v", bad)
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
