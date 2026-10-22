package install

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/toyinogun/AIITsupport/internal/config"
)

// The day two commands (spec 0015, AC-19 to AC-22).

var sealingKeyLabel = "sealedsecrets.bitnami.com/sealed-secrets-key"

// preflightLight opens the folder, its values, and the git and kube
// handles, and checks the clone as install does, without the tools, images
// and cluster checks.
func (s *state) preflightLight() error {
	var err error
	if s.folder, err = OpenFolder(s.Root, s.Cluster); err != nil {
		return err
	}
	if s.values, err = s.folder.Values(); err != nil {
		return err
	}
	s.git = Git{run: s.Run}
	if s.env, err = ReadEnvFile(s.folder.path(secretsFile)); err != nil {
		return err
	}
	if s.cfg, err = config.LoadInstall(s.env.Lookup); err != nil {
		return err
	}
	s.kube = Kube{run: s.Run, context: s.cfg.KubeContext}
	return nil
}

// change sets one values.yaml scalar, commits it and pushes (the AC-5 push
// rules). It reports whether anything changed.
func (s *state) change(ctx context.Context, keyPath []string, value, tag, message string) (bool, error) {
	if err := s.checkGit(ctx); err != nil {
		return false, err
	}
	changed, err := s.folder.SetScalar(keyPath, value, tag)
	if err != nil || !changed {
		return false, err
	}
	if _, err := s.git.Commit(ctx, s.folder.Dir, message); err != nil {
		return true, err
	}
	if err := s.git.Push(ctx, s.track); err != nil {
		return true, fmt.Errorf("the push was rejected: %w", err)
	}
	return true, nil
}

// Release points a cluster at the images built for sha (AC-19). It writes
// nothing unless all four images exist.
func Release(ctx context.Context, opts Options, sha string) error {
	s := &state{Options: opts.Defaults()}
	if err := s.releasePrep(ctx, &sha); err != nil {
		return &ExitError{Code: ExitPreflight, Step: "release", Err: err}
	}
	if sha == s.values.Image.Tag {
		s.say("already at %s, nothing to do", short(sha))
		return nil
	}
	if _, err := s.change(ctx, []string{"image", "tag"}, sha, "!!str", "chore(release): "+s.Cluster+" to "+short(sha)); err != nil {
		return &ExitError{Code: ExitFailed, Step: "release", Err: err}
	}
	s.say("released %s; run make install CLUSTER=%s to roll it out", short(sha), s.Cluster)
	return nil
}

func (s *state) releasePrep(ctx context.Context, sha *string) error {
	var err error
	if s.folder, err = OpenFolder(s.Root, s.Cluster); err != nil {
		return err
	}
	if s.values, err = s.folder.Values(); err != nil {
		return err
	}
	s.git = Git{run: s.Run}
	full, err := s.git.git(ctx, nil, "rev-parse", "--verify", *sha+"^{commit}")
	if err != nil || !fullSHA.MatchString(full) {
		return fmt.Errorf("%q is not a commit in this clone", *sha)
	}
	*sha = full
	var missing []string
	for _, name := range Images {
		ok, err := s.Registry.Exists(ctx, s.values.Image.Registry, s.values.Image.Owner+"/aiitsupport-"+name, full)
		if err != nil {
			return fmt.Errorf("check image %s: %w", name, err)
		}
		if !ok {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("no public %s image for %s; wait for CI on the default branch", strings.Join(missing, ", "), short(full))
	}
	return nil
}

var fullSHA = regexp.MustCompile(`^[0-9a-f]{40}$`)

// Pause stops the agent and the seed Jobs (AC-20) and waits for the agent
// pod to go.
func Pause(ctx context.Context, opts Options) error {
	s := &state{Options: opts.Defaults()}
	if err := s.preflightLight(); err != nil {
		return &ExitError{Code: ExitPreflight, Step: "pause", Err: err}
	}
	ctx, cancel := context.WithTimeout(ctx, s.cfg.InstallTimeout)
	defer cancel()
	if _, err := s.change(ctx, []string{"paused"}, "true", "!!bool", "chore(deploy): pause "+s.Cluster); err != nil {
		return &ExitError{Code: ExitFailed, Step: "pause", Err: err}
	}
	for {
		_ = s.kube.Refresh(ctx, s.appsApp())
		_ = s.kube.Refresh(ctx, "noord-"+componentAgent)
		out, err := s.kube.kubectl(ctx, nil, "-n", "noord-agent", "get", "pods", "-l", "app.kubernetes.io/name=agent", "-o", "name")
		if err == nil && len(strings.TrimSpace(string(out))) == 0 {
			s.say("paused; the agent is stopped")
			return nil
		}
		select {
		case <-ctx.Done():
			return &ExitError{Code: ExitFailed, Step: "pause", Err: errors.New("the agent pod is still there at INSTALL_TIMEOUT")}
		case <-time.After(s.Poll):
		}
	}
}

// Resume clears paused and bumps seedRun so every seed step runs again.
func Resume(ctx context.Context, opts Options) error {
	s := &state{Options: opts.Defaults()}
	if err := s.preflightLight(); err != nil {
		return &ExitError{Code: ExitPreflight, Step: "resume", Err: err}
	}
	if err := s.checkGit(ctx); err != nil {
		return &ExitError{Code: ExitPreflight, Step: "resume", Err: err}
	}
	if _, err := s.folder.SetScalar([]string{"seedRun"}, strconv.Itoa(s.values.SeedRun+1), "!!int"); err != nil {
		return &ExitError{Code: ExitFailed, Step: "resume", Err: err}
	}
	if _, err := s.change(ctx, []string{"paused"}, "false", "!!bool", "chore(deploy): resume "+s.Cluster); err != nil {
		return &ExitError{Code: ExitFailed, Step: "resume", Err: err}
	}
	s.say("resumed; every seed step runs once more. Check with make smoke CLUSTER=%s", s.Cluster)
	return nil
}

var clusterNamespaces = []string{"noord-agent", "noord-directory", "noord-client"}

// Uninstall deletes the cluster's Applications and what they own (AC-21).
// Bucket backups, ~/.noord/<cluster>/ and ArgoCD stay.
func Uninstall(ctx context.Context, opts Options) error {
	s := &state{Options: opts.Defaults()}
	if err := s.preflightLight(); err != nil {
		return &ExitError{Code: ExitPreflight, Step: "uninstall", Err: err}
	}
	if err := s.uninstallGuard(ctx); err != nil {
		return &ExitError{Code: ExitPreflight, Step: "uninstall", Err: err}
	}
	ctx, cancel := context.WithTimeout(ctx, s.cfg.InstallTimeout)
	defer cancel()
	for _, app := range []string{s.appsApp(), s.platformApp()} {
		s.say("deleting %s", app)
		if _, err := s.kube.kubectl(ctx, nil, "-n", argocdNamespace, "delete", "applications.argoproj.io", app, "--ignore-not-found", "--wait=true"); err != nil {
			return &ExitError{Code: ExitFailed, Step: "uninstall", Err: err}
		}
	}
	for _, ns := range clusterNamespaces {
		if ok, err := s.kube.Exists(ctx, "", "namespace", ns); err == nil && ok {
			if _, err := s.kube.kubectl(ctx, nil, "delete", "pvc", "--all", "-n", ns, "--ignore-not-found", "--wait=false"); err != nil {
				return &ExitError{Code: ExitFailed, Step: "uninstall", Err: err}
			}
		}
	}
	for {
		var left []string
		for _, ns := range clusterNamespaces {
			if ok, err := s.kube.Exists(ctx, "", "namespace", ns); err != nil || ok {
				left = append(left, ns)
			}
		}
		if len(left) == 0 {
			s.say("uninstalled; backups, ~/.noord/%s and ArgoCD stay", s.Cluster)
			return nil
		}
		select {
		case <-ctx.Done():
			return &ExitError{Code: ExitFailed, Step: "uninstall", Err: fmt.Errorf("namespaces still there at INSTALL_TIMEOUT: %s", strings.Join(left, ", "))}
		case <-time.After(s.Poll):
		}
	}
}

// uninstallGuard asks for the cluster name and refuses an Application that
// another Application owns.
func (s *state) uninstallGuard(ctx context.Context) error {
	out, err := s.kube.kubectl(ctx, nil, "-n", argocdNamespace, "get", "applications.argoproj.io", s.appsApp(), "-o", "json", "--ignore-not-found")
	if err != nil {
		return fmt.Errorf("read %s: %w", s.appsApp(), err)
	}
	if len(strings.TrimSpace(string(out))) > 0 {
		var app struct {
			Metadata struct {
				Labels map[string]string `json:"labels"`
			} `json:"metadata"`
		}
		if err := json.Unmarshal(out, &app); err != nil {
			return fmt.Errorf("parse %s: %w", s.appsApp(), err)
		}
		if owner := app.Metadata.Labels["argocd.argoproj.io/instance"]; owner != "" {
			return fmt.Errorf("%s is owned by the ArgoCD Application %s; remove its pointer there first, or ArgoCD makes it again", s.appsApp(), owner)
		}
	}
	if s.In == nil {
		return errors.New("no input to read the cluster name from")
	}
	s.say("this deletes the Applications and every volume of %s. Type the cluster name to go on:", s.Cluster)
	line, _ := bufio.NewReader(s.In).ReadString('\n')
	if strings.TrimSpace(line) != s.Cluster {
		return errors.New("the name did not match; nothing was deleted")
	}
	return nil
}

// sealingKeyNames lists the controller's key Secrets by name.
func (s *state) liveKeys(ctx context.Context) ([]byte, []string, error) {
	ss := s.values.Platform.SealedSecrets
	out, err := s.kube.kubectl(ctx, nil, "-n", ss.Namespace, "get", "secret", "-l", sealingKeyLabel, "-o", "yaml")
	if err != nil {
		return nil, nil, err
	}
	names, err := keyNames(out)
	return out, names, err
}

func keyNames(raw []byte) ([]string, error) {
	var list struct {
		Items []struct {
			Metadata struct {
				Name string `yaml:"name"`
			} `yaml:"metadata"`
		} `yaml:"items"`
	}
	if err := yaml.Unmarshal(raw, &list); err != nil {
		return nil, fmt.Errorf("parse the sealing keys: %w", err)
	}
	var names []string
	for _, it := range list.Items {
		names = append(names, it.Metadata.Name)
	}
	slices.Sort(names)
	return names, nil
}

func (s *state) keyFile() string { return filepath.Join(s.certDir(), "sealing-key.yaml") }

// SealKeyBackup writes the controller's keys outside the repo (AC-22).
func SealKeyBackup(ctx context.Context, opts Options) error {
	s := &state{Options: opts.Defaults()}
	if err := s.preflightLight(); err != nil {
		return &ExitError{Code: ExitPreflight, Step: "seal-key-backup", Err: err}
	}
	raw, names, err := s.liveKeys(ctx)
	if err != nil || len(names) == 0 {
		return &ExitError{Code: ExitFailed, Step: "seal-key-backup", Err: errors.Join(errors.New("no sealing keys found"), err)}
	}
	if err := os.MkdirAll(s.certDir(), 0o700); err != nil {
		return &ExitError{Code: ExitFailed, Step: "seal-key-backup", Err: err}
	}
	if err := os.WriteFile(s.keyFile(), raw, 0o600); err != nil {
		return &ExitError{Code: ExitFailed, Step: "seal-key-backup", Err: err}
	}
	s.say("wrote %d sealing keys to %s", len(names), s.keyFile())
	return nil
}

// keyNotice reminds you of the key backup, or warns that it is out of date.
// It never fails the install.
func (s *state) keyNotice(ctx context.Context) {
	_, live, err := s.liveKeys(ctx)
	if err != nil {
		return
	}
	saved, err := os.ReadFile(s.keyFile())
	if err != nil {
		s.say("no sealing key backup yet; run make seal-key-backup CLUSTER=%s and keep the file safe", s.Cluster)
		return
	}
	if have, err := keyNames(saved); err != nil || !slices.Equal(have, live) {
		s.say("warning: the sealing keys differ from your backup; run make seal-key-backup CLUSTER=%s again", s.Cluster)
	}
}
