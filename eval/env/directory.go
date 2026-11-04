package env

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/toyinogun/AIITsupport/internal/seed/directory"
	"github.com/toyinogun/AIITsupport/internal/seed/directory/sambatest"
	"github.com/toyinogun/AIITsupport/internal/seed/evalreset"
)

// Samba is the run's throwaway Active Directory, seeded from
// seed/directory/noord.yaml.
type Samba struct {
	Container *sambatest.Samba
	Model     directory.Model
	Dir       evalreset.Directory
	// BindDN and BindPassword are the tool server's account, svc-itsupport.
	BindDN, BindPassword string
	BaseDN               string
}

// StartSamba builds the image if needed, starts the container, and seeds it.
// repoRoot is the repository root (it holds seed/directory/).
func StartSamba(ctx context.Context, repoRoot string) (*Samba, error) {
	model, err := directory.Load(filepath.Join(repoRoot, "seed", "directory", "noord.yaml"), "")
	if err != nil {
		return nil, fmt.Errorf("load the Noord directory: %w", err)
	}
	ctr, err := sambatest.Start(ctx, filepath.Join(repoRoot, "seed", "directory", "samba"))
	if err != nil {
		return nil, fmt.Errorf("start Samba: %w", err)
	}
	s := &Samba{
		Container: ctr, Model: model,
		BindDN: sambatest.SvcDN, BindPassword: sambatest.SvcPassword, BaseDN: sambatest.BaseDN,
		Dir: evalreset.Directory{
			URL: ctr.URL, RootCAs: ctr.RootCAs,
			AdminDN: sambatest.AdminDN, AdminPassword: sambatest.AdminPassword,
			UserPassword: sambatest.UserPassword,
		},
	}
	if err := s.Dir.Seed(ctx, model); err != nil {
		_ = ctr.Terminate()
		return nil, err
	}
	return s, nil
}

// CAPath is the PEM file that signed the container's certificate.
func (s *Samba) CAPath() string { return s.Container.CAPath }

// Close removes the container.
func (s *Samba) Close() error { return s.Container.Terminate() }
