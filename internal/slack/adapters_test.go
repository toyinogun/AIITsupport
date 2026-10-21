package slack

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	slackapi "github.com/slack-go/slack"

	"github.com/toyinogun/AIITsupport/internal/agent"
	"github.com/toyinogun/AIITsupport/internal/cause"
)

// covers: the review's major finding (2026-10-19). A Slack Web API error
// carries a fixed cause token for the agent's logs, never its text.
func TestSlackErrorCause(t *testing.T) {
	cases := map[string]struct {
		err  error
		want string
	}{
		"api code":     {slackapi.SlackErrorResponse{Err: "missing_scope"}, "slack_missing_scope"},
		"odd code":     {slackapi.SlackErrorResponse{Err: "Not A Code ZEBRA"}, "slack_api"},
		"rate limited": {&slackapi.RateLimitedError{}, "slack_rate_limited"},
		"status":       {slackapi.StatusCodeError{Code: 503, Status: "503 ZEBRA"}, "slack_503"},
		"wrapped":      {fmt.Errorf("post: %w", slackapi.SlackErrorResponse{Err: "channel_not_found"}), "slack_channel_not_found"},
		"plain":        {errors.New("ZEBRA"), ""},
	}
	for name, c := range cases {
		err := wrapErr(c.err)
		var causer interface{ Cause() string }
		got := ""
		if errors.As(err, &causer) {
			got = causer.Cause()
		}
		if got != c.want {
			t.Errorf("%s: cause = %q, want %q", name, got, c.want)
		}
		if err.Error() != c.err.Error() {
			t.Errorf("%s: wrapped error text = %q, want the original %q", name, err.Error(), c.err.Error())
		}
	}
	if wrapErr(nil) != nil {
		t.Error("wrapErr(nil) is not nil")
	}
}

// fakeSlackAPI answers every Web API method with the given status and body.
func fakeSlackAPI(t *testing.T, status int, body string) *slackapi.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if status == http.StatusTooManyRequests {
			w.Header().Set("Retry-After", "1")
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return slackapi.New("xoxb-test", slackapi.OptionAPIURL(srv.URL+"/"))
}

// causeOf is the cause.Causer token an adapter error carries, or "".
func causeOf(err error) string {
	var c cause.Causer
	if errors.As(err, &c) {
		return c.Cause()
	}
	return ""
}

// covers: AC-3 and the review's major finding (2026-10-19). users.info and
// chat.postMessage failures reach the agent with a fixed slack_<code> token,
// decoded by slack-go from real Web API answers.
func TestAdaptersCarryTheSlackCause(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{"api error code", http.StatusOK, `{"ok":false,"error":"missing_scope"}`, "slack_missing_scope"},
		{"rate limited", http.StatusTooManyRequests, `{"ok":false,"error":"ratelimited"}`, "slack_rate_limited"},
		{"server error", http.StatusServiceUnavailable, `oops`, "slack_503"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			api := fakeSlackAPI(t, c.status, c.body)
			_, err := users{api: api}.UserInfo(t.Context(), "U0JAN")
			if got := causeOf(err); got != c.want {
				t.Errorf("users.info cause = %q (err %v), want %q", got, err, c.want)
			}
			err = poster{api: api}.Reply(t.Context(), "D0JAN", "1.0", "hi")
			if got := causeOf(err); got != c.want {
				t.Errorf("chat.postMessage cause = %q (err %v), want %q", got, err, c.want)
			}
		})
	}
}

// covers: AC-3. A users.info answer reaches the gate with every field it reads.
func TestUserInfoMapsTheGateFields(t *testing.T) {
	api := fakeSlackAPI(t, http.StatusOK, `{"ok":true,"user":{"id":"U0JAN","team_id":"T0NOORD",
		"deleted":false,"is_bot":false,"is_restricted":true,"is_email_confirmed":true,
		"profile":{"email":"jan@example.com"}}}`)

	u, err := users{api: api}.UserInfo(t.Context(), "U0JAN")
	if err != nil {
		t.Fatal(err)
	}
	want := agent.SlackUser{ID: "U0JAN", TeamID: "T0NOORD", IsRestricted: true, IsEmailConfirmed: true, Email: "jan@example.com"}
	if u != want {
		t.Errorf("user = %+v, want %+v", u, want)
	}
}

// covers: spec 0009 (the API surface). A post with no thread sends no
// thread_ts, so it lands in the IT channel itself; a threaded reply sends it.
func TestReplyThreadTS(t *testing.T) {
	var forms []map[string][]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		forms = append(forms, r.PostForm)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"channel":"C0IT","ts":"1.0"}`))
	}))
	t.Cleanup(srv.Close)
	p := poster{api: slackapi.New("xoxb-test", slackapi.OptionAPIURL(srv.URL+"/"))}

	if err := p.Reply(t.Context(), "C0IT", "", "flag"); err != nil {
		t.Fatal(err)
	}
	if err := p.Reply(t.Context(), "D0JAN", "1792400000.000100", "hi"); err != nil {
		t.Fatal(err)
	}
	if _, sent := forms[0]["thread_ts"]; sent || forms[0]["channel"][0] != "C0IT" {
		t.Errorf("channel post form = %v, want no thread_ts", forms[0])
	}
	if got := forms[1]["thread_ts"]; len(got) != 1 || got[0] != "1792400000.000100" {
		t.Errorf("threaded reply thread_ts = %v", got)
	}
}
