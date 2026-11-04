// Package runner plays one eval ticket against the real agent and the real
// tool server child, and reads what happened from the attempt's database
// (spec 0018, AC-3 and AC-7).
package runner

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/toyinogun/AIITsupport/eval"
	"github.com/toyinogun/AIITsupport/eval/env"
	"github.com/toyinogun/AIITsupport/eval/score"
	"github.com/toyinogun/AIITsupport/eval/ticketset"
	"github.com/toyinogun/AIITsupport/internal/agent"
	"github.com/toyinogun/AIITsupport/internal/agent/agenttest"
	"github.com/toyinogun/AIITsupport/internal/config"
	"github.com/toyinogun/AIITsupport/internal/llm"
	"github.com/toyinogun/AIITsupport/internal/mcpclient"
	"github.com/toyinogun/AIITsupport/internal/zammad"
)

// settleGrace is added to the message and approval waits for the ticket timeout
// (AC-3).
const settleGrace = 30 * time.Second

// Observation is what one attempt did, scored.
type Observation struct {
	TicketID string
	Score    score.Result
	// Posts are the texts the fake Slack received, for the transcript.
	Posts          []string
	DurationMS     int64
	ApprovalWaitMS int64
	InputTokens    int
	OutputTokens   int
	CostMicroUSD   int64
	// Thread is the fake Slack thread ts of the attempt.
	Thread string
	// ToolServerLog is the child's log tail, kept only for a debugging run.
	ToolServerLog string
	TimedOut      bool
	// LLMFailed is true when the conversation has an agent_failures row at stage llm.
	LLMFailed bool
	// HasConversation is false for a gate ticket.
	HasConversation bool
}

// Runner plays tickets in one environment.
type Runner struct {
	Env    *env.Env
	Set    ticketset.Set
	Config config.Eval
	// Logger receives the agent's own log lines; it must be the redacting one.
	Logger *slog.Logger

	toolsHash string
	zammad    *zammad.Client
}

// ToolsHash is the hash of the tools/list the first attempt's child returned
// (AC-14); empty before the first attempt.
func (r *Runner) ToolsHash() string { return r.toolsHash }

// Run plays one ticket on a fresh attempt and returns what it saw. The
// attempt's database and tool server child are removed before it returns.
func (r *Runner) Run(ctx context.Context, t ticketset.Ticket) (Observation, error) {
	obs, err := r.attempt(ctx, t)
	if err != nil {
		return Observation{}, err
	}
	// The attempt's database and child are gone; now undo the outside state and
	// check it is the seed again (AC-7). A dirty environment ends the run.
	if err := r.Env.UndoAndVerify(context.WithoutCancel(ctx)); err != nil {
		return Observation{}, fmt.Errorf("after %s: %w", t.ID, err)
	}
	return obs, nil
}

func (r *Runner) attempt(ctx context.Context, t ticketset.Ticket) (obs Observation, err error) {
	attempt, err := r.Env.NewAttempt(ctx)
	if err != nil {
		return Observation{}, fmt.Errorf("start the attempt: %w", err)
	}
	defer func() {
		err = errors.Join(err, attempt.Close(context.WithoutCancel(ctx)))
	}()

	if err := r.hashTools(ctx, attempt); err != nil {
		return Observation{}, err
	}
	sender, err := SenderID(r.Env.Samba.Model, t)
	if err != nil {
		return Observation{}, err
	}
	if err := r.applySetup(ctx, attempt, t, sender); err != nil {
		return Observation{}, err
	}

	pool, err := pgxpool.New(ctx, attempt.AgentURL)
	if err != nil {
		return Observation{}, fmt.Errorf("open the agent pool: %w", err)
	}
	defer pool.Close()
	owner, err := pgxpool.New(ctx, attempt.OwnerURL)
	if err != nil {
		return Observation{}, fmt.Errorf("open the evidence pool: %w", err)
	}
	defer owner.Close()

	a, poster, stop, err := r.startAgent(ctx, attempt, pool)
	if err != nil {
		return Observation{}, err
	}
	defer func() { err = errors.Join(err, stop()) }()

	thread := strconv.FormatInt(time.Now().Unix(), 10) + ".000100"
	start := time.Now()
	a.Submit(agent.Inbound{
		EventID: "Ev" + t.ID, EventType: "message.im", ChannelID: evalChannel,
		ThreadTS: thread, TS: thread, UserID: sender, Text: t.Message,
	})
	wait, cancel := context.WithTimeout(ctx, r.ticketTimeout())
	defer cancel()
	approval := make(chan approvalResult, 1)
	if t.Approval == "approve" || t.Approval == "deny" {
		go func() { approval <- r.approve(wait, owner, a, t, thread) }()
	} else {
		approval <- approvalResult{}
	}
	waitErr := a.WaitIdle(wait, evalChannel, thread)
	duration := time.Since(start).Milliseconds()
	timedOut := false
	if waitErr != nil {
		if ctx.Err() != nil {
			return Observation{}, ctx.Err()
		}
		timedOut = true
	}
	cancel()
	ap := <-approval

	obs, err = r.observe(ctx, owner, t, thread, poster.Texts(), ap)
	if err != nil {
		return Observation{}, err
	}
	obs.DurationMS, obs.TimedOut, obs.Thread = duration, timedOut, thread
	obs.ToolServerLog = attempt.ToolServerLog()
	return obs, nil
}

// observe reads the evidence and scores the attempt.
func (r *Runner) observe(ctx context.Context, owner *pgxpool.Pool, t ticketset.Ticket, thread string, posts []string, ap approvalResult) (Observation, error) {
	ev, zammadID, u, err := readEvidence(ctx, owner, thread)
	if err != nil {
		return Observation{}, err
	}
	ev.Sender = t.Sender.User
	ev.Replies = posts
	ev.GateText = u.Rows == 0 && !ev.HasConversation && gateTextSeen(posts)
	ev.ApprovalMissing = (t.Approval == "approve" || t.Approval == "deny") && ap.Missing
	if zammadID != 0 && t.Expect.FinalTicketState != "" {
		if ev.FinalTicketState, err = r.ticketState(ctx, int(zammadID)); err != nil {
			return Observation{}, err
		}
	}
	failed, err := llmFailed(ctx, owner, thread)
	if err != nil {
		return Observation{}, err
	}
	return Observation{
		TicketID: t.ID, Score: score.Score(t, ev), Posts: posts, ApprovalWaitMS: ap.WaitMS,
		InputTokens: u.Input, OutputTokens: u.Output, CostMicroUSD: u.CostMicroUSD,
		LLMFailed: failed, HasConversation: ev.HasConversation,
	}, nil
}

// llmFailed is true when the thread's conversation has an llm stage failure.
func llmFailed(ctx context.Context, owner *pgxpool.Pool, thread string) (bool, error) {
	var n int
	err := owner.QueryRow(ctx,
		`SELECT COUNT(*) FROM agent_failures f JOIN conversations c ON c.id = f.conversation_id
		 WHERE c.slack_channel_id = $1 AND c.slack_thread_ts = $2 AND f.stage = 'llm'`, evalChannel, thread).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("read the agent failures: %w", err)
	}
	return n > 0, nil
}

// ticketState reads the Zammad ticket's state through the assistant's read client.
func (r *Runner) ticketState(ctx context.Context, id int) (string, error) {
	if r.zammad == nil {
		c, err := zammad.New(zammad.Options{BaseURL: r.Env.Zammad.URL, Timeout: 30 * time.Second, Auth: zammad.TokenAuth(r.Env.Zammad.Token)})
		if err != nil {
			return "", fmt.Errorf("make the Zammad read client: %w", err)
		}
		r.zammad = c
	}
	ticket, err := r.zammad.GetTicket(ctx, id)
	if err != nil {
		return "", fmt.Errorf("read the ticket: %w", err)
	}
	states, err := r.zammad.TicketStates(ctx)
	if err != nil {
		return "", fmt.Errorf("read the ticket states: %w", err)
	}
	for _, s := range states {
		if s.ID == ticket.StateID {
			return ticketStateName(s.Name), nil
		}
	}
	return "", fmt.Errorf("ticket state %d is unknown", ticket.StateID)
}

// hashTools hashes the first child's tools/list once per run (AC-14).
func (r *Runner) hashTools(ctx context.Context, at *env.Attempt) error {
	if r.toolsHash != "" {
		return nil
	}
	client, err := mcpclient.New(at.Child.URL+"/mcp", r.Env.Token)
	if err != nil {
		return err
	}
	list, err := client.ListTools(ctx)
	if err != nil {
		return fmt.Errorf("list the tool server's tools: %w", err)
	}
	r.toolsHash, err = ToolsHash(list)
	return err
}

func (r *Runner) ticketTimeout() time.Duration {
	return 45*time.Second + r.Config.ApprovalWait + settleGrace
}

// startAgent builds the in process agent over the attempt: the real model, the
// real store, and the real MCP and approval clients, with fake Slack.
func (r *Runner) startAgent(ctx context.Context, at *env.Attempt, pool *pgxpool.Pool) (*agent.Agent, *agenttest.Poster, func() error, error) {
	cfg, err := r.agentConfig(at)
	if err != nil {
		return nil, nil, nil, err
	}
	price, err := eval.Price(cfg.LLMModel)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("LLM_MODEL %q has no price in eval/pricing.yaml: %w", cfg.LLMModel, err)
	}
	client, err := llm.New(cfg)
	if err != nil {
		return nil, nil, nil, err
	}
	tools, err := mcpclient.New(cfg.ToolServerURL, cfg.ToolServerToken)
	if err != nil {
		return nil, nil, nil, err
	}
	approvals, err := mcpclient.NewApprovalClient(cfg.ToolServerURL, cfg.ToolServerToken)
	if err != nil {
		return nil, nil, nil, err
	}
	poster := &agenttest.Poster{}
	a, err := agent.New(agent.Config{
		Model: cfg.LLMModel, Price: price, TeamID: agenttest.Team,
		MaxTurns: cfg.MaxTurns, MaxResponseTokens: int64(cfg.MaxResponseTokens),
		MaxTokensPerTicket: int64(cfg.MaxTokensPerTicket), MaxConcurrent: cfg.MaxConcurrent,
		ConversationsPerHour: int64(cfg.ConversationsPerHour), MessageTimeout: cfg.MessageTimeout,
		ToolTimeout: cfg.ToolTimeout, PostTimeout: 10 * time.Second, ShutdownGrace: cfg.ShutdownGrace,
		ITChannel:             ITChannel,
		ApprovalDecideTimeout: cfg.ApprovalDecideTimeout, ApprovalPollInterval: cfg.ApprovalPollInterval,
		ApprovalTimeout: cfg.ApprovalTimeout, AccessApprovalTimeout: cfg.AccessApprovalTimeout,
	}, agent.Deps{
		Slack: SlackUsers(r.Env.Samba.Model), Poster: poster, Tools: tools,
		Model: &client.Messages, Store: agent.NewStore(pool), Approvals: approvals, Logger: r.Logger,
	})
	if err != nil {
		return nil, nil, nil, err
	}
	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	done := make(chan error, 1)
	go func() { done <- a.Run(runCtx) }()
	stop := func() error {
		cancel()
		return <-done
	}
	return a, poster, stop, nil
}

// agentConfig reads the agent's defaults and limits through config.LoadAgent,
// so no default is declared a second time.
func (r *Runner) agentConfig(at *env.Attempt) (config.Agent, error) {
	values := map[string]string{
		"AGENT_DATABASE_URL": at.AgentURL, "TOOLSERVER_URL": at.Child.URL + "/mcp", "TOOLSERVER_TOKEN": r.Env.Token,
		"ANTHROPIC_API_KEY": r.Config.AnthropicAPIKey, "LLM_MODEL": r.Config.LLMModel,
	}
	return config.LoadAgent(func(k string) (string, bool) { v, ok := values[k]; return v, ok })
}
