package tools

import (
	"fmt"
	"os"
	"strings"
)

// The two kill switch modes (spec 0016, AC-8). ModeReadWrite is also the
// only file content that allows account changes.
const (
	ModeReadWrite = "read_write"
	ModeReadOnly  = "read_only"
)

const readWrite = ModeReadWrite

// NewKillSwitch returns the file switch when path is set. With no path
// (local dev and tests) READ_ONLY decides, fixed for the process.
func NewKillSwitch(path string, readOnly bool) KillSwitch {
	if path != "" {
		return FileSwitch{Path: path}
	}
	return staticSwitch(!readOnly)
}

// FileSwitch reads the kill switch file on every call, with no cache, so a
// flip takes effect on the next call. Only content that is exactly
// read_write after trimming allows account changes; anything else, an empty
// or missing file, or a read error pauses them.
type FileSwitch struct {
	Path string
}

// AccountChangesAllowed reads the file now.
func (s FileSwitch) AccountChangesAllowed() bool {
	b, err := os.ReadFile(s.Path)
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(b)) == readWrite
}

// Mode is what is enforced right now. An unreadable file is read_only (what
// AccountChangesAllowed does) and its error is returned for the caller to
// log.
func (s FileSwitch) Mode() (string, error) {
	b, err := os.ReadFile(s.Path)
	if err != nil {
		return ModeReadOnly, fmt.Errorf("read kill switch file: %w", err)
	}
	if strings.TrimSpace(string(b)) == ModeReadWrite {
		return ModeReadWrite, nil
	}
	return ModeReadOnly, nil
}

type staticSwitch bool

// Mode comes from the READ_ONLY setting.
func (s staticSwitch) Mode() (string, error) {
	if s {
		return ModeReadWrite, nil
	}
	return ModeReadOnly, nil
}

func (s staticSwitch) AccountChangesAllowed() bool { return bool(s) }
