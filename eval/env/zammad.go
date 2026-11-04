package env

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/toyinogun/AIITsupport/internal/seed/directory/sambatest"
	"github.com/toyinogun/AIITsupport/internal/seed/helpdesk"
	"github.com/toyinogun/AIITsupport/internal/zammad"
)

// zammadServices are the Compose services the helpdesk needs, as the Makefile's
// ZAMMAD_SERVICES lists them.
var zammadServices = []string{
	"zammad-init", "zammad-elasticsearch", "zammad-memcached", "zammad-postgresql",
	"zammad-redis", "zammad-railsserver", "zammad-scheduler", "zammad-websocket", "zammad-nginx",
}

const (
	zammadWait   = 15 * time.Minute
	healthPoll   = 5 * time.Second
	adminEmail   = "eval.admin@noord.example"
	zammadGroup  = "IT Support"
	retentionDay = 30
)

// Zammad is the run's throwaway helpdesk, in its own Compose project and on a
// port Docker picks, so it never meets a developer's `make dev` stack.
type Zammad struct {
	// URL is the browser and API address, on loopback.
	URL string
	// Token is the noord-assistant token the tool server uses.
	Token string
	// AdminEmail and AdminPassword are the seed admin, for the harness's own
	// reads and its ticket deletes (spec 0018, follow up).
	AdminEmail, AdminPassword string
	Group                     string

	project string
	dir     string
}

// StartZammad starts the Zammad stack, joins samba to its network under the
// alias the LDAP source uses, and seeds it.
func StartZammad(ctx context.Context, repoRoot string, s *Samba) (*Zammad, error) {
	repoRoot, err := filepath.Abs(repoRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve the repository root: %w", err)
	}
	suffix, err := randomHex(4)
	if err != nil {
		return nil, err
	}
	token, err := randomHex(24)
	if err != nil {
		return nil, err
	}
	adminPass, err := randomHex(16)
	if err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("", "noord-eval-zammad-")
	if err != nil {
		return nil, fmt.Errorf("make the Compose directory: %w", err)
	}
	z := &Zammad{
		Token: token, AdminEmail: adminEmail, AdminPassword: adminPass, Group: zammadGroup,
		project: "noord-eval-" + suffix, dir: dir,
	}
	if err := z.up(ctx, repoRoot, s); err != nil {
		return nil, errors.Join(err, z.Close())
	}
	if err := z.seed(ctx, repoRoot, s); err != nil {
		return nil, errors.Join(err, z.Close())
	}
	return z, nil
}

func (z *Zammad) up(ctx context.Context, repoRoot string, s *Samba) error {
	zdir := filepath.Join(repoRoot, "seed", "helpdesk", "zammad")
	files := map[string]string{
		"compose.yaml": fmt.Sprintf(`name: %s
include:
  - path:
      - %s
      - %s
    env_file: %s
`, z.project, filepath.Join(zdir, "compose.yaml"), filepath.Join(z.dir, "override.yaml"), filepath.Join(zdir, "zammad.env")),
		"override.yaml": fmt.Sprintf(`services:
  zammad-backup:
    profiles: [disabled]
  zammad-elasticsearch:
    profiles: [helpdesk]
    # A throwaway index on a nearly full Docker disk would never allocate its
    # shards (cluster red, zammad-init stuck); the data is deleted at the end.
    environment:
      cluster.routing.allocation.disk.threshold_enabled: "false"
  zammad-init:
    profiles: [helpdesk]
  zammad-memcached:
    profiles: [helpdesk]
  zammad-nginx:
    profiles: [helpdesk]
    ports: !override
      - "127.0.0.1::8080"
  zammad-postgresql:
    profiles: [helpdesk]
  zammad-railsserver:
    profiles: [helpdesk]
    volumes:
      - %[1]s:/opt/noord-seed/rails:ro
      - %[2]s:/opt/noord-seed/ca.pem:ro
  zammad-redis:
    profiles: [helpdesk]
  zammad-scheduler:
    profiles: [helpdesk]
    volumes:
      - %[1]s:/opt/noord-seed/rails:ro
      - %[2]s:/opt/noord-seed/ca.pem:ro
  zammad-websocket:
    profiles: [helpdesk]
`, filepath.Join(repoRoot, "seed", "helpdesk", "rails"), s.CAPath()),
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(z.dir, name), []byte(body), 0o600); err != nil {
			return fmt.Errorf("write the Compose file %s: %w", name, err)
		}
	}

	upCtx, cancel := context.WithTimeout(ctx, zammadWait)
	defer cancel()
	if _, err := z.compose(upCtx, append([]string{"up", "-d", "--wait"}, zammadServices...)...); err != nil {
		return fmt.Errorf("start Zammad: %w", err)
	}
	// zammad-init configures Elasticsearch last; seeding before it ends leaves
	// LDAP users out of the search index (journal 0028).
	if _, err := z.compose(upCtx, "wait", "zammad-init"); err != nil {
		return fmt.Errorf("wait for zammad-init: %w", err)
	}
	port, err := z.compose(ctx, "port", "zammad-nginx", "8080")
	if err != nil {
		return fmt.Errorf("find the Zammad port: %w", err)
	}
	_, hostPort, ok := strings.Cut(strings.TrimSpace(port), ":")
	if !ok {
		return errors.New("find the Zammad port: unexpected docker compose output")
	}
	z.URL = "http://127.0.0.1:" + hostPort

	// Samba joins Zammad's network as "samba", the host the LDAP source dials
	// on port 636 and the name the server certificate carries.
	ctr := s.Container.Container.GetContainerID()
	netw := z.project + "_default"
	if out, err := exec.CommandContext(ctx, "docker", "network", "connect", "--alias", "samba", netw, ctr).CombinedOutput(); err != nil { //nolint:gosec // fixed command, arguments are generated names
		return fmt.Errorf("join Samba to the Zammad network: %w: %s", err, bytes.TrimSpace(out))
	}
	return waitHealthy(ctx, z.URL)
}

func (z *Zammad) seed(ctx context.Context, repoRoot string, s *Samba) error {
	helpdeskDir := filepath.Join(repoRoot, "seed", "helpdesk")
	content, err := helpdesk.Load(helpdeskDir, s.Model)
	if err != nil {
		return fmt.Errorf("load the helpdesk seed: %w", err)
	}
	// The CA lives in a temp directory outside the repository, so the marker
	// is rooted at "/" (it only has to be stable within this run).
	marker, err := helpdesk.Marker("/", helpdeskDir, filepath.Join(repoRoot, "seed", "directory", "noord.yaml"), s.CAPath())
	if err != nil {
		return err
	}
	admin, err := zammad.New(zammad.Options{BaseURL: z.URL, Timeout: 30 * time.Second, Auth: zammad.BasicAuth{Login: z.AdminEmail, Password: z.AdminPassword}})
	if err != nil {
		return err
	}
	assistant, err := zammad.New(zammad.Options{BaseURL: z.URL, Timeout: 30 * time.Second, Auth: zammad.TokenAuth(z.Token)})
	if err != nil {
		return err
	}
	seeder := helpdesk.Seeder{
		Runner: helpdesk.ComposeRunner{Dir: z.dir, Limit: zammadWait},
		Admin:  admin, Assistant: assistant, Content: content, Model: s.Model, Marker: marker,
		Group: z.Group, RetentionDays: retentionDay, LDAPHost: "samba", PublicURL: z.URL,
		Secrets: helpdesk.Secrets{
			AdminEmail: z.AdminEmail, AdminPassword: z.AdminPassword,
			BindPassword: sambatest.ZammadPassword, AssistantToken: z.Token,
		},
	}
	if _, err := seeder.Run(ctx); err != nil {
		return fmt.Errorf("seed the helpdesk: %w", err)
	}
	return nil
}

// compose runs docker compose in the generated project directory.
func (z *Zammad) compose(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "docker", append([]string{"compose", "--profile", "helpdesk"}, args...)...) //nolint:gosec // fixed command; the arguments are service names and generated paths
	cmd.Dir = z.dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return stdout.String(), fmt.Errorf("docker compose %s: %w: %s", args[0], err, tail(stderr.String()))
	}
	return stdout.String(), nil
}

// Close removes the stack and its volumes.
func (z *Zammad) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	var errs []error
	if z.project != "" {
		_, err := z.compose(ctx, "down", "-v", "--remove-orphans")
		errs = append(errs, err)
	}
	errs = append(errs, os.RemoveAll(z.dir))
	return errors.Join(errs...)
}

func waitHealthy(ctx context.Context, url string) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	client := &http.Client{Timeout: healthPoll}
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url+"/", nil)
		if err != nil {
			return fmt.Errorf("build the health request: %w", err)
		}
		resp, err := client.Do(req)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return errors.New("Zammad was not healthy within 5 minutes")
		case <-time.After(healthPoll):
		}
	}
}

// tail keeps the last part of a command's stderr for an error message.
func tail(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 400 {
		return s[len(s)-400:]
	}
	return s
}
