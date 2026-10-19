// Package cause turns a failure into a fixed token for logs and spans, read
// from the error's type, never its text (spec 0010, AC-3). It is a leaf: it
// imports pgx's pgconn only, so any package can use it.
package cause

import (
	"context"
	"errors"
	"net"
	"regexp"

	"github.com/jackc/pgx/v5/pgconn"
)

// Causer is an error that names its own fixed cause token. Adapters whose
// error types the caller does not import (the Slack Web API) implement it.
type Causer interface {
	Cause() string
}

// token is the shape every adapter token must have: an error's text can
// quote content, so anything else is reported as "other".
var token = regexp.MustCompile(`^[a-z0-9_]{1,64}$`)

// sqlState is a Postgres error code, five digits or capital letters.
var sqlState = regexp.MustCompile(`^[0-9A-Z]{5}$`)

// Of returns "none", "deadline", "canceled", "postgres_<sqlstate>",
// "postgres", an adapter's own token, "network", or "other".
func Of(err error) string {
	var (
		pg *pgconn.PgError
		c  Causer
		ne net.Error
	)
	switch {
	case err == nil:
		return "none"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline"
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.As(err, &pg):
		if sqlState.MatchString(pg.Code) {
			return "postgres_" + pg.Code
		}
		return "postgres"
	case errors.As(err, &c):
		if token.MatchString(c.Cause()) {
			return c.Cause()
		}
		return "other"
	case errors.As(err, &ne):
		return "network"
	}
	return "other"
}
