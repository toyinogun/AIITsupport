package env

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/toyinogun/AIITsupport/internal/seed/directory/sambatest"
	"github.com/toyinogun/AIITsupport/internal/seed/evalreset"
	"github.com/toyinogun/AIITsupport/internal/zammad"
)

// Options are the run's inputs to Start.
type Options struct {
	// RepoRoot is the repository root (it holds seed/, cmd/, and db/).
	RepoRoot string
	// KBStaleAsOf is the ticket set's as_of date, the child's KB_STALE_AS_OF.
	KBStaleAsOf string
}

// Env is the whole throwaway environment of one run (spec 0018, AC-2).
type Env struct {
	Postgres *Postgres
	Samba    *Samba
	Zammad   *Zammad
	Entra    *Entra
	// Token is the run's TOOLSERVER_TOKEN, also the agent's MCP bearer.
	Token string
	// Baseline is the outside state right after seeding, the state every
	// attempt must leave behind (AC-7).
	Baseline evalreset.Snapshot

	opts Options
	dir  string
	bin  string
}

// Start builds the tool server and starts every container. On any error it
// removes what it already started.
func Start(ctx context.Context, opts Options) (*Env, error) {
	root, err := filepath.Abs(opts.RepoRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve the repository root: %w", err)
	}
	opts.RepoRoot = root
	dir, err := os.MkdirTemp("", "noord-eval-")
	if err != nil {
		return nil, fmt.Errorf("make the run directory: %w", err)
	}
	token, err := randomHex(32)
	if err != nil {
		return nil, errors.Join(err, os.RemoveAll(dir))
	}
	e := &Env{opts: opts, dir: dir, Token: token}
	if err := e.start(ctx); err != nil {
		return nil, errors.Join(err, e.Close())
	}
	return e, nil
}

func (e *Env) start(ctx context.Context) (err error) {
	if e.bin, err = BuildToolServer(ctx, e.opts.RepoRoot, e.dir); err != nil {
		return err
	}
	if e.Postgres, err = StartPostgres(ctx); err != nil {
		return err
	}
	if e.Samba, err = StartSamba(ctx, e.opts.RepoRoot); err != nil {
		return err
	}
	if e.Entra, err = StartEntra(e.Samba.Model); err != nil {
		return err
	}
	if e.Zammad, err = StartZammad(ctx, e.opts.RepoRoot, e.Samba); err != nil {
		return err
	}
	reset, err := e.Reset()
	if err != nil {
		return err
	}
	e.Baseline, err = evalreset.Take(ctx, reset)
	return err
}

// Reset is the undo and verify view of the environment.
func (e *Env) Reset() (evalreset.Env, error) {
	admin, err := zammad.New(zammad.Options{
		BaseURL: e.Zammad.URL, Timeout: 30 * time.Second,
		Auth: zammad.BasicAuth{Login: e.Zammad.AdminEmail, Password: e.Zammad.AdminPassword},
	})
	if err != nil {
		return evalreset.Env{}, fmt.Errorf("make the Zammad admin client: %w", err)
	}
	return evalreset.Env{
		Dir: e.Samba.Dir, Model: e.Samba.Model, Zammad: admin, Graph: e.Entra.Fake, EntraIDs: e.Entra.UserIDs,
	}, nil
}

// UndoAndVerify puts the outside state back to the baseline and checks it. A
// difference left over is an error wrapping evalreset.ErrDirty.
func (e *Env) UndoAndVerify(ctx context.Context) error {
	reset, err := e.Reset()
	if err != nil {
		return err
	}
	if err := evalreset.Undo(ctx, reset, e.Baseline); err != nil {
		return fmt.Errorf("undo the attempt: %w", err)
	}
	return evalreset.Verify(ctx, reset, e.Baseline)
}

// Attempt is one ticket attempt's own database and tool server child.
type Attempt struct {
	// DBName is the attempt database; OwnerURL reads its evidence as the owner
	// (AC-7) and AgentURL is the in process agent's connection (noord_agent).
	DBName, OwnerURL, AgentURL string
	// Child is the running tool server.
	Child *Child

	env *Env
}

// NewAttempt clones the template into a fresh database and starts the tool
// server against it.
func (e *Env) NewAttempt(ctx context.Context) (*Attempt, error) {
	name, err := e.Postgres.NewAttemptDB(ctx)
	if err != nil {
		return nil, err
	}
	a := &Attempt{
		DBName: name, env: e,
		OwnerURL: e.Postgres.URL(RoleOwner, name), AgentURL: e.Postgres.URL(RoleAgent, name),
	}
	child, err := StartToolServer(ctx, ChildConfig{
		Bin: e.bin, Token: e.Token, DatabaseURL: e.Postgres.URL(RoleToolServer, name),
		LDAPURL: e.Samba.Dir.URL, LDAPBaseDN: sambatest.BaseDN, LDAPBindDN: e.Samba.BindDN,
		LDAPBindPassword: e.Samba.BindPassword, LDAPCAFile: e.Samba.CAPath(),
		ResetBindDN: sambatest.PwresetDN, ResetBindPassword: sambatest.PwresetPassword,
		ZammadURL: e.Zammad.URL, ZammadToken: e.Zammad.Token, ZammadGroup: e.Zammad.Group,
		GraphBaseURL: e.Entra.BaseURL(), GraphAuthorityHost: e.Entra.AuthorityHost(), GraphCAFile: e.Entra.CAFile,
		KBStaleAsOf: e.opts.KBStaleAsOf, LogFile: filepath.Join(e.dir, "toolserver.log"),
	})
	if err != nil {
		return nil, errors.Join(err, e.Postgres.DropDatabase(ctx, name))
	}
	a.Child = child
	return a, nil
}

// ToolServerLog is the tail of the tool server child's log (ids and fixed
// tokens only), for debugging a surprising attempt.
func (a *Attempt) ToolServerLog() string {
	b, err := os.ReadFile(filepath.Join(a.env.dir, "toolserver.log"))
	if err != nil {
		return ""
	}
	return tail(string(b))
}

// Close stops the child and drops the attempt database.
func (a *Attempt) Close(ctx context.Context) error {
	return errors.Join(a.Child.Stop(), a.env.Postgres.DropDatabase(ctx, a.DBName))
}

// Close removes every container and file the run made.
func (e *Env) Close() error {
	var errs []error
	if e.Zammad != nil {
		errs = append(errs, e.Zammad.Close())
	}
	if e.Entra != nil {
		errs = append(errs, e.Entra.Close())
	}
	if e.Samba != nil {
		errs = append(errs, e.Samba.Close())
	}
	if e.Postgres != nil {
		errs = append(errs, e.Postgres.Close())
	}
	errs = append(errs, os.RemoveAll(e.dir))
	return errors.Join(errs...)
}
