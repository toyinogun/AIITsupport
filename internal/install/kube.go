package install

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
)

// Cmd is one external command: kubectl, kubeseal, or git.
type Cmd struct {
	Name  string
	Args  []string
	Stdin []byte   // never echoed into an error
	Env   []string // added to the environment
}

// Runner runs a command and returns its stdout. Tests pass a fake.
type Runner interface {
	Run(ctx context.Context, c Cmd) ([]byte, error)
}

// ExecRunner runs commands for real, in Dir.
type ExecRunner struct{ Dir string }

// CmdError is a command that failed, with the last line it wrote to stderr.
type CmdError struct {
	Name, Sub, Stderr string
	Err               error
}

func (e *CmdError) Error() string {
	msg := e.Name + " " + e.Sub + " failed"
	if e.Stderr != "" {
		msg += ": " + e.Stderr
	}
	return msg
}

func (e *CmdError) Unwrap() error { return e.Err }

// Run runs c and returns its stdout.
func (r ExecRunner) Run(ctx context.Context, c Cmd) ([]byte, error) {
	cmd := exec.CommandContext(ctx, c.Name, c.Args...) //nolint:gosec // fixed tool names; the arguments are built here, never from user text
	cmd.Dir = r.Dir
	cmd.Env = append(os.Environ(), c.Env...)
	if c.Stdin != nil {
		cmd.Stdin = bytes.NewReader(c.Stdin)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		sub := ""
		if len(c.Args) > 0 {
			sub = c.Args[0]
		}
		return out, &CmdError{Name: c.Name, Sub: sub, Stderr: lastLine(stderr.String()), Err: err}
	}
	return out, nil
}

// lastLine is the last line of stderr, with any credentials in a URL
// (https://user:token@host) replaced, so a remote URL holding a token
// never reaches the terminal.
func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return urlCredentials.ReplaceAllString(strings.TrimSpace(lines[len(lines)-1]), "://[redacted]@")
}

var urlCredentials = regexp.MustCompile(`://[^/@\s]+@`)

// Kube talks to one cluster, always naming its context, so a changed
// current context mid run cannot point a step at another cluster.
type Kube struct {
	run     Runner
	context string
}

func (k Kube) kubectl(ctx context.Context, stdin []byte, args ...string) ([]byte, error) {
	return k.run.Run(ctx, Cmd{Name: "kubectl", Args: append([]string{"--context", k.context}, args...), Stdin: stdin})
}

// CurrentContext is kubectl's current context.
func CurrentContext(ctx context.Context, r Runner) (string, error) {
	out, err := r.Run(ctx, Cmd{Name: "kubectl", Args: []string{"config", "current-context"}})
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// Secret reads a Secret's decoded data; ok is false when it does not exist.
func (k Kube) Secret(ctx context.Context, namespace, name string) (map[string]string, bool, error) {
	out, err := k.kubectl(ctx, nil, "get", "secret", name, "-n", namespace, "-o", "json", "--ignore-not-found")
	if err != nil {
		return nil, false, err
	}
	if len(bytes.TrimSpace(out)) == 0 {
		return nil, false, nil
	}
	var s struct {
		Data map[string]string `json:"data"`
	}
	if err := json.Unmarshal(out, &s); err != nil {
		return nil, false, fmt.Errorf("parse Secret %s/%s: %w", namespace, name, err)
	}
	data := make(map[string]string, len(s.Data))
	for key, v := range s.Data {
		raw, err := base64.StdEncoding.DecodeString(v)
		if err != nil {
			return nil, false, fmt.Errorf("decode Secret %s/%s key %s: %w", namespace, name, key, err)
		}
		data[key] = string(raw)
	}
	return data, true, nil
}

// HasDefaultStorageClass reports whether a StorageClass is marked default.
func (k Kube) HasDefaultStorageClass(ctx context.Context) (bool, error) {
	out, err := k.kubectl(ctx, nil, "get", "storageclass", "-o", "json")
	if err != nil {
		return false, err
	}
	var list struct {
		Items []struct {
			Metadata struct {
				Annotations map[string]string `json:"annotations"`
			} `json:"metadata"`
		} `json:"items"`
	}
	if err := json.Unmarshal(out, &list); err != nil {
		return false, fmt.Errorf("parse StorageClasses: %w", err)
	}
	for _, sc := range list.Items {
		if sc.Metadata.Annotations["storageclass.kubernetes.io/is-default-class"] == "true" {
			return true, nil
		}
	}
	return false, nil
}

// Exists reports whether one object exists.
func (k Kube) Exists(ctx context.Context, namespace, kind, name string) (bool, error) {
	out, err := k.kubectl(ctx, nil, "get", kind, name, "-n", namespace, "-o", "name", "--ignore-not-found")
	if err != nil {
		return false, err
	}
	return len(bytes.TrimSpace(out)) > 0, nil
}

// ApplyPart applies the bootstrap Applications labelled with part.
func (k Kube) ApplyPart(ctx context.Context, bootstrap []byte, part string) error {
	_, err := k.kubectl(ctx, bootstrap, "apply", "-f", "-", "-l", partLabel+"="+part)
	return err
}

// Refresh asks ArgoCD to read git again for one Application now.
func (k Kube) Refresh(ctx context.Context, app string) error {
	_, err := k.kubectl(ctx, nil, "-n", argocdNamespace, "annotate", "applications.argoproj.io", app,
		"argocd.argoproj.io/refresh=normal", "--overwrite")
	return err
}

// AppStatus is what the wait step reads from one Application.
type AppStatus struct {
	Name     string
	Sync     string
	Health   string
	Revision string
}

// Applications reads the named Applications; a missing one is left out.
func (k Kube) Applications(ctx context.Context, names []string) (map[string]AppStatus, error) {
	out, err := k.kubectl(ctx, nil, append([]string{"-n", argocdNamespace, "get", "applications.argoproj.io", "-o", "json", "--ignore-not-found"}, names...)...)
	if err != nil {
		return nil, err
	}
	var list struct {
		Items []struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
			Status struct {
				Sync struct {
					Status   string `json:"status"`
					Revision string `json:"revision"`
				} `json:"sync"`
				Health struct {
					Status string `json:"status"`
				} `json:"health"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := decodeList(out, &list); err != nil {
		return nil, fmt.Errorf("parse Applications: %w", err)
	}
	apps := map[string]AppStatus{}
	for _, a := range list.Items {
		apps[a.Metadata.Name] = AppStatus{
			Name: a.Metadata.Name, Sync: a.Status.Sync.Status, Health: a.Status.Health.Status, Revision: a.Status.Sync.Revision,
		}
	}
	return apps, nil
}

// SealedSecretsSynced reads the Synced condition of the SealedSecrets
// labelled with this cluster, by namespace/name.
func (k Kube) SealedSecretsSynced(ctx context.Context, cluster string) (map[string]bool, error) {
	out, err := k.kubectl(ctx, nil, "get", "sealedsecrets.bitnami.com", "-A", "-l", "noord.example/cluster="+cluster, "-o", "json")
	if err != nil {
		return nil, err
	}
	var list struct {
		Items []struct {
			Metadata struct {
				Name      string `json:"name"`
				Namespace string `json:"namespace"`
			} `json:"metadata"`
			Status struct {
				Conditions []struct {
					Type   string `json:"type"`
					Status string `json:"status"`
				} `json:"conditions"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := decodeList(out, &list); err != nil {
		return nil, fmt.Errorf("parse SealedSecrets: %w", err)
	}
	synced := map[string]bool{}
	for _, s := range list.Items {
		ok := false
		for _, c := range s.Status.Conditions {
			if c.Type == "Synced" && c.Status == "True" {
				ok = true
			}
		}
		synced[s.Metadata.Namespace+"/"+s.Metadata.Name] = ok
	}
	return synced, nil
}

// decodeList parses a kubectl list, or one object when a single name was
// asked for, into a list shape.
func decodeList(out []byte, list any) error {
	out = bytes.TrimSpace(out)
	if len(out) == 0 {
		return nil
	}
	var probe struct {
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal(out, &probe); err != nil {
		return err
	}
	if probe.Kind != "List" && !strings.HasSuffix(probe.Kind, "List") {
		out = append(append([]byte(`{"items":[`), out...), "]}"...)
	}
	return json.Unmarshal(out, list)
}

// Sealer seals values for one controller with kubeseal.
type Sealer struct {
	run       Runner
	context   string
	namespace string // the controller's
	name      string
	certPath  string
}

// FetchCert reads the controller's public certificate once, so sealing
// each value needs no further call to the cluster.
func (s *Sealer) FetchCert(ctx context.Context, dir string) (cleanup func(), err error) {
	out, err := s.run.Run(ctx, Cmd{Name: "kubeseal", Args: []string{
		"--context", s.context, "--controller-namespace", s.namespace, "--controller-name", s.name, "--fetch-cert",
	}})
	if err != nil {
		return nil, fmt.Errorf("no Sealed Secrets controller %s/%s answered: %w", s.namespace, s.name, err)
	}
	f, err := os.CreateTemp(dir, "noord-seal-*.pem")
	if err != nil {
		return nil, fmt.Errorf("save the sealing certificate: %w", err)
	}
	_, werr := f.Write(out)
	if err := errors.Join(werr, f.Close()); err != nil {
		_ = os.Remove(f.Name())
		return nil, fmt.Errorf("save the sealing certificate: %w", err)
	}
	s.certPath = f.Name()
	return func() { _ = os.Remove(f.Name()) }, nil
}

// Seal encrypts one value for one Secret's name and namespace (strict scope).
func (s *Sealer) Seal(ctx context.Context, namespace, name, value string) (string, error) {
	out, err := s.run.Run(ctx, Cmd{Name: "kubeseal", Args: []string{
		"--raw", "--scope", "strict", "--namespace", namespace, "--name", name, "--cert", s.certPath, "--from-file=/dev/stdin",
	}, Stdin: []byte(value)})
	if err != nil {
		return "", fmt.Errorf("seal a value for %s/%s: %w", namespace, name, err)
	}
	return strings.TrimSpace(string(out)), nil
}
