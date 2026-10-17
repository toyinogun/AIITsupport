package audit

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/toyinogun/AIITsupport/internal/db/queries"
)

// Queries is the part of the generated queries the recorder uses.
// *queries.Queries satisfies it, on the pool or inside a transaction.
type Queries interface {
	InsertAuditIntent(ctx context.Context, arg queries.InsertAuditIntentParams) (queries.InsertAuditIntentRow, error)
	InsertAuditOutcome(ctx context.Context, arg queries.InsertAuditOutcomeParams) (int64, error)
	CreateApproval(ctx context.Context, arg queries.CreateApprovalParams) (queries.Approval, error)
	GetApproval(ctx context.Context, id uuid.UUID) (queries.Approval, error)
	ApproveApproval(ctx context.Context, arg queries.ApproveApprovalParams) (queries.Approval, error)
	DenyApproval(ctx context.Context, arg queries.DenyApprovalParams) (queries.Approval, error)
	ExpireDueApprovals(ctx context.Context, maxRows int32) ([]queries.Approval, error)
}

// Store is Queries plus a way to run several of them in one transaction.
// Tests pass a fake; the tool server passes a PgStore.
type Store interface {
	Queries
	InTx(ctx context.Context, fn func(Queries) error) error
}

// PgStore is the Postgres Store, connected as noord_toolserver.
type PgStore struct {
	*queries.Queries
	pool *pgxpool.Pool
}

// NewPgStore wraps pool, which must connect as noord_toolserver.
func NewPgStore(pool *pgxpool.Pool) *PgStore {
	return &PgStore{Queries: queries.New(pool), pool: pool}
}

// InTx commits when fn returns nil and rolls back otherwise.
func (s *PgStore) InTx(ctx context.Context, fn func(Queries) error) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		return fn(queries.New(tx))
	})
}
