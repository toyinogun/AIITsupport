package cause_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/toyinogun/AIITsupport/internal/cause"
)

// causer is an adapter error that names its own token.
type causer struct{ token string }

func (c causer) Error() string { return "adapter failed for Jan de Vries" }
func (c causer) Cause() string { return c.token }

// covers: AC-3 (spec 0010). Of reads a failure's type, never its text, and
// returns one fixed token per kind, through any number of wraps.
func TestOfReturnsATokenPerKind(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"nil", nil, "none"},
		{"deadline", context.DeadlineExceeded, "deadline"},
		{"wrapped deadline", fmt.Errorf("query: %w", context.DeadlineExceeded), "deadline"},
		{"canceled", fmt.Errorf("query: %w", context.Canceled), "canceled"},
		{"postgres with a sqlstate", fmt.Errorf("dedup: %w", &pgconn.PgError{Code: "57P01", Message: "jan.devries"}), "postgres_57P01"},
		{"postgres with a bad code", &pgconn.PgError{Code: "57p01 x"}, "postgres"},
		{"postgres with no code", &pgconn.PgError{}, "postgres"},
		{"adapter token", fmt.Errorf("post: %w", causer{"slack_ratelimited"}), "slack_ratelimited"},
		{"adapter token at 64 bytes", causer{strings.Repeat("a", 64)}, strings.Repeat("a", 64)},
		{"adapter token at 65 bytes", causer{strings.Repeat("a", 65)}, "other"},
		{"adapter token with text", causer{"user jan@noord.example not found"}, "other"},
		{"adapter token in capitals", causer{"Slack_Error"}, "other"},
		{"empty adapter token", causer{""}, "other"},
		{"network", fmt.Errorf("dial: %w", &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("refused")}), "network"},
		{"plain error", errors.New("Jan de Vries is locked out"), "other"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := cause.Of(tc.err); got != tc.want {
				t.Errorf("Of = %q, want %q", got, tc.want)
			}
		})
	}
}

// covers: AC-3 (spec 0010). A deadline is also a net.Error (it times out),
// and a Postgres error can wrap an adapter error: the earlier, more specific
// kind wins, so the token does not change with how an error was wrapped.
func TestOfChecksKindsInOrder(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"deadline before network", &net.OpError{Op: "read", Err: context.DeadlineExceeded}, "deadline"},
		{"canceled before postgres", errors.Join(&pgconn.PgError{Code: "57014"}, context.Canceled), "canceled"},
		{"postgres before adapter", errors.Join(causer{"slack_x"}, &pgconn.PgError{Code: "08006"}), "postgres_08006"},
		{"adapter before network", errors.Join(&net.OpError{Op: "dial", Err: errors.New("x")}, causer{"slack_x"}), "slack_x"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := cause.Of(tc.err); got != tc.want {
				t.Errorf("Of = %q, want %q", got, tc.want)
			}
		})
	}
}
