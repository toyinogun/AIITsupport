package env

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
)

// ITApproverSlackID is the fixed IT approver the harness plays (spec 0018,
// Value sourcing); it is the tool server child's SLACK_IT_APPROVERS.
const ITApproverSlackID = "UEVIT00001"

const (
	childStartTimeout = 60 * time.Second
	childStopTimeout  = 10 * time.Second
)

// BuildToolServer compiles cmd/toolserver into dir, so a stale bin/ file is
// never used (AC-2). It returns the binary's path.
func BuildToolServer(ctx context.Context, repoRoot, dir string) (string, error) {
	bin := filepath.Join(dir, "toolserver")
	cmd := exec.CommandContext(ctx, "go", "build", "-o", bin, "./cmd/toolserver") //nolint:gosec // fixed command and arguments
	cmd.Dir = repoRoot
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("build the tool server: %w: %s", err, tail(string(out)))
	}
	return bin, nil
}

// ChildConfig is everything one tool server child needs. Every value is made
// by the harness; none comes from the harness's own environment.
type ChildConfig struct {
	Bin, Token, DatabaseURL                                       string
	LDAPURL, LDAPBaseDN, LDAPBindDN, LDAPBindPassword, LDAPCAFile string
	ResetBindDN, ResetBindPassword                                string
	ZammadURL, ZammadToken, ZammadGroup                           string
	GraphBaseURL, GraphAuthorityHost, GraphCAFile                 string
	KBStaleAsOf                                                   string
	LogFile                                                       string
}

// Child is a running tool server process.
type Child struct {
	// URL is the tool server's address, for the MCP client and approval routes.
	URL string
	// ResetURL is the reset page's address.
	ResetURL string

	cmd  *exec.Cmd
	done chan struct{}
	kill string
}

// StartToolServer starts the production binary with an environment built from
// an empty list (AC-2) and waits until it answers /healthz.
func StartToolServer(ctx context.Context, cfg ChildConfig) (*Child, error) {
	addr, err := freeAddr(ctx)
	if err != nil {
		return nil, err
	}
	resetAddr, err := freeAddr(ctx)
	if err != nil {
		return nil, err
	}
	killFile, err := os.CreateTemp("", "noord-eval-killswitch-")
	if err != nil {
		return nil, fmt.Errorf("make the kill switch file: %w", err)
	}
	// Without this exact content READ_ONLY defaults to true and every write is
	// refused.
	if _, err := killFile.WriteString("read_write"); err != nil {
		return nil, errors.Join(fmt.Errorf("write the kill switch file: %w", err), killFile.Close(), os.Remove(killFile.Name()))
	}
	if err := killFile.Close(); err != nil {
		return nil, errors.Join(fmt.Errorf("close the kill switch file: %w", err), os.Remove(killFile.Name()))
	}

	logs, err := os.OpenFile(cfg.LogFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("open the tool server log: %w", err), os.Remove(killFile.Name()))
	}
	defer func() { _ = logs.Close() }() // the child holds its own descriptor

	// Stop ends the child, so the attempt context must not.
	cmd := exec.CommandContext(context.WithoutCancel(ctx), cfg.Bin) //nolint:gosec // the binary this run built
	cmd.Env = childEnv(cfg, addr, resetAddr, killFile.Name())
	cmd.Stdout, cmd.Stderr = logs, logs
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return nil, errors.Join(fmt.Errorf("start the tool server: %w", err), os.Remove(killFile.Name()))
	}
	c := &Child{URL: "http://" + addr, ResetURL: "http://" + resetAddr, cmd: cmd, done: make(chan struct{}), kill: killFile.Name()}
	go func() { _ = cmd.Wait(); close(c.done) }()

	if err := c.waitHealthy(ctx); err != nil {
		return nil, errors.Join(err, c.Stop())
	}
	return c, nil
}

// childEnv is the child's whole environment. The list starts empty, so a
// LDAP_URL in the harness's own environment can never reach the child.
func childEnv(cfg ChildConfig, addr, resetAddr, killFile string) []string {
	_, resetPort, _ := net.SplitHostPort(resetAddr)
	vars := map[string]string{
		"TOOLSERVER_TOKEN": cfg.Token, "TOOLSERVER_DATABASE_URL": cfg.DatabaseURL, "TOOLSERVER_HTTP_ADDR": addr,
		"LDAP_URL": cfg.LDAPURL, "LDAP_BASE_DN": cfg.LDAPBaseDN, "LDAP_BIND_DN": cfg.LDAPBindDN,
		"LDAP_BIND_PASSWORD": cfg.LDAPBindPassword, "LDAP_CA_CERT": cfg.LDAPCAFile,
		"ZAMMAD_URL": cfg.ZammadURL, "ZAMMAD_TOKEN": cfg.ZammadToken, "ZAMMAD_DEFAULT_GROUP": cfg.ZammadGroup,
		"GRAPH_TENANT_ID": graphTenantID, "GRAPH_CLIENT_ID": graphClientID, "GRAPH_CLIENT_SECRET": graphClientSecret,
		"GRAPH_BASE_URL": cfg.GraphBaseURL, "GRAPH_AUTHORITY_HOST": cfg.GraphAuthorityHost,
		"SSL_CERT_FILE":   cfg.GraphCAFile,
		"RESET_HTTP_ADDR": resetAddr, "RESET_PUBLIC_URL": "http://127.0.0.1:" + resetPort,
		"RESET_LDAP_BIND_DN": cfg.ResetBindDN, "RESET_LDAP_BIND_PASSWORD": cfg.ResetBindPassword,
		"KILL_SWITCH_FILE": killFile, "SLACK_IT_APPROVERS": ITApproverSlackID,
		"KB_STALE_AS_OF": cfg.KBStaleAsOf,
	}
	out := make([]string, 0, len(vars))
	for k, v := range vars {
		out = append(out, k+"="+v)
	}
	return out
}

func (c *Child) waitHealthy(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, childStartTimeout)
	defer cancel()
	client := &http.Client{Timeout: 2 * time.Second}
	for {
		select {
		case <-c.done:
			return errors.New("the tool server exited before it was healthy")
		default:
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.URL+"/healthz", nil)
		if err != nil {
			return fmt.Errorf("build the health request: %w", err)
		}
		if resp, err := client.Do(req); err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return errors.New("the tool server was not healthy in time")
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// Stop ends the child (SIGTERM, then SIGKILL) and removes its kill switch file.
func (c *Child) Stop() error {
	if c == nil || c.cmd == nil || c.cmd.Process == nil {
		return nil
	}
	select {
	case <-c.done:
	default:
		_ = syscall.Kill(-c.cmd.Process.Pid, syscall.SIGTERM)
		select {
		case <-c.done:
		case <-time.After(childStopTimeout):
			_ = syscall.Kill(-c.cmd.Process.Pid, syscall.SIGKILL)
			<-c.done
		}
	}
	return os.Remove(c.kill)
}

// freeAddr picks a loopback port from 127.0.0.1:0.
func freeAddr(ctx context.Context) (string, error) {
	l, err := (&net.ListenConfig{}).Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		return "", fmt.Errorf("pick a loopback port: %w", err)
	}
	addr := l.Addr().String()
	if err := l.Close(); err != nil {
		return "", fmt.Errorf("release the loopback port: %w", err)
	}
	return addr, nil
}
