package env

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/toyinogun/AIITsupport/internal/graph"
	"github.com/toyinogun/AIITsupport/internal/graph/graphtest"
	"github.com/toyinogun/AIITsupport/internal/seed/directory"
)

// Fake Entra credentials. They are made up, mean nothing outside this run, and
// only the fake's token endpoint accepts them (spec 0018, AC-20).
const (
	graphTenantID     = "eval-tenant"
	graphClientID     = "00000000-0000-0000-0000-00000000e7a1"
	graphClientSecret = "eval-harness-fake-secret" //nolint:gosec // G101: a made up value for the fake Entra, valid nowhere else
)

// Entra is the run's fake Microsoft Entra: Graph and its token endpoint, over
// TLS, with one managed user per synced directory user.
type Entra struct {
	Fake *graphtest.Fake
	// CAFile is the PEM the tool server child trusts through SSL_CERT_FILE.
	CAFile string
	// UserIDs maps a username to its fake object id.
	UserIDs map[string]string

	dir string
}

// StartEntra starts the fake and registers the directory's synced users.
func StartEntra(m directory.Model) (*Entra, error) {
	dir, err := os.MkdirTemp("", "noord-eval-entra-")
	if err != nil {
		return nil, fmt.Errorf("make the fake Entra directory: %w", err)
	}
	f := graphtest.StartTLS(graphClientID, graphClientSecret)
	e := &Entra{Fake: f, dir: dir, UserIDs: map[string]string{}, CAFile: filepath.Join(dir, "graph-ca.pem")}
	if err := os.WriteFile(e.CAFile, f.CAPEM(), 0o600); err != nil {
		f.Close()
		return nil, errors.Join(fmt.Errorf("write the fake Entra CA: %w", err), os.RemoveAll(dir))
	}
	for _, u := range m.Users {
		if !u.Synced || u.Left != nil {
			continue
		}
		mail := u.Mail
		e.UserIDs[u.Username] = f.AddUser(map[string]any{
			"mailNickname": u.Username, "userPrincipalName": mail, "displayName": u.DisplayName(),
			"givenName": u.GivenName, "surname": u.Surname, "accountEnabled": true,
			"employeeType": graph.ManagedMarker,
		})
	}
	return e, nil
}

// BaseURL is the Graph address the tool server child is pointed at.
func (e *Entra) BaseURL() string { return e.Fake.URL() }

// AuthorityHost is the sign in address the tool server child is pointed at.
func (e *Entra) AuthorityHost() string { return e.Fake.URL() }

// Close stops the fake and removes its files.
func (e *Entra) Close() error {
	e.Fake.Close()
	return os.RemoveAll(e.dir)
}
