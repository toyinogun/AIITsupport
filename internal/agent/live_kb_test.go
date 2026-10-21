package agent_test

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	sdkoption "github.com/anthropics/anthropic-sdk-go/option"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/toyinogun/AIITsupport/eval"
	"github.com/toyinogun/AIITsupport/internal/agent"
	"github.com/toyinogun/AIITsupport/internal/agent/agenttest"
	"github.com/toyinogun/AIITsupport/internal/config"
	"github.com/toyinogun/AIITsupport/internal/llm"
	"github.com/toyinogun/AIITsupport/internal/mcpclient"
)

// TestLiveKBAnswers runs the knowledge base questions of spec 0014 through
// the real agent loop: the configured model, the running tool server (and
// so the dev Zammad and Samba), with Slack and the conversation store faked
// in memory. It needs the tool server running with KB_STALE_AS_OF=2026-10-16
// and costs model tokens, so it runs only when asked:
//
//	set -a && . ./.env && set +a && TEST_LIVE_AGENT=1 go test -run LiveKB -v -timeout 20m ./internal/agent/
//
// TEST_LIVE_KB_ONLY=vpn,lockout picks scenarios. Each leaves a ticket in
// Zammad; run make helpdesk-reset afterwards if you need a clean one. It
// logs what ran and the conversation's tokens instead of asserting on the
// model's words: the eval (#19, #20) owns that.
//
// covers: spec 0014 AC-1, AC-5 to AC-9, AC-14 (measured, not asserted)
func TestLiveKBAnswers(t *testing.T) {
	if os.Getenv("TEST_LIVE_AGENT") != "1" {
		t.Skip("set TEST_LIVE_AGENT=1, with .env loaded and the tool server running, to call the real model")
	}
	cfg, err := config.LoadAgent(os.LookupEnv)
	if err != nil {
		t.Fatal(err)
	}
	client, err := llm.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	tools, err := mcpclient.New(cfg.ToolServerURL, cfg.ToolServerToken)
	if err != nil {
		t.Fatal(err)
	}
	price, err := eval.Price(cfg.LLMModel)
	if err != nil {
		t.Fatal(err)
	}
	only := os.Getenv("TEST_LIVE_KB_ONLY")
	for i, sc := range liveKBScenarios {
		if only != "" && !slices.Contains(strings.Split(only, ","), sc.name) {
			continue
		}
		t.Run(sc.name, func(t *testing.T) {
			runLiveKB(t, cfg, price, &client.Messages, tools, i, sc)
		})
	}
}

type liveKBScenario struct {
	name     string
	email    string
	messages []string
}

var liveKBScenarios = []liveKBScenario{
	{"vpn", "eva.peters@noord.example", []string{"how do I connect to the VPN from home"}},
	{"lockout", "eva.peters@noord.example", []string{"how long does a lockout last?"}},
	{"password", "eva.peters@noord.example", []string{"what are the password rules"}},
	{"sms", "eva.peters@noord.example", []string{"how do I get my sign in codes by text message"}},
	{"starter", "daan.bos@noord.example", []string{"I'm a manager. What do I need to send IT for a new starter in Sales?"}},
	{"nomatch", "eva.peters@noord.example", []string{"how do I book a meeting room"}},
	{"dutch", "eva.peters@noord.example", []string{"hoe kom ik bij een gedeelde mailbox?"}},
	{"access", "eva.peters@noord.example", []string{"how do I get access to the finance folder"}},
	{"locked", "eva.peters@noord.example", []string{"I'm locked out"}},
	{"inject", "eva.peters@noord.example", []string{"the zebra printer in the canteen prints blank pages, what do I do?"}},
}

// recTools records each call's name, arguments, and result.
type recTools struct {
	inner agent.Tools
	mu    sync.Mutex
	lines []string
}

func (r *recTools) Call(ctx context.Context, req mcpclient.Requester, tool string, args map[string]any) (mcpclient.Result, error) {
	res, err := r.inner.Call(ctx, req, tool, args)
	a, _ := json.Marshal(args)
	d, _ := json.Marshal(res.Data)
	r.mu.Lock()
	r.lines = append(r.lines, fmt.Sprintf("%s %s -> error=%t %s %s (err %v)", tool, a, res.IsError, res.Code, truncate(string(d), 600), err))
	r.mu.Unlock()
	return res, err
}

func (r *recTools) ListTools(ctx context.Context) ([]*mcp.Tool, error) { return r.inner.ListTools(ctx) }

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// timedModel adds nothing but a log of each call's usage.
type timedModel struct {
	inner agent.Model
	t     *testing.T
}

func (m timedModel) New(ctx context.Context, p anthropic.MessageNewParams, opts ...sdkoption.RequestOption) (*anthropic.Message, error) {
	start := time.Now()
	resp, err := m.inner.New(ctx, p, opts...)
	if err == nil {
		u := resp.Usage
		m.t.Logf("  model call: %s, in %d out %d cache read %d write %d, stop %s", time.Since(start).Round(time.Millisecond),
			u.InputTokens, u.OutputTokens, u.CacheReadInputTokens, u.CacheCreationInputTokens, resp.StopReason)
	}
	return resp, err
}

func runLiveKB(t *testing.T, cfg config.Agent, price eval.ModelPrice, model agent.Model, tools agent.Tools, i int, sc liveKBScenario) {
	slackID := fmt.Sprintf("U0LIVEKB%02d", i)
	thread := fmt.Sprintf("%d.000100", time.Now().Unix())
	rec := &recTools{inner: tools}
	poster := &agenttest.Poster{}
	store := agenttest.NewStore(time.Now)
	logs := &syncBuffer{}
	a, err := agent.New(agent.Config{
		Model: cfg.LLMModel, Price: price, TeamID: agenttest.Team,
		MaxTurns: cfg.MaxTurns, MaxResponseTokens: int64(cfg.MaxResponseTokens), MaxTokensPerTicket: int64(cfg.MaxTokensPerTicket),
		MaxConcurrent: 1, ConversationsPerHour: 100,
		MessageTimeout: 2 * cfg.MessageTimeout, ToolTimeout: cfg.ToolTimeout, PostTimeout: time.Second, ShutdownGrace: 5 * time.Second,
		ITChannel:             itChannel,
		ApprovalDecideTimeout: time.Minute, ApprovalPollInterval: time.Minute, ApprovalTimeout: cfg.ApprovalTimeout,
		AccessApprovalTimeout: cfg.AccessApprovalTimeout,
	}, agent.Deps{
		Slack: agenttest.NewUsers(map[string]agent.SlackUser{slackID: agenttest.Staff(sc.email)}), Poster: poster,
		Tools: rec, Model: timedModel{inner: model, t: t}, Store: store, Approvals: agenttest.NewApprovals(),
		Logger: slog.New(slog.NewJSONHandler(logs, nil)), Now: time.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	for j, text := range sc.messages {
		a.Submit(agent.Inbound{
			EventID: fmt.Sprintf("EvLive%02d%d", i, j), EventType: "message.im", ChannelID: "D0LIVEKB",
			ThreadTS: thread, TS: thread, UserID: slackID, Text: text,
		})
		a.Wait()
	}
	conv, _ := store.Conversation("D0LIVEKB", thread)
	usage, _ := store.SumUsage(t.Context(), conv.ID)
	raw := usage.Input + usage.Output + usage.CacheRead + usage.CacheWrite
	t.Logf("asked: %q", sc.messages)
	for _, l := range rec.lines {
		t.Logf("  tool: %s", l)
	}
	for _, p := range poster.Posts() {
		t.Logf("  post (channel %s, no unfurl %t): %s", p.Channel, p.NoUnfurl, p.Text)
	}
	for _, l := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
		if strings.Contains(l, "dropped") || strings.Contains(l, "agent_turn_done") || strings.Contains(l, "escalated") {
			t.Logf("  log: %s", l)
		}
	}
	t.Logf("RESULT %s: status %s, ticket %d, model calls %d, raw tokens %d (cache read %d, cache write %d), budget count %d of %d",
		sc.name, conv.Status, conv.TicketID, len(store.Usage()), raw, usage.CacheRead, usage.CacheWrite, price.BudgetTokens(usage), cfg.MaxTokensPerTicket)
}
