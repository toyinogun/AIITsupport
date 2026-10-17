// Command toolserver is the MCP tool server, the only process allowed to
// touch the client's systems (Samba AD, Entra, Zammad). At this stage it only
// loads its config, reaches Postgres, and serves health; tools arrive in
// feature #9.
package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/toyinogun/AIITsupport/internal/config"
	"github.com/toyinogun/AIITsupport/internal/db"
	"github.com/toyinogun/AIITsupport/internal/logging"
	"github.com/toyinogun/AIITsupport/internal/server"
	"github.com/toyinogun/AIITsupport/internal/telemetry"
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

	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", cfg.HTTPAddr)
	if err != nil {
		return fmt.Errorf("listen on TOOLSERVER_HTTP_ADDR %s: %w", cfg.HTTPAddr, err)
	}
	logger.Info("tool server starting", "read_only", cfg.ReadOnly)
	return server.Run(ctx, ln, server.NewMux(), logger)
}
