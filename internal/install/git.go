package install

import (
	"context"
	"errors"
	"fmt"
	"path"
	"slices"
	"strconv"
	"strings"
)

// Git runs git in the clone.
type Git struct{ run Runner }

func (g Git) git(ctx context.Context, env []string, args ...string) (string, error) {
	out, err := g.run.Run(ctx, Cmd{Name: "git", Args: args, Env: env})
	return strings.TrimSpace(string(out)), err
}

// Changed lists changed or untracked paths, sorted. Ignored files (every
// secrets.env) are not listed.
func (g Git) Changed(ctx context.Context) ([]string, error) {
	// Not g.git: trimming would eat the first line's status column.
	raw, err := g.run.Run(ctx, Cmd{Name: "git", Args: []string{"status", "--porcelain=v1", "-uall"}})
	if err != nil {
		return nil, err
	}
	var paths []string
	for line := range strings.SplitSeq(strings.TrimRight(string(raw), "\n"), "\n") {
		if len(line) < 4 {
			continue
		}
		p := line[3:]
		if _, to, ok := strings.Cut(p, " -> "); ok {
			p = to
		}
		paths = append(paths, strings.Trim(p, `"`))
	}
	slices.Sort(paths)
	return paths, nil
}

func inDir(p, dir string) bool {
	return strings.HasPrefix(path.Clean(p)+"/", strings.TrimSuffix(dir, "/")+"/")
}

// committable are the only files the installer commits: everything else in
// a cluster folder is yours to commit (AC-5, AC-6).
var committable = []string{valuesFile, sealedFile}

// Unexpected lists the changed paths preflight refuses: anything outside
// dir, and anything inside it but values.yaml and sealed-values.yaml.
func Unexpected(changed []string, dir string) []string {
	var out []string
	for _, p := range changed {
		if !inDir(p, dir) || !slices.Contains(committable, path.Base(p)) || path.Dir(path.Clean(p)) != path.Clean(dir) {
			out = append(out, p)
		}
	}
	return out
}

// Tracking is the branch's relation to its upstream after a fetch.
type Tracking struct {
	Remote, Branch string // the upstream, as origin and main
	Ahead, Behind  int
}

// Track fetches and compares HEAD with its upstream. An empty Branch means
// the branch has none.
func (g Git) Track(ctx context.Context) (Tracking, error) {
	up, err := g.git(ctx, nil, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}")
	if err != nil {
		return Tracking{}, nil //nolint:nilerr // no upstream is a preflight finding, not a failure
	}
	remote, branch, ok := strings.Cut(up, "/")
	if !ok {
		return Tracking{}, fmt.Errorf("unexpected upstream %q", up)
	}
	if _, err := g.git(ctx, nil, "fetch", "--quiet", remote); err != nil {
		return Tracking{}, fmt.Errorf("fetch: %w", err)
	}
	counts, err := g.git(ctx, nil, "rev-list", "--left-right", "--count", "HEAD...@{u}")
	if err != nil {
		return Tracking{}, err
	}
	fields := strings.Fields(counts)
	if len(fields) != 2 {
		return Tracking{}, fmt.Errorf("unexpected rev-list output %q", counts)
	}
	ahead, err1 := strconv.Atoi(fields[0])
	behind, err2 := strconv.Atoi(fields[1])
	if err := errors.Join(err1, err2); err != nil {
		return Tracking{}, fmt.Errorf("unexpected rev-list output %q", counts)
	}
	return Tracking{Remote: remote, Branch: branch, Ahead: ahead, Behind: behind}, nil
}

// RemoteURL is a remote's fetch URL.
func (g Git) RemoteURL(ctx context.Context, remote string) (string, error) {
	return g.git(ctx, nil, "remote", "get-url", remote)
}

// sameRepo compares two repo URLs, https or ssh form, ignoring .git and case.
func sameRepo(a, b string) bool {
	return normalizeRepo(a) == normalizeRepo(b)
}

func normalizeRepo(u string) string {
	u = strings.ToLower(strings.TrimSpace(u))
	u = strings.TrimSuffix(strings.TrimSuffix(u, "/"), ".git")
	for _, prefix := range []string{"https://", "http://", "ssh://", "git@"} {
		u = strings.TrimPrefix(u, prefix)
	}
	if at := strings.LastIndex(u, "@"); at >= 0 { // credentials in an https URL
		u = u[at+1:]
	}
	return strings.Replace(u, ":", "/", 1)
}

// Commit commits only values.yaml and sealed-values.yaml in dir, with the
// git hooks off (LEFTHOOK=0): these commits hold only cluster data (AC-5).
// It returns false when neither changed.
func (g Git) Commit(ctx context.Context, dir, message string) (bool, error) {
	paths := make([]string, 0, len(committable))
	for _, f := range committable {
		paths = append(paths, path.Join(dir, f))
	}
	if _, err := g.git(ctx, nil, append([]string{"add", "--"}, paths...)...); err != nil {
		return false, err
	}
	staged, err := g.git(ctx, nil, append([]string{"diff", "--cached", "--name-only", "--"}, paths...)...)
	if err != nil {
		return false, err
	}
	if staged == "" {
		return false, nil
	}
	for p := range strings.SplitSeq(staged, "\n") {
		if !slices.Contains(paths, p) {
			return false, fmt.Errorf("refusing to commit %s; only %s may be committed by the installer", p, strings.Join(committable, " and "))
		}
	}
	if _, err := g.git(ctx, []string{"LEFTHOOK=0"}, append([]string{"commit", "--quiet", "-m", message, "--"}, paths...)...); err != nil {
		return false, err
	}
	return true, nil
}

// Push pushes HEAD to the upstream branch by name, so a local branch named
// differently from its upstream still lands where ArgoCD reads.
func (g Git) Push(ctx context.Context, tr Tracking) error {
	_, err := g.git(ctx, []string{"LEFTHOOK=0"}, "push", "--quiet", tr.Remote, "HEAD:refs/heads/"+tr.Branch)
	return err
}

// Head is HEAD's full SHA.
func (g Git) Head(ctx context.Context) (string, error) {
	return g.git(ctx, nil, "rev-parse", "HEAD")
}
