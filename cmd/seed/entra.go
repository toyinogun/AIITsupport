package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/toyinogun/AIITsupport/internal/config"
	"github.com/toyinogun/AIITsupport/internal/graph"
	"github.com/toyinogun/AIITsupport/internal/logging"
	"github.com/toyinogun/AIITsupport/internal/seed/certs"
	"github.com/toyinogun/AIITsupport/internal/seed/directory"
	"github.com/toyinogun/AIITsupport/internal/seed/entra"
)

// clearPollInterval is how often `entra mfa --clear` checks the phones are gone (AC-9).
const clearPollInterval = 2 * time.Second

func runEntra(ctx context.Context, args []string, out io.Writer) error {
	if len(args) == 0 {
		return errUsage
	}
	switch args[0] {
	case "sync":
		return runEntraSync(ctx, args[1:], out)
	case "mfa":
		return runEntraMFA(ctx, args[1:], out)
	case "demo-password":
		return runEntraDemoPassword(ctx, args[1:], out)
	default:
		return fmt.Errorf("unknown entra command %q\n%w", args[0], errUsage)
	}
}

// runEntraSync makes Entra match SYNC-Entra-Users with the seed app (AC-2).
func runEntraSync(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("entra sync", flag.ContinueOnError)
	only := fs.String("only", "", "sync just this one user (no deletes)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return errUsage
	}
	start := time.Now()
	logger := logging.New(out, "info", service)

	cfg, err := config.LoadSeed(os.LookupEnv)
	if err != nil {
		return err
	}
	if !cfg.EntraConfigured() {
		logger.InfoContext(ctx, "Entra not configured, skipping")
		return nil
	}
	m, demo, err := loadEntraModel(cfg)
	if err != nil {
		return err
	}
	scope, err := readScope(cfg, m)
	if err != nil {
		return err
	}
	client, err := seedGraphClient(cfg)
	if err != nil {
		return err
	}

	syncer := entra.Syncer{Client: client, Logger: logger, Password: func(username string) string {
		if demo.IsDemo(username) {
			return cfg.EntraDemoPassword
		}
		return entra.RandomPassword()
	}}
	res, err := syncer.Run(ctx, scope, entra.Options{
		Domain: cfg.EntraDomain, Phones: entra.ExpectedPhones(m, demo),
		DeleteLimit: cfg.EntraDeleteLimit, Only: *only,
	})
	if err != nil {
		return fmt.Errorf("entra sync: %w", err)
	}
	logger.InfoContext(ctx, "entra synced",
		"created", res.Created, "updated", res.Updated, "restored", res.Restored, "deleted", res.Deleted,
		"phones_set", res.PhonesSet, "managers_set", res.ManagersSet, "managers_cleared", res.ManagersCleared,
		"managers_skipped", res.ManagersSkipped, "writes", res.Writes, "duration_ms", time.Since(start).Milliseconds())
	return nil
}

// loadEntraModel loads the seed file and the demo file, and checks the demo
// password when there are demo users, all before any network call (AC-8).
func loadEntraModel(cfg config.Seed) (directory.Model, entra.Demo, error) {
	m, err := directory.Load(cfg.File, cfg.OverlayFile)
	if err != nil {
		return directory.Model{}, entra.Demo{}, err
	}
	demo, err := entra.LoadDemo(cfg.EntraDemoFile, m)
	if err != nil {
		return directory.Model{}, entra.Demo{}, err
	}
	if len(demo.Users) > 0 {
		if err := entra.CheckDemoPassword(cfg.EntraDemoPassword, m); err != nil {
			return directory.Model{}, entra.Demo{}, err
		}
	}
	return m, demo, nil
}

// readScope binds to Samba as the Administrator; a directory that does not
// answer fails naming SEED_LDAP_URL before any Graph call (AC-12).
func readScope(cfg config.Seed, m directory.Model) ([]entra.ADUser, error) {
	pool, err := certs.Pool(cfg.CACert)
	if err != nil {
		return nil, err
	}
	dialer := directory.Dialer{URL: cfg.LDAPURL, RootCAs: pool}
	conn, err := dialer.Bind(cfg.AdminUser, cfg.AdminPassword)
	if err != nil {
		return nil, fmt.Errorf("read the sync scope from SEED_LDAP_URL: %w", err)
	}
	defer func() { _ = conn.Close() }()
	return entra.ReadScope(conn, m)
}

func seedGraphClient(cfg config.Seed) (*graph.Client, error) {
	return graph.NewWithSecret(
		graph.Credentials{TenantID: cfg.GraphTenantID, ClientID: cfg.GraphClientID, ClientSecret: cfg.GraphClientSecret},
		graph.Options{MaxRetries: cfg.GraphMaxRetries, Timeout: cfg.GraphTimeout, ClientIDSetting: "SEED_GRAPH_CLIENT_ID"},
	)
}

// runEntraMFA lists, and with --clear removes, a user's phone methods using
// only the tool server app (AC-9, AC-10).
func runEntraMFA(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("entra mfa", flag.ContinueOnError)
	clearPhonesFlag := fs.Bool("clear", false, "delete every phone method, then wait until none remain")
	if err := fs.Parse(args); err != nil {
		return err
	}
	username, err := oneUsername(fs.Args())
	if err != nil {
		return err
	}
	cfg, err := config.LoadSeedEntraMFA(os.LookupEnv)
	if err != nil {
		return err
	}
	client, err := graph.NewWithSecret(
		graph.Credentials{TenantID: cfg.GraphTenantID, ClientID: cfg.GraphClientID, ClientSecret: cfg.GraphClientSecret},
		graph.Options{MaxRetries: cfg.GraphMaxRetries, Timeout: cfg.GraphTimeout, ClientIDSetting: "GRAPH_CLIENT_ID"},
	)
	if err != nil {
		return err
	}
	u, err := client.UserByUsername(ctx, username)
	if err != nil {
		return err
	}
	if err := printMethods(ctx, client, u, out, "auth methods of "+username); err != nil {
		return err
	}
	if !*clearPhonesFlag {
		return nil
	}
	if err := clearPhones(ctx, client, u, cfg.ClearWait); err != nil {
		return err
	}
	return printMethods(ctx, client, u, out, "after clearing")
}

func printMethods(ctx context.Context, c *graph.Client, u graph.ManagedUser, out io.Writer, title string) error {
	methods, err := c.AuthMethods(ctx, u)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "%s:\n", title)
	for _, m := range methods {
		if m.Type == "phone" {
			fmt.Fprintf(out, "  phone %s %s\n", m.PhoneType, graph.MaskPhone(m.PhoneNumber))
			continue
		}
		fmt.Fprintf(out, "  %s\n", m.Type)
	}
	return nil
}

// clearPhones deletes every phone method, then polls until none remain.
func clearPhones(ctx context.Context, c *graph.Client, u graph.ManagedUser, wait time.Duration) error {
	phones, err := c.PhoneMethods(ctx, u)
	if err != nil {
		return err
	}
	for _, p := range phones {
		if err := c.DeletePhoneMethod(ctx, u, p.ID); err != nil {
			return fmt.Errorf("delete the %s phone method: %w", p.PhoneType, err)
		}
	}
	deadline := time.Now().Add(wait)
	for {
		left, err := c.PhoneMethods(ctx, u)
		if err != nil {
			return err
		}
		if len(left) == 0 {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%d phone methods still listed after %s (SEED_ENTRA_CLEAR_WAIT)", len(left), wait)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(clearPollInterval):
		}
	}
}

// runEntraDemoPassword sets SEED_ENTRA_DEMO_PASSWORD on one existing demo user.
func runEntraDemoPassword(ctx context.Context, args []string, out io.Writer) error {
	username, err := oneUsername(args)
	if err != nil {
		return err
	}
	cfg, err := config.LoadSeed(os.LookupEnv)
	if err != nil {
		return err
	}
	if !cfg.EntraConfigured() {
		return errors.New("entra demo-password: Entra is not configured; set SEED_GRAPH_* (see docs/runbooks/entra-tenant.md)")
	}
	_, demo, err := loadEntraModel(cfg)
	if err != nil {
		return err
	}
	if !demo.IsDemo(username) {
		return fmt.Errorf("%s is not a demo user in %s", username, cfg.EntraDemoFile)
	}
	client, err := seedGraphClient(cfg)
	if err != nil {
		return err
	}
	u, err := client.UserByUsername(ctx, username)
	if err != nil {
		return err
	}
	if err := entra.SetPassword(ctx, client, u, cfg.EntraDemoPassword); err != nil {
		return err
	}
	fmt.Fprintf(out, "set the demo password on %s\n", username)
	return nil
}
