package install

import (
	"context"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/toyinogun/AIITsupport/internal/seed/certs"
)

// The demo directory's parts (spec 0015, AC-10).
const (
	componentDirectory = "directory"
	directoryNamespace = "noord-directory"
	// componentClient is the demo helpdesk: Zammad and its seed (AC-10).
	componentClient = "client"
	overlaysSecret  = "noord-seed-overlays" //nolint:gosec // a Secret name, not a credential
	// renewBefore is how close to expiry the Samba server certificate is
	// made again.
	renewBefore = 60 * 24 * time.Hour
)

// overlayFiles are the optional, gitignored seed overlays (real Slack test
// emails, a real MFA phone): repo path to the key in noord-seed-overlays.
var overlayFiles = []struct{ path, key string }{
	{"seed/directory/overlay.local.yaml", "overlay.local.yaml"},
	{"seed/entra/demo.local.yaml", "demo.local.yaml"},
}

// components are the parts this install renders: the agent stack, plus the
// demo directory and helpdesk.
func components(demo bool) []string {
	if demo {
		return []string{componentAgent, componentDirectory, componentClient}
	}
	return []string{componentAgent}
}

// certDir is ~/.noord/<cluster>/: the CA key and the server pair live only
// there, never in the repo (AC-6).
func (s *state) certDir() string { return filepath.Join(s.Home, ".noord", s.Cluster) }

// certs makes the demo directory's CA and Samba server certificate in
// ~/.noord/<cluster>/ when absent, makes the server certificate again when
// it is within 60 days of expiry, writes the CA certificate into values.yaml,
// and loads the server pair for sealing. With demo off, ca.certificate is
// yours (preflight checked it is set) and nothing is loaded.
func (s *state) certs(context.Context) error {
	if !s.demo {
		s.say("demo off, keeping your ca.certificate")
		return nil
	}
	dir := s.certDir()
	if err := renewServerCert(dir, time.Now()); err != nil {
		return err
	}
	res, err := certs.Generate(dir, nil)
	if err != nil {
		return err
	}
	if len(res.Written) > 0 {
		s.say("made %v in %s", res.Written, dir)
	}
	files := map[string]string{}
	for _, name := range []string{certs.CAFile, certs.ServerFile, certs.ServerKeyFile} {
		raw, err := os.ReadFile(filepath.Join(dir, name)) //nolint:gosec // our own cert dir
		if err != nil {
			return fmt.Errorf("read %s: %w", name, err)
		}
		files[name] = string(raw)
	}
	s.files = files
	changed, err := s.folder.SetCACertificate(files[certs.CAFile])
	if changed {
		s.say("wrote ca.certificate")
	}
	return err
}

// renewServerCert removes the server pair when its certificate expires
// within renewBefore, so Generate signs a new one with the same CA. Samba
// copies its certs in on every boot, and the pair's sealed block changes,
// so the Deployment restarts the pod and serves the new one.
func renewServerCert(dir string, now time.Time) error {
	raw, err := os.ReadFile(filepath.Join(dir, certs.ServerFile)) //nolint:gosec // our own cert dir
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read the server certificate: %w", err)
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		return errors.New("the server certificate in the cert dir is not PEM")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return fmt.Errorf("parse the server certificate: %w", err)
	}
	if cert.NotAfter.Sub(now) > renewBefore {
		return nil
	}
	for _, name := range []string{certs.ServerFile, certs.ServerKeyFile} {
		if err := os.Remove(filepath.Join(dir, name)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("remove the old %s: %w", name, err)
		}
	}
	return nil
}

// overlays creates or updates noord-seed-overlays, the one Secret outside
// git (AC-6), from the gitignored overlay files: built in memory and piped
// to kubectl apply, never written to disk. It waits for ArgoCD to make the
// namespace first. The secret always exists, empty when there are no
// overlays, because the seed Job waits for it before it starts.
func (s *state) overlays(ctx context.Context) error {
	if !s.demo {
		return nil
	}
	if err := s.waitNamespace(ctx, directoryNamespace); err != nil {
		return err
	}
	data := map[string]string{}
	for _, f := range overlayFiles {
		raw, err := os.ReadFile(filepath.Join(s.Root, f.path))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("read %s: %w", f.path, err)
		}
		data[f.key] = base64.StdEncoding.EncodeToString(raw)
	}
	if len(data) == 0 {
		s.say("no seed overlays found; the seed runs without them")
	}
	manifest, err := yaml.Marshal(map[string]any{
		"apiVersion": "v1",
		"kind":       "Secret",
		"type":       "Opaque",
		"metadata": map[string]any{
			"name":      overlaysSecret,
			"namespace": directoryNamespace,
			"labels":    map[string]string{"app.kubernetes.io/part-of": "noord", "noord.example/cluster": s.Cluster},
		},
		"data": data,
	})
	if err != nil {
		return fmt.Errorf("build the overlays Secret: %w", err)
	}
	return s.kube.Apply(ctx, manifest)
}

// waitNamespace polls until ArgoCD has created the namespace or the run's
// deadline passes.
func (s *state) waitNamespace(ctx context.Context, ns string) error {
	for {
		ok, err := s.kube.Exists(ctx, "", "namespace", ns)
		if err == nil && ok {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("namespace %s did not appear within INSTALL_TIMEOUT", ns)
		case <-time.After(s.Poll):
		}
	}
}
