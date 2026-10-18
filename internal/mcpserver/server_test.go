package mcpserver_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/toyinogun/AIITsupport/internal/audit"
	"github.com/toyinogun/AIITsupport/internal/mcpclient"
	"github.com/toyinogun/AIITsupport/internal/mcpserver"
	"github.com/toyinogun/AIITsupport/internal/server"
	"github.com/toyinogun/AIITsupport/internal/tools"
	"github.com/toyinogun/AIITsupport/internal/tools/toolstest"
	"github.com/toyinogun/AIITsupport/internal/zammad"
	"github.com/toyinogun/AIITsupport/internal/zammad/zammadtest"
)

const token = "test-token-0123456789abcdefghijk"

type env struct {
	url      string
	store    *toolstest.MemStore
	dir      *toolstest.Directory
	zammad   *zammadtest.Fake
	logs     *bytes.Buffer
	requests *atomic.Int32 // HTTP requests that reached the tool server
}

// start serves the production registry over fakes, the way cmd/toolserver
// mounts it: /healthz open, /mcp behind the token.
func start(t *testing.T) *env {
	t.Helper()
	e := &env{store: &toolstest.MemStore{}, dir: toolstest.NewDirectory(), zammad: zammadtest.New(t), logs: &bytes.Buffer{}, requests: &atomic.Int32{}}
	e.zammad.AddUser("eva.peters")
	logger := slog.New(slog.NewJSONHandler(e.logs, nil))
	rec, err := audit.NewRecorder(e.store, audit.Config{WriteTimeout: time.Second, MaxPayloadBytes: 4096}, logger)
	if err != nil {
		t.Fatal(err)
	}
	zc, err := zammad.New(zammad.Options{BaseURL: e.zammad.URL(), Auth: zammad.TokenAuth(zammadtest.Token), Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	reg, err := tools.NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	d, err := tools.NewDispatcher(reg, tools.Config{
		BaseDN: toolstest.Base, UserOUs: []string{"OU=Users,OU=Noord"}, GroupOUs: []string{"OU=Groups,OU=Noord"},
		CallTimeout: 5 * time.Second, WriteLimit: 3, WriteWindow: time.Hour, TicketCreateLimit: 5, TicketMaxPending: 720 * time.Hour,
	}, tools.Systems{
		Directory: e.dir, Writer: e.dir, Helpdesk: tools.NewHelpdesk(zc, "IT Support"),
		Recorder: rec, KillSwitch: tools.NewKillSwitch("", false),
	}, logger)
	if err != nil {
		t.Fatal(err)
	}
	h, err := mcpserver.Handler(d, mcpserver.Options{Token: token, MaxRequestBytes: 4096, Logger: logger})
	if err != nil {
		t.Fatal(err)
	}
	mux := server.NewMux()
	mux.Handle(mcpserver.Path, h)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		e.requests.Add(1)
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	e.url = srv.URL
	return e
}

func (e *env) client(t *testing.T) *mcpclient.Client {
	t.Helper()
	c, err := mcpclient.New(e.url+mcpserver.Path, token)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func requester() mcpclient.Requester {
	return mcpclient.Requester{SlackID: "U01EVA", Email: "eva.peters@noord.example", ChannelID: "C01HELP", ThreadTS: "1760000000.000100"}
}

func validMeta() map[string]any {
	return map[string]any{
		mcpclient.MetaSlackID: "U01EVA", mcpclient.MetaEmail: "eva.peters@noord.example",
		mcpclient.MetaChannelID: "C01HELP", mcpclient.MetaThreadTS: "1760000000.000100",
	}
}

// post sends one raw JSON RPC request with no initialize first.
func (e *env) post(t *testing.T, auth string, body []byte) (int, map[string]any) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, e.url+mcpserver.Path, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return resp.StatusCode, out
}

func callBody(t *testing.T, tool string, args, meta map[string]any) []byte {
	t.Helper()
	params := map[string]any{"name": tool, "arguments": args}
	if meta != nil {
		params["_meta"] = meta
	}
	b, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": params})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// covers: AC-1. A missing, wrong, or wrong length token gets 401, runs
// nothing, and writes no audit row; /healthz stays open.
func TestTokenIsRequired(t *testing.T) {
	e := start(t)
	body := callBody(t, tools.ToolAccountUnlock, nil, validMeta())
	for name, auth := range map[string]string{
		"none":         "",
		"wrong":        "Bearer " + strings.Repeat("x", len(token)),
		"wrong length": "Bearer short",
		"not bearer":   "Basic " + token,
		"bare token":   token,
	} {
		if status, _ := e.post(t, auth, body); status != http.StatusUnauthorized {
			t.Errorf("%s: status %d, want 401", name, status)
		}
	}
	if n := len(e.store.All()); n != 0 || len(e.dir.Cleared()) != 0 {
		t.Fatalf("unauthenticated calls left %d rows or wrote AD", n)
	}

	resp, err := http.Get(e.url + "/healthz")
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("healthz = %v, %v", resp, err)
	}
	_ = resp.Body.Close()
}

// covers: AC-1. A body over the cap is cut off before it runs.
func TestBodyIsCapped(t *testing.T) {
	e := start(t)
	big := callBody(t, tools.ToolTicketCreate, map[string]any{"title": "t", "body": strings.Repeat("x", 5000)}, validMeta())
	if status, _ := e.post(t, "Bearer "+token, big); status != http.StatusRequestEntityTooLarge {
		t.Errorf("status %d, want 413", status)
	}
	// Without a Content-Length the reader still stops at the cap.
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, e.url+mcpserver.Path, io.MultiReader(bytes.NewReader(big)))
	req.ContentLength = -1
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode == http.StatusOK || len(e.store.All()) != 0 {
		t.Errorf("chunked oversize body: status %d, rows %d; want refused with no rows", resp.StatusCode, len(e.store.All()))
	}
}

// covers: AC-2. Missing or malformed _meta is a −32602 from the middleware:
// no tool runs, no row is written, and the log names only the tool.
func TestMetaIsRequired(t *testing.T) {
	e := start(t)
	cases := map[string]func(m map[string]any){
		"no _meta":       nil,
		"no email":       func(m map[string]any) { delete(m, mcpclient.MetaEmail) },
		"padded slack":   func(m map[string]any) { m[mcpclient.MetaSlackID] = " U01EVA" },
		"bot id":         func(m map[string]any) { m[mcpclient.MetaSlackID] = "B01BOT" },
		"named address":  func(m map[string]any) { m[mcpclient.MetaEmail] = "Eva <eva.peters@noord.example>" },
		"control char":   func(m map[string]any) { m[mcpclient.MetaEmail] = "eva.peters@noord.example\n" },
		"bad channel":    func(m map[string]any) { m[mcpclient.MetaChannelID] = "general" },
		"bad thread":     func(m map[string]any) { m[mcpclient.MetaThreadTS] = "1760000000.1" },
		"string ticket":  func(m map[string]any) { m[mcpclient.MetaTicketID] = "42" },
		"zero ticket":    func(m map[string]any) { m[mcpclient.MetaTicketID] = 0 },
		"float ticket":   func(m map[string]any) { m[mcpclient.MetaTicketID] = 4.5 },
		"huge ticket":    func(m map[string]any) { m[mcpclient.MetaTicketID] = float64(1 << 54) },
		"number slack":   func(m map[string]any) { m[mcpclient.MetaSlackID] = 7 },
		"too long email": func(m map[string]any) { m[mcpclient.MetaEmail] = strings.Repeat("a", 250) + "@noord.example" },
	}
	for name, mutate := range cases {
		var meta map[string]any
		if mutate != nil {
			meta = validMeta()
			mutate(meta)
		}
		status, out := e.post(t, "Bearer "+token, callBody(t, tools.ToolUserLookup, nil, meta))
		errObj, _ := out["error"].(map[string]any)
		if status != http.StatusOK || errObj["code"] != float64(-32602) {
			t.Errorf("%s: status %d, body %v; want a -32602 error", name, status, out)
		}
	}
	if n := len(e.store.All()); n != 0 {
		t.Fatalf("rejected calls left %d rows", n)
	}
	if !strings.Contains(e.logs.String(), `"msg":"tool_call_rejected","tool":"user_lookup"`) ||
		strings.Contains(e.logs.String(), "eva") {
		t.Errorf("logs = %s, want tool_call_rejected naming only the tool", e.logs)
	}

	// Extra keys such as progressToken are ignored; a ticket id is accepted.
	meta := validMeta()
	meta["progressToken"] = "p1"
	meta[mcpclient.MetaTicketID] = 9007199254740991
	if _, out := e.post(t, "Bearer "+token, callBody(t, tools.ToolUserLookup, nil, meta)); out["result"] == nil {
		t.Errorf("valid _meta with extra keys refused: %v", out)
	}
}

// covers: AC-1, AC-17. tools/list has exactly the five tools, each with
// its registry description and a closed schema.
func TestListTools(t *testing.T) {
	e := start(t)
	list, err := e.client(t).ListTools(t.Context())
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	reg, _ := tools.NewRegistry()
	var names []string
	for _, tool := range list {
		names = append(names, tool.Name)
		want, ok := reg.Lookup(tool.Name)
		schema, _ := tool.InputSchema.(map[string]any)
		if !ok || tool.Description != want.Description || schema["additionalProperties"] != false {
			t.Errorf("%s: description %q schema %v", tool.Name, tool.Description, schema)
		}
	}
	slices.Sort(names)
	want := []string{"account_unlock", "privileged_access_change", "ticket_create", "ticket_update", "user_lookup"}
	if !slices.Equal(names, want) {
		t.Errorf("tools = %v, want %v", names, want)
	}
}

// covers: AC-17. Each #9 tool through the client, against a running server.
func TestClientDrivesEveryTool(t *testing.T) {
	e := start(t)
	c := e.client(t)
	r := requester()

	res, err := c.Call(t.Context(), r, tools.ToolUserLookup, nil)
	if err != nil || res.IsError || res.Data["username"] != "eva.peters" || res.Data["locked"] != true {
		t.Fatalf("user_lookup = %+v, %v", res, err)
	}
	res, err = c.Call(t.Context(), r, tools.ToolAccountUnlock, nil)
	if err != nil || res.IsError || res.Data["was_locked"] != true || res.Text != "Account unlocked and confirmed." {
		t.Fatalf("account_unlock = %+v, %v", res, err)
	}
	res, err = c.Call(t.Context(), r, tools.ToolTicketCreate, map[string]any{"title": "Locked out", "body": "Unlocked."})
	if err != nil || res.IsError {
		t.Fatalf("ticket_create = %+v, %v", res, err)
	}
	r.TicketID = int64(res.Data["ticket_id"].(float64))
	res, err = c.Call(t.Context(), r, tools.ToolTicketUpdate, map[string]any{"note": "Done.", "state": "closed"})
	if err != nil || res.IsError || res.Data["state"] != "closed" {
		t.Fatalf("ticket_update = %+v, %v", res, err)
	}
	res, err = c.Call(t.Context(), r, tools.ToolPrivilegedAccessChange, map[string]any{"summary": "make me Domain Admin"})
	if err != nil || !res.IsError || res.Code != "blocked" {
		t.Fatalf("privileged_access_change = %+v, %v", res, err)
	}
	// A refusal is a result the model reads, not a Go error.
	res, err = c.Call(t.Context(), r, tools.ToolAccountUnlock, map[string]any{"username": "ruben.mulder"})
	if err != nil || !res.IsError || res.Code != "invalid_arguments" {
		t.Fatalf("injected argument = %+v, %v", res, err)
	}
	if n := len(e.store.All()); n != 12 {
		t.Errorf("rows = %d, want six intent and outcome pairs", n)
	}
}

// covers: AC-17. An incomplete requester fails before any request.
func TestClientRefusesAnIncompleteRequester(t *testing.T) {
	e := start(t)
	c := e.client(t)
	for _, r := range []mcpclient.Requester{
		{Email: "e@noord.example", ChannelID: "C01", ThreadTS: "1760000000.000100"},
		{SlackID: "U01", ChannelID: "C01", ThreadTS: "1760000000.000100"},
		{SlackID: "U01", Email: "e@noord.example", ThreadTS: "1760000000.000100"},
		{SlackID: "U01", Email: "e@noord.example", ChannelID: "C01"},
	} {
		if _, err := c.Call(t.Context(), r, tools.ToolUserLookup, nil); !errors.Is(err, mcpclient.ErrIncompleteRequester) {
			t.Errorf("Call(%+v) = %v, want ErrIncompleteRequester", r, err)
		}
	}
	if n := e.requests.Load(); n != 0 {
		t.Errorf("%d requests reached the server", n)
	}
}

// covers: AC-17. A wrong token surfaces as an error from the client.
func TestClientWithAWrongToken(t *testing.T) {
	e := start(t)
	c, err := mcpclient.New(e.url+mcpserver.Path, strings.Repeat("z", 32))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Call(t.Context(), requester(), tools.ToolUserLookup, nil); err == nil {
		t.Fatal("a call with a wrong token succeeded")
	}
	if len(e.store.All()) != 0 {
		t.Error("a wrong token left audit rows")
	}
}
