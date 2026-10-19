package slack

import (
	"errors"
	"fmt"
	"testing"

	slackapi "github.com/slack-go/slack"
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
