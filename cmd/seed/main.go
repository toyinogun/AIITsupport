// Command seed builds Noord's fictional directory (spec 0004) and helpdesk
// (spec 0006), migrates the database, and installs the system into a
// cluster (spec 0015). It is a dev and ops tool, never a long running
// service, and it is the only code that binds as the domain Administrator.
//
//	seed certs [--dir D] [--host H]...   make the dev CA and the Samba server cert
//	seed validate                        check the seed file, no network
//	seed directory                       fill a fresh directory from the seed file
//	seed login [--password P] <user>     bind as a user and report the lock state
//	seed lock <user>                     lock a user by failed binds
//	seed entra sync [--only U]           make Entra match SYNC-Entra-Users (spec 0005)
//	seed entra mfa [--clear] <user>      list or clear a user's phone methods (tool server app)
//	seed helpdesk                        fill a fresh Zammad from seed/helpdesk/ (spec 0006)
//	seed export <dir>                    copy the seed executable and the helpdesk tree to dir (spec 0015)
//	seed migrate                         wait for the database, then apply the migrations (spec 0015)
//	seed install --cluster C             install or update a cluster through ArgoCD (spec 0015)
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/toyinogun/AIITsupport/internal/install"
)

const service = "seed"

const usage = `usage:
  seed certs [--dir D] [--host H]...
  seed validate
  seed directory
  seed login [--password P] <username>
  seed lock <username>
  seed entra sync [--only <username>]
  seed entra mfa [--clear] <username>
  seed helpdesk
  seed export <dir>
  seed migrate
  seed smoke [--cluster <name>]
  seed install --cluster <name>`

// errUsage marks a mistake in how the command was called.
var errUsage = errors.New(usage)

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", service, err)
		os.Exit(exitCode(err))
	}
}

func run(args []string, out io.Writer) error {
	if len(args) == 0 {
		return errUsage
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cmd, rest := args[0], args[1:]
	switch cmd {
	case "certs":
		return runCerts(rest, out)
	case "validate":
		return runValidate(rest, out)
	case "directory":
		return runDirectory(ctx, rest, out)
	case "login":
		return runLogin(rest, out)
	case "lock":
		return runLock(ctx, rest, out)
	case "entra":
		return runEntra(ctx, rest, out)
	case "helpdesk":
		return runHelpdesk(ctx, rest, out)
	case "export":
		return runExport(rest, out)
	case "migrate":
		return runMigrate(ctx, rest, out)
	case "smoke":
		return runSmoke(ctx, rest, out)
	case "install":
		return runInstall(ctx, rest, out)
	case "cluster-init":
		return runOps(ctx, cmd, rest, out)
	case "release", "pause", "resume", "uninstall", "seal-key-backup":
		return runOps(ctx, cmd, rest, out)
	default:
		return fmt.Errorf("unknown command %q\n%w", cmd, errUsage)
	}
}

// exitCode is 1, or the installer's own code: 2 when its preflight found a
// problem and nothing was changed (spec 0015).
func exitCode(err error) int {
	if exit, ok := errors.AsType[*install.ExitError](err); ok {
		return exit.Code
	}
	return 1
}
