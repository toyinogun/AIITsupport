package install

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"slices"

	"gopkg.in/yaml.v3"
)

// Sealed is sealed-values.yaml: Secret name to key to ciphertext. The chart
// renders each block as a SealedSecret (AC-5).
type Sealed map[string]map[string]string

type sealedDoc struct {
	Sealed Sealed `yaml:"sealed"`
}

const sealedHeader = `# Written by make install (spec 0015, AC-5); do not edit by hand. Each block
# is sealed for its own Secret name and namespace (strict scope), so only this
# cluster's Sealed Secrets controller can open it.
`

// ReadSealed reads the folder's sealed-values.yaml; a missing file is empty.
func (f Folder) ReadSealed() (Sealed, error) {
	raw, err := os.ReadFile(f.path(sealedFile))
	if errors.Is(err, fs.ErrNotExist) {
		return Sealed{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s/%s: %w", f.Dir, sealedFile, err)
	}
	var file sealedDoc
	if err := yaml.Unmarshal(raw, &file); err != nil {
		return nil, fmt.Errorf("parse %s/%s: %w", f.Dir, sealedFile, err)
	}
	if file.Sealed == nil {
		file.Sealed = Sealed{}
	}
	return file.Sealed, nil
}

// WriteSealed writes sealed-values.yaml when its content changes and
// reports whether it did.
func (f Folder) WriteSealed(s Sealed) (bool, error) {
	var out bytes.Buffer
	out.WriteString(sealedHeader)
	enc := yaml.NewEncoder(&out)
	enc.SetIndent(2)
	if err := enc.Encode(sealedDoc{Sealed: s}); err != nil {
		return false, fmt.Errorf("encode %s: %w", sealedFile, err)
	}
	path := f.path(sealedFile)
	if old, err := os.ReadFile(path); err == nil && bytes.Equal(old, out.Bytes()) { //nolint:gosec // the cluster folder's own file
		return false, nil
	}
	if err := os.WriteFile(path, out.Bytes(), 0o644); err != nil { //nolint:gosec // ciphertext only, committed on purpose
		return false, fmt.Errorf("write %s/%s: %w", f.Dir, sealedFile, err)
	}
	return true, nil
}

// SealPlan counts what a seal did, never which values.
type SealPlan struct {
	Kept, Resealed, Dropped int
}

// sealer is the part of Sealer that Reseal needs.
type sealer interface {
	Seal(ctx context.Context, namespace, name, value string) (string, error)
}

// Reseal builds the new sealed values from the plaintext. A key keeps its
// old ciphertext when the live Secret already holds the same value;
// otherwise it is sealed again. Sealing is not deterministic, so comparing
// ciphertext would always differ (AC-5). Keys and Secrets no longer in the
// plaintext are dropped.
func (t Table) Reseal(ctx context.Context, plain Plain, old Sealed, live Live, seal sealer) (Sealed, SealPlan, error) {
	next := Sealed{}
	var plan SealPlan
	for _, name := range slices.Sorted(maps.Keys(plain)) {
		ns := t.Secrets[name].Namespace
		current, _, err := live.Secret(ctx, ns, name)
		if err != nil {
			return nil, plan, fmt.Errorf("read the live %s Secret: %w", name, err)
		}
		block := map[string]string{}
		for _, key := range slices.Sorted(maps.Keys(plain[name])) {
			value := plain[name][key]
			if cipher, ok := old[name][key]; ok && cipher != "" && current[key] == value {
				block[key] = cipher
				plan.Kept++
				continue
			}
			cipher, err := seal.Seal(ctx, ns, name, value)
			if err != nil {
				return nil, plan, err
			}
			block[key] = cipher
			plan.Resealed++
		}
		next[name] = block
		plan.Dropped += len(old[name]) - countShared(old[name], block)
	}
	for name, keys := range old {
		if _, ok := plain[name]; !ok {
			plan.Dropped += len(keys)
		}
	}
	return next, plan, nil
}

func countShared(a, b map[string]string) int {
	n := 0
	for k := range a {
		if _, ok := b[k]; ok {
			n++
		}
	}
	return n
}
