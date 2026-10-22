package install

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
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
// ciphertext would always differ (AC-5). A Secret not in the cluster yet is
// judged by the Record instead. Keys and Secrets no longer in the
// plaintext are dropped.
func (t Table) Reseal(ctx context.Context, plain Plain, old Sealed, live Live, seal sealer, rec Record) (Sealed, SealPlan, error) {
	next := Sealed{}
	var plan SealPlan
	for _, name := range slices.Sorted(maps.Keys(plain)) {
		ns := t.Secrets[name].Namespace
		current, exists, err := live.Secret(ctx, ns, name)
		if err != nil {
			return nil, plan, fmt.Errorf("read the live %s Secret: %w", name, err)
		}
		block := map[string]string{}
		for _, key := range slices.Sorted(maps.Keys(plain[name])) {
			value := plain[name][key]
			if cipher, ok := old[name][key]; ok && cipher != "" && (current[key] == value || (!exists && rec.Matches(ns, name, key, value))) {
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

// Record remembers, outside the repo, a hash of each value that was sealed
// (~/.noord/<cluster>/sealed-record.json, mode 0600). It lets a rerun keep
// the ciphertext of a value whose Secret is not in the cluster yet, which is
// the state after a pull request merges but before ArgoCD applied anything
// (AC-5). Without it every such rerun would seal again and open another pull
// request. A nil Record keeps nothing.
type Record map[string]string

func recordKey(ns, name, key string) string { return ns + "/" + name + "/" + key }

func recordHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

// Matches reports whether value is the one sealed last time.
func (r Record) Matches(ns, name, key, value string) bool {
	return r != nil && r[recordKey(ns, name, key)] == recordHash(value)
}

// Remember is the record for these plaintexts.
func (t Table) Remember(plain Plain) Record {
	rec := Record{}
	for name, keys := range plain {
		for key, value := range keys {
			rec[recordKey(t.Secrets[name].Namespace, name, key)] = recordHash(value)
		}
	}
	return rec
}

// ReadRecord reads the record at path; a missing or unreadable file is an
// empty record, which only costs a reseal.
func ReadRecord(path string) Record {
	raw, err := os.ReadFile(path) //nolint:gosec // the cluster's own cert dir
	if err != nil {
		return nil
	}
	var rec Record
	if json.Unmarshal(raw, &rec) != nil {
		return nil
	}
	return rec
}

// WriteRecord saves the record with mode 0600.
func WriteRecord(path string, rec Record) error {
	raw, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0o600)
}
