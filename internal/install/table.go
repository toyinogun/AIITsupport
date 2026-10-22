// Package install puts the whole system into a Kubernetes cluster through
// ArgoCD with one command (spec 0015): it checks the cluster and the clone,
// fills the cluster's generated secrets, seals every secret into
// deploy/clusters/<name>/sealed-values.yaml, commits and pushes that folder,
// applies the cluster's bootstrap Applications, and waits until everything is
// Synced and Healthy. Only cmd/seed uses it; it is on both agent deny lists.
package install

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"

	"gopkg.in/yaml.v3"
)

// TablePath is the Secret table, relative to the repo root. The chart reads
// the same file, so the installer and the chart cannot disagree on keys.
const TablePath = "deploy/helm/noord/files/secrets.yaml"

// Table is files/secrets.yaml: which Secret each workload reads, which keys
// it holds, and where each key's value comes from.
type Table struct {
	Database  Database             `yaml:"database"`
	Generated map[string]Generated `yaml:"generated"`
	Secrets   map[string]Secret    `yaml:"secrets"`
}

// Database is where every derived database URL points (AC-9).
type Database struct {
	Host   string `yaml:"host"`
	Port   int    `yaml:"port"`
	Name   string `yaml:"name"`
	CAPath string `yaml:"caPath"`
}

// Generated is a value made once with crypto/rand (AC-5).
type Generated struct {
	Length int   `yaml:"length"`
	Demo   *bool `yaml:"demo"`
}

// Secret is one workload's Secret.
type Secret struct {
	Namespace string         `yaml:"namespace"`
	Component string         `yaml:"component"`
	Loader    string         `yaml:"loader"` // the config struct it carries (AC-7); empty for deploy only Secrets
	Type      string         `yaml:"type"`
	Keys      map[string]Key `yaml:"keys"`
}

// Key is one key of a Secret and where its value comes from.
type Key struct {
	Optional bool   `yaml:"optional"`
	Demo     *bool  `yaml:"demo"`
	Reset    *bool  `yaml:"reset"`
	From     string `yaml:"from"`
	Value    string `yaml:"value"`
	Derive   string `yaml:"derive"`
	Role     string `yaml:"role"`
	Password string `yaml:"password"`
	Host     string `yaml:"host"`
	File     string `yaml:"file"`
	// FromFile names the secrets.env setting holding a local file's path;
	// the file's content is the value, and the chart mounts it as a file
	// instead of an environment variable (the Graph certificate, AC-24).
	FromFile string `yaml:"fromFile"`
	// Rails marks a key the Zammad image reads, not our loader (AC-7).
	Rails bool `yaml:"rails"`
}

// The derivations the table names.
const (
	// deriveDatabaseURL is a role's URL to noord-pg, with role and password.
	deriveDatabaseURL = "database_url"
	// deriveTailnetURL is https://<host>.<TAILNET>, the host one of the
	// tailnet hosts by role (AC-15).
	deriveTailnetURL = "tailnet_url"
	// tailnetSetting is the secrets.env name a tailnet URL reads.
	tailnetSetting = "TAILNET"
	// hostReset is the reset page's role in Sources.Hosts.
	hostReset = "reset"
)

// Conditions are the install wide switches a key may depend on.
type Conditions struct {
	Demo  bool // demo.enabled
	Reset bool // reset.enabled
}

// holds reports whether a key applies to an install with these conditions.
func (c Conditions) holds(k Key) bool {
	return applies(k.Demo, c.Demo) && applies(k.Reset, c.Reset)
}

// LoadTable reads and checks the Secret table at path.
func LoadTable(path string) (Table, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // a fixed path inside the clone
	if err != nil {
		return Table{}, fmt.Errorf("read the secret table: %w", err)
	}
	var t Table
	if err := yaml.Unmarshal(raw, &t); err != nil {
		return Table{}, fmt.Errorf("parse the secret table %s: %w", path, err)
	}
	if err := t.check(); err != nil {
		return Table{}, fmt.Errorf("secret table %s: %w", path, err)
	}
	return t, nil
}

func (t Table) check() error {
	if t.Database.Host == "" || t.Database.Name == "" || t.Database.Port == 0 || t.Database.CAPath == "" {
		return fmt.Errorf("database needs host, port, name, and caPath")
	}
	for _, name := range slices.Sorted(maps.Keys(t.Secrets)) {
		s := t.Secrets[name]
		if s.Namespace == "" || s.Component == "" || len(s.Keys) == 0 {
			return fmt.Errorf("secret %s needs a namespace, a component, and keys", name)
		}
		for _, key := range slices.Sorted(maps.Keys(s.Keys)) {
			k := s.Keys[key]
			if err := k.check(); err != nil {
				return fmt.Errorf("%s.%s: %w", name, key, err)
			}
		}
	}
	for _, name := range slices.Sorted(maps.Keys(t.Generated)) {
		if t.Generated[name].Length < minGeneratedLength {
			return fmt.Errorf("generated %s needs a length of at least %d", name, minGeneratedLength)
		}
	}
	return nil
}

// check refuses a key whose sources contradict each other.
func (k Key) check() error {
	switch {
	case k.Derive == deriveDatabaseURL && (k.Role == "" || k.Password == "" || k.Host != ""):
		return fmt.Errorf("derive %s needs a role and a password and no host", deriveDatabaseURL)
	case k.Derive == deriveTailnetURL && (k.Host == "" || k.Role != "" || k.Password != ""):
		return fmt.Errorf("derive %s needs a host and no role or password", deriveTailnetURL)
	case k.Derive != "" && k.Derive != deriveDatabaseURL && k.Derive != deriveTailnetURL:
		return fmt.Errorf("derive must be %s or %s", deriveDatabaseURL, deriveTailnetURL)
	case k.Value != "" && (k.From != "" || k.Derive != ""):
		return errors.New("value excludes from and derive")
	case k.File != "" && (k.Value != "" || k.From != "" || k.Derive != "" || k.File != filepath.Base(k.File)):
		return errors.New("file is a bare file name and excludes value, from, and derive")
	case k.FromFile != "" && (k.Value != "" || k.From != "" || k.Derive != "" || k.File != ""):
		return errors.New("fromFile excludes value, from, derive, and file")
	}
	return nil
}

// applies reports whether a switch condition holds for this install.
func applies(want *bool, enabled bool) bool {
	return want == nil || *want == enabled
}

// Mounted reports whether the chart mounts the key as a file instead of
// setting it in the environment.
func (k Key) Mounted() bool { return k.FromFile != "" }

// source is the secrets.env name a key reads, or "" for a fixed value or a
// file from the cluster's cert dir.
func (k Key) source(key string) string {
	switch {
	case k.Value != "" || k.File != "":
		return ""
	case k.FromFile != "":
		return k.FromFile
	case k.Derive == deriveTailnetURL:
		return tailnetSetting
	case k.Derive != "":
		return k.Password
	case k.From != "":
		return k.From
	default:
		return key
	}
}

// Inputs lists every secrets.env name the table reads, sorted: the template
// secrets.env must hold exactly these plus the Install settings (AC-7).
func (t Table) Inputs() []string {
	var names []string
	for _, s := range t.Secrets {
		for key, k := range s.Keys {
			if src := k.source(key); src != "" {
				names = append(names, src)
			}
		}
	}
	names = append(names, slices.Collect(maps.Keys(t.Generated))...)
	slices.Sort(names)
	return slices.Compact(names)
}

// Enabled lists the Secrets of the components this install renders, sorted.
func (t Table) Enabled(components []string) []string {
	var names []string
	for name, s := range t.Secrets {
		if slices.Contains(components, s.Component) {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	return names
}

// Required lists the secrets.env names an install with these components
// must have set, sorted, leaving out the ones it generates.
func (t Table) Required(components []string, c Conditions) []string {
	var names []string
	for _, secret := range t.Enabled(components) {
		for key, k := range t.Secrets[secret].Keys {
			src := k.source(key)
			if src == "" || k.Optional || !c.holds(k) || t.generates(src, c.Demo) {
				continue
			}
			names = append(names, src)
		}
	}
	slices.Sort(names)
	return slices.Compact(names)
}

func (t Table) generates(name string, demo bool) bool {
	g, ok := t.Generated[name]
	return ok && applies(g.Demo, demo)
}

// home is where a generated value lives in the cluster: the first Secret and
// key (sorted) that hold it as is, so a lost secrets.env is refilled from
// the live Secret instead of making a new value (AC-5).
func (t Table) home(name string) (secret, key string, ok bool) {
	for _, s := range slices.Sorted(maps.Keys(t.Secrets)) {
		keys := t.Secrets[s].Keys
		for _, k := range slices.Sorted(maps.Keys(keys)) {
			spec := keys[k]
			if spec.Derive == "" && spec.source(k) == name {
				return s, k, true
			}
		}
	}
	return "", "", false
}
