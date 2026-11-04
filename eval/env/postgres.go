// Package env starts the throwaway environment the eval harness runs in
// (spec 0018, AC-2): Postgres, Samba, Zammad, and a fake Entra. Everything in
// it is generated per run and removed at the end.
package env

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	schema "github.com/toyinogun/AIITsupport/db"
	"github.com/toyinogun/AIITsupport/internal/migrate"
)

// postgresImage must match compose.yaml and the CI workflow.
const postgresImage = "postgres:17.6-alpine"

// App roles, as db/roles.sql creates them.
const (
	RoleOwner      = "noord_owner"
	RoleAgent      = "noord_agent"
	RoleToolServer = "noord_toolserver"
	RoleEval       = "noord_eval"
)

const (
	templateDB = "noord_eval_template"
	resultsDB  = "noord_eval_results"
	pgTimeout  = 2 * time.Minute
)

// Postgres is the run's database server: a migrated template, a results
// database, and one cloned database per attempt.
type Postgres struct {
	container *testcontainers.DockerContainer
	host      string
	superPass string
	// passwords are the per run passwords of the app roles.
	passwords map[string]string
}

// StartPostgres starts the server, gives every app role a fresh password,
// migrates a template database and clones the results database from it.
func StartPostgres(ctx context.Context) (*Postgres, error) {
	super, err := randomHex(16)
	if err != nil {
		return nil, err
	}
	ctr, err := testcontainers.Run(ctx, postgresImage,
		testcontainers.WithEnv(map[string]string{"POSTGRES_PASSWORD": super}),
		testcontainers.WithExposedPorts("5432/tcp"),
		testcontainers.WithWaitStrategyAndDeadline(pgTimeout,
			wait.ForLog("database system is ready to accept connections").WithOccurrence(2)),
	)
	p := &Postgres{container: ctr, superPass: super, passwords: map[string]string{}}
	if err != nil {
		return nil, errors.Join(fmt.Errorf("run the Postgres container: %w", err), p.Close())
	}
	hostPort, err := ctr.PortEndpoint(ctx, "5432/tcp", "")
	if err != nil {
		return nil, errors.Join(fmt.Errorf("find the Postgres port: %w", err), p.Close())
	}
	p.host = hostPort
	if err := p.prepare(ctx); err != nil {
		return nil, errors.Join(err, p.Close())
	}
	return p, nil
}

// prepare applies db/roles.sql, replaces the roles' fixed dev passwords,
// migrates the template, and clones the results database.
func (p *Postgres) prepare(ctx context.Context) error {
	admin, err := pgx.Connect(ctx, p.superURL("postgres"))
	if err != nil {
		return fmt.Errorf("connect as the superuser: %w", err)
	}
	defer func() { _ = admin.Close(context.WithoutCancel(ctx)) }()

	if _, err := admin.Exec(ctx, schema.Roles); err != nil {
		return fmt.Errorf("apply db/roles.sql: %w", err)
	}
	for _, role := range []string{RoleOwner, RoleAgent, RoleToolServer, RoleEval} {
		pw, err := randomHex(16)
		if err != nil {
			return err
		}
		// pw is hex, so it needs no quoting beyond the literal.
		if _, err := admin.Exec(ctx, fmt.Sprintf("ALTER ROLE %s PASSWORD '%s'", role, pw)); err != nil {
			return fmt.Errorf("set the password of %s: %w", role, err)
		}
		p.passwords[role] = pw
	}
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+templateDB+" OWNER "+RoleOwner); err != nil {
		return fmt.Errorf("create the template database: %w", err)
	}
	cfg, err := pgx.ParseConfig(p.URL(RoleOwner, templateDB))
	if err != nil {
		return fmt.Errorf("parse the owner url: %w", err)
	}
	if _, err := migrate.Up(ctx, cfg); err != nil {
		return err
	}
	return p.clone(ctx, admin, resultsDB)
}

// clone makes name from the template. CREATE DATABASE does not copy database
// privileges, so the REVOKE of migration 00008 is repeated (db/AGENTS.md).
func (p *Postgres) clone(ctx context.Context, admin *pgx.Conn, name string) error {
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name+" TEMPLATE "+templateDB+" OWNER "+RoleOwner); err != nil {
		return fmt.Errorf("clone the template into %s: %w", name, err)
	}
	if _, err := admin.Exec(ctx, "REVOKE TEMPORARY ON DATABASE "+name+" FROM PUBLIC"); err != nil {
		return fmt.Errorf("revoke temporary on %s: %w", name, err)
	}
	return nil
}

// NewAttemptDB clones the template into a fresh database, so no approval,
// audit row, limit, or window of an earlier attempt exists in it (AC-7).
func (p *Postgres) NewAttemptDB(ctx context.Context) (name string, err error) {
	suffix, err := randomHex(6)
	if err != nil {
		return "", err
	}
	name = "noord_attempt_" + suffix
	admin, err := pgx.Connect(ctx, p.superURL("postgres"))
	if err != nil {
		return "", fmt.Errorf("connect as the superuser: %w", err)
	}
	defer func() { _ = admin.Close(context.WithoutCancel(ctx)) }()
	if err := p.clone(ctx, admin, name); err != nil {
		return "", err
	}
	return name, nil
}

// DropDatabase removes a database, closing any connection still open to it.
func (p *Postgres) DropDatabase(ctx context.Context, name string) error {
	admin, err := pgx.Connect(ctx, p.superURL("postgres"))
	if err != nil {
		return fmt.Errorf("connect as the superuser: %w", err)
	}
	defer func() { _ = admin.Close(context.WithoutCancel(ctx)) }()
	if _, err := admin.Exec(ctx, "DROP DATABASE IF EXISTS "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)"); err != nil {
		return fmt.Errorf("drop %s: %w", name, err)
	}
	return nil
}

// URL is the connection URL for role on database name.
func (p *Postgres) URL(role, name string) string {
	u := url.URL{Scheme: "postgres", User: url.UserPassword(role, p.passwords[role]), Host: p.host, Path: "/" + name}
	q := u.Query()
	q.Set("sslmode", "disable")
	u.RawQuery = q.Encode()
	return u.String()
}

// ResultsURL is the results database as role.
func (p *Postgres) ResultsURL(role string) string { return p.URL(role, resultsDB) }

func (p *Postgres) superURL(name string) string {
	u := url.URL{Scheme: "postgres", User: url.UserPassword("postgres", p.superPass), Host: p.host, Path: "/" + name}
	q := u.Query()
	q.Set("sslmode", "disable")
	u.RawQuery = q.Encode()
	return u.String()
}

// Close stops the container.
func (p *Postgres) Close() error {
	if p.container == nil {
		return nil
	}
	return testcontainers.TerminateContainer(p.container)
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("read random bytes: %w", err)
	}
	return hex.EncodeToString(b), nil
}
