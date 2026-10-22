package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"slices"
	"strings"

	"github.com/toyinogun/AIITsupport/internal/cause"
	"github.com/toyinogun/AIITsupport/internal/config"
	"github.com/toyinogun/AIITsupport/internal/install"
	"github.com/toyinogun/AIITsupport/internal/mcpclient"
	"github.com/toyinogun/AIITsupport/internal/migrate"
	"github.com/toyinogun/AIITsupport/internal/readiness"
)

// smokeCheck is one line of seed smoke's output. A failure's detail holds
// only fixed tokens and numbers, never error text.
type smokeCheck struct {
	name string
	run  func(context.Context) (detail string, err error)
}

// smokeError is a check that ran but found the wrong answer; it is its own
// cause token.
type smokeError string

func (f smokeError) Error() string { return string(f) }
func (f smokeError) Cause() string { return string(f) }

// smokeDeps are what the checks call, so tests can stand in for the MCP
// session and the database.
type smokeDeps struct {
	http      *http.Client
	listTools func(context.Context) (int, error)
	current   func(context.Context) (int64, error)
	latest    func() (int64, error)
}

// runSmoke is the cluster's smoke check (spec 0015, AC-17): both readiness
// reports, the tool list, and the migration version. Inside the cluster it
// takes no arguments; with --cluster <name> it runs that check as a Job on
// the cluster from your machine (make smoke).
func runSmoke(ctx context.Context, args []string, out io.Writer) error {
	if len(args) != 0 {
		return runClusterSmoke(ctx, args, out)
	}
	cfg, err := config.LoadSmoke(os.LookupEnv)
	if err != nil {
		return err
	}
	tools, err := mcpclient.New(cfg.ToolServerURL, cfg.ToolServerToken)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, cfg.Timeout)
	defer cancel()
	checks, err := smokeChecks(cfg, smokeDeps{
		http: &http.Client{},
		listTools: func(ctx context.Context) (int, error) {
			list, err := tools.ListTools(ctx)
			return len(list), err
		},
		current: func(ctx context.Context) (int64, error) { return migrate.Current(ctx, cfg.DatabaseURL) },
		latest:  migrate.Latest,
	})
	if err != nil {
		return err
	}
	return smoke(ctx, out, checks)
}

func runClusterSmoke(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("smoke", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	cluster := fs.String("cluster", "", "the folder under deploy/clusters/")
	if err := fs.Parse(args); err != nil || *cluster == "" || fs.NArg() != 0 {
		return fmt.Errorf("smoke takes no arguments, or --cluster <name>\n%w", errUsage)
	}
	root, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("find the working directory: %w", err)
	}
	return install.Smoke(ctx, install.Options{Root: root, Cluster: *cluster, Out: out})
}

func smokeChecks(cfg config.Smoke, d smokeDeps) ([]smokeCheck, error) {
	toolserver, err := readiness.URL(cfg.ToolServerURL)
	if err != nil {
		return nil, fmt.Errorf("TOOLSERVER_URL: %w", err)
	}
	agent, err := readiness.URL(cfg.AgentURL)
	if err != nil {
		return nil, fmt.Errorf("SMOKE_AGENT_URL: %w", err)
	}
	ready := func(url, token string) func(context.Context) (string, error) {
		return func(ctx context.Context) (string, error) {
			r, err := readiness.Fetch(ctx, d.http, url, token)
			return unavailable(r), err
		}
	}
	return []smokeCheck{
		{"toolserver_readyz", ready(toolserver, cfg.ToolServerToken)},
		{"agent_readyz", ready(agent, "")},
		{"tools_list", func(ctx context.Context) (string, error) {
			n, err := d.listTools(ctx)
			if err == nil && n == 0 {
				err = smokeError("no_tools")
			}
			return "", err
		}},
		{"migrations", func(ctx context.Context) (string, error) {
			want, err := d.latest()
			if err != nil {
				return "", err
			}
			got, err := d.current(ctx)
			if err != nil {
				return "", err
			}
			if got != want {
				return fmt.Sprintf(" database=%d embedded=%d", got, want), smokeError("version_mismatch")
			}
			return "", nil
		}},
	}, nil
}

// unavailable lists a report's failed checks as name=cause, sorted.
func unavailable(r readiness.Report) string {
	var out []string
	for name, c := range r.Checks {
		if c.Status == readiness.Unavailable {
			out = append(out, name+"="+c.Cause)
		}
	}
	if len(out) == 0 {
		return ""
	}
	slices.Sort(out)
	return " " + strings.Join(out, " ")
}

// smoke runs every check and prints one line each: pass, or fail with the
// cause token. Any failure fails the command.
func smoke(ctx context.Context, out io.Writer, checks []smokeCheck) error {
	failed := 0
	for _, c := range checks {
		detail, err := c.run(ctx)
		if err != nil {
			failed++
			fmt.Fprintf(out, "fail %s %s%s\n", c.name, cause.Of(err), detail)
			continue
		}
		fmt.Fprintf(out, "pass %s\n", c.name)
	}
	if failed > 0 {
		return fmt.Errorf("%d of %d smoke checks failed", failed, len(checks))
	}
	return nil
}
