// Package sambatest runs a fresh Noord Samba AD container for integration
// tests (spec 0004, AC-19). Tests that use it run only with TEST_SAMBA=1, the
// way DB tests run only with TEST_DATABASE_URL.
package sambatest

import (
	"context"
	"crypto/rand"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	noordldap "github.com/toyinogun/AIITsupport/internal/ldap"
	"github.com/toyinogun/AIITsupport/internal/seed/certs"
)

// Throwaway passwords, fresh for every test run, so none is ever written
// down. Each meets Samba's complexity rule (upper, lower, digits) and the hex
// part holds no staff name token.
var (
	AdminPassword   = newPassword()
	SvcPassword     = newPassword()
	ZammadPassword  = newPassword()
	PwresetPassword = newPassword()
	UserPassword    = newPassword()
)

func newPassword() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b) // crypto/rand.Read never fails
	return "Tst-" + hex.EncodeToString(b) + "-Q9"
}

// Fixed names inside every container our image provisions.
const (
	BaseDN    = "DC=ad,DC=noord,DC=example"
	AdminDN   = "CN=Administrator,CN=Users," + BaseDN
	SvcDN     = "CN=svc-itsupport,OU=Service Accounts,OU=Noord," + BaseDN
	ZammadDN  = "CN=svc-zammad,OU=Service Accounts,OU=Noord," + BaseDN
	PwresetDN = "CN=svc-pwreset,OU=Service Accounts,OU=Noord," + BaseDN
)

const startupTimeout = 4 * time.Minute

// The one image every package's live tests share, and the lock file that
// lets only one test process build it at a time.
const (
	imageRepo     = "noord-samba"
	imageTag      = "test"
	buildLockFile = "noord-samba-build.lock"
)

// Samba is a running, provisioned container that has not been seeded.
type Samba struct {
	URL       string
	CAPath    string
	RootCAs   *x509.CertPool
	Container *testcontainers.DockerContainer
	certDir   string
}

// Enabled reports whether TEST_SAMBA is set.
func Enabled() bool { return os.Getenv("TEST_SAMBA") != "" }

// New starts a container for one test and removes it when the test ends. It
// skips the test unless TEST_SAMBA is set, and fails it if Docker is missing.
func New(t testing.TB, buildContext string) *Samba {
	t.Helper()
	if !Enabled() {
		t.Skip("set TEST_SAMBA=1 to run Samba integration tests (needs Docker)")
	}
	s, err := Start(t.Context(), buildContext)
	if err != nil {
		t.Fatalf("start Samba: %v", err)
	}
	t.Cleanup(func() {
		if err := s.Terminate(); err != nil {
			t.Errorf("stop Samba: %v", err)
		}
	})
	return s
}

// Start builds the image from buildContext (seed/directory/samba) if needed,
// makes certs in a temp dir, and starts a fresh container. Call Terminate.
func Start(ctx context.Context, buildContext string) (*Samba, error) {
	certDir, err := os.MkdirTemp("", "noord-samba-certs-")
	if err != nil {
		return nil, fmt.Errorf("make cert dir: %w", err)
	}
	if _, err := certs.Generate(certDir, nil); err != nil {
		return nil, errors.Join(err, os.RemoveAll(certDir))
	}

	image, err := buildImage(ctx, buildContext)
	if err != nil {
		return nil, errors.Join(err, os.RemoveAll(certDir))
	}

	ctr, err := testcontainers.Run(ctx, image,
		testcontainers.WithEnv(map[string]string{
			"SAMBA_ADMIN_PASSWORD":   AdminPassword,
			"SAMBA_SVC_PASSWORD":     SvcPassword,
			"SAMBA_ZAMMAD_PASSWORD":  ZammadPassword,
			"SAMBA_PWRESET_PASSWORD": PwresetPassword,
		}),
		testcontainers.WithFiles(
			testcontainers.ContainerFile{HostFilePath: filepath.Join(certDir, certs.CAFile), ContainerFilePath: "/certs/" + certs.CAFile, FileMode: 0o644},
			testcontainers.ContainerFile{HostFilePath: filepath.Join(certDir, certs.ServerFile), ContainerFilePath: "/certs/" + certs.ServerFile, FileMode: 0o644},
			testcontainers.ContainerFile{HostFilePath: filepath.Join(certDir, certs.ServerKeyFile), ContainerFilePath: "/certs/" + certs.ServerKeyFile, FileMode: 0o600},
		),
		testcontainers.WithExposedPorts("636/tcp"),
		// The image only serves as host "samba", the name it provisions with.
		testcontainers.WithConfigModifier(func(c *container.Config) { c.Hostname = "samba" }),
		// Hold the container to the Compose setup: no added capabilities (AC-17).
		testcontainers.WithHostConfigModifier(func(hc *container.HostConfig) { hc.CapAdd = nil }),
		testcontainers.WithWaitStrategyAndDeadline(startupTimeout,
			wait.ForExec([]string{"sh", "-c", "LDAPTLS_CACERT=/certs/ca.pem ldapsearch -H ldaps://localhost:636 -x -b '' -s base"}).
				WithStartupTimeout(startupTimeout).
				WithPollInterval(time.Second),
		),
	)
	s := &Samba{Container: ctr, certDir: certDir, CAPath: filepath.Join(certDir, certs.CAFile)}
	if err != nil {
		return nil, errors.Join(fmt.Errorf("run Samba container: %w", err), s.Terminate())
	}

	endpoint, err := ctr.PortEndpoint(ctx, "636/tcp", "ldaps")
	if err != nil {
		return nil, errors.Join(fmt.Errorf("find the LDAPS port: %w", err), s.Terminate())
	}
	s.URL = endpoint
	s.RootCAs, err = noordldap.LoadCA(s.CAPath)
	if err != nil {
		return nil, errors.Join(err, s.Terminate())
	}
	return s, nil
}

// buildImage builds the image from buildContext (cached layers make a repeat
// fast) and returns its tag. go test runs packages as parallel processes, and
// two builds of one tag race in the image store ("already exists"), so the
// build holds an exclusive lock on a file in the temp dir. Containers then
// start from the built tag in parallel.
func buildImage(ctx context.Context, buildContext string) (string, error) {
	lock, err := os.OpenFile(filepath.Join(os.TempDir(), buildLockFile), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return "", fmt.Errorf("open the image build lock: %w", err)
	}
	defer lock.Close() // closing the file releases the lock
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return "", fmt.Errorf("take the image build lock: %w", err)
	}

	provider, err := testcontainers.NewDockerProvider()
	if err != nil {
		return "", fmt.Errorf("reach Docker: %w", err)
	}
	defer provider.Close()
	image, err := provider.BuildImage(ctx, &testcontainers.ContainerRequest{
		FromDockerfile: testcontainers.FromDockerfile{
			Context:   buildContext,
			Repo:      imageRepo,
			Tag:       imageTag,
			KeepImage: true,
		},
	})
	if err != nil {
		return "", fmt.Errorf("build the Samba image: %w", err)
	}
	return image, nil
}

// Terminate removes the container and its certs.
func (s *Samba) Terminate() error {
	var errs []error
	if s.Container != nil {
		errs = append(errs, testcontainers.TerminateContainer(s.Container))
	}
	if s.certDir != "" {
		errs = append(errs, os.RemoveAll(s.certDir))
	}
	return errors.Join(errs...)
}
