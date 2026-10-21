// Command agent is the Slack facing IT support agent. It talks to Claude and
// calls the tool server over MCP; it never holds client system credentials.
// It loads its config, reaches Postgres, and serves health. When both Slack
// tokens are set it also runs the agent loop (spec 0008): it checks the bot
// token, sweeps conversations a restart interrupted, then listens.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	// The group owner's card shows its expiry in Europe/Amsterdam (spec 0013,
	// AC-12), so the zone database goes into the binary for a bare container.
	_ "time/tzdata"

	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/sync/errgroup"

	"github.com/toyinogun/AIITsupport/eval"
	"github.com/toyinogun/AIITsupport/internal/agent"
	"github.com/toyinogun/AIITsupport/internal/config"
	"github.com/toyinogun/AIITsupport/internal/db"
	"github.com/toyinogun/AIITsupport/internal/db/queries"
	"github.com/toyinogun/AIITsupport/internal/llm"
	"github.com/toyinogun/AIITsupport/internal/logging"
	"github.com/toyinogun/AIITsupport/internal/mcpclient"
	"github.com/toyinogun/AIITsupport/internal/server"
	"github.com/toyinogun/AIITsupport/internal/slack"
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
	var model agentModel
	if cfg.SlackConfigured() {
		if model, err = loadModel(cfg); err != nil {
			return err
		}
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

	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", cfg.HTTPAddr)
	if err != nil {
		return fmt.Errorf("listen on AGENT_HTTP_ADDR %s: %w", cfg.HTTPAddr, err)
	}

	// A refused Slack token ends the listener with an error, which stops
	// the health server too, so run() exits 1 naming the setting.
	g, ctx := errgroup.WithContext(ctx)
	g.Go(func() error { return server.Run(ctx, ln, server.NewMux(), logger) })
	// Chat text expires with or without Slack (spec 0010, AC-8).
	retention := agent.NewRetention(agent.NewStore(pool), cfg.TicketRetentionDays, agent.RetentionEvery, time.Now, logger)
	g.Go(func() error { return retention.Run(ctx) })
	if cfg.SlackConfigured() {
		g.Go(func() error { return runSlack(ctx, cfg, model, pool, logger) })
	} else {
		logger.Info("slack_disabled")
	}
	return g.Wait()
}

// agentModel is what the loop needs from the model side, checked before
// anything connects (AC-15, AC-16).
type agentModel struct {
	price     eval.ModelPrice
	model     agent.Model
	client    *mcpclient.Client
	approvals *mcpclient.ApprovalClient
}

func loadModel(cfg config.Agent) (agentModel, error) {
	price, err := eval.Price(cfg.LLMModel)
	if err != nil {
		return agentModel{}, fmt.Errorf("LLM_MODEL %q has no price in eval/pricing.yaml: %w", cfg.LLMModel, err)
	}
	client, err := llm.New(cfg)
	if err != nil {
		return agentModel{}, err
	}
	tools, err := mcpclient.New(cfg.ToolServerURL, cfg.ToolServerToken)
	if err != nil {
		return agentModel{}, err
	}
	approvals, err := mcpclient.NewApprovalClient(cfg.ToolServerURL, cfg.ToolServerToken)
	if err != nil {
		return agentModel{}, err
	}
	return agentModel{price: price, model: &client.Messages, client: tools, approvals: approvals}, nil
}

// runSlack checks the bot token, builds the agent, sweeps interrupted
// conversations before the socket starts, then runs the agent and the
// listener until ctx ends. A refused token ends run() with an error.
func runSlack(ctx context.Context, cfg config.Agent, m agentModel, pool *pgxpool.Pool, logger *slog.Logger) error {
	listener := slack.New(cfg.SlackBotToken, cfg.SlackAppToken, queries.New(pool), logger, cfg.SlackEventDedupTTL)
	team, err := listener.CheckBotToken(ctx)
	if err != nil {
		return err
	}
	if ctx.Err() != nil { // the signal came during the token check
		return nil
	}
	a, err := agent.New(agent.Config{
		Model:                cfg.LLMModel,
		Price:                m.price,
		TeamID:               team,
		MaxTurns:             cfg.MaxTurns,
		MaxResponseTokens:    int64(cfg.MaxResponseTokens),
		MaxTokensPerTicket:   int64(cfg.MaxTokensPerTicket),
		MaxConcurrent:        cfg.MaxConcurrent,
		ConversationsPerHour: int64(cfg.ConversationsPerHour),
		MessageTimeout:       cfg.MessageTimeout,
		ToolTimeout:          cfg.ToolTimeout,
		PostTimeout:          slack.ReplyTimeout,
		ShutdownGrace:        cfg.ShutdownGrace,
		ITChannel:            cfg.SlackITChannel,

		ApprovalDecideTimeout: cfg.ApprovalDecideTimeout,
		ApprovalPollInterval:  cfg.ApprovalPollInterval,
		ApprovalTimeout:       cfg.ApprovalTimeout,
		AccessApprovalTimeout: cfg.AccessApprovalTimeout,
	}, agent.Deps{
		Slack: listener.Users, Poster: listener.Poster, Tools: m.client,
		Model: m.model, Store: agent.NewStore(pool), Approvals: m.approvals, Logger: logger,
	})
	if err != nil {
		return err
	}
	listener.Handler = a
	a.Sweep(ctx)

	// The agent drains only after the listener has stopped, so no accepted
	// message reaches Submit after the drain (AC-13).
	agentCtx, stopAgent := context.WithCancel(context.WithoutCancel(ctx))
	defer stopAgent()
	g := new(errgroup.Group)
	g.Go(func() error { return a.Run(agentCtx) })
	g.Go(func() error {
		defer stopAgent()
		return listener.Run(ctx)
	})
	return g.Wait()
}
