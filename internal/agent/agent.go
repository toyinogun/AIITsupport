// Package agent is the Slack facing agent loop (spec 0008). Agent code
// decides who is asking (the Slack profile, then the tool server's
// user_lookup) before the model runs; the model then drives the tool
// server's tools one call at a time inside hard limits, and code owns every
// failure. It never holds a client system credential: the tool server is
// reached only through internal/mcpclient.
package agent

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/trace"

	"github.com/toyinogun/AIITsupport/eval"
)

// queueCap is how many messages may wait behind a thread's running turn (AC-13).
const queueCap = 10

// sweepSlack is added to the turn deadline and one tool call to make the
// stale window (AC-14).
const sweepSlack = 30 * time.Second

var tracer = otel.Tracer("github.com/toyinogun/AIITsupport/internal/agent")

// Config holds the agent's limits, all from internal/config.
type Config struct {
	Model                string // LLM_MODEL
	Price                eval.ModelPrice
	TeamID               string // the workspace, from auth.test
	MaxTurns             int    // AGENT_MAX_TURNS
	MaxResponseTokens    int64  // AGENT_MAX_RESPONSE_TOKENS
	MaxTokensPerTicket   int64  // AGENT_MAX_TOKENS_PER_TICKET
	MaxConcurrent        int    // AGENT_MAX_CONCURRENT
	ConversationsPerHour int64  // AGENT_CONVERSATIONS_PER_HOUR
	MessageTimeout       time.Duration
	ToolTimeout          time.Duration
	PostTimeout          time.Duration // the listener's reply timeout
	ShutdownGrace        time.Duration
	ITChannel            string // SLACK_IT_CHANNEL: where escalations are posted (spec 0009)
}

func (c Config) check() error {
	var errs []error
	if c.Model == "" || c.TeamID == "" || c.ITChannel == "" {
		errs = append(errs, errors.New("agent: needs a model, the workspace team id, and the IT channel"))
	}
	if c.MaxTurns < 1 || c.MaxResponseTokens < 1 || c.MaxTokensPerTicket < 1 || c.MaxConcurrent < 1 || c.ConversationsPerHour < 1 {
		errs = append(errs, errors.New("agent: every limit must be at least 1"))
	}
	if c.MessageTimeout <= 0 || c.ToolTimeout <= 0 || c.PostTimeout <= 0 || c.ShutdownGrace <= 0 {
		errs = append(errs, errors.New("agent: every timeout must be positive"))
	}
	return errors.Join(errs...)
}

// staleWindow is how long an open conversation must sit untouched before
// the sweep treats it as interrupted.
func (c Config) staleWindow() time.Duration {
	return c.MessageTimeout + c.ToolTimeout + sweepSlack
}

// Deps are the systems the agent talks to; tests pass the fakes in agenttest.
type Deps struct {
	Slack  SlackUsers
	Poster Poster
	Tools  Tools
	Model  Model
	Store  Store
	Logger *slog.Logger
	Now    func() time.Time // defaults to time.Now
}

// Agent runs one turn per accepted Slack message.
type Agent struct {
	cfg  Config
	deps Deps

	// workCtx is cancelled only when AGENT_SHUTDOWN_GRACE runs out, never by
	// the signal: every turn and detached call derives from it.
	workCtx    context.Context
	cancelWork context.CancelFunc
	sem        chan struct{}
	stopCh     chan struct{}
	toolDefs   toolDefs

	mu       sync.Mutex
	workers  map[threadKey]*worker
	stopping bool
	wg       sync.WaitGroup
}

type threadKey struct{ channel, thread string }

type worker struct{ queue []Inbound }

// New checks cfg and deps and returns an agent ready for Submit and Run.
func New(cfg Config, deps Deps) (*Agent, error) {
	if err := cfg.check(); err != nil {
		return nil, err
	}
	if deps.Slack == nil || deps.Poster == nil || deps.Tools == nil || deps.Model == nil || deps.Store == nil || deps.Logger == nil {
		return nil, errors.New("agent: needs Slack, a poster, the tools, a model, a store, and a logger")
	}
	if deps.Now == nil {
		deps.Now = time.Now
	}
	workCtx, cancel := context.WithCancel(context.Background())
	return &Agent{
		cfg: cfg, deps: deps,
		workCtx: workCtx, cancelWork: cancel,
		sem:     make(chan struct{}, cfg.MaxConcurrent),
		stopCh:  make(chan struct{}),
		workers: map[threadKey]*worker{},
	}, nil
}

// Submit hands one accepted message to its thread's worker. It never blocks:
// the listener calls it on its event loop.
func (a *Agent) Submit(in Inbound) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.stopping {
		// Not tracked: Run may already be waiting for the last workers.
		go a.post(context.Background(), in, TextShutdown, "shutdown")
		return
	}
	// The queue holds every message of the thread that is not running yet,
	// including one waiting for a concurrency slot (AC-13).
	if in.EventType == EventAppMention {
		a.wg.Go(func() { a.redirect(in) })
		return
	}
	key := threadKey{in.ChannelID, in.ThreadTS}
	if w, ok := a.workers[key]; ok {
		if len(w.queue) >= queueCap {
			a.deps.Logger.Warn("agent_thread_busy", "event_id", in.EventID)
			a.wg.Go(func() { a.post(a.workCtx, in, TextBusy, "busy") })
			return
		}
		w.queue = append(w.queue, in)
		return
	}
	w := &worker{queue: []Inbound{in}}
	a.workers[key] = w
	a.wg.Go(func() { a.runWorker(key, w) })
}

// runWorker runs a thread's messages one at a time, in arrival order, and
// exits when the queue is empty (AC-13). The head stays queued until it holds
// a concurrency slot, so the queue cap counts it; on shutdown stop() answers
// everything still queued.
func (a *Agent) runWorker(key threadKey, w *worker) {
	for {
		a.mu.Lock()
		if len(w.queue) == 0 {
			delete(a.workers, key)
			a.mu.Unlock()
			return
		}
		a.mu.Unlock()

		select {
		case a.sem <- struct{}{}:
		case <-a.stopCh:
			continue // stop() emptied the queue and answered it
		}
		a.mu.Lock()
		if a.stopping || len(w.queue) == 0 {
			a.mu.Unlock()
			<-a.sem
			continue
		}
		in := w.queue[0]
		w.queue = w.queue[1:]
		a.mu.Unlock()
		a.runTurn(in)
		<-a.sem
	}
}

// Run waits for ctx to end, runs the second sweep after the stale window,
// then drains: waiting messages get the shutdown text, and running turns get
// AGENT_SHUTDOWN_GRACE before their contexts are cancelled.
func (a *Agent) Run(ctx context.Context) error {
	second := time.NewTimer(a.cfg.staleWindow())
	defer second.Stop()
wait:
	for {
		select {
		case <-ctx.Done():
			break wait
		case <-second.C:
			a.mu.Lock()
			if !a.stopping {
				a.wg.Go(func() { a.Sweep(a.workCtx) })
			}
			a.mu.Unlock()
		}
	}
	a.stop()

	done := make(chan struct{})
	go func() { a.wg.Wait(); close(done) }()
	grace := time.NewTimer(a.cfg.ShutdownGrace)
	defer grace.Stop()
	select {
	case <-done:
	case <-grace.C:
		a.cancelWork()
		<-done
	}
	a.cancelWork()
	return nil
}

// stop refuses new work and answers every waiting message with the shutdown text.
func (a *Agent) stop() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.stopping {
		return
	}
	a.stopping = true
	close(a.stopCh)
	for _, w := range a.workers {
		for _, in := range w.queue {
			a.wg.Go(func() { a.post(a.workCtx, in, TextShutdown, "shutdown") })
		}
		w.queue = nil
	}
}

// running reports whether this process has a worker for the thread.
func (a *Agent) running(channel, thread string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	_, ok := a.workers[threadKey{channel, thread}]
	return ok
}

// redirect answers a channel @mention and does nothing else (AC-1).
func (a *Agent) redirect(in Inbound) {
	start := a.deps.Now()
	a.post(a.workCtx, in, TextChannelRedirect, "channel_redirect")
	a.logTurnDone(context.Background(), in, OutcomeRedirected, 0, 0, start)
}

// detached returns a context bounded by d that ignores the turn deadline
// but keeps the turn's span, and still ends when the shutdown grace runs out.
func (a *Agent) detached(parent context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	ctx := trace.ContextWithSpan(a.workCtx, trace.SpanFromContext(parent))
	return context.WithTimeout(ctx, d)
}

// post sends text in the message's thread on its own context with the
// reply timeout. It survives the shutdown grace too, so a turn cut short
// still tells the user something. A failed post is logged, never retried.
func (a *Agent) post(parent context.Context, in Inbound, text, kind string) {
	a.postTo(parent, in.ChannelID, in.ThreadTS, in.EventID, kind, text)
}

// postTo sends text to a channel, in a thread unless thread is empty.
func (a *Agent) postTo(parent context.Context, channel, thread, eventID, kind, text string) {
	ctx := trace.ContextWithSpan(context.WithoutCancel(a.workCtx), trace.SpanFromContext(parent))
	ctx, cancel := context.WithTimeout(ctx, a.cfg.PostTimeout)
	defer cancel()
	if err := a.deps.Poster.Reply(ctx, channel, thread, text); err != nil {
		a.deps.Logger.Warn("agent_post_failed", "event_id", eventID, "kind", kind, "cause", cause(err))
	}
}
