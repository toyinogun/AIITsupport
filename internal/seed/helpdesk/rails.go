package helpdesk

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Rails steps, the scripts in seed/helpdesk/rails/.
const (
	StepBootstrap = "bootstrap"
	StepBackdate  = "backdate"
	StepRetention = "retention"
)

const (
	resultPrefix = "NOORD_RESULT "
	failedPrefix = "NOORD_FAILED "
	// railsDir is where the override mounts seed/helpdesk/rails/.
	railsDir = "/opt/noord-seed/rails"
	// zammadAppDir is the Rails root inside every Zammad image.
	zammadAppDir = "/opt/zammad"
	// killGrace is how long `timeout` waits after TERM before KILL.
	killGrace = 5 * time.Second
	// dockerGrace is extra time for docker itself around the step limit.
	dockerGrace = 30 * time.Second
	// maxOutput caps the step output kept for an error message.
	maxOutput = 4000
)

var (
	stepName = regexp.MustCompile(`^[a-z]+$`)
	envName  = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)
)

// Runner runs one Rails step inside Zammad. env holds the step's secrets
// and settings; they travel only as environment variables. ComposeRunner
// serves the local stack, LocalRunner the cluster's helpdesk Job.
type Runner interface {
	Run(ctx context.Context, step string, env map[string]string, stdin []byte) (Output, error)
}

// Output is what a step printed.
type Output struct {
	Stdout, Stderr []byte
}

// ErrStepTimeout means a step ran past SEED_ZAMMAD_WAIT_TIMEOUT and was
// stopped inside the container.
var ErrStepTimeout = errors.New("stopped after SEED_ZAMMAD_WAIT_TIMEOUT")

// ComposeRunner runs a step with `docker compose exec` in the local stack:
//
//	docker compose --profile helpdesk exec -T -e NAME... zammad-railsserver
//	  timeout -k 5 <seconds> bundle exec rails r /opt/noord-seed/rails/<step>.rb
//
// The argument list is fixed; `-e NAME` without a value makes docker copy
// the value from this process's environment, so no secret is ever an
// argument.
type ComposeRunner struct {
	// Dir is the Compose project directory (the repo root).
	Dir string
	// Limit stops the step inside the container (SEED_ZAMMAD_WAIT_TIMEOUT).
	Limit time.Duration
	// Service defaults to zammad-railsserver.
	Service string
}

// Run runs one step.
func (r ComposeRunner) Run(ctx context.Context, step string, env map[string]string, stdin []byte) (Output, error) {
	names, err := checkStep(step, env, r.Limit)
	if err != nil {
		return Output{}, err
	}
	service := r.Service
	if service == "" {
		service = "zammad-railsserver"
	}

	args := []string{"compose", "--profile", "helpdesk", "exec", "-T"}
	cmdEnv := os.Environ()
	for _, name := range names {
		args = append(args, "-e", name)
		cmdEnv = append(cmdEnv, name+"="+env[name])
	}
	args = append(args, service)
	args = append(args, railsCommand(r.Limit, railsDir+"/"+step+".rb")...)

	ctx, cancel := context.WithTimeout(ctx, r.Limit+killGrace+dockerGrace)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Dir = r.Dir
	cmd.Env = cmdEnv
	return runCommand(ctx, cmd, stdin, "docker compose exec")
}

// LocalRunner runs a step in process, inside a Zammad image (the cluster's
// helpdesk Job, spec 0015 AC-11), with the same command, limit, and
// environment handling as ComposeRunner:
//
//	timeout -k 5 <seconds> bundle exec rails r <RailsDir>/<step>.rb
//
// in Zammad's Rails root. The step's env joins this process's environment,
// which already holds the chart's database, Redis, and Memcached settings;
// no secret is ever an argument.
type LocalRunner struct {
	// RailsDir holds the step scripts (seed export's copy of
	// seed/helpdesk/rails/).
	RailsDir string
	// Limit stops the step (SEED_ZAMMAD_WAIT_TIMEOUT).
	Limit time.Duration
	// AppDir defaults to /opt/zammad.
	AppDir string
}

// Run runs one step.
func (r LocalRunner) Run(ctx context.Context, step string, env map[string]string, stdin []byte) (Output, error) {
	names, err := checkStep(step, env, r.Limit)
	if err != nil {
		return Output{}, err
	}
	if r.RailsDir == "" {
		return Output{}, errors.New("rails runner: the rails directory is required")
	}
	appDir := r.AppDir
	if appDir == "" {
		appDir = zammadAppDir
	}
	cmdEnv := railsEnv(os.Environ())
	for _, name := range names {
		cmdEnv = append(cmdEnv, name+"="+env[name])
	}
	args := railsCommand(r.Limit, filepath.Join(r.RailsDir, step+".rb"))

	ctx, cancel := context.WithTimeout(ctx, r.Limit+killGrace+dockerGrace)
	defer cancel()
	cmd := exec.CommandContext(ctx, args[0], args[1:]...) //nolint:gosec // G204: a fixed command; the step name is checked
	cmd.Dir = appDir
	cmd.Env = cmdEnv
	return runCommand(ctx, cmd, stdin, "rails runner")
}

// seedOnly are the Job's own settings and secrets, which Rails never needs:
// a step gets what it reads through its env, as with docker compose exec -e.
var seedOnly = []string{"SEED_", "ZAMMAD_TOKEN", "ZAMMAD_POSTGRES_PASSWORD", "ZAMMAD_REDIS_PASSWORD"}

// railsEnv drops the seedOnly variables from environ, so Rails and its gems
// see Zammad's own environment only.
func railsEnv(environ []string) []string {
	out := make([]string, 0, len(environ))
	for _, kv := range environ {
		if !slices.ContainsFunc(seedOnly, func(p string) bool { return strings.HasPrefix(kv, p) }) {
			out = append(out, kv)
		}
	}
	return out
}

// checkStep refuses a bad step name, environment name, or limit, and
// returns the environment names sorted.
func checkStep(step string, env map[string]string, limit time.Duration) ([]string, error) {
	if !stepName.MatchString(step) {
		return nil, fmt.Errorf("rails step %q: invalid name", step)
	}
	if limit <= 0 {
		return nil, errors.New("rails runner: the step limit must be positive")
	}
	names := make([]string, 0, len(env))
	for name := range env {
		if !envName.MatchString(name) {
			return nil, fmt.Errorf("rails step %s: invalid environment name %q", step, name)
		}
		names = append(names, name)
	}
	slices.Sort(names)
	return names, nil
}

// railsCommand stops the script inside the container after limit, then
// kills it killGrace later.
func railsCommand(limit time.Duration, script string) []string {
	return []string{
		"timeout", "-k", strconv.Itoa(int(killGrace.Seconds())), strconv.Itoa(int(limit.Seconds())),
		"bundle", "exec", "rails", "r", script,
	}
}

// runCommand runs cmd and maps a timeout (exit 124 or 137, or the context)
// to ErrStepTimeout.
func runCommand(ctx context.Context, cmd *exec.Cmd, stdin []byte, what string) (Output, error) {
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	out := Output{Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}
	if err == nil {
		return out, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && (exitErr.ExitCode() == 124 || exitErr.ExitCode() == 137) {
		return out, ErrStepTimeout
	}
	if ctx.Err() != nil {
		return out, ErrStepTimeout
	}
	return out, fmt.Errorf("%s: %w", what, err)
}

// StepError is a failed Rails step. Its text holds the step's output with
// every secret value removed.
type StepError struct {
	Step   string
	Output string
	Err    error
}

func (e *StepError) Error() string {
	msg := fmt.Sprintf("rails step %s failed: %v", e.Step, e.Err)
	if errors.Is(e.Err, ErrStepTimeout) {
		msg += "; run make helpdesk-reset (a stopped step leaves Zammad half seeded)"
	}
	if e.Output != "" {
		msg += "\n" + e.Output
	}
	return msg
}

func (e *StepError) Unwrap() error { return e.Err }

// runStep runs a step and returns its NOORD_RESULT JSON. Any failure comes
// back as a *StepError with the secrets redacted.
func runStep(ctx context.Context, r Runner, step string, env map[string]string, stdin []byte) (json.RawMessage, error) {
	out, err := r.Run(ctx, step, env, stdin)
	secrets := secretValues(env)
	if err != nil {
		return nil, &StepError{Step: step, Output: redact(failure(out), secrets), Err: err}
	}
	result, ok := findResult(out.Stdout)
	if !ok {
		return nil, &StepError{Step: step, Output: redact(failure(out), secrets), Err: errors.New("no NOORD_RESULT line")}
	}
	return result, nil
}

// findResult returns the JSON after the last NOORD_RESULT line. Rails log
// lines share stdout, so the marker prefix finds ours.
func findResult(stdout []byte) (json.RawMessage, bool) {
	var found json.RawMessage
	sc := bufio.NewScanner(bytes.NewReader(stdout))
	sc.Buffer(make([]byte, 0, 64*1024), 16<<20)
	for sc.Scan() {
		if rest, ok := strings.CutPrefix(sc.Text(), resultPrefix); ok {
			found = json.RawMessage(rest)
		}
	}
	return found, found != nil
}

// failure keeps the NOORD_FAILED line when there is one, else the tail of
// the output.
func failure(out Output) string {
	for _, line := range strings.Split(string(out.Stderr), "\n") {
		if strings.HasPrefix(line, failedPrefix) {
			return strings.TrimPrefix(line, failedPrefix)
		}
	}
	text := strings.TrimSpace(string(out.Stderr) + "\n" + string(out.Stdout))
	if len(text) > maxOutput {
		text = "..." + text[len(text)-maxOutput:]
	}
	return text
}

// minSecretLen keeps short values (a group name) from being redacted all
// over the output; every real secret is longer.
const minSecretLen = 8

// secretValues are the env values to remove from any output.
func secretValues(env map[string]string) []string {
	var out []string
	for name, v := range env {
		if len(v) >= minSecretLen && isSecretName(name) {
			out = append(out, v)
		}
	}
	return out
}

func isSecretName(name string) bool {
	for _, s := range []string{"PASSWORD", "TOKEN", "SECRET"} {
		if strings.Contains(name, s) {
			return true
		}
	}
	return false
}

func redact(text string, secrets []string) string {
	for _, s := range secrets {
		text = strings.ReplaceAll(text, s, "[redacted]")
	}
	return text
}
