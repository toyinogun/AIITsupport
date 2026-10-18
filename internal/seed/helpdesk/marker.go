package helpdesk

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/yuin/goldmark"
)

// MarkerPrefix starts every helpdesk marker.
const MarkerPrefix = "seed:sha256:"

// Marker hashes the seed inputs (spec 0006 key invariants): every regular
// file under helpdeskDir whose name does not start with ".", plus the extra
// files (noord.yaml and the dev CA). For each file in bytewise order of its
// slash separated path relative to root (the repo root): the path, a NUL
// byte, the raw bytes, a NUL byte. So a changed user list or regenerated
// certs also means reset, and the marker is the same from any directory.
func Marker(root, helpdeskDir string, extra ...string) (string, error) {
	var files []string
	err := filepath.WalkDir(helpdeskDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path != helpdeskDir && strings.HasPrefix(d.Name(), ".") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Type().IsRegular() {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("marker: list %s: %w", helpdeskDir, err)
	}
	files = append(files, extra...)

	byRel := map[string]string{}
	for _, f := range files {
		rel, err := relSlash(root, f)
		if err != nil {
			return "", err
		}
		byRel[rel] = f
	}
	rels := slices.Sorted(maps.Keys(byRel))

	h := sha256.New()
	for _, rel := range rels {
		data, err := os.ReadFile(byRel[rel])
		if err != nil {
			return "", fmt.Errorf("marker: read %s: %w", rel, err)
		}
		h.Write([]byte(rel))
		h.Write([]byte{0})
		h.Write(data)
		h.Write([]byte{0})
	}
	return MarkerPrefix + hex.EncodeToString(h.Sum(nil)), nil
}

// relSlash is path relative to root, slash separated. A path outside root
// is refused, since its name would depend on where root is.
func relSlash(root, path string) (string, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("marker: %w", err)
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("marker: %w", err)
	}
	rel, err := filepath.Rel(absRoot, absPath)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("marker: %s is outside the repo root %s", path, root)
	}
	return filepath.ToSlash(rel), nil
}

// markdown renders article bodies. goldmark escapes raw HTML by default
// (no html.WithUnsafe), so a body can never inject markup into Zammad.
var markdown = goldmark.New()

// ToHTML converts an article's Markdown body to HTML.
func ToHTML(body string) (string, error) {
	var buf bytes.Buffer
	if err := markdown.Convert([]byte(body), &buf); err != nil {
		return "", fmt.Errorf("convert markdown: %w", err)
	}
	return buf.String(), nil
}
