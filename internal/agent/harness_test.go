package agent_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/anthropics/anthropic-sdk-go"

	"github.com/toyinogun/AIITsupport/eval"
	"github.com/toyinogun/AIITsupport/internal/agent"
	"github.com/toyinogun/AIITsupport/internal/agent/agenttest"
)

const (
	janSlack  = "U0JAN"
	janEmail  = "jan.devries@noord.example"
	janUser   = "jan.devries"
	janDM     = "D0JAN"
	firstTS   = "1792400000.000100"
	modelName = "claude-sonnet-5-5"
)

// now is the fake clock: 20 seconds after firstTS.
var now = time.Unix(1792400020, 0).UTC()

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

type harness struct {
	t      *testing.T
	a      *agent.Agent
	cfg    agent.Config
	users  *agenttest.Users
	poster *agenttest.Poster
	tools  *agenttest.Tools
	model  *agenttest.Model
	store  *agenttest.Store
	logs   *syncBuffer
}

func testConfig(t *testing.T) agent.Config {
	t.Helper()
	price, err := eval.Price(modelName)
	if err != nil {
		t.Fatal(err)
	}
	return agent.Config{
		Model: modelName, Price: price, TeamID: agenttest.Team,
		MaxTurns: 8, MaxResponseTokens: 1024, MaxTokensPerTicket: 30000,
		MaxConcurrent: 4, ConversationsPerHour: 10,
		MessageTimeout: 3 * time.Second, ToolTimeout: time.Second,
		PostTimeout: time.Second, ShutdownGrace: 2 * time.Second,
	}
}

// option changes the harness before the agent is built.
type option func(*harness)

func withConfig(f func(*agent.Config)) option { return func(h *harness) { f(&h.cfg) } }
func withTools(t *agenttest.Tools) option     { return func(h *harness) { h.tools = t } }

// newHarness builds an agent with a locked Jan, the default tools, and the
// scripted model.
func newHarness(t *testing.T, model *agenttest.Model, opts ...option) *harness {
	t.Helper()
	h := &harness{
		t: t, cfg: testConfig(t),
		users:  agenttest.NewUsers(map[string]agent.SlackUser{janSlack: agenttest.Staff(janEmail)}),
		poster: &agenttest.Poster{},
		tools:  agenttest.NewTools(janUser, true),
		model:  model,
		store:  agenttest.NewStore(func() time.Time { return now }),
		logs:   &syncBuffer{},
	}
	for _, o := range opts {
		o(h)
	}
	a, err := agent.New(h.cfg, agent.Deps{
		Slack: h.users, Poster: h.poster, Tools: h.tools, Model: h.model, Store: h.store,
		Logger: slog.New(slog.NewJSONHandler(h.logs, nil)),
		Now:    func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	h.a = a
	return h
}

// dm is a DM from Jan that starts its own thread.
func dm(eventID, text string) agent.Inbound {
	return agent.Inbound{
		EventID: eventID, EventType: "message.im", ChannelID: janDM,
		ThreadTS: firstTS, TS: firstTS, UserID: janSlack, Text: text,
	}
}

// send submits messages and waits until the agent is idle.
func (h *harness) send(in ...agent.Inbound) {
	h.t.Helper()
	for _, m := range in {
		h.a.Submit(m)
	}
	h.a.Wait()
}

func (h *harness) conv() agent.Conversation {
	h.t.Helper()
	c, ok := h.store.Conversation(janDM, firstTS)
	if !ok {
		h.t.Fatal("no conversation for the thread")
	}
	return c
}

func (h *harness) wantPosts(want ...string) {
	h.t.Helper()
	got := h.poster.Texts()
	if strings.Join(got, "\n---\n") != strings.Join(want, "\n---\n") {
		h.t.Errorf("posts:\n%q\nwant:\n%q", got, want)
	}
}

func (h *harness) wantTools(want ...string) {
	h.t.Helper()
	if got := h.tools.Names(); strings.Join(got, ",") != strings.Join(want, ",") {
		h.t.Errorf("tool calls = %v, want %v", got, want)
	}
}

func (h *harness) wantStatus(want string) {
	h.t.Helper()
	if got := h.conv().Status; got != want {
		h.t.Errorf("status = %s, want %s", got, want)
	}
}

// noteOf returns the note of the last ticket_update call.
func (h *harness) lastNote() string {
	h.t.Helper()
	calls := h.tools.Calls()
	for i := len(calls) - 1; i >= 0; i-- {
		if calls[i].Tool == "ticket_update" {
			note, _ := calls[i].Args["note"].(string)
			return note
		}
	}
	return ""
}

// blocks decodes a stored message.
func blocks(t *testing.T, m agent.StoredMessage) []anthropic.ContentBlockParamUnion {
	t.Helper()
	var out []anthropic.ContentBlockParamUnion
	if err := json.Unmarshal(m.Content, &out); err != nil {
		t.Fatalf("stored content %s: %v", m.Content, err)
	}
	return out
}

func answerWith(text, footer string) string { return text + "\n\n" + footer }
