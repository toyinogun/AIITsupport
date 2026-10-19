// Command toolserver is the MCP tool server, the only process allowed to
// touch the client's systems (Samba AD, Entra, Zammad). It serves the tool
// layer (spec 0007) at /mcp and health at /healthz. Startup checks
// configuration only, never whether AD or Zammad are reachable.
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
	"syscall"

	"github.com/toyinogun/AIITsupport/internal/audit"
	"github.com/toyinogun/AIITsupport/internal/config"
	"github.com/toyinogun/AIITsupport/internal/db"
	"github.com/toyinogun/AIITsupport/internal/ldap"
	"github.com/toyinogun/AIITsupport/internal/logging"
	"github.com/toyinogun/AIITsupport/internal/mcpserver"
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
	toolCfg := toolConfig(cfg)
	if err := toolCfg.Check(); err != nil {
		return fmt.Errorf("toolserver config: %w", err)
	}
	rootCAs, err := ldap.LoadCA(cfg.LDAPCACert)
	if err != nil {
		return fmt.Errorf("LDAP_CA_CERT: %w", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	shutdownTracing, err := telemetry.Setup(ctx, service, cfg.OTelEndpoint)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, shutdownTracing(context.Background())) }()

	pool, err := db.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	mcp, err := toolLayer(cfg, toolCfg, rootCAs, audit.NewPgStore(pool), logger)
	if err != nil {
		return err
	}
	mux := server.NewMux()
	mux.Handle(mcpserver.Path, mcp)

	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", cfg.HTTPAddr)
	if err != nil {
		return fmt.Errorf("listen on TOOLSERVER_HTTP_ADDR %s: %w", cfg.HTTPAddr, err)
	}
	if cfg.KillSwitchFile == "" {
		logger.Warn("kill_switch_file_unset", "read_only", cfg.ReadOnly)
	}
	logger.Info("tool server starting")
	return server.Run(ctx, ln, mux, logger)
}

// toolConfig is the tool layer's part of the settings.
func toolConfig(cfg config.ToolServer) tools.Config {
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
	}
}

// toolLayer builds the dispatcher and its /mcp handler. It dials nothing.
func toolLayer(cfg config.ToolServer, toolCfg tools.Config, rootCAs *x509.CertPool, store audit.Store, logger *slog.Logger) (http.Handler, error) {
	recorder, err := audit.NewRecorder(store, audit.Config{
		OutcomeRetries:  cfg.AuditOutcomeRetries,
		WriteTimeout:    cfg.AuditWriteTimeout,
		MaxPayloadBytes: cfg.AuditMaxPayloadBytes,
	}, logger)
	if err != nil {
		return nil, err
	}
	dir := ldap.Config{
		Dialer:   ldap.Dialer{URL: cfg.LDAPURL, RootCAs: rootCAs, Timeout: cfg.LDAPTimeout},
		BindDN:   cfg.LDAPBindDN,
		Password: cfg.LDAPBindPassword,
		BaseDN:   cfg.LDAPBaseDN,
	}
	desk, err := zammad.New(zammad.Options{BaseURL: cfg.ZammadURL, Auth: zammad.TokenAuth(cfg.ZammadToken), Timeout: cfg.ZammadTimeout})
	if err != nil {
		return nil, err
	}
	reg, err := tools.NewRegistry()
	if err != nil {
		return nil, err
	}
	d, err := tools.NewDispatcher(reg, toolCfg, tools.Systems{
		Directory:  ldap.NewClient(dir),
		Writer:     ldap.NewWriter(dir),
		Helpdesk:   tools.NewHelpdesk(desk, cfg.ZammadDefaultGroup, cfg.ZammadPublicURL),
		Recorder:   recorder,
		KillSwitch: tools.NewKillSwitch(cfg.KillSwitchFile, cfg.ReadOnly),
	}, logger)
	if err != nil {
		return nil, err
	}
	return mcpserver.Handler(d, mcpserver.Options{
		Token: cfg.ToolServerToken, MaxRequestBytes: int64(cfg.MaxRequestBytes), Logger: logger,
	})
}
