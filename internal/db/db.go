// Package db opens the Postgres connection pool. Generated sqlc queries live
// in internal/db/queries; tables and roles arrive with feature #5.
package db

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/toyinogun/AIITsupport/internal/db/queries"
)

const pingTimeout = 5 * time.Second

// Open connects to Postgres and pings it, so a binary fails at startup
// rather than on its first query when the database is unreachable.
func Open(ctx context.Context, url string) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("parse database url: %w", err)
	}

	pingCtx, cancel := context.WithTimeout(ctx, pingTimeout)
	defer cancel()
	if _, err := queries.New(pool).Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("reach database: %w", err)
	}
	return pool, nil
}
