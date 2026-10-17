// Command agent is the Slack facing IT support agent. It talks to Claude and
// calls the tool server over MCP; it never holds client system credentials.
// At this stage it only loads its config, reaches Postgres, and serves health.
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

const service = "agent"

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", service, err)
		os.Exit(1)
	}
}

func run() (err error) {
	cfg, err := config.LoadAgent(os.LookupEnv)
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
		return fmt.Errorf("listen on AGENT_HTTP_ADDR %s: %w", cfg.HTTPAddr, err)
	}
	return server.Run(ctx, ln, server.NewMux(), logger)
}
