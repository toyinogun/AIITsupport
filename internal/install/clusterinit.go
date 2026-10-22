package install

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// cluster-init (spec 0015, AC-19, Repo layout): make deploy/clusters/<name>/
// from the template, with what the repo and the live cluster can say.

// bootstrapTemplate is a cluster's bootstrap.yaml (AC-23). A test checks
// that k3sprox's committed copy is this text filled in.
const bootstrapTemplate = `# The two ArgoCD Applications for @NAME@ (spec 0015, AC-23). make install
# applies this file as is.
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: noord-@NAME@-platform
  namespace: argocd
  labels:
    app.kubernetes.io/part-of: noord-@NAME@
    noord.example/part: platform
  finalizers:
    - resources-finalizer.argocd.argoproj.io
spec:
  project: default
  source:
    repoURL: @REPO@
    targetRevision: @REV@
    path: deploy/argocd
    helm:
      valueFiles:
        - values.yaml
        - ../clusters/@NAME@/values.yaml
      valuesObject:
        part: platform
  destination:
    server: https://kubernetes.default.svc
    namespace: argocd
  syncPolicy:
    automated:
      prune: true
      selfHeal: true
    syncOptions:
      - ServerSideApply=true
      - SkipDryRunOnMissingResource=true
    retry:
      limit: 10
      backoff:
        duration: 5s
        factor: 2
        maxDuration: 5m
---
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: noord-@NAME@
  namespace: argocd
  labels:
    app.kubernetes.io/part-of: noord-@NAME@
    noord.example/part: apps
  finalizers:
    - resources-finalizer.argocd.argoproj.io
spec:
  project: default
  source:
    repoURL: @REPO@
    targetRevision: @REV@
    path: deploy/argocd
    helm:
      valueFiles:
        - values.yaml
        - ../clusters/@NAME@/values.yaml
      valuesObject:
        part: apps
  destination:
    server: https://kubernetes.default.svc
    namespace: argocd
  syncPolicy:
    automated:
      prune: true
      selfHeal: true
    syncOptions:
      - ServerSideApply=true
      - SkipDryRunOnMissingResource=true
    retry:
      limit: 10
      backoff:
        duration: 5s
        factor: 2
        maxDuration: 5m
`

// Bootstrap renders a cluster's bootstrap.yaml.
func Bootstrap(name, repoURL, revision string) []byte {
	return []byte(strings.NewReplacer("@NAME@", name, "@REPO@", repoURL, "@REV@", revision).Replace(bootstrapTemplate))
}

// commitScan is how many commits back cluster-init looks for one that has
// all four images.
const commitScan = 50

// ClusterInit creates the cluster's folder.
func ClusterInit(ctx context.Context, opts Options) error {
	s := &state{Options: opts.Defaults()}
	if err := s.initFolder(ctx); err != nil {
		return &ExitError{Code: ExitPreflight, Step: "cluster-init", Err: err}
	}
	return nil
}

func (s *state) initFolder(ctx context.Context) error {
	if !clusterName.MatchString(s.Cluster) || s.Cluster == "_template" {
		return fmt.Errorf("cluster name %q must be 2 to 32 of a-z, 0-9 and -, starting with a letter", s.Cluster)
	}
	dir := filepath.Join(s.Root, ClustersDir, s.Cluster)
	if _, err := os.Stat(dir); err == nil {
		return fmt.Errorf("%s/%s already exists; cluster-init never overwrites", ClustersDir, s.Cluster)
	}
	s.git = Git{run: s.Run}
	remote, err := s.git.RemoteURL(ctx, "origin")
	if err != nil {
		return fmt.Errorf("read the origin remote: %w", err)
	}
	repoURL, owner, err := httpsRepo(remote)
	if err != nil {
		return err
	}
	branch := s.defaultBranch(ctx)
	tag, err := s.newestImageCommit(ctx, owner, branch)
	if err != nil {
		return err
	}
	kctx, err := CurrentContext(ctx, s.Run)
	if err != nil {
		return fmt.Errorf("read the kube context: %w", err)
	}
	s.kube = Kube{run: s.Run, context: kctx}
	found, err := s.kube.Discover(ctx)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	tpl := filepath.Join(s.Root, ClustersDir, "_template")
	for _, f := range []string{valuesFile, secretsFile} {
		raw, err := os.ReadFile(filepath.Join(tpl, f)) //nolint:gosec // the repo's own template
		if err != nil {
			return fmt.Errorf("read the template %s: %w", f, err)
		}
		mode := os.FileMode(0o644)
		if f == secretsFile {
			mode = 0o600
		}
		if err := os.WriteFile(filepath.Join(dir, f), raw, mode); err != nil { //nolint:gosec // values.yaml is committed on purpose
			return err
		}
	}
	if err := os.WriteFile(filepath.Join(dir, bootstrapFile), Bootstrap(s.Cluster, repoURL, branch), 0o644); err != nil { //nolint:gosec // committed on purpose
		return err
	}
	folder := Folder{Name: s.Cluster, Dir: filepath.ToSlash(filepath.Join(ClustersDir, s.Cluster)), root: s.Root}
	sets := []struct {
		path       []string
		value, tag string
	}{
		{[]string{"cluster", "name"}, s.Cluster, "!!str"},
		{[]string{"cluster", "podCIDR"}, found.PodCIDR, "!!str"},
		{[]string{"cluster", "serviceCIDR"}, found.ServiceCIDR, "!!str"},
		{[]string{"cluster", "nodeCIDR"}, found.NodeCIDR, "!!str"},
		{[]string{"cluster", "apiServerCIDR"}, found.APIServerCIDR, "!!str"},
		{[]string{"cilium", "enabled"}, fmt.Sprint(found.Cilium), "!!bool"},
		{[]string{"cilium", "fqdnEgress"}, fmt.Sprint(found.Cilium), "!!bool"},
		{[]string{"repo", "url"}, repoURL, "!!str"},
		{[]string{"repo", "revision"}, branch, "!!str"},
		{[]string{"image", "registry"}, s.registryHost(), "!!str"},
		{[]string{"image", "owner"}, owner, "!!str"},
		{[]string{"image", "tag"}, tag, "!!str"},
		{[]string{"platform", "sealedSecrets", "enabled"}, fmt.Sprint(!found.SealedSecrets), "!!bool"},
		{[]string{"platform", "certManager", "enabled"}, fmt.Sprint(!found.CertManager), "!!bool"},
		{[]string{"platform", "cnpg", "enabled"}, fmt.Sprint(!found.CNPG), "!!bool"},
		{[]string{"platform", "tailscale", "enabled"}, fmt.Sprint(!found.Tailscale), "!!bool"},
	}
	for _, set := range sets {
		if _, err := folder.SetScalar(set.path, set.value, set.tag); err != nil {
			return err
		}
	}
	env, err := ReadEnvFile(filepath.Join(dir, secretsFile))
	if err != nil {
		return err
	}
	env.Set("KUBE_CONTEXT", kctx)
	if err := env.Write(filepath.Join(dir, secretsFile)); err != nil {
		return err
	}
	s.say("made %s/%s for kube context %s at image %s", ClustersDir, s.Cluster, kctx, short(tag))
	s.say("next: fill %s/%s/%s (the CHANGE_ME lines and your tokens), commit the folder, then make install CLUSTER=%s", ClustersDir, s.Cluster, secretsFile, s.Cluster)
	return nil
}

// defaultBranch is origin's default branch, else main.
func (s *state) defaultBranch(ctx context.Context) string {
	out, err := s.git.git(ctx, nil, "symbolic-ref", "--short", "refs/remotes/origin/HEAD")
	if err != nil {
		return "main"
	}
	return strings.TrimPrefix(out, "origin/")
}

// newestImageCommit is the newest commit on the default branch with all four
// images in the registry (AC-19).
func (s *state) newestImageCommit(ctx context.Context, owner, branch string) (string, error) {
	_, _ = s.git.git(ctx, nil, "fetch", "--quiet", "origin")
	out, err := s.git.git(ctx, nil, "log", fmt.Sprintf("-n%d", commitScan), "--format=%H", "origin/"+branch)
	if err != nil {
		return "", fmt.Errorf("read the commits of origin/%s: %w", branch, err)
	}
	for sha := range strings.FieldsSeq(out) {
		all := true
		for _, name := range Images {
			ok, err := s.Registry.Exists(ctx, s.registryHost(), owner+"/aiitsupport-"+name, sha)
			if err != nil {
				return "", fmt.Errorf("check image %s: %w", name, err)
			}
			if !ok {
				all = false
				break
			}
		}
		if all {
			return sha, nil
		}
	}
	return "", fmt.Errorf("none of the last %d commits on origin/%s has all four public images in %s/%s; wait for CI, or make the packages public", commitScan, branch, s.registryHost(), owner)
}

const defaultRegistry = "ghcr.io"

func (s *state) registryHost() string {
	if s.RegistryHost != "" {
		return s.RegistryHost
	}
	return defaultRegistry
}

// httpsRepo turns an origin URL (https or scp style ssh) into its https form
// and the lowercased owner.
func httpsRepo(remote string) (repoURL, owner string, err error) {
	r := strings.TrimSpace(remote)
	if rest, ok := strings.CutPrefix(r, "git@"); ok {
		host, path, _ := strings.Cut(rest, ":")
		r = "https://" + host + "/" + path
	}
	u, perr := url.Parse(r)
	if perr != nil || u.Host == "" {
		return "", "", fmt.Errorf("origin %q is not a repository URL", remote)
	}
	path := strings.Trim(strings.TrimSuffix(u.Path, ".git"), "/")
	parts := strings.Split(path, "/")
	if len(parts) < 2 {
		return "", "", fmt.Errorf("origin %q has no owner and repository", remote)
	}
	return "https://" + u.Host + "/" + path + ".git", strings.ToLower(parts[0]), nil
}

// Found is what cluster-init reads from the live cluster.
type Found struct {
	PodCIDR, ServiceCIDR, NodeCIDR, APIServerCIDR string
	Cilium                                        bool
	SealedSecrets, CertManager, CNPG, Tailscale   bool // already installed
}

// Discover reads the ranges and the operators the cluster already has.
func (k Kube) Discover(ctx context.Context) (Found, error) {
	var f Found
	nodes, err := k.kubectl(ctx, nil, "get", "nodes", "-o", "json")
	if err != nil {
		return f, fmt.Errorf("read nodes: %w", err)
	}
	var nl struct {
		Items []struct {
			Spec struct {
				PodCIDRs []string `json:"podCIDRs"`
			} `json:"spec"`
			Status struct {
				Addresses []struct{ Type, Address string } `json:"addresses"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal(nodes, &nl); err != nil {
		return f, fmt.Errorf("parse nodes: %w", err)
	}
	var nodeIPs, podNets []netip.Prefix
	for _, n := range nl.Items {
		for _, a := range n.Status.Addresses {
			if a.Type == "InternalIP" {
				if ip, err := netip.ParseAddr(a.Address); err == nil {
					nodeIPs = append(nodeIPs, netip.PrefixFrom(ip, ip.BitLen()))
				}
			}
		}
		for _, c := range n.Spec.PodCIDRs {
			if p, err := netip.ParsePrefix(c); err == nil && p.Addr().Is4() {
				podNets = append(podNets, p)
			}
		}
	}
	if len(nodeIPs) == 0 || len(podNets) == 0 {
		return f, errors.New("the nodes report no InternalIP or podCIDR; set cluster.nodeCIDR and cluster.podCIDR by hand")
	}
	f.NodeCIDR = cover(nodeIPs).String()
	f.PodCIDR = widen(cover(podNets), 16).String()

	svc, err := k.kubectl(ctx, nil, "get", "service", "kubernetes", "-n", "default", "-o", "jsonpath={.spec.clusterIP}")
	if err != nil {
		return f, fmt.Errorf("read the kubernetes Service: %w", err)
	}
	ip, err := netip.ParseAddr(strings.TrimSpace(string(svc)))
	if err != nil {
		return f, fmt.Errorf("the kubernetes Service has no ClusterIP: %w", err)
	}
	f.ServiceCIDR = widen(netip.PrefixFrom(ip, ip.BitLen()), 16).String()

	sliceOut, err := k.kubectl(ctx, nil, "get", "endpointslices.discovery.k8s.io", "-n", "default", "-l", "kubernetes.io/service-name=kubernetes", "-o", "json")
	if err != nil {
		return f, fmt.Errorf("read the API server address: %w", err)
	}
	var sl struct {
		Items []struct {
			Endpoints []struct {
				Addresses []string `json:"addresses"`
			} `json:"endpoints"`
		} `json:"items"`
	}
	if err := json.Unmarshal(sliceOut, &sl); err != nil {
		return f, fmt.Errorf("parse the API server address: %w", err)
	}
	var api []netip.Prefix
	for _, it := range sl.Items {
		for _, e := range it.Endpoints {
			for _, a := range e.Addresses {
				if ip, err := netip.ParseAddr(a); err == nil {
					api = append(api, netip.PrefixFrom(ip, ip.BitLen()))
				}
			}
		}
	}
	if len(api) == 0 {
		return f, errors.New("no API server address found; set cluster.apiServerCIDR by hand")
	}
	f.APIServerCIDR = cover(api).String()

	for _, probe := range []struct {
		dst            *bool
		ns, kind, name string
	}{
		{&f.Cilium, "kube-system", "daemonset", "cilium"},
		{&f.SealedSecrets, "kube-system", "deployment", "sealed-secrets-controller"},
		{&f.CertManager, "", "crd", "certificates.cert-manager.io"},
		{&f.CNPG, "", "crd", "clusters.postgresql.cnpg.io"},
		{&f.Tailscale, "", "crd", "proxyclasses.tailscale.com"},
	} {
		if *probe.dst, err = k.Exists(ctx, probe.ns, probe.kind, probe.name); err != nil {
			return f, fmt.Errorf("look for %s: %w", probe.name, err)
		}
	}
	return f, nil
}

// cover is the smallest prefix holding all of ps.
func cover(ps []netip.Prefix) netip.Prefix {
	out := ps[0]
	for _, p := range ps[1:] {
		for out.Bits() > 0 && (!out.Contains(p.Addr()) || out.Bits() > p.Bits()) {
			out = netip.PrefixFrom(out.Addr(), out.Bits()-1).Masked()
		}
	}
	return out.Masked()
}

// widen grows a prefix to at most bits long, never shrinking it.
func widen(p netip.Prefix, bits int) netip.Prefix {
	if p.Bits() <= bits {
		return p
	}
	return netip.PrefixFrom(p.Addr(), bits).Masked()
}
