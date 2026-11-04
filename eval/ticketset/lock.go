package ticketset

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// lockFile is eval/tickets.lock: it freezes a ticket set by hash (AC-10).
type lockFile struct {
	Version int    `yaml:"version"`
	SHA256  string `yaml:"sha256"`
}

// CheckLock compares the ticket file with its lock file. It returns false
// with no error when there is no lock file (the set is not frozen yet).
func CheckLock(ticketsPath, lockPath string) (frozen bool, err error) {
	raw, err := os.ReadFile(lockPath) //nolint:gosec // G304: the path comes from the caller
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read lock file: %w", err)
	}
	var l lockFile
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&l); err != nil {
		return true, fmt.Errorf("decode lock file: %w", err)
	}
	if l.Version != 1 {
		return true, fmt.Errorf("lock file version is %d, want 1", l.Version)
	}
	data, err := os.ReadFile(ticketsPath) //nolint:gosec // G304: the path comes from the caller
	if err != nil {
		return true, fmt.Errorf("read ticket file: %w", err)
	}
	sum := sha256.Sum256(data)
	if got := hex.EncodeToString(sum[:]); got != l.SHA256 {
		return true, errors.New("ticket file changed after it was frozen; add a new numbered set instead of editing this one")
	}
	return true, nil
}

// LockPath is the lock file beside a ticket file: eval/tickets.yaml has
// eval/tickets.lock (spec 0018, AC-15).
func LockPath(ticketsPath string) string {
	return strings.TrimSuffix(ticketsPath, filepath.Ext(ticketsPath)) + ".lock"
}

// WriteLock freezes a ticket set by its hash. An existing lock with the same
// hash is left alone (a no op); one with another hash is an error.
func WriteLock(lockPath, hash string) error {
	if raw, err := os.ReadFile(lockPath); err == nil { //nolint:gosec // G304: the path comes from the caller
		var l lockFile
		if yaml.Unmarshal(raw, &l) == nil && l.SHA256 == hash {
			return nil
		}
		return errors.New("a lock file with another hash already exists; add a new numbered set instead")
	}
	raw, err := yaml.Marshal(lockFile{Version: 1, SHA256: hash})
	if err != nil {
		return fmt.Errorf("encode lock file: %w", err)
	}
	if err := os.WriteFile(lockPath, raw, 0o644); err != nil { //nolint:gosec // G306: the lock is committed to the repo
		return fmt.Errorf("write lock file: %w", err)
	}
	return nil
}
