package logging

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"
	"golang.org/x/tools/go/types/typeutil"
)

const (
	slogPath      = "log/slog"
	attributePath = "go.opentelemetry.io/otel/attribute"
	tracePath     = "go.opentelemetry.io/otel/trace"
	modulePath    = "github.com/toyinogun/AIITsupport"
)

// scanExempt are the runtime packages the scan skips: the seeders (operator
// tools that log user lines on purpose), the sqlc output, and the resource
// attributes telemetry builds.
var scanExempt = []string{modulePath + "/internal/seed", modulePath + "/internal/db"}

// slogArgsFrom is where the key value pairs start in each slog call.
var slogArgsFrom = map[string]int{
	"Debug": 1, "Info": 1, "Warn": 1, "Error": 1,
	"DebugContext": 2, "InfoContext": 2, "WarnContext": 2, "ErrorContext": 2,
	"Log": 3, "LogAttrs": 3, "With": 0,
}

var (
	slogConstructors = []string{"String", "Int", "Int64", "Uint64", "Float64", "Bool", "Time", "Duration", "Any", "Group"}
	attrConstructors = []string{
		"String", "Int", "Int64", "Float64", "Bool", "Stringer",
		"StringSlice", "IntSlice", "Int64Slice", "Float64Slice", "BoolSlice",
	}
	builtinKeys = []string{"time", "level", "msg", "source"}
)

// covers: AC-2 (spec 0010). Every log key and span attribute key in the
// runtime code is a string literal on the allow list, and every span name is
// a string literal.
func TestRuntimeKeysAreAllowListed(t *testing.T) {
	root := filepath.Join("..", "..")
	pkgs := load(t, root, "./cmd/agent", "./cmd/toolserver", "./internal/...")
	var problems []string
	for _, p := range pkgs {
		if exempt(p.PkgPath) {
			continue
		}
		problems = append(problems, scan(p)...)
	}
	if len(problems) > 0 {
		t.Errorf("log or span keys outside the allow list (add a key only if it never holds personal data):\n%s",
			strings.Join(problems, "\n"))
	}
}

// covers: AC-2 (spec 0010). The scan names the file and line of each bad
// call in the fixture, and nothing else.
func TestScanRejectsTheFixture(t *testing.T) {
	pkgs := load(t, ".", "./testdata/scanfixture")
	got := map[int]string{}
	for _, problem := range scan(pkgs[0]) {
		parts := strings.SplitN(problem, ":", 3)
		line, err := strconv.Atoi(parts[1])
		if err != nil || !strings.HasSuffix(parts[0], "fixture.go") {
			t.Fatalf("problem %q does not name fixture.go:line", problem)
		}
		got[line] = problem
	}
	want := wants(t, filepath.Join("testdata", "scanfixture", "fixture.go"))
	for line, text := range want {
		if !strings.Contains(got[line], text) {
			t.Errorf("line %d: got %q, want a problem containing %q", line, got[line], text)
		}
	}
	for line, problem := range got {
		if _, ok := want[line]; !ok {
			t.Errorf("unexpected problem: %s", problem)
		}
	}
}

func load(t *testing.T, dir string, patterns ...string) []*packages.Package {
	t.Helper()
	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedSyntax | packages.NeedTypes | packages.NeedTypesInfo,
		Dir:  dir,
	}
	pkgs, err := packages.Load(cfg, patterns...)
	if err != nil {
		t.Fatalf("load packages: %v", err)
	}
	if packages.PrintErrors(pkgs) > 0 || len(pkgs) == 0 {
		t.Fatal("packages did not load cleanly")
	}
	return pkgs
}

func exempt(path string) bool {
	for _, e := range scanExempt {
		if path == e || strings.HasPrefix(path, e+"/") {
			return true
		}
	}
	return false
}

// scan returns "file:line: problem" for each bad key or span name in p.
func scan(p *packages.Package) []string {
	s := scanner{pkg: p, attrs: p.PkgPath != modulePath+"/internal/telemetry"}
	for _, f := range p.Syntax {
		ast.Inspect(f, func(n ast.Node) bool {
			if call, ok := n.(*ast.CallExpr); ok {
				s.call(call)
			}
			return true
		})
	}
	return s.problems
}

type scanner struct {
	pkg      *packages.Package
	attrs    bool // check attribute keys (off for the resource attributes in internal/telemetry)
	problems []string
}

func (s *scanner) report(n ast.Node, format string, a ...any) {
	pos := s.pkg.Fset.Position(n.Pos())
	s.problems = append(s.problems, fmt.Sprintf("%s:%d: %s", pos.Filename, pos.Line, fmt.Sprintf(format, a...)))
}

func (s *scanner) call(call *ast.CallExpr) {
	info := s.pkg.TypesInfo
	if tv, ok := info.Types[call.Fun]; ok && tv.IsType() {
		if s.attrs && isNamed(tv.Type, attributePath, "Key") && len(call.Args) == 1 {
			s.key(call.Args[0], "attribute.Key")
		}
		return
	}
	fn, ok := typeutil.Callee(info, call).(*types.Func)
	if !ok || fn.Pkg() == nil {
		return
	}
	recv := fn.Signature().Recv()
	switch path := fn.Pkg().Path(); {
	case path == slogPath && (recv == nil || isNamed(recv.Type(), slogPath, "Logger")):
		if from, ok := slogArgsFrom[fn.Name()]; ok {
			s.slogArgs(call, from)
		} else if recv == nil && slices.Contains(slogConstructors, fn.Name()) && len(call.Args) > 0 {
			s.key(call.Args[0], "slog."+fn.Name())
		}
	case path == attributePath && recv == nil && s.attrs && slices.Contains(attrConstructors, fn.Name()):
		s.key(call.Args[0], "attribute."+fn.Name())
	case path == tracePath && fn.Name() == "Start" && recv != nil && len(call.Args) > 1:
		if !isStringLit(call.Args[1]) {
			s.report(call.Args[1], "span name is not a string literal")
		}
	case path == tracePath && fn.Name() == "SetName" && recv != nil && len(call.Args) == 1:
		// The exporter passes span names as is, so a rename must be fixed too.
		if !isStringLit(call.Args[0]) {
			s.report(call.Args[0], "span name is not a string literal")
		}
	}
}

// slogArgs walks a slog call's key value pairs the way slog does: an Attr
// stands alone (its constructor is checked as its own call), anything else
// is a key followed by its value.
func (s *scanner) slogArgs(call *ast.CallExpr, from int) {
	if call.Ellipsis.IsValid() {
		s.report(call, "spread arguments hide their keys")
		return
	}
	for i := from; i < len(call.Args); i++ {
		arg := call.Args[i]
		if isNamed(s.pkg.TypesInfo.TypeOf(arg), slogPath, "Attr") {
			if c, ok := arg.(*ast.CallExpr); !ok || !isSlogConstructor(s.pkg.TypesInfo, c) {
				s.report(arg, "slog.Attr not built by a slog constructor in place")
			}
			continue
		}
		s.key(arg, "slog key")
		i++ // skip the value
	}
}

func (s *scanner) key(expr ast.Expr, what string) {
	lit, ok := expr.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		s.report(expr, "%s is not a string literal", what)
		return
	}
	key, err := strconv.Unquote(lit.Value)
	switch {
	case err != nil:
		s.report(expr, "%s does not unquote", what)
	case what == "slog key" && slices.Contains(builtinKeys, key):
		s.report(expr, "%s %q is a built in key", what, key)
	case !Allowed(key):
		s.report(expr, "%s %q is not on the allow list", what, key)
	}
}

func isSlogConstructor(info *types.Info, call *ast.CallExpr) bool {
	fn, ok := typeutil.Callee(info, call).(*types.Func)
	return ok && fn.Pkg() != nil && fn.Pkg().Path() == slogPath && fn.Signature().Recv() == nil &&
		slices.Contains(slogConstructors, fn.Name())
}

func isStringLit(expr ast.Expr) bool {
	lit, ok := expr.(*ast.BasicLit)
	return ok && lit.Kind == token.STRING
}

func isNamed(t types.Type, path, name string) bool {
	if p, ok := t.(*types.Pointer); ok {
		t = p.Elem()
	}
	n, ok := t.(*types.Named)
	return ok && n.Obj().Pkg() != nil && n.Obj().Pkg().Path() == path && n.Obj().Name() == name
}

var wantComment = regexp.MustCompile(`// want "([^"]+)"`)

// wants maps each fixture line with a want comment to its expected text.
func wants(t *testing.T, path string) map[int]string {
	t.Helper()
	src, err := os.ReadFile(path) //nolint:gosec // the fixture path is a constant in this test
	if err != nil {
		t.Fatal(err)
	}
	out := map[int]string{}
	for i, line := range strings.Split(string(src), "\n") {
		if m := wantComment.FindStringSubmatch(line); m != nil {
			out[i+1] = m[1]
		}
	}
	return out
}
