package agent

import (
	"context"
	"errors"
	"net"
	"regexp"
	"strconv"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
)

// Causer is an error that names its own fixed cause token. Adapters whose
// error types agent code does not import (the Slack Web API) implement it.
type Causer interface {
	Cause() string
}

// causeToken is the shape every adapter token must have: an error's text can
// quote content, so anything else is reported as "other".
var causeToken = regexp.MustCompile(`^[a-z0-9_]{1,64}$`)

// sqlState is a Postgres error code, five digits or capital letters.
var sqlState = regexp.MustCompile(`^[0-9A-Z]{5}$`)

// cause turns a failure into a fixed token for logs and spans, read from the
// error's type, never its text (AC-17): "none", "deadline", "canceled",
// "anthropic_<status>", "mcp_rpc_<code>", "postgres_<sqlstate>", "not_open",
// "network", an adapter's own token, or "other".
func cause(err error) string {
	var (
		api *anthropic.Error
		rpc *jsonrpc.Error
		pg  *pgconn.PgError
		c   Causer
		ne  net.Error
	)
	switch {
	case err == nil:
		return "none"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline"
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, ErrNotOpen):
		return "not_open"
	case errors.As(err, &api):
		return "anthropic_" + strconv.Itoa(api.StatusCode)
	case errors.As(err, &rpc):
		return "mcp_rpc_" + strconv.FormatInt(rpc.Code, 10)
	case errors.As(err, &pg):
		if sqlState.MatchString(pg.Code) {
			return "postgres_" + pg.Code
		}
		return "postgres"
	case errors.As(err, &c):
		if causeToken.MatchString(c.Cause()) {
			return c.Cause()
		}
		return "other"
	case errors.As(err, &ne):
		return "network"
	}
	return "other"
}
