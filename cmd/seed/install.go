package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/toyinogun/AIITsupport/internal/install"
)

// runInstall is `seed install --cluster <name>` (spec 0015): run from the
// clone's root, normally through `make install CLUSTER=<name>`.
func runInstall(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("install", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	cluster := fs.String("cluster", "", "the folder under deploy/clusters/")
	if err := fs.Parse(args); err != nil || *cluster == "" || fs.NArg() != 0 {
		return fmt.Errorf("install needs --cluster <name>\n%w", errUsage)
	}
	root, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("find the working directory: %w", err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("find your home directory: %w", err)
	}
	return install.Run(ctx, install.Options{Root: root, Cluster: *cluster, Out: out, Home: home, TempDir: os.TempDir()})
}
