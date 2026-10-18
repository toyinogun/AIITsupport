package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/go-ldap/ldap/v3"

	"github.com/toyinogun/AIITsupport/internal/config"
	noordldap "github.com/toyinogun/AIITsupport/internal/ldap"
	"github.com/toyinogun/AIITsupport/internal/logging"
	"github.com/toyinogun/AIITsupport/internal/seed/certs"
	"github.com/toyinogun/AIITsupport/internal/seed/directory"
	"github.com/toyinogun/AIITsupport/internal/seed/entra"
	"github.com/toyinogun/AIITsupport/internal/seed/helpdesk"
)

const defaultCertDir = ".dev/certs"

// hostList collects repeated --host flags.
type hostList []string

func (h *hostList) String() string     { return strings.Join(*h, ",") }
func (h *hostList) Set(v string) error { *h = append(*h, v); return nil }

func runCerts(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("certs", flag.ContinueOnError)
	dir := fs.String("dir", defaultCertDir, "directory for ca.pem, ca.key, server.pem, server.key")
	var hosts hostList
	fs.Var(&hosts, "host", "an extra name or IP for the server cert (repeatable)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	res, err := certs.Generate(*dir, hosts)
	if err != nil {
		return err
	}
	if len(res.Written) > 0 {
		fmt.Fprintf(out, "wrote %s in %s\n", strings.Join(res.Written, ", "), *dir)
	}
	if len(res.Skipped) > 0 {
		fmt.Fprintf(out, "kept existing %s in %s\n", strings.Join(res.Skipped, ", "), *dir)
	}
	return nil
}

func runValidate(args []string, out io.Writer) error {
	if len(args) > 0 {
		return errUsage
	}
	files, err := config.LoadSeedFiles(os.LookupEnv)
	if err != nil {
		return err
	}
	m, err := directory.Load(files.File, files.OverlayFile)
	if err != nil {
		return err
	}
	demo, err := entra.LoadDemo(files.EntraDemoFile, m)
	if err != nil {
		return err
	}
	content, err := helpdesk.Load(files.HelpdeskDir, m)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "%s is valid: %d users, %d admins, %d groups, %d Entra demo users\n",
		files.File, len(m.Users), len(m.Admins), len(m.Groups), len(demo.Users))
	fmt.Fprintf(out, "%s is valid: %d KB categories, %d KB articles, %d tickets\n",
		files.HelpdeskDir, len(content.Categories), len(content.Articles), len(content.Tickets))
	return nil
}

func runDirectory(ctx context.Context, args []string, out io.Writer) error {
	if len(args) > 0 {
		return errUsage
	}
	start := time.Now()
	logger := logging.New(out, "info", service)

	s, err := loadSession()
	if err != nil {
		return err
	}
	// The Samba container creates svc-zammad from this value (spec 0006);
	// without it the directory would be missing Zammad's bind account.
	if s.cfg.ZammadBindPassword == "" {
		return errors.New("seed config: SEED_ZAMMAD_BIND_PASSWORD is required")
	}
	admin, err := s.dialer.WaitAndBind(ctx, s.cfg.AdminUser, s.cfg.AdminPassword, s.cfg.WaitTimeout)
	if err != nil {
		return err
	}
	defer admin.Close()

	res, err := directory.Apply(ctx, admin, s.model, directory.Options{UserPassword: s.cfg.UserPassword, Dialer: s.dialer})
	if err != nil {
		return fmt.Errorf("seed directory: %w", err)
	}
	if res.AlreadySeeded {
		logger.Info("already seeded", "duration_ms", time.Since(start).Milliseconds())
		return nil
	}
	logger.Info("directory seeded",
		"departments", res.Departments, "users", res.Users, "admins", res.Admins,
		"groups", res.Groups, "locked", res.Locked, "duration_ms", time.Since(start).Milliseconds())
	return nil
}

func runLogin(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("login", flag.ContinueOnError)
	password := fs.String("password", "", "the password to try (default SEED_USER_PASSWORD)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	username, err := oneUsername(fs.Args())
	if err != nil {
		return err
	}
	s, err := loadSession()
	if err != nil {
		return err
	}
	if *password == "" {
		*password = s.cfg.UserPassword
	}
	userDN, err := s.userDN(username)
	if err != nil {
		return err
	}

	bindErr := bindOnce(s.dialer, userDN, *password)
	state, err := s.state(userDN)
	if err != nil {
		return err
	}
	switch {
	case bindErr == nil:
		fmt.Fprintf(out, "bound as %s\nlocked: %t, bad password count: %d\n", userDN, state.Locked, state.BadPwdCount)
		return nil
	case state.Locked:
		return fmt.Errorf("login as %s failed: the account is locked", username)
	default:
		return fmt.Errorf("login as %s failed (locked: false, bad password count: %d): %w", username, state.BadPwdCount, bindErr)
	}
}

func runLock(ctx context.Context, args []string, out io.Writer) error {
	username, err := oneUsername(args)
	if err != nil {
		return err
	}
	s, err := loadSession()
	if err != nil {
		return err
	}
	userDN, err := s.userDN(username)
	if err != nil {
		return err
	}
	admin, err := s.dialer.Bind(s.cfg.AdminUser, s.cfg.AdminPassword)
	if err != nil {
		return err
	}
	defer admin.Close()

	base, err := noordldap.BaseDN(admin)
	if err != nil {
		return err
	}
	threshold, err := directory.LockoutThreshold(admin, base)
	if err != nil {
		return err
	}
	if err := directory.LockAndConfirm(ctx, admin, s.dialer, userDN, directory.WrongPassword(s.cfg.UserPassword), threshold); err != nil {
		return err
	}
	fmt.Fprintf(out, "locked %s\n", username)
	return nil
}

func oneUsername(args []string) (string, error) {
	if len(args) != 1 || args[0] == "" {
		return "", fmt.Errorf("give exactly one username\n%w", errUsage)
	}
	return args[0], nil
}

// session is what every command that talks to the directory needs.
type session struct {
	cfg    config.Seed
	model  directory.Model
	dialer noordldap.Dialer
}

func loadSession() (session, error) {
	cfg, err := config.LoadSeed(os.LookupEnv)
	if err != nil {
		return session{}, err
	}
	m, err := directory.Load(cfg.File, cfg.OverlayFile)
	if err != nil {
		return session{}, err
	}
	pool, err := noordldap.LoadCA(cfg.CACert)
	if err != nil {
		return session{}, err
	}
	return session{cfg: cfg, model: m, dialer: noordldap.Dialer{URL: cfg.LDAPURL, RootCAs: pool}}, nil
}

// userDN derives the bind DN from the model; users bind by DN, never by UPN.
func (s session) userDN(username string) (string, error) {
	admin, err := s.dialer.Bind(s.cfg.AdminUser, s.cfg.AdminPassword)
	if err != nil {
		return "", err
	}
	defer admin.Close()
	base, err := noordldap.BaseDN(admin)
	if err != nil {
		return "", err
	}
	dn, ok := s.model.UserDN(base, username)
	if !ok {
		return "", fmt.Errorf("unknown user %q (not in %s)", username, s.cfg.File)
	}
	return dn, nil
}

// state reads the lock state as the Administrator, never from bind errors.
func (s session) state(userDN string) (noordldap.LockState, error) {
	admin, err := s.dialer.Bind(s.cfg.AdminUser, s.cfg.AdminPassword)
	if err != nil {
		return noordldap.LockState{}, err
	}
	defer admin.Close()
	return noordldap.State(admin, userDN)
}

func bindOnce(d noordldap.Dialer, dn, password string) error {
	conn, err := d.Dial()
	if err != nil {
		return err
	}
	defer conn.Close()
	if err := conn.Bind(dn, password); err != nil {
		if ldap.IsErrorWithCode(err, ldap.LDAPResultInvalidCredentials) {
			return errors.New("invalid credentials")
		}
		return fmt.Errorf("bind: %w", err)
	}
	return nil
}
