package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/toyinogun/AIITsupport/internal/mcpclient"
)

func stored(role, content string) StoredMessage {
	return StoredMessage{Role: role, Content: json.RawMessage(content)}
}

// covers: AC-5. The history check accepts paired tool calls and refuses
// unpaired ones, purged content, and a history that starts with the assistant.
func TestBuildHistory(t *testing.T) {
	user := stored("user", `[{"type":"text","text":"hi"}]`)
	use := stored("assistant", `[{"type":"thinking","thinking":"","signature":"s"},{"type":"tool_use","id":"t1","name":"account_unlock","input":{}}]`)
	result := stored("user", `[{"type":"tool_result","tool_use_id":"t1","content":[{"type":"text","text":"ok"}]}]`)
	wrong := stored("user", `[{"type":"tool_result","tool_use_id":"t9","content":[{"type":"text","text":"ok"}]}]`)
	final := stored("assistant", `[{"type":"text","text":"done"}]`)

	if msgs, err := buildHistory([]StoredMessage{user, use, result, final}); err != nil || len(msgs) != 4 {
		t.Errorf("valid history: %d messages, %v", len(msgs), err)
	}
	bad := map[string][]StoredMessage{
		"unanswered":      {user, use},
		"answered later":  {user, use, user, result},
		"wrong id":        {user, use, wrong},
		"purged":          {{Role: "user"}},
		"assistant first": {final, user},
		"empty":           {},
		"bad role":        {stored("system", `[{"type":"text","text":"x"}]`)},
		"not blocks":      {stored("user", `"just text"`)},
	}
	for name, msgs := range bad {
		if _, err := buildHistory(msgs); err == nil {
			t.Errorf("%s: buildHistory accepted it", name)
		}
	}
}

// Stored blocks round trip through the SDK param types unchanged.
func TestBlocksRoundTrip(t *testing.T) {
	raw, err := encodeBlocks(userText("I'm locked out"))
	if err != nil || string(raw) != `[{"text":"I'm locked out","type":"text"}]` {
		t.Fatalf("encoded = %s, %v", raw, err)
	}
	msgs, err := buildHistory([]StoredMessage{{Role: "user", Content: raw}})
	if err != nil || msgs[0].Content[0].OfText.Text != "I'm locked out" {
		t.Errorf("decoded = %+v, %v", msgs, err)
	}
}

func TestToolResultText(t *testing.T) {
	if got := toolResultText(mcpclient.Result{Text: "Ticket created.", Data: map[string]any{"ticket_id": 42.0}}); got != "Ticket created.\n{\"ticket_id\":42}" {
		t.Errorf("with data: %q", got)
	}
	if got := toolResultText(mcpclient.Result{Text: "x"}); got != "x" {
		t.Errorf("without data: %q", got)
	}
}

// covers: spec 0008, The model loop. The schema passes through unchanged.
func TestToToolParam(t *testing.T) {
	tool := &mcp.Tool{Name: "ticket_create", Description: "Open a ticket.", InputSchema: map[string]any{
		"type": "object", "additionalProperties": false, "required": []any{"title"},
		"properties": map[string]any{"title": map[string]any{"type": "string", "maxLength": 200}},
	}}
	p, err := toToolParam(tool)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(p)
	for _, want := range []string{`"name":"ticket_create"`, `"description":"Open a ticket."`, `"additionalProperties":false`, `"required":["title"]`, `"maxLength":200`, `"type":"object"`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("%s lacks %s", raw, want)
		}
	}
}

func TestRequesterBlock(t *testing.T) {
	at := time.Date(2026, 10, 19, 9, 0, 0, 0, time.UTC)
	data := map[string]any{"username": "jan.devries", "display_name": "Jan de Vries", "department": "Operations", "enabled": true, "locked": true, "lock_ends_at": nil}
	b := requesterBlock(data, at)
	for _, want := range []string{"verified by agent code", "Username: jan.devries", "Account enabled: yes", "Account locked: yes", "Lock ends: until IT clears it", "Current time (UTC): 2026-10-19T09:00:00Z"} {
		if !strings.Contains(b, want) {
			t.Errorf("block lacks %q:\n%s", want, b)
		}
	}
	data["locked"] = false
	if b := requesterBlock(data, at); strings.Contains(b, "Lock ends") || !strings.Contains(b, "Account locked: no") {
		t.Errorf("unlocked block:\n%s", b)
	}
}

func TestStatusChangeFor(t *testing.T) {
	if ch := statusChangeFor("ticket_create", mcpclient.Result{Data: map[string]any{"ticket_id": 7.0}}, 0); ch == nil || ch.TicketID != 7 || ch.Status != StatusOpen {
		t.Errorf("create: %+v", ch)
	}
	if ch := statusChangeFor("ticket_update", mcpclient.Result{Data: map[string]any{"state": "closed"}}, 7); ch == nil || ch.Status != StatusResolved {
		t.Errorf("close: %+v", ch)
	}
	for _, r := range []mcpclient.Result{
		{Data: map[string]any{"state": "open"}},
		{Data: map[string]any{"state": "closed"}, IsError: true},
	} {
		if ch := statusChangeFor("ticket_update", r, 7); ch != nil {
			t.Errorf("%+v changed status: %+v", r, ch)
		}
	}
	// spec 0009 AC-8: an ok escalate ends the conversation, storing its
	// ticket only when the conversation has none.
	esc := mcpclient.Result{Data: map[string]any{"ticket_id": 9.0}}
	if ch := statusChangeFor("escalate", esc, 0); ch == nil || ch.Status != StatusEscalated || ch.TicketID != 9 {
		t.Errorf("escalate with no ticket: %+v", ch)
	}
	if ch := statusChangeFor("escalate", esc, 7); ch == nil || ch.Status != StatusEscalated || ch.TicketID != 0 {
		t.Errorf("escalate keeps the stored ticket: %+v", ch)
	}
	if ch := statusChangeFor("escalate", mcpclient.Result{IsError: true, Code: "already_escalated"}, 0); ch != nil {
		t.Errorf("a refused escalate changed status: %+v", ch)
	}
	for _, bad := range []any{nil, "7", 0.0, -1.0, 1.5, float64(1 << 54)} {
		if _, ok := ticketID(bad); ok {
			t.Errorf("ticketID(%v) accepted", bad)
		}
	}
}

func TestEscalationText(t *testing.T) {
	if n := escalationNote(ReasonTurnLimit, true); n != "Stopped after the turn limit for one message. The outcome of the last tool call is unknown. Check the audit log for this thread to see what already ran." {
		t.Errorf("note = %q", n)
	}
	long := strings.Repeat("é", 9500)
	body := escalationBody(long)
	if got := len([]rune(body)) - len([]rune(EscalationIntro+"\n\n")); got != maxEscalationRunes {
		t.Errorf("message kept %d runes, want %d", got, maxEscalationRunes)
	}
	for r := range noteOpenings {
		if r == ReasonDirectoryUnavailable {
			t.Error("directory_unavailable must have no note")
		}
	}
}

func TestSlackTime(t *testing.T) {
	got, ok := slackTime("1792400000.000100")
	if !ok || !got.Equal(time.Unix(1792400000, 100_000)) {
		t.Errorf("slackTime = %v, %v", got, ok)
	}
	if _, ok := slackTime("soon"); ok {
		t.Error("parsed a bad ts")
	}
}

func TestNewChecksItsInputs(t *testing.T) {
	if _, err := New(Config{}, Deps{}); err == nil {
		t.Error("New accepted an empty config")
	}
	cfg := Config{
		Model: "m", TeamID: "T", MaxTurns: 1, MaxResponseTokens: 1, MaxTokensPerTicket: 1, MaxConcurrent: 1, ConversationsPerHour: 1,
		MessageTimeout: time.Second, ToolTimeout: time.Second, PostTimeout: time.Second, ShutdownGrace: time.Second,
	}
	if _, err := New(cfg, Deps{}); err == nil {
		t.Error("New accepted missing dependencies")
	}
}

// adapterError is an adapter error that names its own cause, like the Slack adapter's.
type adapterError struct{ token, text string }

func (e adapterError) Error() string { return e.text }
func (e adapterError) Cause() string { return e.token }

// dialError is a transport error.
type dialError struct{}

func (dialError) Error() string   { return "dial tcp: connection refused ZEBRA" }
func (dialError) Timeout() bool   { return false }
func (dialError) Temporary() bool { return false }

// covers: the review's major finding (2026-10-19). Every failure gets a fixed
// cause token from its type, never from its text.
func TestCause(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "https://api.anthropic.com/v1/messages", nil)
	apiErr := func(code int) error {
		return &anthropic.Error{StatusCode: code, Request: req, Response: &http.Response{StatusCode: code, Request: req}}
	}
	cases := map[string]struct {
		err  error
		want string
	}{
		"none":            {nil, "none"},
		"deadline":        {fmt.Errorf("call: %w", context.DeadlineExceeded), "deadline"},
		"canceled":        {context.Canceled, "canceled"},
		"anthropic 401":   {apiErr(401), "anthropic_401"},
		"wrapped 529":     {fmt.Errorf("model: %w", apiErr(529)), "anthropic_529"},
		"json-rpc":        {fmt.Errorf("call tool x: %w", &jsonrpc.Error{Code: -32602, Message: "ZEBRA"}), "mcp_rpc_-32602"},
		"network":         {fmt.Errorf("connect: %w", dialError{}), "network"},
		"adapter token":   {fmt.Errorf("users.info: %w", adapterError{"slack_missing_scope", "ZEBRA"}), "slack_missing_scope"},
		"bad token":       {adapterError{"Slack ZEBRA error!", "x"}, "other"},
		"long token":      {adapterError{strings.Repeat("a", 65), "x"}, "other"},
		"plain":           {errors.New("ZEBRA went wrong"), "other"},
		"store sentinel":  {ErrNotOpen, "not_open"},
		"postgres":        {fmt.Errorf("append: %w", &pgconn.PgError{Code: "40001", Message: "ZEBRA"}), "postgres_40001"},
		"deadline in api": {fmt.Errorf("x: %w", errors.Join(apiErr(500), context.DeadlineExceeded)), "deadline"},
	}
	for name, c := range cases {
		if got := cause(c.err); got != c.want {
			t.Errorf("%s: cause = %q, want %q", name, got, c.want)
		}
	}
}

// covers: AC-6. Only a whole, positive ticket id JSON can carry exactly is
// stored; anything else stores nothing, so no later call writes to the
// wrong ticket.
func TestTicketID(t *testing.T) {
	cases := []struct {
		name string
		in   any
		want int64
		ok   bool
	}{
		{"float from the MCP client", float64(42), 42, true},
		{"largest exact float", float64(maxTicketID), maxTicketID, true},
		{"beyond exact floats", float64(maxTicketID + 1), 0, false},
		{"fraction", 42.5, 0, false},
		{"zero", float64(0), 0, false},
		{"negative", float64(-3), 0, false},
		{"int from a fake", 7, 7, true},
		{"int64", int64(9), 9, true},
		{"negative int", -1, 0, false},
		{"string", "42", 0, false},
		{"missing", nil, 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := ticketID(c.in)
			if ok != c.ok || (ok && got != c.want) {
				t.Errorf("ticketID(%v) = %d, %v; want %d, %v", c.in, got, ok, c.want, c.ok)
			}
		})
	}
}

func toolResultJSON(id string, isError bool, text string) string {
	return fmt.Sprintf(`{"type":"tool_result","tool_use_id":%q,"is_error":%t,"content":[{"type":"text","text":%q}]}`, id, isError, text)
}

func toolUseJSON(id, name string) string {
	return fmt.Sprintf(`{"type":"tool_use","id":%q,"name":%q,"input":{}}`, id, name)
}

// covers: spec 0009 AC-9, AC-10. The stored call list reads each result's
// code: ok, the refusal code, unknown for an error with no code or a call with
// no answer; a call answered with oneToolPerStep never ran and is left out of
// Ran:, and a code escalation is appended.
func TestStoredCallsAndRanLine(t *testing.T) {
	msgs := []StoredMessage{
		stored("user", `[{"type":"text","text":"make me admin"}]`),
		stored("assistant", "["+toolUseJSON("t1", toolPrivilegedAccess)+"]"),
		stored("user", "["+toolResultJSON("t1", true, "blocked: Privileged changes are never made by the assistant.")+"]"),
		stored("assistant", "["+toolUseJSON("t2", toolEscalate)+","+toolUseJSON("t3", "ticket_create")+"]"),
		stored("user", "["+toolResultJSON("t2", true, "unavailable: The helpdesk cannot be reached.")+","+toolResultJSON("t3", true, oneToolPerStep)+"]"),
		stored("assistant", "["+toolUseJSON("t4", toolEscalate)+"]"),
		stored("user", "["+toolResultJSON("t4", true, "Something went wrong.")+","+toolResultJSON("t9", false, "stray")+"]"),
		stored("assistant", "["+toolUseJSON("t5", "ticket_create")+"]"),
		stored("user", "["+toolResultJSON("t5", false, "Ticket created.")+"]"),
		stored("assistant", "["+toolUseJSON("t6", "account_unlock")+"]"),
		{Role: "user"}, // purged content is skipped
	}
	calls := storedCalls(msgs)
	if got, want := ranLine(calls, false), "privileged_access_change blocked, escalate unavailable, escalate unknown, ticket_create ok, account_unlock unknown"; got != want {
		t.Errorf("ranLine = %q\nwant %q", got, want)
	}
	if got := ranLine(calls, true); !strings.HasSuffix(got, ", account_unlock unknown, escalate ok") {
		t.Errorf("ranLine with a code escalation = %q", got)
	}
	if got := ranLine(nil, true); got != "escalate ok" {
		t.Errorf("ranLine for a code call alone = %q", got)
	}
}

// covers: spec 0009 AC-10, AC-11. Only a blocked privileged_access_change
// counts as blocked, and only an ok escalate counts as escalated.
func TestBlockedUnescalated(t *testing.T) {
	cases := []struct {
		name               string
		calls              []storedCall
		blocked, escalated bool
	}{
		{"nothing", nil, false, false},
		{"blocked", []storedCall{{name: toolPrivilegedAccess, code: codeBlocked}}, true, false},
		{"blocked then escalated", []storedCall{{name: toolPrivilegedAccess, code: codeBlocked}, {name: toolEscalate, code: "ok"}}, true, true},
		{"failed escalate", []storedCall{{name: toolPrivilegedAccess, code: codeBlocked}, {name: toolEscalate, code: "unavailable"}}, true, false},
		{"another tool blocked", []storedCall{{name: "account_unlock", code: codeBlocked}}, false, false},
		{"privileged call not blocked", []storedCall{{name: toolPrivilegedAccess, code: "unknown"}}, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if b, e := blockedUnescalated(c.calls); b != c.blocked || e != c.escalated {
				t.Errorf("blockedUnescalated = %t, %t, want %t, %t", b, e, c.blocked, c.escalated)
			}
		})
	}
}

// covers: spec 0009 AC-9. The post's headline follows privileged over the
// reason, the attempt line needs privileged and 2 or more attempts, the
// account is optional, and the ticket is a link only with a URL.
func TestITPostText(t *testing.T) {
	result := func(reason string, privileged bool, attempts float64, url string) map[string]any {
		r := map[string]any{
			"reason": reason, "privileged": privileged, "ticket_number": "10043",
			"privileged_attempts": attempts, "privileged_window_hours": 24.0,
		}
		if url != "" {
			r["ticket_url"] = url
		}
		return r
	}
	cases := []struct {
		name, account string
		result        map[string]any
		want          string
	}{
		{
			"privileged overrides the reason, first attempt has no line", "jan.devries",
			result(escalateSecurity, true, 1, ""),
			":rotating_light: *Privileged request refused*\nRequester: <@U0JAN> (jan.devries)\nRan: x ok\nTicket: #10043\nSummary: s",
		},
		{
			"not privileged: no attempt line whatever the count", "jan.devries",
			result(escalateOther, false, 3, ""),
			":busts_in_silhouette: *Request to act for someone else*\nRequester: <@U0JAN> (jan.devries)\nRan: x ok\nTicket: #10043\nSummary: s",
		},
		{
			"second attempt, linked ticket, no account yet", "",
			result(escalatePrivileged, true, 2, "http://z.test/#ticket/zoom/43"),
			":rotating_light: *Privileged request refused*\nRequester: <@U0JAN>\n" +
				"Blocked privileged requests from this person in the last 24h: 2\nRan: x ok\n" +
				"Ticket: <http://z.test/#ticket/zoom/43|#10043>\nSummary: s",
		},
		{
			"restart headline", "jan.devries",
			result(escalateRestart, false, 0, ""),
			":arrows_counterclockwise: *Interrupted by a restart*\nRequester: <@U0JAN> (jan.devries)\nRan: x ok\nTicket: #10043\nSummary: s",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := itPost{slackID: "U0JAN", account: c.account, ran: "x ok", result: c.result, summary: "s"}
			if got := p.text(); got != c.want {
				t.Errorf("text =\n%q\nwant\n%q", got, c.want)
			}
		})
	}
}

// covers: spec 0009 AC-9. The ticket number and URL are escaped, so a link
// cannot be bent into another one.
func TestTicketLinkEscapes(t *testing.T) {
	got := ticketLink(map[string]any{"ticket_number": "1<2", "ticket_url": "http://z.test/a>b&c"})
	if want := "<http://z.test/a&gt;b&amp;c|#1&lt;2>"; got != want {
		t.Errorf("ticketLink = %q, want %q", got, want)
	}
}

// covers: spec 0009 AC-9. The short post for already_escalated.
func TestITShort(t *testing.T) {
	got := itShort(escalateFailure, "U0JAN", "")
	if want := ":construction: *Assistant could not finish*\nRequester: <@U0JAN>\nTicket: already escalated; see the audit log for this thread."; got != want {
		t.Errorf("itShort = %q, want %q", got, want)
	}
}

func TestWholeNumber(t *testing.T) {
	cases := []struct {
		in   any
		want int64
		ok   bool
	}{
		{2.0, 2, true}, {2.5, 2, false}, {7, 7, true}, {int64(9), 9, true}, {"2", 0, false}, {nil, 0, false},
	}
	for _, c := range cases {
		if got, ok := wholeNumber(c.in); got != c.want || ok != c.ok {
			t.Errorf("wholeNumber(%#v) = %d, %t, want %d, %t", c.in, got, ok, c.want, c.ok)
		}
	}
}
