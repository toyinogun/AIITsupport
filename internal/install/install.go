package install

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/toyinogun/AIITsupport/internal/config"
)

// Exit codes (spec 0015, Interface surface).
const (
	ExitFailed    = 1 // a step failed, or the wait timed out
	ExitPreflight = 2 // nothing was changed
)

// ExitError carries the exit code and the step that failed.
type ExitError struct {
	Code int
	Step string
	Err  error
}

func (e *ExitError) Error() string { return e.Step + ": " + e.Err.Error() }

func (e *ExitError) Unwrap() error { return e.Err }

// Options is one run of the installer.
type Options struct {
	Root     string // the clone's root
	Cluster  string
	Out      io.Writer
	Run      Runner
	Registry Registry
	Home     string // holds .noord/<cluster>/; the user's home in production
	TempDir  string // for the sealing certificate
	Poll     time.Duration
	LookPath func(string) (string, error)
	Now      func() time.Time // names the smoke Job
}

// Defaults fills what a production run leaves unset.
func (o Options) Defaults() Options {
	if o.Run == nil {
		o.Run = ExecRunner{Dir: o.Root}
	}
	if o.Registry.Client == nil {
		o.Registry.Client = &http.Client{Timeout: 20 * time.Second}
	}
	if o.Poll == 0 {
		o.Poll = 5 * time.Second
	}
	if o.LookPath == nil {
		o.LookPath = exec.LookPath
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return o
}

// tools are the commands the installer runs (AC-2).
var tools = []string{"kubectl", "helm", "kubeseal", "git"}

// state is what the steps share.
type state struct {
	Options
	folder    Folder
	values    Values
	env       *EnvFile
	cfg       config.Install
	table     Table
	kube      Kube
	git       Git
	demo      bool
	conds     Conditions
	comps     []string
	files     map[string]string // the cluster's cert dir, by file name, for sealing
	bootstrap []byte
	track     Tracking
}

// Run installs or updates the cluster: preflight, platform, certs,
// generate, seal, commit and push, apply, wait, smoke, report. Each step is a no op
// when its work is already done, so a rerun with unchanged inputs commits
// nothing and restarts nothing (AC-8).
func Run(ctx context.Context, opts Options) error {
	s := &state{Options: opts.Defaults()}
	if err := s.preflight(ctx); err != nil {
		return &ExitError{Code: ExitPreflight, Step: "preflight", Err: err}
	}
	// One INSTALL_TIMEOUT bounds the whole run, both waits included (AC-1).
	ctx, cancel := context.WithTimeout(ctx, s.cfg.InstallTimeout)
	defer cancel()
	steps := []struct {
		name string
		fn   func(context.Context) error
	}{
		{"platform", s.platform},
		{"certs", s.certs},
		{"generate", s.generate},
		{"seal", s.seal},
		{"commit and push", s.commitAndPush},
		{"apply", s.apply},
		{"overlays", s.overlays},
		{"wait", s.wait},
		{"smoke", s.smoke},
	}
	for _, step := range steps {
		s.say("%s", step.name)
		if err := step.fn(ctx); err != nil {
			return &ExitError{Code: ExitFailed, Step: step.name, Err: err}
		}
	}
	s.report()
	return nil
}

func (s *state) say(format string, args ...any) {
	_, _ = fmt.Fprintf(s.Out, "install %s: "+format+"\n", append([]any{s.Cluster}, args...)...)
}

func (s *state) preflight(ctx context.Context) error {
	var err error
	if s.folder, err = OpenFolder(s.Root, s.Cluster); err != nil {
		return err
	}
	if s.values, err = s.folder.Values(); err != nil {
		return err
	}
	if s.env, err = ReadEnvFile(s.folder.path(secretsFile)); err != nil {
		return fmt.Errorf("%w (copy %s/_template/%s and fill it)", err, ClustersDir, secretsFile)
	}
	if info, err := os.Stat(s.folder.path(secretsFile)); err == nil && info.Mode().Perm()&0o077 != 0 {
		s.say("warning: %s/%s is readable by others (mode %o); it is set to 0600 when the installer next writes it, or run chmod 600 now",
			s.folder.Dir, secretsFile, info.Mode().Perm())
	}
	if s.cfg, err = config.LoadInstall(s.env.Lookup); err != nil {
		return err
	}
	if s.table, err = LoadTable(filepath.Join(s.Root, TablePath)); err != nil {
		return err
	}
	if s.bootstrap, err = os.ReadFile(s.folder.path(bootstrapFile)); err != nil {
		return fmt.Errorf("read %s/%s: %w", s.folder.Dir, bootstrapFile, err)
	}
	s.conds = s.values.Conditions()
	s.demo = s.conds.Demo
	s.comps = components(s.demo)
	s.kube = Kube{run: s.Run, context: s.cfg.KubeContext}
	s.git = Git{run: s.Run}

	var problems []error
	for _, check := range []func(context.Context) error{s.checkTools, s.checkSettings, s.checkContext, s.checkCluster, s.checkGit, s.checkImages} {
		if err := check(ctx); err != nil {
			problems = append(problems, err)
			if errors.Is(err, errStop) {
				break
			}
		}
	}
	return errors.Join(problems...)
}

// errStop ends the preflight early: later checks would only repeat it.
var errStop = errors.New("")

func stop(err error) error { return fmt.Errorf("%w%w", err, errStop) }

func (s *state) checkTools(context.Context) error {
	var missing []string
	for _, t := range tools {
		if _, err := s.LookPath(t); err != nil {
			missing = append(missing, t)
		}
	}
	if len(missing) > 0 {
		return stop(fmt.Errorf("install %s first; the installer runs it", strings.Join(missing, ", ")))
	}
	return nil
}

func (s *state) checkSettings(context.Context) error {
	var errs []error
	if !s.demo && strings.TrimSpace(s.values.CA.Certificate) == "" {
		errs = append(errs, errors.New("ca.certificate is empty; with demo off it must hold your directory's CA certificate"))
	}
	for _, name := range s.table.Required(s.comps, s.conds) {
		if s.env.Get(name) == "" {
			errs = append(errs, fmt.Errorf("%s is not set in secrets.env", name))
		}
	}
	if err := s.checkGraphCert(); err != nil {
		errs = append(errs, err)
	}
	if s.conds.Backup && (s.values.Backup.Endpoint == "" || s.values.Backup.Bucket == "") {
		errs = append(errs, errors.New("backup.enabled needs backup.endpoint and backup.bucket in values.yaml"))
	}
	for _, name := range slices.Sorted(slices.Values(s.env.Names())) {
		if s.env.Get(name) == placeholder {
			errs = append(errs, fmt.Errorf("%s in secrets.env still holds %s", name, placeholder))
		}
	}
	return errors.Join(errs...)
}

// checkGraphCert refuses a Graph certificate and secret at once, which the
// tool server would refuse at boot (AC-24), and a certificate file it
// cannot read.
func (s *state) checkGraphCert() error {
	const certSetting, secretSetting = "GRAPH_CERT_FILE", "GRAPH_CLIENT_SECRET" //nolint:gosec // G101: setting names, not credentials
	if s.env.Get(certSetting) == "" {
		return nil
	}
	if s.env.Get(secretSetting) != "" {
		return fmt.Errorf("set %s or %s in secrets.env, not both", certSetting, secretSetting)
	}
	if _, err := readLocal(certSetting, s.env.Get(certSetting), s.Root); err != nil {
		return err
	}
	return nil
}

func (s *state) checkContext(ctx context.Context) error {
	current, err := CurrentContext(ctx, s.Run)
	if err != nil {
		return stop(fmt.Errorf("read the kube context: %w", err))
	}
	if current != s.cfg.KubeContext {
		return stop(errors.New("the current kube context is not KUBE_CONTEXT from secrets.env; switch with kubectl config use-context"))
	}
	return nil
}

func (s *state) checkCluster(ctx context.Context) error {
	var errs []error
	if ok, err := s.kube.HasDefaultStorageClass(ctx); err != nil {
		errs = append(errs, fmt.Errorf("read StorageClasses: %w", err))
	} else if !ok {
		errs = append(errs, errors.New("the cluster has no default StorageClass"))
	}
	if ok, err := s.kube.Exists(ctx, argocdNamespace, "deployment", argocdServerName); err != nil {
		errs = append(errs, fmt.Errorf("look for ArgoCD: %w", err))
	} else if !ok {
		errs = append(errs, errors.New("ArgoCD is not installed in the argocd namespace; installing it is not built yet"))
	}
	return errors.Join(errs...)
}

func (s *state) checkGit(ctx context.Context) error {
	var errs []error
	changed, err := s.git.Changed(ctx)
	if err != nil {
		return fmt.Errorf("read git status: %w", err)
	}
	if bad := Unexpected(changed, s.folder.Dir); len(bad) > 0 {
		errs = append(errs, fmt.Errorf("the git tree has changes the installer would not commit (%d paths, first %s); "+
			"commit, stash, or remove them (in %s only values.yaml and sealed-values.yaml may differ)", len(bad), bad[0], s.folder.Dir))
	}
	tr, err := s.git.Track(ctx)
	upstream := tr.Remote + "/" + tr.Branch
	switch {
	case err != nil:
		errs = append(errs, err)
	case tr.Branch == "":
		errs = append(errs, errors.New("the branch has no upstream; push it with git push -u first"))
	case tr.Branch != s.values.Repo.Revision:
		errs = append(errs, fmt.Errorf("the branch tracks %s, but ArgoCD follows repo.revision %s; check out %s", upstream, s.values.Repo.Revision, s.values.Repo.Revision))
	case tr.Behind > 0:
		errs = append(errs, fmt.Errorf("the branch is %d commits behind %s; pull first", tr.Behind, upstream))
	case tr.Ahead > 0:
		errs = append(errs, fmt.Errorf("the branch has %d commits not on %s; push them first, so ArgoCD sees what you see", tr.Ahead, upstream))
	}
	if err == nil && tr.Branch != "" {
		remoteURL, uerr := s.git.RemoteURL(ctx, tr.Remote)
		switch {
		case uerr != nil:
			errs = append(errs, fmt.Errorf("read the %s remote: %w", tr.Remote, uerr))
		case !sameRepo(remoteURL, s.values.Repo.URL):
			errs = append(errs, fmt.Errorf("the %s remote is not repo.url in values.yaml; ArgoCD would never see the push", tr.Remote))
		}
	}
	s.track = tr
	return errors.Join(errs...)
}

func (s *state) checkImages(ctx context.Context) error {
	var missing []string
	for _, name := range Images {
		repo := s.values.Image.Owner + "/aiitsupport-" + name
		ok, err := s.Registry.Exists(ctx, s.values.Image.Registry, repo, s.values.Image.Tag)
		if err != nil {
			return fmt.Errorf("check image %s: %w", name, err)
		}
		if !ok {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("image.tag %s has no public %s image in %s/%s; wait for CI on the default branch, or make the packages public",
			s.values.Image.Tag, strings.Join(missing, ", "), s.values.Image.Registry, s.values.Image.Owner)
	}
	return nil
}

// platform applies noord-<cluster>-platform and waits for it (AC-4). With
// no operator enabled it holds only the AppProject.
func (s *state) platform(ctx context.Context) error {
	if err := s.kube.ApplyPart(ctx, s.bootstrap, partPlatform); err != nil {
		return err
	}
	rev, err := s.git.Head(ctx)
	if err != nil {
		return err
	}
	return s.waitApps(ctx, s.platformApps(), rev, nil)
}

func (s *state) platformApp() string { return "noord-" + s.Cluster + "-platform" }

// platformApps are the platform Application and each enabled operator's
// own, which the parent's health does not include.
func (s *state) platformApps() []string {
	return append([]string{s.platformApp()}, s.operatorApps()...)
}

// operatorApps are the enabled operators' Applications. Each follows a
// pinned chart version, so its revision is that version, never a commit:
// the wait checks only that it is Synced and Healthy.
func (s *state) operatorApps() []string {
	var apps []string
	if s.values.Platform.BarmanPlugin.Enabled {
		apps = append(apps, "noord-"+s.Cluster+"-barman")
	}
	return apps
}

func (s *state) appsApp() string { return "noord-" + s.Cluster }

func (s *state) generate(ctx context.Context) error {
	filled, err := s.table.Generate(ctx, s.env, s.kube, s.comps, s.conds)
	if len(filled) > 0 {
		if werr := s.env.Write(s.folder.path(secretsFile)); werr != nil {
			return errors.Join(err, werr)
		}
		s.say("generated or recovered %s", strings.Join(filled, ", "))
	}
	return err
}

func (s *state) seal(ctx context.Context) error {
	plain, err := s.table.Build(s.env, s.sources(), s.comps, s.conds)
	if err != nil {
		return err
	}
	old, err := s.folder.ReadSealed()
	if err != nil {
		return err
	}
	ss := s.values.Platform.SealedSecrets
	sealer := &Sealer{run: s.Run, context: s.cfg.KubeContext, namespace: ss.Namespace, name: ss.Name}
	cleanup, err := sealer.FetchCert(ctx, s.TempDir)
	if err != nil {
		return err
	}
	defer cleanup()
	next, plan, err := s.table.Reseal(ctx, plain, old, s.kube, sealer)
	if err != nil {
		return err
	}
	written, err := s.folder.WriteSealed(next)
	if err != nil {
		return err
	}
	s.say("%d values kept, %d sealed, %d dropped (file changed: %t)", plan.Kept, plan.Resealed, plan.Dropped, written)
	return nil
}

// sources are the values Build reads besides secrets.env.
func (s *state) sources() Sources {
	return Sources{Files: s.files, Hosts: map[string]string{hostReset: s.values.ResetHost()}, Root: s.Root}
}

func (s *state) commitAndPush(ctx context.Context) error {
	committed, err := s.git.Commit(ctx, s.folder.Dir, "chore(deploy): seal secrets for "+s.Cluster)
	if err != nil {
		return err
	}
	if !committed {
		s.say("nothing changed, no commit")
		return nil
	}
	if err := s.git.Push(ctx, s.track); err != nil {
		return fmt.Errorf("the push was rejected, nothing was applied: %w", err)
	}
	s.say("committed and pushed")
	return nil
}

func (s *state) apply(ctx context.Context) error {
	return s.kube.ApplyPart(ctx, s.bootstrap, partApps)
}

// appNames are the Applications of this install, bootstrap ones first.
func (s *state) appNames() []string {
	names := append(s.platformApps(), s.appsApp())
	for _, c := range s.comps {
		names = append(names, "noord-"+c)
	}
	return names
}

func (s *state) wait(ctx context.Context) error {
	rev, err := s.git.Head(ctx)
	if err != nil {
		return err
	}
	var want []string
	for _, name := range s.table.Active(s.comps, s.conds) {
		want = append(want, s.table.Secrets[name].Namespace+"/"+name)
	}
	return s.waitApps(ctx, s.appNames(), rev, want)
}

// waitApps polls until every app is Synced and Healthy at rev and every
// wanted SealedSecret is Synced, or INSTALL_TIMEOUT passes. It asks ArgoCD
// to refresh any app still on another revision.
// A failed read (an API blip, a CRD not there yet) is kept and retried; at
// the deadline the last known pending list and the last error are named.
func (s *state) waitApps(ctx context.Context, apps []string, rev string, sealed []string) error {
	var lastPending []string
	var lastErr error
	for {
		pending, err := s.pending(ctx, apps, rev, sealed)
		switch {
		case err != nil:
			lastErr = err
		case len(pending) == 0:
			return nil
		default:
			lastPending, lastErr = pending, nil
		}
		select {
		case <-ctx.Done():
			msg := fmt.Sprintf("not ready within INSTALL_TIMEOUT (%s): %s", s.cfg.InstallTimeout, strings.Join(lastPending, "; "))
			if lastErr != nil {
				return fmt.Errorf("%s (last read failed: %w)", msg, lastErr)
			}
			return errors.New(msg)
		case <-time.After(s.Poll):
		}
	}
}

// pending lists what is not ready yet, in words.
func (s *state) pending(ctx context.Context, apps []string, rev string, sealed []string) ([]string, error) {
	status, err := s.kube.Applications(ctx, apps)
	if err != nil {
		return nil, err
	}
	var out []string
	operators := s.operatorApps()
	for _, name := range apps {
		a, ok := status[name]
		switch {
		case !ok:
			out = append(out, "Application "+name+" missing")
		case a.Revision != rev && !slices.Contains(operators, name):
			out = append(out, "Application "+name+" not at "+short(rev))
			if err := s.kube.Refresh(ctx, name); err != nil {
				return nil, err
			}
		case a.Sync != "Synced" || a.Health != "Healthy":
			out = append(out, fmt.Sprintf("Application %s %s and %s", name, a.Sync, a.Health))
		}
	}
	if len(sealed) == 0 {
		return out, nil
	}
	synced, err := s.kube.SealedSecretsSynced(ctx, s.Cluster)
	if err != nil {
		return nil, err
	}
	for _, name := range sealed {
		if !synced[name] {
			out = append(out, "SealedSecret "+name+" not Synced")
		}
	}
	return out, nil
}

func short(rev string) string {
	if len(rev) > 7 {
		return rev[:7]
	}
	return rev
}

func (s *state) report() {
	s.say("done; every Application is Synced and Healthy and the smoke check passed")
	if s.cfg.Tailnet != "" {
		if s.demo {
			s.say("the helpdesk: https://%s.%s", s.values.HelpdeskHost(), s.cfg.Tailnet)
		}
		if s.conds.Reset {
			s.say("the reset page: https://%s.%s", s.values.ResetHost(), s.cfg.Tailnet)
		}
	}
	s.say("the kill switch starts read only; to allow account changes run:")
	s.say("  kubectl -n noord-agent patch configmap noord-kill-switch --type merge -p '{\"data\":{\"mode\":\"read_write\"}}'")
}
