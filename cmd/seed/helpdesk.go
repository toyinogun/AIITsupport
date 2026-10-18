package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/go-ldap/ldap/v3"

	"github.com/toyinogun/AIITsupport/internal/config"
	"github.com/toyinogun/AIITsupport/internal/logging"
	"github.com/toyinogun/AIITsupport/internal/seed/certs"
	"github.com/toyinogun/AIITsupport/internal/seed/directory"
	"github.com/toyinogun/AIITsupport/internal/seed/helpdesk"
	"github.com/toyinogun/AIITsupport/internal/zammad"
)

// healthPoll is how often the seed asks whether Zammad is up.
const healthPoll = 5 * time.Second

func runHelpdesk(ctx context.Context, args []string, out io.Writer) error {
	if len(args) > 0 {
		return errUsage
	}
	start := time.Now()
	logger := logging.New(out, "info", service)

	cfg, err := config.LoadSeedHelpdesk(os.LookupEnv)
	if err != nil {
		return err
	}
	seeder, err := newSeeder(cfg)
	if err != nil {
		return err
	}

	if err := waitHealthy(ctx, cfg.ZammadURL, cfg.ZammadWaitTimeout); err != nil {
		return err
	}
	if err := checkZammadBind(cfg); err != nil {
		return err
	}
	res, err := seeder.Run(ctx)
	if err != nil {
		return fmt.Errorf("seed helpdesk: %w", err)
	}
	if res.AlreadySeeded {
		logger.Info("already seeded", "duration_ms", time.Since(start).Milliseconds())
		return nil
	}
	logger.Info("helpdesk seeded",
		"users", res.Users, "tickets", res.Tickets, "articles", res.Articles,
		"categories", res.Categories, "answers", res.Answers, "duration_ms", time.Since(start).Milliseconds())
	return nil
}

func newSeeder(cfg config.SeedHelpdesk) (helpdesk.Seeder, error) {
	m, err := directory.Load(cfg.File, cfg.OverlayFile)
	if err != nil {
		return helpdesk.Seeder{}, err
	}
	content, err := helpdesk.Load(cfg.HelpdeskDir, m)
	if err != nil {
		return helpdesk.Seeder{}, err
	}
	wd, err := os.Getwd()
	if err != nil {
		return helpdesk.Seeder{}, fmt.Errorf("working directory: %w", err)
	}
	marker, err := helpdesk.Marker(wd, cfg.HelpdeskDir, cfg.File, cfg.CACert)
	if err != nil {
		return helpdesk.Seeder{}, err
	}
	admin, err := zammad.New(zammad.Options{
		BaseURL: cfg.ZammadURL, Timeout: cfg.ZammadTimeout,
		Auth: zammad.BasicAuth{Login: cfg.ZammadAdminEmail, Password: cfg.ZammadAdminPassword},
	})
	if err != nil {
		return helpdesk.Seeder{}, err
	}
	assistant, err := zammad.New(zammad.Options{BaseURL: cfg.ZammadURL, Timeout: cfg.ZammadTimeout, Auth: zammad.TokenAuth(cfg.ZammadToken)})
	if err != nil {
		return helpdesk.Seeder{}, err
	}
	return helpdesk.Seeder{
		Runner: helpdesk.ComposeRunner{Dir: wd, Limit: cfg.ZammadWaitTimeout},
		Admin:  admin, Assistant: assistant, Content: content, Model: m, Marker: marker,
		Group: cfg.ZammadDefaultGroup,
		Secrets: helpdesk.Secrets{
			AdminEmail: cfg.ZammadAdminEmail, AdminPassword: cfg.ZammadAdminPassword,
			BindPassword: cfg.ZammadBindPassword, AssistantToken: cfg.ZammadToken,
		},
	}, nil
}

// waitHealthy polls Zammad's front page until it answers 200.
func waitHealthy(ctx context.Context, url string, wait time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	client := &http.Client{Timeout: healthPoll}
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url+"/", nil)
		if err != nil {
			return fmt.Errorf("ZAMMAD_URL %s: %w", url, err)
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
			return fmt.Errorf("zammad at %s was not healthy within %s (SEED_ZAMMAD_WAIT_TIMEOUT); is make helpdesk-up running?", url, wait)
		case <-time.After(healthPoll):
		}
	}
}

// checkZammadBind binds as svc-zammad, so a directory older than spec 0006
// (no svc-zammad) fails before Zammad is touched.
func checkZammadBind(cfg config.SeedHelpdesk) error {
	pool, err := certs.Pool(cfg.CACert)
	if err != nil {
		return err
	}
	conn, err := directory.Dialer{URL: cfg.LDAPURL, RootCAs: pool}.Bind(helpdesk.ZammadBindDN, cfg.ZammadBindPassword)
	if err != nil {
		if ldap.IsErrorWithCode(errors.Unwrap(err), ldap.LDAPResultInvalidCredentials) || ldap.IsErrorWithCode(err, ldap.LDAPResultInvalidCredentials) {
			return errors.New("svc-zammad cannot bind with SEED_ZAMMAD_BIND_PASSWORD (a directory older than the helpdesk, or a changed password); run make directory-reset")
		}
		return fmt.Errorf("check svc-zammad: %w", err)
	}
	return conn.Close()
}
