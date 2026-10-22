package install

import (
	"context"
	"crypto/rand"
	"fmt"
	"maps"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// minGeneratedLength is the shortest generated value the table may ask for.
const minGeneratedLength = 24

// Plain is the plaintext of each Secret this install renders: Secret name
// to key to value. It lives only in memory, on the way to kubeseal.
type Plain map[string]map[string]string

// Live reads a Secret in the cluster; ok is false when it does not exist.
type Live interface {
	Secret(ctx context.Context, namespace, name string) (data map[string]string, ok bool, err error)
}

// Sources are where Build finds values besides secrets.env.
type Sources struct {
	Files map[string]string // the cluster's cert dir, by file name
	Hosts map[string]string // the tailnet host names, by role ("reset")
	Root  string            // a relative fromFile path is under the clone
}

// Build makes the plaintext of every enabled Secret from secrets.env and
// the other sources. Optional keys with no value are left out; a missing
// required value is an error naming the setting or the file (preflight
// reports settings first).
func (t Table) Build(env *EnvFile, src Sources, components []string, c Conditions) (Plain, error) {
	plain := Plain{}
	var missing []string
	for _, name := range t.Enabled(components) {
		s := t.Secrets[name]
		data := map[string]string{}
		for _, key := range slices.Sorted(maps.Keys(s.Keys)) {
			k := s.Keys[key]
			if !c.holds(k) {
				continue
			}
			v, err := t.value(key, k, env, src)
			if err != nil {
				return nil, err
			}
			switch {
			case v != "":
				data[key] = v
			case !k.Optional && k.File != "":
				missing = append(missing, "the cert file "+k.File)
			case !k.Optional:
				missing = append(missing, k.source(key))
			}
		}
		plain[name] = data
	}
	if len(missing) > 0 {
		slices.Sort(missing)
		return nil, fmt.Errorf("missing %s", strings.Join(slices.Compact(missing), ", "))
	}
	return plain, nil
}

func (t Table) value(key string, k Key, env *EnvFile, src Sources) (string, error) {
	switch {
	case k.Value != "":
		return k.Value, nil
	case k.File != "":
		return src.Files[k.File], nil
	case k.FromFile != "":
		return readLocal(k.FromFile, env.Get(k.FromFile), src.Root)
	case k.Derive == deriveDatabaseURL:
		password := env.Get(k.Password)
		if password == "" {
			return "", nil
		}
		return t.Database.url(k.Role, password), nil
	case k.Derive == deriveTailnetURL:
		tailnet, host := env.Get(tailnetSetting), src.Hosts[k.Host]
		if tailnet == "" || host == "" {
			return "", nil
		}
		return "https://" + host + "." + tailnet, nil
	default:
		return env.Get(k.source(key)), nil
	}
}

// readLocal reads the file a secrets.env setting names, relative to the
// clone when not absolute; an unset setting is no value. Errors name the
// setting, never the content.
func readLocal(setting, path, root string) (string, error) {
	if path == "" {
		return "", nil
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(root, path)
	}
	raw, err := os.ReadFile(path) //nolint:gosec // G304: a path you set in your own secrets.env
	if err != nil {
		return "", fmt.Errorf("read the file %s names: %w", setting, err)
	}
	return string(raw), nil
}

// url is one role's connection URL, verified against the CNPG CA (AC-9).
func (d Database) url(role, password string) string {
	u := url.URL{
		Scheme:   "postgresql",
		User:     url.UserPassword(role, password),
		Host:     net.JoinHostPort(d.Host, strconv.Itoa(d.Port)),
		Path:     "/" + d.Name,
		RawQuery: url.Values{"sslmode": {"verify-full"}, "sslrootcert": {d.CAPath}}.Encode(),
	}
	return u.String()
}

// Generate fills every generated value secrets.env lacks: from the live
// Secret that holds it when there is one, else a new random value (AC-5).
// It returns the names it filled, sorted, and never their values.
func (t Table) Generate(ctx context.Context, env *EnvFile, live Live, components []string, c Conditions) (filled []string, err error) {
	inputs := map[string]bool{}
	for _, name := range t.Enabled(components) {
		for key, k := range t.Secrets[name].Keys {
			if c.holds(k) {
				inputs[k.source(key)] = true
			}
		}
	}
	for _, name := range slices.Sorted(maps.Keys(t.Generated)) {
		g := t.Generated[name]
		if !inputs[name] || !applies(g.Demo, c.Demo) || env.Get(name) != "" {
			continue
		}
		v, err := t.recover(ctx, live, name)
		if err != nil {
			return filled, err
		}
		if v == "" {
			v = randomValue(g.Length)
		}
		env.Set(name, v)
		filled = append(filled, name)
	}
	return filled, nil
}

// recover reads a generated value back from the Secret that holds it.
func (t Table) recover(ctx context.Context, live Live, name string) (string, error) {
	secret, key, ok := t.home(name)
	if !ok {
		return "", nil
	}
	data, found, err := live.Secret(ctx, t.Secrets[secret].Namespace, secret)
	if err != nil {
		return "", fmt.Errorf("read the live %s Secret for %s: %w", secret, name, err)
	}
	if !found {
		return "", nil
	}
	return data[key], nil
}

const (
	lower  = "abcdefghijklmnopqrstuvwxyz"
	upper  = "ABCDEFGHIJKLMNOPQRSTUVWXYZ"
	digits = "0123456789"
)

// randomValue is n letters and digits from crypto/rand with at least one
// lower, upper, and digit: Samba's complexity rule, and safe in a URL.
func randomValue(n int) string {
	alphabet := lower + upper + digits
	for {
		v := make([]byte, n)
		for i := range v {
			v[i] = alphabet[randIndex(len(alphabet))]
		}
		s := string(v)
		if strings.ContainsAny(s, lower) && strings.ContainsAny(s, upper) && strings.ContainsAny(s, digits) {
			return s
		}
	}
}

// randIndex is a uniform index below n, by rejection sampling.
func randIndex(n int) int {
	var b [1]byte
	limit := 256 - 256%n
	for {
		_, _ = rand.Read(b[:]) // crypto/rand.Read never returns an error (Go 1.24 on)
		if int(b[0]) < limit {
			return int(b[0]) % n
		}
	}
}
