package install

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/toyinogun/AIITsupport/internal/config"
)

// Where the smoke check lives (AC-17): a suspended CronJob the chart
// renders, run on demand as a Job.
const (
	smokeNamespace = "noord-agent"
	smokeCronJob   = "noord-smoke"
)

// errSmokeFailed means the smoke Job ran and a check failed; its lines say which.
var errSmokeFailed = errors.New("the smoke check failed (see the lines above)")

// StartJob creates a Job named name from a CronJob's template.
func (k Kube) StartJob(ctx context.Context, namespace, cronJob, name string) error {
	_, err := k.kubectl(ctx, nil, "-n", namespace, "create", "job", name, "--from=cronjob/"+cronJob)
	return err
}

// JobOutcome reads whether a Job has finished, and whether it succeeded.
func (k Kube) JobOutcome(ctx context.Context, namespace, name string) (done, succeeded bool, err error) {
	out, err := k.kubectl(ctx, nil, "-n", namespace, "get", "job", name, "-o", "json")
	if err != nil {
		return false, false, err
	}
	var job struct {
		Status struct {
			Succeeded int `json:"succeeded"`
			Failed    int `json:"failed"`
		} `json:"status"`
	}
	if err := json.Unmarshal(out, &job); err != nil {
		return false, false, fmt.Errorf("parse Job %s: %w", name, err)
	}
	return job.Status.Succeeded > 0 || job.Status.Failed > 0, job.Status.Succeeded > 0, nil
}

// JobLogs is a finished Job's pod log.
func (k Kube) JobLogs(ctx context.Context, namespace, name string) ([]byte, error) {
	return k.kubectl(ctx, nil, "-n", namespace, "logs", "job/"+name)
}

// smoke runs the smoke check in the cluster and prints its lines (AC-17):
// one Job from the CronJob, named by the time so each run is its own.
func (s *state) smoke(ctx context.Context) error {
	name := smokeCronJob + "-" + s.Now().UTC().Format("20060102-150405")
	if err := s.kube.StartJob(ctx, smokeNamespace, smokeCronJob, name); err != nil {
		return err
	}
	var lastErr error
	for {
		done, ok, err := s.kube.JobOutcome(ctx, smokeNamespace, name)
		switch {
		case err != nil:
			lastErr = err // an API blip; the deadline names it
		case done:
			return s.smokeResult(ctx, name, ok)
		}
		select {
		case <-ctx.Done():
			msg := fmt.Sprintf("Job %s did not finish in time", name)
			if lastErr != nil {
				return fmt.Errorf("%s (last read failed: %w)", msg, lastErr)
			}
			return errors.New(msg)
		case <-time.After(s.Poll):
		}
	}
}

// smokeResult prints the Job's pass and fail lines, the only lines seed
// smoke writes, and fails when the Job did.
func (s *state) smokeResult(ctx context.Context, name string, ok bool) error {
	logs, err := s.kube.JobLogs(ctx, smokeNamespace, name)
	if err != nil {
		return err
	}
	for line := range strings.SplitSeq(string(bytes.TrimSpace(logs)), "\n") {
		if strings.HasPrefix(line, "pass ") || strings.HasPrefix(line, "fail ") {
			s.say("  %s", line)
		}
	}
	if !ok {
		return errSmokeFailed
	}
	return nil
}

// Smoke is `make smoke CLUSTER=<name>`: the smoke check alone, on the
// cluster that folder's KUBE_CONTEXT names. It changes nothing but the Job.
func Smoke(ctx context.Context, opts Options) error {
	s := &state{Options: opts.Defaults()}
	var err error
	if s.folder, err = OpenFolder(s.Root, s.Cluster); err != nil {
		return &ExitError{Code: ExitPreflight, Step: "preflight", Err: err}
	}
	if s.env, err = ReadEnvFile(s.folder.path(secretsFile)); err != nil {
		return &ExitError{Code: ExitPreflight, Step: "preflight", Err: err}
	}
	if s.cfg, err = config.LoadInstall(s.env.Lookup); err != nil {
		return &ExitError{Code: ExitPreflight, Step: "preflight", Err: err}
	}
	s.kube = Kube{run: s.Run, context: s.cfg.KubeContext}
	if err := s.checkContext(ctx); err != nil {
		return &ExitError{Code: ExitPreflight, Step: "preflight", Err: err}
	}
	ctx, cancel := context.WithTimeout(ctx, s.cfg.InstallTimeout)
	defer cancel()
	s.say("smoke")
	if err := s.smoke(ctx); err != nil {
		return &ExitError{Code: ExitFailed, Step: "smoke", Err: err}
	}
	s.say("smoke passed")
	return nil
}
