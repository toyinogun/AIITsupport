package tools

import (
	"os"
	"strings"
)

// readWrite is the only kill switch content that allows account changes.
const readWrite = "read_write"

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

type staticSwitch bool

func (s staticSwitch) AccountChangesAllowed() bool { return bool(s) }
