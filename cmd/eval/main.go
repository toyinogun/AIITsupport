// Command eval plays the 50 eval tickets against the real agent and the real
// tool server, in a throwaway environment it starts itself (spec 0018).
package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"slices"
	"strings"
	"syscall"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/toyinogun/AIITsupport/eval/report"
	"github.com/toyinogun/AIITsupport/eval/runstore"
	"github.com/toyinogun/AIITsupport/eval/score"

	"github.com/toyinogun/AIITsupport/eval/env"
	"github.com/toyinogun/AIITsupport/eval/runner"
	"github.com/toyinogun/AIITsupport/eval/ticketset"
	"github.com/toyinogun/AIITsupport/internal/config"
	"github.com/toyinogun/AIITsupport/internal/logging"
)

const service = "eval"

const usage = `usage:
  eval run [--only ev-001,ev-002] [--category <name>] [--repeat N] [--label <name>] [--out <dir>]
           [--tickets <path>] [--model <id>] [--freeze] [--force]
  eval report <label or path>
  eval compare <label or path> <label or path>`

var errUsage = errors.New(usage)

// errUnsafe means a finished run had an unsafe or wrong action (exit code 3).
var errUnsafe = errors.New("a ticket had an unsafe or wrong action")

// exitCode is 0 for success, 3 for a finished run with an unsafe or wrong
// action, and 1 for anything else, including a failed run (AC-12).
func exitCode(err error) int {
	switch {
	case err == nil:
		return 0
	case errors.Is(err, errUnsafe) && !errors.Is(err, errRunFailed):
		return 3
	}
	return 1
}

// errRunFailed marks a run that failed or stopped early; exit code 1 wins.
var errRunFailed = errors.New("the run failed")

func main() {
	err := run(os.Args[1:], os.Stdout)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", service, err)
	}
	os.Exit(exitCode(err))
}

func run(args []string, out io.Writer) error {
	if len(args) == 0 {
		return errUsage
	}
	switch args[0] {
	case "run":
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		return runTickets(ctx, args[1:], out)
	case "report":
		return reportCommand(args[1:], out)
	case "compare":
		return compareCommand(args[1:], out)
	}
	return errUsage
}

type runFlags struct {
	only, category, tickets, model, label, out string
	debug, freeze, force                       bool
	repeat                                     int
}

func parseRunFlags(args []string) (runFlags, error) {
	var f runFlags
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&f.only, "only", "", "comma separated ticket ids")
	fs.StringVar(&f.tickets, "tickets", "eval/tickets.yaml", "the ticket file")
	fs.StringVar(&f.model, "model", "", "the model id; default LLM_MODEL")
	fs.IntVar(&f.repeat, "repeat", 1, "how many times each ticket runs, 1 to 5")
	fs.StringVar(&f.label, "label", "", "a name for the saved run")
	fs.StringVar(&f.out, "out", "", "the directory for the report files")
	fs.StringVar(&f.category, "category", "", "run one category only")
	fs.BoolVar(&f.freeze, "freeze", false, "lock the ticket set after a complete labeled run")
	fs.BoolVar(&f.force, "force", false, "replace a report file with the same label")
	fs.BoolVar(&f.debug, "debug", false, "print replies and the tool server log tail")
	if err := fs.Parse(args); err != nil {
		return runFlags{}, fmt.Errorf("%w: %w", errUsage, err)
	}
	if fs.NArg() > 0 {
		return runFlags{}, errUsage
	}
	if f.repeat < 1 || f.repeat > 5 {
		return runFlags{}, fmt.Errorf("%w: --repeat must be 1 to 5", errUsage)
	}
	if f.freeze && f.label == "" {
		return runFlags{}, fmt.Errorf("%w: --freeze needs --label", errUsage)
	}
	return f, nil
}

func runTickets(ctx context.Context, args []string, out io.Writer) error {
	f, err := parseRunFlags(args)
	if err != nil {
		return err
	}
	cfg, err := config.LoadEval(os.LookupEnv)
	if err != nil {
		return err
	}
	if f.model != "" {
		cfg.LLMModel = f.model
	}
	if err := checkLabel(f); err != nil {
		return err
	}
	if frozen, err := ticketset.CheckLock(f.tickets, ticketset.LockPath(f.tickets)); frozen && err != nil {
		return fmt.Errorf("refusing to run: %w", err)
	}
	set, err := ticketset.Load(f.tickets, "seed/directory/noord.yaml", "seed/helpdesk/kb.yaml")
	if err != nil {
		return err
	}
	selected, err := selectTickets(set, f.only, f.category)
	if err != nil {
		return err
	}

	e, err := env.Start(ctx, env.Options{RepoRoot: ".", KBStaleAsOf: set.AsOf})
	if err != nil {
		return fmt.Errorf("start the environment: %w", err)
	}
	defer func() {
		if !cfg.KeepEnv {
			if cerr := e.Close(); cerr != nil {
				fmt.Fprintf(os.Stderr, "%s: remove the environment: %v\n", service, cerr)
			}
		}
	}()

	r := &runner.Runner{Env: e, Set: set, Config: cfg, Logger: logging.New(os.Stderr, "info", service)}
	return playTickets(ctx, r, set, selected, f, out)
}

// playTickets runs the tickets, prints a line for each attempt, saves each
// one, and prints the summary. A run that stops early is saved as failed and
// returns an error (exit 1).
func playTickets(ctx context.Context, r *runner.Runner, set ticketset.Set, selected []ticketset.Ticket, f runFlags, out io.Writer) error {
	var store *runstore.Store
	rows := &collector{}
	res, err := runner.Play(ctx, r, selected,
		runner.PlayConfig{Repeat: f.repeat, MaxCostMicroUSD: r.Config.MaxCostMicroUSD()},
		func(t ticketset.Ticket, attempt int, obs runner.Observation) error {
			var serr error
			if store == nil {
				if store, serr = startStore(ctx, r, set, f); serr != nil {
					return serr
				}
			}
			printTicket(out, t, attempt, obs, f.debug)
			rows.add(t, attempt, obs)
			rows.saved = append(rows.saved, saved(t, attempt, obs))
			return store.Add(ctx, rows.saved[len(rows.saved)-1])
		})
	metrics := score.Summarize(res.Attempts)
	printSummary(out, metrics)
	if store == nil {
		return err
	}
	partial := f.only != "" || f.category != "" || res.StopReason != ""
	status := "completed"
	extra := map[string]any{"partial": partial, "planned_attempts": len(selected) * f.repeat}
	if res.StopReason != "" {
		status, extra["stop_reason"] = "failed", res.StopReason
	}
	if ferr := store.Finish(context.WithoutCancel(ctx), status, metrics, extra); ferr != nil {
		err = errors.Join(err, ferr)
	}
	info := runInfo{
		Flags: f, Model: r.Config.LLMModel, Set: set, PromptHash: runner.PromptHash(), ToolsHash: r.ToolsHash(),
		Status: status, StopReason: res.StopReason, Partial: partial, PlannedAttempts: len(selected) * f.repeat,
	}
	if sha, serr := gitSHA(ctx); serr == nil {
		info.GitSHA = sha
	}
	if werr := writeReport(out, f, rows.file(info, metrics)); werr != nil {
		err = errors.Join(err, werr)
	}
	if res.StopReason != "" {
		err = errors.Join(err, fmt.Errorf("stopped early: %s", res.StopReason))
	}
	if err == nil && f.freeze && status == "completed" && !partial {
		err = ticketset.WriteLock(ticketset.LockPath(f.tickets), set.Hash)
	}
	if cerr := copyRun(ctx, r.Config.DatabaseURL, store, status, metrics, extra, rows.saved); cerr != nil {
		err = errors.Join(err, fmt.Errorf("copy the run to EVAL_DATABASE_URL: %w", cerr))
	}
	if err != nil {
		return fmt.Errorf("%w: %w", errRunFailed, err)
	}
	if metrics["unsafe_tickets"]+metrics["wrong_tickets"] > 0 {
		return fmt.Errorf("%w (unsafe %g, wrong %g)", errUnsafe, metrics["unsafe_tickets"], metrics["wrong_tickets"])
	}
	return nil
}

// copyRun copies the finished run to EVAL_DATABASE_URL once, when it is set (AC-10).
func copyRun(ctx context.Context, url string, store *runstore.Store, status string,
	metrics map[string]float64, extra map[string]any, results []runstore.Result,
) error {
	if url == "" || store == nil {
		return nil
	}
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return fmt.Errorf("open the database: %w", err)
	}
	defer pool.Close()
	return runstore.Copy(ctx, pool, store.Header(), results, status, metrics, extra)
}

// writeReport writes the run file and its Markdown and says where.
func writeReport(out io.Writer, f runFlags, file report.File) error {
	dir, err := outDir(f)
	if err != nil {
		return err
	}
	name := f.label
	if name == "" {
		name = "run"
	}
	jsonPath, mdPath, err := report.Write(dir, name, file)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "report %s\nreport %s\n", jsonPath, mdPath)
	return nil
}

func startStore(ctx context.Context, r *runner.Runner, set ticketset.Set, f runFlags) (*runstore.Store, error) {
	pool, err := pgxpool.New(ctx, r.Env.Postgres.ResultsURL(env.RoleEval))
	if err != nil {
		return nil, fmt.Errorf("open the results database: %w", err)
	}
	sha, err := gitSHA(ctx)
	if err != nil {
		return nil, err
	}
	return runstore.Start(ctx, pool, runstore.Header{
		Label: f.label, GitSHA: sha, Model: r.Config.LLMModel, TicketSetHash: set.Hash, Repeats: f.repeat,
		PromptHash: runner.PromptHash(), ToolsHash: r.ToolsHash(),
	})
}

// gitSHA is the working tree's commit, with -dirty when it has changes.
func gitSHA(ctx context.Context) (string, error) {
	sha, err := exec.CommandContext(ctx, "git", "rev-parse", "HEAD").Output()
	if err != nil {
		return "", fmt.Errorf("read the git commit: %w", err)
	}
	status, err := exec.CommandContext(ctx, "git", "status", "--porcelain").Output()
	if err != nil {
		return "", fmt.Errorf("read the git status: %w", err)
	}
	out := strings.TrimSpace(string(sha))
	if len(bytes.TrimSpace(status)) > 0 {
		out += "-dirty"
	}
	return out, nil
}

func saved(t ticketset.Ticket, attempt int, o runner.Observation) runstore.Result {
	return runstore.Result{
		TicketKey: t.ID, Attempt: attempt, ExpectedOutcome: t.Expected.Outcome, ActualOutcome: o.Score.Outcome,
		RefusalCode: o.Score.RefusalCode, ExpectedTools: t.Expected.Tools, ActualTools: o.Score.Tools,
		Passed: o.Score.Passed, Unsafe: o.Score.Unsafe, Wrong: o.Score.Wrong, FailedChecks: o.Score.FailedChecks,
		DurationMS: int(o.DurationMS), ApprovalWaitMS: int(o.ApprovalWaitMS),
		InputTokens: o.InputTokens, OutputTokens: o.OutputTokens, CostMicroUSD: o.CostMicroUSD,
		SlackChannelID: "DEVAL0001", SlackThreadTS: o.Thread,
	}
}

// printTicket prints ids, tokens, milliseconds, and costs only (AC-17).
func printTicket(out io.Writer, t ticketset.Ticket, attempt int, o runner.Observation, debug bool) {
	if debug {
		fmt.Fprintf(out, "posts=%q\ntoolserver log:\n%s\n", o.Posts, o.ToolServerLog)
	}
	fmt.Fprintf(out, "%s#%d outcome=%s passed=%t ms=%d cost_microusd=%d checks=%s tools=%s timed_out=%t\n",
		t.ID, attempt, orNone(o.Score.Outcome), o.Score.Passed, o.DurationMS, o.CostMicroUSD,
		orNone(strings.Join(o.Score.FailedChecks, ",")), orNone(strings.Join(o.Score.Tools, ",")), o.TimedOut)
}

func printSummary(out io.Writer, metrics map[string]float64) {
	keys := make([]string, 0, len(metrics))
	for k := range metrics {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	fmt.Fprintln(out, "summary")
	for _, k := range keys {
		fmt.Fprintf(out, "  %s=%g\n", k, metrics[k])
	}
}

func selectTickets(set ticketset.Set, only, category string) ([]ticketset.Ticket, error) {
	if category != "" {
		var out []ticketset.Ticket
		for _, t := range set.Tickets {
			if t.Category == category {
				out = append(out, t)
			}
		}
		if len(out) == 0 {
			return nil, fmt.Errorf("--category: no tickets in category %q", category)
		}
		return out, nil
	}
	if only == "" {
		return set.Tickets, nil
	}
	byID := map[string]ticketset.Ticket{}
	for _, t := range set.Tickets {
		byID[t.ID] = t
	}
	var out []ticketset.Ticket
	for _, id := range strings.Split(only, ",") {
		t, ok := byID[strings.TrimSpace(id)]
		if !ok {
			return nil, fmt.Errorf("--only: no ticket %q in the ticket file", id)
		}
		out = append(out, t)
	}
	return out, nil
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}
