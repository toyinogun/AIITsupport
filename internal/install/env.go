package install

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// placeholder marks a site value the template leaves for you to fill (AC-2).
const placeholder = "CHANGE_ME"

// EnvFile is a cluster's secrets.env: KEY=value lines, comments, and blank
// lines, kept in order so writing a generated value back changes one line.
type EnvFile struct {
	lines  []string
	values map[string]string
	index  map[string]int // name to its line
}

// ReadEnvFile parses path. A value may be wrapped in double or single
// quotes; nothing is expanded.
func ReadEnvFile(path string) (*EnvFile, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // the cluster folder's own file
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return ParseEnv(raw)
}

// ParseEnv parses secrets.env content. Errors name the line, never a value.
func ParseEnv(raw []byte) (*EnvFile, error) {
	f := &EnvFile{values: map[string]string{}, index: map[string]int{}}
	sc := bufio.NewScanner(bytes.NewReader(raw))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for n := 0; sc.Scan(); n++ {
		line := sc.Text()
		f.lines = append(f.lines, line)
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		name, value, ok := strings.Cut(trimmed, "=")
		name = strings.TrimSpace(strings.TrimPrefix(name, "export "))
		if !ok || name == "" || strings.ContainsAny(name, " \t") {
			return nil, fmt.Errorf("secrets.env line %d is not NAME=value", n+1)
		}
		if _, dup := f.index[name]; dup {
			return nil, fmt.Errorf("secrets.env sets %s twice (line %d)", name, n+1)
		}
		f.values[name] = unquote(strings.TrimSpace(value))
		f.index[name] = n
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read secrets.env: %w", err)
	}
	return f, nil
}

func unquote(v string) string {
	switch {
	case len(v) >= 2 && v[0] == '"' && v[len(v)-1] == '"':
		return strings.ReplaceAll(v[1:len(v)-1], `\"`, `"`) // undoes quote
	case len(v) >= 2 && v[0] == '\'' && v[len(v)-1] == '\'':
		return v[1 : len(v)-1]
	}
	return v
}

// Get returns a value; a blank value counts as unset.
func (f *EnvFile) Get(name string) string { return f.values[name] }

// Lookup is Get as a config.LookupFunc.
func (f *EnvFile) Lookup(name string) (string, bool) {
	v, ok := f.values[name]
	return v, ok && v != ""
}

// Names lists every name the file sets, blank or not.
func (f *EnvFile) Names() []string {
	names := make([]string, 0, len(f.index))
	for name := range f.index {
		names = append(names, name)
	}
	return names
}

// Set changes or appends one value. Values are written double quoted when
// they hold anything but letters, digits, and URL safe punctuation.
func (f *EnvFile) Set(name, value string) {
	line := name + "=" + quote(value)
	f.values[name] = value
	if i, ok := f.index[name]; ok {
		f.lines[i] = line
		return
	}
	f.index[name] = len(f.lines)
	f.lines = append(f.lines, line)
}

func quote(v string) string {
	if strings.IndexFunc(v, func(r rune) bool { return !isPlain(r) }) < 0 {
		return v
	}
	return `"` + strings.ReplaceAll(v, `"`, `\"`) + `"`
}

func isPlain(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || strings.ContainsRune("-_.:/@", r)
}

// Bytes is the file's content.
func (f *EnvFile) Bytes() []byte {
	return []byte(strings.Join(f.lines, "\n") + "\n")
}

// Write saves the file with mode 0600, whatever mode it had: it holds every
// secret of the cluster. It writes a temp file beside it and renames it over,
// so an interrupt never leaves the only copy half written.
func (f *EnvFile) Write(path string) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".secrets-*.env")
	if err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }() // a no op after the rename
	_, werr := tmp.Write(f.Bytes())
	if err := errors.Join(werr, tmp.Chmod(0o600), tmp.Sync(), tmp.Close()); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}
