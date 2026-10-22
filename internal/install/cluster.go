package install

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"gopkg.in/yaml.v3"
)

// The files in a cluster folder (spec 0015, Repo layout).
const (
	ClustersDir      = "deploy/clusters"
	valuesFile       = "values.yaml"
	sealedFile       = "sealed-values.yaml"
	secretsFile      = "secrets.env"
	bootstrapFile    = "bootstrap.yaml"
	partLabel        = "noord.example/part"
	partPlatform     = "platform"
	partApps         = "apps"
	componentAgent   = "agent"
	argocdNamespace  = "argocd"
	argocdServerName = "argocd-server"
)

// clusterName is a folder name and a DNS label part (noord-<name>-platform).
var clusterName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,30}[a-z0-9]$`)

// Values is the part of a cluster's values.yaml the installer reads.
type Values struct {
	Cluster struct {
		Name string `yaml:"name"`
	} `yaml:"cluster"`
	Repo struct {
		URL      string `yaml:"url"`
		Revision string `yaml:"revision"`
	} `yaml:"repo"`
	Image struct {
		Registry string `yaml:"registry"`
		Owner    string `yaml:"owner"`
		Tag      string `yaml:"tag"`
	} `yaml:"image"`
	Demo struct {
		Enabled       *bool  `yaml:"enabled"`
		MinFreeMemory string `yaml:"minFreeMemory"`
	} `yaml:"demo"`
	// Reset is the password reset page (spec 0011) and its tailnet host
	// (AC-15).
	Reset struct {
		Enabled       *bool  `yaml:"enabled"`
		TailscaleHost string `yaml:"tailscaleHost"`
	} `yaml:"reset"`
	// Helpdesk is the demo Zammad's tailnet host (AC-15).
	Helpdesk struct {
		TailscaleHost string `yaml:"tailscaleHost"`
	} `yaml:"helpdesk"`
	// Backup is the database's backups to object storage (AC-18).
	Backup struct {
		Enabled  *bool  `yaml:"enabled"`
		Endpoint string `yaml:"endpoint"`
		Bucket   string `yaml:"bucket"`
	} `yaml:"backup"`
	Platform struct {
		SealedSecrets struct {
			Enabled   bool   `yaml:"enabled"`
			Namespace string `yaml:"namespace"`
			Name      string `yaml:"name"`
		} `yaml:"sealedSecrets"`
		BarmanPlugin struct {
			Enabled bool `yaml:"enabled"`
		} `yaml:"barmanPlugin"`
		CertManager struct {
			Enabled bool `yaml:"enabled"`
		} `yaml:"certManager"`
		CNPG struct {
			Enabled bool `yaml:"enabled"`
		} `yaml:"cnpg"`
		Tailscale struct {
			Enabled bool `yaml:"enabled"`
		} `yaml:"tailscale"`
	} `yaml:"platform"`
	CA struct {
		Certificate string `yaml:"certificate"`
	} `yaml:"ca"`
	Paused  bool `yaml:"paused"`
	SeedRun int  `yaml:"seedRun"`
}

// DemoEnabled is demo.enabled, on unless the folder turns it off.
func (v Values) DemoEnabled() bool { return v.Demo.Enabled == nil || *v.Demo.Enabled }

// ResetEnabled is reset.enabled, on unless the folder turns it off (the
// chart's default agrees).
func (v Values) ResetEnabled() bool { return v.Reset.Enabled == nil || *v.Reset.Enabled }

// ResetHost is the reset page's tailnet host: reset.tailscaleHost, else
// noord-<cluster>-reset, so two clusters on one tailnet never collide.
func (v Values) ResetHost() string {
	if v.Reset.TailscaleHost != "" {
		return v.Reset.TailscaleHost
	}
	return "noord-" + v.Cluster.Name + "-reset"
}

// HelpdeskHost is the demo Zammad's tailnet host: helpdesk.tailscaleHost,
// else noord-<cluster>-helpdesk (the chart's default agrees).
func (v Values) HelpdeskHost() string {
	if v.Helpdesk.TailscaleHost != "" {
		return v.Helpdesk.TailscaleHost
	}
	return "noord-" + v.Cluster.Name + "-helpdesk"
}

// BackupEnabled is backup.enabled, off unless the folder turns it on (the
// chart's default agrees).
func (v Values) BackupEnabled() bool { return v.Backup.Enabled != nil && *v.Backup.Enabled }

// Conditions are the switches this folder sets.
func (v Values) Conditions() Conditions {
	return Conditions{Demo: v.DemoEnabled(), Reset: v.ResetEnabled(), Backup: v.BackupEnabled(), Tailscale: v.Platform.Tailscale.Enabled}
}

// Folder is one cluster's folder in the clone.
type Folder struct {
	Name string
	Dir  string // relative to the repo root
	root string
}

// OpenFolder finds deploy/clusters/<name>/ under the repo root.
func OpenFolder(root, name string) (Folder, error) {
	if !clusterName.MatchString(name) {
		return Folder{}, fmt.Errorf("cluster name %q must be 2 to 32 of a-z, 0-9 and -, starting with a letter", name)
	}
	f := Folder{Name: name, Dir: filepath.ToSlash(filepath.Join(ClustersDir, name)), root: root}
	info, err := os.Stat(f.path(""))
	if err != nil || !info.IsDir() {
		return Folder{}, fmt.Errorf("%s does not exist; run make cluster-init CLUSTER=%s first", f.Dir, name)
	}
	return f, nil
}

func (f Folder) path(file string) string { return filepath.Join(f.root, f.Dir, file) }

// Values reads the folder's values.yaml and checks it names this cluster.
func (f Folder) Values() (Values, error) {
	raw, err := os.ReadFile(f.path(valuesFile))
	if err != nil {
		return Values{}, fmt.Errorf("read %s/%s: %w", f.Dir, valuesFile, err)
	}
	var v Values
	if err := yaml.Unmarshal(raw, &v); err != nil {
		return Values{}, fmt.Errorf("parse %s/%s: %w", f.Dir, valuesFile, err)
	}
	var errs []error
	if v.Cluster.Name != f.Name {
		errs = append(errs, fmt.Errorf("cluster.name must be %s", f.Name))
	}
	for name, val := range map[string]string{
		"repo.url": v.Repo.URL, "repo.revision": v.Repo.Revision, "image.registry": v.Image.Registry,
		"image.owner": v.Image.Owner, "image.tag": v.Image.Tag,
		"platform.sealedSecrets.namespace": v.Platform.SealedSecrets.Namespace, "platform.sealedSecrets.name": v.Platform.SealedSecrets.Name,
	} {
		if val == "" || val == placeholder {
			errs = append(errs, fmt.Errorf("%s is not set", name))
		}
	}
	if err := errors.Join(errs...); err != nil {
		return Values{}, fmt.Errorf("%s/%s: %w", f.Dir, valuesFile, err)
	}
	return v, nil
}

// SetCACertificate writes ca.certificate into values.yaml, keeping the
// rest of the file (comments included) as it is. It reports whether the
// file changed.
func (f Folder) SetCACertificate(pem string) (bool, error) {
	path := f.path(valuesFile)
	raw, err := os.ReadFile(path) //nolint:gosec // the cluster folder's own file
	if err != nil {
		return false, fmt.Errorf("read %s/%s: %w", f.Dir, valuesFile, err)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil || len(doc.Content) == 0 {
		return false, fmt.Errorf("parse %s/%s: %w", f.Dir, valuesFile, err)
	}
	ca := mapping(doc.Content[0], "ca")
	cert := scalar(ca, "certificate")
	if cert.Value == pem {
		return false, nil
	}
	cert.Value, cert.Style, cert.Tag = pem, yaml.LiteralStyle, "!!str"
	var out bytes.Buffer
	enc := yaml.NewEncoder(&out)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return false, fmt.Errorf("encode %s/%s: %w", f.Dir, valuesFile, err)
	}
	if err := os.WriteFile(path, out.Bytes(), 0o644); err != nil { //nolint:gosec // committed, public values
		return false, fmt.Errorf("write %s/%s: %w", f.Dir, valuesFile, err)
	}
	return true, nil
}

// mapping returns the mapping under key in m, adding it when absent.
func mapping(m *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key && m.Content[i+1].Kind == yaml.MappingNode {
			return m.Content[i+1]
		}
	}
	child := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	m.Content = append(m.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, child)
	return child
}

// scalar returns the scalar under key in m, adding it when absent.
func scalar(m *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			n := m.Content[i+1]
			n.Kind = yaml.ScalarNode
			return n
		}
	}
	child := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str"}
	m.Content = append(m.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, child)
	return child
}

// SetScalar writes one scalar at path in values.yaml (tag is !!str, !!int or
// !!bool), keeping the rest of the file, comments included. It reports
// whether the file changed.
func (f Folder) SetScalar(keyPath []string, value, tag string) (bool, error) {
	file := f.path(valuesFile)
	raw, err := os.ReadFile(file) //nolint:gosec // the cluster folder's own file
	if err != nil {
		return false, fmt.Errorf("read %s/%s: %w", f.Dir, valuesFile, err)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil || len(doc.Content) == 0 {
		return false, fmt.Errorf("parse %s/%s: %w", f.Dir, valuesFile, err)
	}
	node := doc.Content[0]
	for _, key := range keyPath[:len(keyPath)-1] {
		node = mapping(node, key)
	}
	leaf := scalar(node, keyPath[len(keyPath)-1])
	if leaf.Value == value && leaf.Tag == tag {
		return false, nil
	}
	leaf.Value, leaf.Tag, leaf.Style = value, tag, 0
	var out bytes.Buffer
	enc := yaml.NewEncoder(&out)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return false, fmt.Errorf("encode %s/%s: %w", f.Dir, valuesFile, err)
	}
	if err := os.WriteFile(file, out.Bytes(), 0o644); err != nil { //nolint:gosec // committed, public values
		return false, fmt.Errorf("write %s/%s: %w", f.Dir, valuesFile, err)
	}
	return true, nil
}
