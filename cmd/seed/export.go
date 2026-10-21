package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/toyinogun/AIITsupport/internal/config"
)

// What seed export writes under its target (spec 0015, AC-11): the seed
// executable, and the helpdesk tree and user list it reads, so a Job on
// the Zammad image can run `<dir>/seed helpdesk` with no shell and no copy
// of our image.
const (
	exportBinary      = "seed"
	exportHelpdeskDir = "data/seed/helpdesk"
	exportSeedFile    = "data/seed/directory/noord.yaml"
)

func runExport(args []string, out io.Writer) error {
	if len(args) != 1 {
		return errUsage
	}
	dir := args[0]
	files, err := config.LoadSeedFiles(os.LookupEnv)
	if err != nil {
		return err
	}
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("find the seed executable: %w", err)
	}
	if err := copyFile(self, filepath.Join(dir, exportBinary), 0o755); err != nil {
		return err
	}
	if err := copyFile(files.File, filepath.Join(dir, exportSeedFile), 0o644); err != nil {
		return err
	}
	n, err := copyTree(files.HelpdeskDir, filepath.Join(dir, exportHelpdeskDir))
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "exported seed, %s and %d helpdesk files to %s\n", filepath.Base(files.File), n, dir)
	return nil
}

// copyTree copies every regular file under src to dst and returns how many.
func copyTree(src, dst string) (int, error) {
	n := 0
	err := filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		n++
		return copyFile(path, filepath.Join(dst, rel), 0o644)
	})
	if err != nil {
		return n, fmt.Errorf("export %s: %w", src, err)
	}
	return n, nil
}

func copyFile(src, dst string, mode fs.FileMode) (err error) {
	in, err := os.Open(src) //nolint:gosec // G304: paths come from config and the walk
	if err != nil {
		return fmt.Errorf("export %s: %w", src, err)
	}
	defer func() { _ = in.Close() }()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil { //nolint:gosec // G301: read by the Zammad image's user
		return fmt.Errorf("export to %s: %w", filepath.Dir(dst), err)
	}
	outFile, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode) //nolint:gosec // G302,G304: an executable and its data, under the target
	if err != nil {
		return fmt.Errorf("export to %s: %w", dst, err)
	}
	defer func() { err = errors.Join(err, outFile.Close()) }()
	if _, err := io.Copy(outFile, in); err != nil {
		return fmt.Errorf("export %s: %w", src, err)
	}
	return nil
}
