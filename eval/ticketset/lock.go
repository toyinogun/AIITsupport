package ticketset

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"

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
