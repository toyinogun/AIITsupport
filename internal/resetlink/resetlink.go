// Package resetlink owns password reset links (spec 0011): the token, its
// hash, and the reset_links rows. Only the hash is stored; the plain token
// lives in the tool result, the agent's turn, and the Slack message. Every
// expiry check uses the database's clock. Only the tool server imports it.
package resetlink

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/toyinogun/AIITsupport/internal/db/queries"
)

// tokenBytes is the token's entropy; base64url without padding makes it
// TokenLen characters.
const (
	tokenBytes = 32
	TokenLen   = 43
)

// ErrNotValid means no live, unexpired link has that token.
var ErrNotValid = errors.New("reset link is not valid")

// NewToken returns 32 random bytes as base64url without padding.
func NewToken() string {
	b := make([]byte, tokenBytes)
	_, _ = rand.Read(b) // crypto/rand.Read never fails
	return base64.RawURLEncoding.EncodeToString(b)
}

// WellFormed reports whether token could be one NewToken made, without
// touching the database.
func WellFormed(token string) bool {
	if len(token) != TokenLen {
		return false
	}
	b, err := base64.RawURLEncoding.Strict().DecodeString(token)
	return err == nil && len(b) == tokenBytes
}

// Hash is what the database holds for a token.
func Hash(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

// Link is one reset_links row, without its hash.
type Link struct {
	ID               uuid.UUID
	Account          string // directory_account, the only account it resets
	RequesterSlackID string
	ChannelID        string
	ThreadTS         string
	TicketID         int64
	IssueCallID      uuid.UUID
	ExpiresAt        time.Time
	UsedAt           time.Time // zero unless used
}

// Issue is what a new link is for.
type Issue struct {
	Account          string
	RequesterSlackID string
	ChannelID        string
	ThreadTS         string
	TicketID         int64
	IssueCallID      uuid.UUID
	TTL              time.Duration
}

// Issued is a new link with its plain token and how many older links it voided.
type Issued struct {
	Link
	Token  string
	Voided int64
}

// Store reads and writes reset_links as noord_toolserver.
type Store struct {
	pool *pgxpool.Pool
	q    *queries.Queries
}

// NewStore wraps pool, which must connect as noord_toolserver.
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool, q: queries.New(pool)}
}

// Issue makes a new live link for the account in one transaction: the
// account's advisory lock, every live link voided, then the new row with
// expires_at = now() + TTL.
func (s *Store) Issue(ctx context.Context, in Issue) (Issued, error) {
	token := NewToken()
	var out Issued
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := queries.New(tx)
		if err := q.LockAccountForIssue(ctx, in.Account); err != nil {
			return fmt.Errorf("lock the account's links: %w", err)
		}
		voided, err := q.VoidLiveForAccount(ctx, in.Account)
		if err != nil {
			return fmt.Errorf("void live links: %w", err)
		}
		row, err := q.InsertResetLink(ctx, queries.InsertResetLinkParams{
			TokenHash:        Hash(token),
			DirectoryAccount: in.Account,
			RequesterSlackID: in.RequesterSlackID,
			SlackChannelID:   in.ChannelID,
			SlackThreadTs:    in.ThreadTS,
			ZammadTicketID:   in.TicketID,
			IssueCallID:      in.IssueCallID,
			TtlSeconds:       in.TTL.Seconds(),
		})
		if err != nil {
			return fmt.Errorf("insert reset link: %w", err)
		}
		out = Issued{Link: toLink(row), Token: token, Voided: voided}
		return nil
	})
	if err != nil {
		return Issued{}, fmt.Errorf("issue reset link: %w", err)
	}
	return out, nil
}

// CountRecent counts the account's links created inside window.
func (s *Store) CountRecent(ctx context.Context, account string, window time.Duration) (int64, error) {
	n, err := s.q.CountRecentForAccount(ctx, queries.CountRecentForAccountParams{
		DirectoryAccount: account, WindowSeconds: window.Seconds(),
	})
	if err != nil {
		return 0, fmt.Errorf("count recent reset links: %w", err)
	}
	return n, nil
}

// Valid returns the live, unexpired link for token, or ErrNotValid. It
// changes nothing, so a link preview can fetch the page safely.
func (s *Store) Valid(ctx context.Context, token string) (Link, error) {
	row, err := s.q.GetValidResetLinkByHash(ctx, Hash(token))
	return rowOrNotValid(row, err, "read reset link")
}

// Claim moves the live, unexpired link for token to used and commits, or
// returns ErrNotValid when there is none (used, voided, expired, or a
// concurrent claim won).
func (s *Store) Claim(ctx context.Context, token string) (Link, error) {
	row, err := s.q.ClaimResetLink(ctx, Hash(token))
	return rowOrNotValid(row, err, "claim reset link")
}

// Revert undoes a claim after a failed write, guarded on the claim's own
// used_at and only while unexpired; reverted is false when it did not apply.
func (s *Store) Revert(ctx context.Context, l Link) (reverted bool, err error) {
	n, err := s.q.RevertResetLinkClaim(ctx, queries.RevertResetLinkClaimParams{
		ID: l.ID, UsedAt: pgtype.Timestamptz{Time: l.UsedAt, Valid: true},
	})
	if err != nil {
		return false, fmt.Errorf("revert reset link claim: %w", err)
	}
	return n == 1, nil
}

// DeleteExpired deletes links that expired more than retentionDays ago.
func (s *Store) DeleteExpired(ctx context.Context, retentionDays int) (int64, error) {
	n, err := s.q.DeleteExpiredResetLinks(ctx, int32(retentionDays)) //nolint:gosec // G115: config caps TICKET_RETENTION_DAYS far below int32
	if err != nil {
		return 0, fmt.Errorf("delete expired reset links: %w", err)
	}
	return n, nil
}

func rowOrNotValid(row queries.ResetLink, err error, what string) (Link, error) {
	if errors.Is(err, pgx.ErrNoRows) {
		return Link{}, ErrNotValid
	}
	if err != nil {
		return Link{}, fmt.Errorf("%s: %w", what, err)
	}
	return toLink(row), nil
}

func toLink(r queries.ResetLink) Link {
	l := Link{
		ID:               r.ID,
		Account:          r.DirectoryAccount,
		RequesterSlackID: r.RequesterSlackID,
		ChannelID:        r.SlackChannelID,
		ThreadTS:         r.SlackThreadTs,
		TicketID:         r.ZammadTicketID,
		IssueCallID:      r.IssueCallID,
		ExpiresAt:        r.ExpiresAt,
	}
	if r.UsedAt.Valid {
		l.UsedAt = r.UsedAt.Time
	}
	return l
}
