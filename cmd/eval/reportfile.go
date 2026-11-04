package main

import (
	"fmt"
	"io"
	"os"
	"regexp"
	"time"

	"github.com/toyinogun/AIITsupport/eval/report"
	"github.com/toyinogun/AIITsupport/eval/runner"
	"github.com/toyinogun/AIITsupport/eval/runstore"
	"github.com/toyinogun/AIITsupport/eval/ticketset"
)

const runsDir = "eval/runs"

var labelRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,79}$`)

// outDir is where a run's files go: --out, else eval/runs for a labeled run,
// else a new temp directory, never the repo (AC-13).
func outDir(f runFlags) (string, error) {
	switch {
	case f.out != "":
		return f.out, nil
	case f.label != "":
		return runsDir, nil
	}
	dir, err := os.MkdirTemp("", "noord-eval-report-")
	if err != nil {
		return "", fmt.Errorf("make a report directory: %w", err)
	}
	return dir, nil
}

// checkLabel refuses a bad label, and a label that already has a report file
// unless --force is given (AC-1).
func checkLabel(f runFlags) error {
	if f.label == "" {
		return nil
	}
	if !labelRe.MatchString(f.label) {
		return fmt.Errorf("%w: --label may hold letters, digits, dot, dash, and underscore", errUsage)
	}
	dir := f.out
	if dir == "" {
		dir = runsDir
	}
	if _, err := os.Stat(dir + "/" + f.label + ".json"); err == nil && !f.force {
		return fmt.Errorf("a run labeled %q already has a report file; use --force to replace it", f.label)
	}
	return nil
}

// collector gathers the report rows as attempts finish.
type collector struct {
	results     []report.Result
	transcripts []report.Transcript
	saved       []runstore.Result
}

func (c *collector) add(t ticketset.Ticket, attempt int, o runner.Observation) {
	c.results = append(c.results, report.Result{
		Ticket: t.ID, Attempt: attempt, Category: t.Category, ExpectedOutcome: t.Expected.Outcome,
		ActualOutcome: o.Score.Outcome, RefusalCode: o.Score.RefusalCode,
		ExpectedTools: nonNil(t.Expected.Tools), ActualTools: nonNil(o.Score.Tools),
		Passed: o.Score.Passed, Unsafe: o.Score.Unsafe, Wrong: o.Score.Wrong, FailedChecks: nonNil(o.Score.FailedChecks),
		DurationMS: o.DurationMS, ApprovalWaitMS: o.ApprovalWaitMS,
		InputTokens: o.InputTokens, OutputTokens: o.OutputTokens, CostMicroUSD: o.CostMicroUSD,
	})
	if !o.Score.Passed || o.Score.Unsafe {
		c.transcripts = append(c.transcripts, report.Transcript{
			Ticket: t.ID, Attempt: attempt, Posts: nonNil(o.Posts), Tools: nonNil(o.Score.Tools),
		})
	}
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// runInfo is the run header's inputs, known once the run ends.
type runInfo struct {
	Flags           runFlags
	GitSHA, Model   string
	Set             ticketset.Set
	PromptHash      string
	ToolsHash       string
	Status          string
	StopReason      string
	Partial         bool
	PlannedAttempts int
}

func (c *collector) file(info runInfo, metrics map[string]float64) report.File {
	return report.File{
		Header: report.Header{
			Label: info.Flags.label, GitSHA: info.GitSHA, Model: info.Model, TicketSetHash: info.Set.Hash,
			PromptHash: info.PromptHash, ToolsHash: info.ToolsHash, Repeats: info.Flags.repeat,
			CreatedAt: time.Now().UTC(), Status: info.Status, StopReason: info.StopReason,
			Partial: info.Partial, PlannedAttempts: info.PlannedAttempts,
		},
		Results: c.results, Metrics: metrics, Transcripts: c.transcripts,
	}
}

// reportCommand rebuilds the Markdown of a saved run: eval report <label or path>.
func reportCommand(args []string, out io.Writer) error {
	if len(args) != 1 {
		return errUsage
	}
	f, err := report.Load(runsDir, args[0])
	if err != nil {
		return err
	}
	_, err = io.WriteString(out, report.Markdown(f))
	return err
}

// compareCommand prints two saved runs side by side: eval compare <a> <b>.
func compareCommand(args []string, out io.Writer) error {
	if len(args) != 2 {
		return errUsage
	}
	a, err := report.Load(runsDir, args[0])
	if err != nil {
		return err
	}
	b, err := report.Load(runsDir, args[1])
	if err != nil {
		return err
	}
	_, err = io.WriteString(out, report.Compare(a, b).Format(args[0], args[1]))
	return err
}
