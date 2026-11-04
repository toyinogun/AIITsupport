// Package evalreset holds the one place outside cmd/seed that binds to Active
// Directory as the domain Administrator: the eval harness's seed and undo of
// the throwaway Samba container it started itself (spec 0018, AC-19). Only
// cmd/eval may import it, and only ever with the address of that container.
package evalreset

import (
	"context"
	"crypto/x509"
	"fmt"
	"time"

	noordldap "github.com/toyinogun/AIITsupport/internal/ldap"
	"github.com/toyinogun/AIITsupport/internal/seed/directory"
)

const bindWait = time.Minute

// Directory is the throwaway Samba container, as the harness started it.
type Directory struct {
	// URL is the container's ldaps:// address.
	URL     string
	RootCAs *x509.CertPool
	// AdminDN and AdminPassword are the container's own, generated per run.
	AdminDN, AdminPassword string
	// UserPassword is set on every seeded account.
	UserPassword string
}

func (d Directory) dialer() noordldap.Dialer {
	return noordldap.Dialer{URL: d.URL, RootCAs: d.RootCAs}
}

// Seed fills the fresh directory from the model, as `seed directory` does.
func (d Directory) Seed(ctx context.Context, m directory.Model) error {
	admin, err := d.dialer().WaitAndBind(ctx, d.AdminDN, d.AdminPassword, bindWait)
	if err != nil {
		return fmt.Errorf("bind to the throwaway directory: %w", err)
	}
	defer admin.Close()
	if _, err := directory.Apply(ctx, admin, m, directory.Options{UserPassword: d.UserPassword, Dialer: d.dialer()}); err != nil {
		return fmt.Errorf("seed the throwaway directory: %w", err)
	}
	return nil
}

// Lock locks the user's account the only way AD allows, by failed binds, and
// confirms the lock took (the ticket setup `lock`, spec 0017).
func (d Directory) Lock(ctx context.Context, m directory.Model, username string) error {
	admin, err := d.dialer().WaitAndBind(ctx, d.AdminDN, d.AdminPassword, bindWait)
	if err != nil {
		return fmt.Errorf("bind to the throwaway directory: %w", err)
	}
	defer admin.Close()
	base, err := noordldap.BaseDN(admin)
	if err != nil {
		return err
	}
	dn, ok := m.UserDN(base, username)
	if !ok {
		return fmt.Errorf("lock %s: not in the directory", username)
	}
	threshold, err := directory.LockoutThreshold(admin, base)
	if err != nil {
		return err
	}
	return directory.LockAndConfirm(ctx, admin, d.dialer(), dn, "Wrong-"+d.UserPassword, threshold)
}
