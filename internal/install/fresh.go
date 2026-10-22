package install

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// The fresh cluster path (spec 0015, AC-2 to AC-5): ArgoCD when missing, a
// memory check for the demo, and the pull request push path.

// ExitPR is the exit code for "the push was blocked by a branch protection
// rule; a pull request holds the sealed values" (AC-5).
const ExitPR = 3

// argocdPinFile holds the ArgoCD chart pin (AC-3).
const argocdPinFile = "deploy/argocd/values.yaml"

// ArgoPin is the ArgoCD chart the installer installs when none runs.
type ArgoPin struct {
	RepoURL string `yaml:"repoURL"`
	Chart   string `yaml:"chart"`
	Version string `yaml:"version"`
}

// ReadArgoPin reads the pin from the bootstrap chart's values.
func ReadArgoPin(root string) (ArgoPin, error) {
	raw, err := os.ReadFile(filepath.Join(root, argocdPinFile)) //nolint:gosec // a fixed path inside the clone
	if err != nil {
		return ArgoPin{}, fmt.Errorf("read %s: %w", argocdPinFile, err)
	}
	var doc struct {
		ArgoCD ArgoPin `yaml:"argocd"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return ArgoPin{}, fmt.Errorf("parse %s: %w", argocdPinFile, err)
	}
	if p := doc.ArgoCD; p.RepoURL == "" || p.Chart == "" || p.Version == "" {
		return ArgoPin{}, fmt.Errorf("%s needs argocd.repoURL, chart, and version", argocdPinFile)
	}
	return doc.ArgoCD, nil
}

// argocd installs ArgoCD at the pinned version when the cluster has no
// argocd-server. An existing ArgoCD is not touched (AC-3).
func (s *state) argocd(ctx context.Context) error {
	ok, err := s.kube.Exists(ctx, argocdNamespace, "deployment", argocdServerName)
	if err != nil {
		return fmt.Errorf("look for ArgoCD: %w", err)
	}
	if ok {
		s.say("ArgoCD is already installed, left as it is")
		return nil
	}
	pin, err := ReadArgoPin(s.Root)
	if err != nil {
		return err
	}
	s.say("installing ArgoCD %s", pin.Version)
	if _, err := s.Run.Run(ctx, Cmd{Name: "helm", Args: []string{
		"--kube-context", s.cfg.KubeContext, "upgrade", "--install", "argocd", pin.Chart, "--repo", pin.RepoURL,
		"--version", pin.Version, "--namespace", argocdNamespace, "--create-namespace", "--wait", "--timeout", "10m",
	}}); err != nil {
		return fmt.Errorf("install ArgoCD: %w", err)
	}
	return nil
}

// FreeMemory is the most allocatable memory, in bytes, any node has left
// after the requests of the pods running on it.
func (k Kube) FreeMemory(ctx context.Context) (int64, error) {
	nodes, err := k.kubectl(ctx, nil, "get", "nodes", "-o", "json")
	if err != nil {
		return 0, err
	}
	pods, err := k.kubectl(ctx, nil, "get", "pods", "-A", "-o", "json", "--field-selector=status.phase!=Succeeded,status.phase!=Failed")
	if err != nil {
		return 0, err
	}
	var nl struct {
		Items []struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
			Status struct {
				Allocatable map[string]string `json:"allocatable"`
			} `json:"status"`
		} `json:"items"`
	}
	var pl struct {
		Items []struct {
			Spec struct {
				NodeName   string `json:"nodeName"`
				Containers []struct {
					Resources struct {
						Requests map[string]string `json:"requests"`
					} `json:"resources"`
				} `json:"containers"`
			} `json:"spec"`
		} `json:"items"`
	}
	if err := errors.Join(json.Unmarshal(nodes, &nl), json.Unmarshal(pods, &pl)); err != nil {
		return 0, fmt.Errorf("parse nodes and pods: %w", err)
	}
	used := map[string]int64{}
	for _, p := range pl.Items {
		for _, c := range p.Spec.Containers {
			if q, ok := c.Resources.Requests["memory"]; ok {
				n, err := ParseQuantity(q)
				if err != nil {
					return 0, err
				}
				used[p.Spec.NodeName] += n
			}
		}
	}
	var best int64
	for _, n := range nl.Items {
		alloc, err := ParseQuantity(n.Status.Allocatable["memory"])
		if err != nil {
			return 0, err
		}
		best = max(best, alloc-used[n.Metadata.Name])
	}
	return best, nil
}

var quantitySuffixes = []struct {
	suffix string
	mult   int64
}{
	{"Ki", 1 << 10},
	{"Mi", 1 << 20},
	{"Gi", 1 << 30},
	{"Ti", 1 << 40},
	{"k", 1e3},
	{"K", 1e3},
	{"M", 1e6},
	{"G", 1e9},
	{"T", 1e12},
}

// ParseQuantity reads a Kubernetes memory quantity (4Gi, 512Mi, 1000000).
func ParseQuantity(q string) (int64, error) {
	q = strings.TrimSpace(q)
	for _, s := range quantitySuffixes {
		if num, ok := strings.CutSuffix(q, s.suffix); ok {
			n, err := strconv.ParseInt(num, 10, 64)
			if err != nil {
				return 0, fmt.Errorf("memory quantity %q is not a whole number of %s", q, s.suffix)
			}
			return n * s.mult, nil
		}
	}
	n, err := strconv.ParseInt(q, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("memory quantity %q not understood", q)
	}
	return n, nil
}

const defaultMinFreeMemory = "6Gi"

// checkMemory refuses a demo install when no node has demo.minFreeMemory
// left (AC-2). A cluster that already runs the install is not asked again:
// its own pods hold the memory.
func (s *state) checkMemory(ctx context.Context) error {
	if !s.demo {
		return nil
	}
	if ok, err := s.kube.Exists(ctx, argocdNamespace, "applications.argoproj.io", s.appsApp()); err == nil && ok {
		return nil
	}
	minimum := s.values.Demo.MinFreeMemory
	if minimum == "" {
		minimum = defaultMinFreeMemory
	}
	need, err := ParseQuantity(minimum)
	if err != nil {
		return fmt.Errorf("demo.minFreeMemory: %w", err)
	}
	free, err := s.kube.FreeMemory(ctx)
	if err != nil {
		return fmt.Errorf("read node memory: %w", err)
	}
	if free < need {
		return fmt.Errorf("no node has demo.minFreeMemory (%s) of allocatable memory left after current requests (best: %d MiB); free some, or set demo.enabled to false", minimum, free>>20)
	}
	return nil
}

// PushRejectedError is a push the remote refused.
type PushRejectedError struct {
	Protected bool // a branch protection rule, so a pull request can carry it
	Err       error
}

func (e *PushRejectedError) Error() string { return e.Err.Error() }
func (e *PushRejectedError) Unwrap() error { return e.Err }

func protectedBranch(out string) bool {
	low := strings.ToLower(out)
	for _, m := range []string{"protected branch", "gh006", "gh013", "hook declined"} {
		if strings.Contains(low, m) {
			return true
		}
	}
	return false
}

// pullRequest pushes the commit to a new branch, opens a pull request and
// puts the local branch back where its upstream is, so a rerun after the
// merge starts clean (AC-5). It returns the pull request's URL.
func (s *state) pullRequest(ctx context.Context) (string, error) {
	branch := "deploy/" + s.Cluster + "-" + s.Now().UTC().Format("20060102T150405Z")
	if _, err := s.git.git(ctx, []string{"LEFTHOOK=0"}, "push", "--quiet", s.track.Remote, "HEAD:refs/heads/"+branch); err != nil {
		return "", fmt.Errorf("push %s: %w", branch, err)
	}
	if _, err := s.git.git(ctx, nil, "reset", "--keep", s.track.Remote+"/"+s.track.Branch); err != nil {
		return "", fmt.Errorf("put the local branch back on %s/%s: %w", s.track.Remote, s.track.Branch, err)
	}
	out, err := s.Run.Run(ctx, Cmd{Name: "gh", Args: []string{
		"pr", "create", "--base", s.track.Branch, "--head", branch,
		"--title", "chore(deploy): seal secrets for " + s.Cluster,
		"--body", "Sealed values for " + s.Cluster + " from make install. Merge it, pull, and run make install CLUSTER=" + s.Cluster + " again.",
	}})
	if err != nil {
		return "", fmt.Errorf("the branch %s is pushed, but gh could not open the pull request (open it yourself, merge it, pull, rerun): %w", branch, err)
	}
	return strings.TrimSpace(string(out)), nil
}
