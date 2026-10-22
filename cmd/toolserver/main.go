// Command toolserver is the MCP tool server, the only process allowed to
// touch the client's systems (Samba AD, Entra, Zammad). It serves the tool
// layer (spec 0007) at /mcp and health at /healthz, and, when the RESET_*
// settings are set, the password reset page on its own listener (spec 0011).
// Startup checks configuration only, never whether AD or Zammad are reachable.
package main

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
	_ "time/tzdata" // the image is distroless, with no zoneinfo (spec 0014, AC-4)

	"golang.org/x/sync/errgroup"

	"github.com/toyinogun/AIITsupport/internal/approvalapi"
	"github.com/toyinogun/AIITsupport/internal/audit"
	"github.com/toyinogun/AIITsupport/internal/config"
	"github.com/toyinogun/AIITsupport/internal/db"
	"github.com/toyinogun/AIITsupport/internal/graph"
	"github.com/toyinogun/AIITsupport/internal/ldap"
	"github.com/toyinogun/AIITsupport/internal/logging"
	"github.com/toyinogun/AIITsupport/internal/mcpclient"
	"github.com/toyinogun/AIITsupport/internal/mcpserver"
	"github.com/toyinogun/AIITsupport/internal/readiness"
	"github.com/toyinogun/AIITsupport/internal/resetlink"
	"github.com/toyinogun/AIITsupport/internal/resetpage"
	"github.com/toyinogun/AIITsupport/internal/server"
	"github.com/toyinogun/AIITsupport/internal/telemetry"
	"github.com/toyinogun/AIITsupport/internal/tools"
	"github.com/toyinogun/AIITsupport/internal/zammad"
)

const service = "toolserver"

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", service, err)
		os.Exit(1)
	}
}

func run() (err error) {
	cfg, err := config.LoadToolServer(os.LookupEnv)
	if err != nil {
		return err
	}
	logger := logging.New(os.Stdout, cfg.LogLevel, service)

	// Check every setting before dialing anything.
	zone, err := tools.LoadKBZone()
	if err != nil {
		return err
	}
	toolCfg := toolConfig(cfg, zone)
	if err := toolCfg.Check(); err != nil {
		return fmt.Errorf("toolserver config: %w", err)
	}
	rootCAs, err := ldap.LoadCA(cfg.LDAPCACert)
	if err != nil {
		return fmt.Errorf("LDAP_CA_CERT: %w", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	shutdownTracing, err := telemetry.Setup(ctx, service, cfg.OTelEndpoint, logger)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, shutdownTracing(context.Background())) }()

	pool, err := db.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	layer, err := toolLayer(cfg, toolCfg, rootCAs, audit.NewPgStore(pool), resetlink.NewStore(pool), logger)
	if err != nil {
		return err
	}
	mux := server.NewMux()
	mux.Handle(mcpserver.Path, layer.mcp)
	// The approval routes beside /mcp, on the same listener (spec 0012, AC-13).
	mux.Handle(mcpclient.ApprovalsPath, layer.approvals)
	mux.Handle(mcpclient.ApprovalsPath+"/", layer.approvals)
	// The readiness report, behind the MCP token (spec 0015, AC-16).
	ready := append([]readiness.Check{{Name: "database", Probe: pool.Ping}}, layer.checks...)
	mux.Handle("GET "+readiness.Path, mcpserver.RequireToken(cfg.ToolServerToken, readiness.Handler(ready...)))

	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", cfg.HTTPAddr)
	if err != nil {
		return fmt.Errorf("listen on TOOLSERVER_HTTP_ADDR %s: %w", cfg.HTTPAddr, err)
	}
	var resetLn net.Listener
	if layer.reset != nil {
		if resetLn, err = (&net.ListenConfig{}).Listen(ctx, "tcp", cfg.ResetHTTPAddr); err != nil {
			_ = ln.Close()
			return fmt.Errorf("listen on RESET_HTTP_ADDR %s: %w", cfg.ResetHTTPAddr, err)
		}
	}
	if cfg.KillSwitchFile == "" {
		logger.Warn("kill_switch_file_unset", "read_only", cfg.ReadOnly)
	}
	logger.Info("tool server starting")

	g, ctx := errgroup.WithContext(ctx)
	g.Go(func() error { return server.Run(ctx, ln, mux, logger) })
	if layer.reset != nil {
		g.Go(func() error {
			return server.Run(ctx, resetLn, layer.reset.Handler(server.NewMux()), logger, server.WithLimits(resetLimits(cfg)))
		})
		g.Go(func() error { resetlink.Cleanup(ctx, layer.links, cfg.TicketRetentionDays, logger); return nil })
	}
	return g.Wait()
}

// resetLimits are the reset listener's server settings (spec 0011, AC-16);
// the MCP listener keeps the defaults.
func resetLimits(cfg config.ToolServer) server.Limits {
	return server.Limits{
		ReadTimeout:    10 * time.Second,
		WriteTimeout:   cfg.ToolCallTimeout + 5*time.Second,
		IdleTimeout:    60 * time.Second,
		MaxHeaderBytes: 8 << 10,
	}
}

// toolConfig is the tool layer's part of the settings; zone is where the
// knowledge base's dates are read.
func toolConfig(cfg config.ToolServer, zone *time.Location) tools.Config {
	staleAsOf, _ := cfg.StaleAsOf()
	return tools.Config{
		BaseDN:            cfg.LDAPBaseDN,
		UserOUs:           cfg.ToolUserOUs,
		GroupOUs:          cfg.ToolGroupOUs,
		DeniedGroups:      cfg.ToolDeniedGroups,
		CallTimeout:       cfg.ToolCallTimeout,
		WriteLimit:        cfg.ToolWriteLimit,
		WriteWindow:       cfg.ToolWriteWindow,
		TicketCreateLimit: cfg.TicketCreateLimit,
		TicketMaxPending:  cfg.TicketMaxPending,
		PrivilegedWindow:  cfg.PrivilegedWindow,
		ExecuteTimeout:    cfg.ApprovalExecuteTimeout,
		ExecuteGrace:      cfg.ApprovalExecuteGrace,
		AccessMaxPending:  cfg.AccessMaxPending,
		KB: tools.KBSettings{
			SearchLimit:      cfg.KBSearchLimit,
			SearchCandidates: cfg.KBSearchCandidates,
			StaleAfterDays:   cfg.KBStaleAfterDays,
			BodyMaxRunes:     cfg.KBBodyMaxRunes,
			StaleAsOf:        staleAsOf,
			Zone:             zone,
		},
	}
}

// layer is what toolLayer builds: the /mcp handler, the approval routes,
// the client system readiness checks, and, when the RESET_* settings are
// set, the reset page and its store.
type layer struct {
	mcp       http.Handler
	approvals http.Handler
	checks    []readiness.Check
	reset     *resetpage.Page
	links     *resetlink.Store
}

// toolLayer builds the dispatcher, its /mcp handler, and the reset page. It
// dials nothing.
func toolLayer(cfg config.ToolServer, toolCfg tools.Config, rootCAs *x509.CertPool, store audit.Store, links *resetlink.Store, logger *slog.Logger) (layer, error) {
	recorder, err := audit.NewRecorder(store, audit.Config{
		OutcomeRetries:  cfg.AuditOutcomeRetries,
		WriteTimeout:    cfg.AuditWriteTimeout,
		MaxPayloadBytes: cfg.AuditMaxPayloadBytes,
	}, logger)
	if err != nil {
		return layer{}, err
	}
	dir := ldap.Config{
		Dialer:   ldap.Dialer{URL: cfg.LDAPURL, RootCAs: rootCAs, Timeout: cfg.LDAPTimeout},
		BindDN:   cfg.LDAPBindDN,
		Password: cfg.LDAPBindPassword,
		BaseDN:   cfg.LDAPBaseDN,
	}
	desk, err := zammad.New(zammad.Options{BaseURL: cfg.ZammadURL, Auth: zammad.TokenAuth(cfg.ZammadToken), Timeout: cfg.ZammadTimeout})
	if err != nil {
		return layer{}, err
	}
	reader := ldap.NewClient(dir)
	helpdesk := tools.NewHelpdesk(desk, cfg.ZammadDefaultGroup, cfg.ZammadPublicURL)
	killSwitch := tools.NewKillSwitch(cfg.KillSwitchFile, cfg.ReadOnly)
	sys := tools.Systems{
		Directory:  reader,
		Groups:     reader,
		Reader:     reader,
		Writer:     ldap.NewWriter(dir),
		Helpdesk:   helpdesk,
		Recorder:   recorder,
		KillSwitch: killSwitch,
	}
	// Each client system, as the tool server's own accounts (AC-16).
	checks := []readiness.Check{
		{Name: "ldap", Probe: reader.Ping},
		{Name: "zammad", Probe: func(ctx context.Context) error { _, err := desk.Me(ctx); return err }},
		{Name: "graph"},
	}
	var opts []tools.RegistryOption
	if cfg.ResetEnabled() {
		opts = append(opts, tools.WithPasswordReset())
		sys.Reset = &tools.ResetSystem{Links: links, DeniedGroups: cfg.ResetDeniedGroups, Settings: tools.ResetSettings{
			PublicURL: cfg.ResetPublicURL, TTL: cfg.ResetLinkTTL, LinkLimit: cfg.ResetLinkLimit, Window: cfg.ResetLinkWindow,
		}}
	}
	if cfg.MFAEnabled() {
		// mfa_clear (spec 0012): the tool server's own Entra app, never the seed app.
		g, err := graph.NewWithCredentials(graph.Credentials{
			TenantID: cfg.GraphTenantID, ClientID: cfg.GraphClientID, ClientSecret: cfg.GraphClientSecret, CertFile: cfg.GraphClientCert,
		}, graph.Options{MaxRetries: cfg.GraphMaxRetries, Timeout: cfg.GraphTimeout, ClientIDSetting: "GRAPH_CLIENT_ID"})
		if err != nil {
			return layer{}, err
		}
		checks[2].Probe = g.Ping
		opts = append(opts, tools.WithMFAClear(cfg.SlackITApprovers, cfg.ApprovalTimeout))
		sys.MFA = &tools.MFASystem{Graph: g, Reader: reader, DeniedGroups: cfg.MFADeniedGroups}
	}
	reg, err := tools.NewRegistry(cfg.AccessApprovalTimeout, opts...)
	if err != nil {
		return layer{}, err
	}
	d, err := tools.NewDispatcher(reg, toolCfg, sys, logger)
	if err != nil {
		return layer{}, err
	}
	mcp, err := mcpserver.Handler(d, mcpserver.Options{
		Token: cfg.ToolServerToken, MaxRequestBytes: int64(cfg.MaxRequestBytes), Logger: logger,
	})
	if err != nil {
		return layer{}, err
	}
	approvals, err := approvalapi.Handler(d, recorder, approvalapi.Options{
		Token: cfg.ToolServerToken, MaxRequestBytes: int64(cfg.MaxRequestBytes), Logger: logger,
	})
	if err != nil || !cfg.ResetEnabled() {
		return layer{mcp: mcp, approvals: approvals, checks: checks}, err
	}
	resetDir := dir
	resetDir.BindDN, resetDir.Password = cfg.ResetLDAPBindDN, cfg.ResetLDAPBindPassword
	page, err := resetpage.New(resetpage.Config{
		Links:       links,
		Directory:   reader,
		Writer:      ldap.NewPasswordWriter(resetDir),
		Tickets:     helpdesk,
		Accounts:    tools.NewAccountFence(reader, cfg.ResetDeniedGroups, logger),
		Recorder:    recorder,
		KillSwitch:  killSwitch,
		BaseDN:      cfg.LDAPBaseDN,
		UserOUs:     cfg.ToolUserOUs,
		CallTimeout: cfg.ToolCallTimeout,
		MaxInflight: cfg.ResetMaxInflight,
		SecureHSTS:  strings.HasPrefix(cfg.ResetPublicURL, "https://"),
		Logger:      logger,
	})
	if err != nil {
		return layer{}, err
	}
	return layer{mcp: mcp, approvals: approvals, checks: checks, reset: page, links: links}, nil
}
