package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/toyinogun/AIITsupport/internal/install"
)

// runOps is the day two commands (spec 0015, AC-19 to AC-22): release,
// pause, resume, uninstall and seal-key-backup, each `seed <name> --cluster
// <name>`, normally through make.
func runOps(ctx context.Context, name string, args []string, out io.Writer) error {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	cluster := fs.String("cluster", "", "the folder under deploy/clusters/")
	sha := fs.String("sha", "", "release: the commit whose images to roll out")
	if err := fs.Parse(args); err != nil || *cluster == "" || fs.NArg() != 0 {
		return fmt.Errorf("%s needs --cluster <name>\n%w", name, errUsage)
	}
	root, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("find the working directory: %w", err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("find your home directory: %w", err)
	}
	opts := install.Options{Root: root, Cluster: *cluster, Out: out, In: os.Stdin, Home: home, TempDir: os.TempDir()}
	switch name {
	case "release":
		if *sha == "" {
			return fmt.Errorf("release needs --sha <commit>\n%w", errUsage)
		}
		return install.Release(ctx, opts, *sha)
	case "cluster-init":
		return install.ClusterInit(ctx, opts)
	case "pause":
		return install.Pause(ctx, opts)
	case "resume":
		return install.Resume(ctx, opts)
	case "uninstall":
		return install.Uninstall(ctx, opts)
	default:
		return install.SealKeyBackup(ctx, opts)
	}
}
