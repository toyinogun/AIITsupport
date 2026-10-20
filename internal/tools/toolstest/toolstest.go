// Package toolstest holds in memory fakes for the tool layer's tests: an
// audit store that answers the history reads like the SQL does, and a
// directory that is also the writer. No binary imports it.
package toolstest

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/toyinogun/AIITsupport/internal/audit"
	"github.com/toyinogun/AIITsupport/internal/db/queries"
	"github.com/toyinogun/AIITsupport/internal/ldap"
)

// Base is the fake directory's base DN.
const Base = "DC=ad,DC=noord,DC=example"

// MemStore is an in memory audit.Store. It answers the history reads the
// same way the SQL does and, like Postgres, fails a query whose context has
// ended.
type MemStore struct {
	mu        sync.Mutex
	rows      []queries.AuditLog
	Approvals []queries.CreateApprovalParams
	// rowsApproval are the approvals as the table holds them.
	rowsApproval []queries.Approval
	IntentErr    error // fails every intent insert
	OutcomeErr   error // fails every outcome insert
	CountErr     error // fails the rate limit count
	ThreadErr    error // fails the thread reads (outcomes and ticket id)
	// ApprovalErr fails the pending approval read.
	ApprovalErr error
	// HidePending makes FindPendingApproval find nothing, as when a racing
	// request's approval has not committed yet at the precheck.
	HidePending bool
}

// InsertAuditIntent stores an intent row, or fails with IntentErr.
func (s *MemStore) InsertAuditIntent(ctx context.Context, p queries.InsertAuditIntentParams) (queries.InsertAuditIntentRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := cmp.Or(ctx.Err(), s.IntentErr); err != nil {
		return queries.InsertAuditIntentRow{}, err
	}
	row := queries.AuditLog{
		ID: int64(len(s.rows) + 1), CallID: uuid.New(), Kind: "intent", OccurredAt: time.Now(),
		ToolName: p.ToolName, RiskTier: p.RiskTier, RequesterSlackID: p.RequesterSlackID,
		RequesterAccount: p.RequesterAccount, SlackChannelID: p.SlackChannelID, SlackThreadTs: p.SlackThreadTs,
		ZammadTicketID: p.ZammadTicketID, Target: p.Target, Arguments: p.Arguments,
		ApprovalID: p.ApprovalID, ApproverSlackID: p.ApproverSlackID, TraceID: p.TraceID,
	}
	s.rows = append(s.rows, row)
	return queries.InsertAuditIntentRow{ID: row.ID, CallID: row.CallID}, nil
}

// InsertAuditOutcome copies the intent's columns into an outcome row, or fails with OutcomeErr.
func (s *MemStore) InsertAuditOutcome(ctx context.Context, p queries.InsertAuditOutcomeParams) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := cmp.Or(ctx.Err(), s.OutcomeErr); err != nil {
		return 0, err
	}
	for _, in := range s.rows {
		if in.CallID == p.CallID && in.Kind == "intent" {
			out := in
			out.ID, out.Kind, out.OccurredAt, out.Arguments = int64(len(s.rows)+1), "outcome", time.Now(), nil
			out.Outcome.String, out.Outcome.Valid = p.Outcome, true
			out.Result, out.Error = p.Result, p.Error
			if p.ApprovalID.Valid {
				out.ApprovalID = p.ApprovalID
			}
			s.rows = append(s.rows, out)
			return out.ID, nil
		}
	}
	return 0, errors.New("no intent")
}

// CreateApproval records the approval request and stores a pending row.
func (s *MemStore) CreateApproval(ctx context.Context, p queries.CreateApprovalParams) (queries.Approval, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return queries.Approval{}, err
	}
	if p.RequesterAccount.Valid && slices.ContainsFunc(s.rowsApproval, func(a queries.Approval) bool {
		return a.Status == "pending" && a.RequesterAccount == p.RequesterAccount && a.ToolName == p.ToolName
	}) {
		// approvals_one_pending_idx, as Postgres reports it.
		return queries.Approval{}, &pgconn.PgError{Code: "23505", ConstraintName: "approvals_one_pending_idx"}
	}
	s.Approvals = append(s.Approvals, p)
	a := queries.Approval{
		ID: uuid.New(), CallID: p.CallID, ToolName: p.ToolName, Arguments: p.Arguments, RiskTier: p.RiskTier,
		RequesterSlackID: p.RequesterSlackID, RequesterAccount: p.RequesterAccount,
		SlackChannelID: p.SlackChannelID, SlackThreadTs: p.SlackThreadTs, ZammadTicketID: p.ZammadTicketID,
		ApproverKind: p.ApproverKind, AllowedApprovers: slices.Clone(p.AllowedApprovers),
		Status: "pending", ExpiresAt: p.ExpiresAt, CreatedAt: time.Now(),
	}
	s.rowsApproval = append(s.rowsApproval, a)
	return a, nil
}

// Approval returns the stored approval with that id.
func (s *MemStore) Approval(id uuid.UUID) (queries.Approval, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.approvalIndex(id)
	if i < 0 {
		return queries.Approval{}, false
	}
	return s.rowsApproval[i], true
}

// SetApproval replaces a stored approval, to put it in a state for a test.
func (s *MemStore) SetApproval(a queries.Approval) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if i := s.approvalIndex(a.ID); i >= 0 {
		s.rowsApproval[i] = a
	}
}

func (s *MemStore) approvalIndex(id uuid.UUID) int {
	return slices.IndexFunc(s.rowsApproval, func(a queries.Approval) bool { return a.ID == id })
}

// GetApproval reads one approval like the SQL does.
func (s *MemStore) GetApproval(_ context.Context, id uuid.UUID) (queries.Approval, error) {
	if a, ok := s.Approval(id); ok {
		return a, nil
	}
	return queries.Approval{}, pgx.ErrNoRows
}

// ApproveApproval is the guarded approve, like the SQL.
func (s *MemStore) ApproveApproval(_ context.Context, p queries.ApproveApprovalParams) (queries.Approval, error) {
	return s.move(p.ID, func(a *queries.Approval) bool {
		if !slices.Contains(a.AllowedApprovers, p.Clicker) || p.Clicker == a.RequesterSlackID {
			return false
		}
		a.Status, a.DecidedBy, a.DecidedAt = "approved", text(p.Clicker), now()
		return true
	})
}

// DenyApproval is the guarded deny, like the SQL.
func (s *MemStore) DenyApproval(_ context.Context, p queries.DenyApprovalParams) (queries.Approval, error) {
	return s.move(p.ID, func(a *queries.Approval) bool {
		if !slices.Contains(a.AllowedApprovers, p.Clicker) {
			return false
		}
		a.Status, a.DecidedBy, a.DecidedAt, a.Arguments = "denied", text(p.Clicker), now(), nil
		return true
	})
}

// move applies a decision to a pending, unexpired approval.
func (s *MemStore) move(id uuid.UUID, apply func(*queries.Approval) bool) (queries.Approval, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.approvalIndex(id)
	if i < 0 || s.rowsApproval[i].Status != "pending" || !s.rowsApproval[i].ExpiresAt.After(time.Now()) {
		return queries.Approval{}, pgx.ErrNoRows
	}
	a := s.rowsApproval[i]
	if !apply(&a) {
		return queries.Approval{}, pgx.ErrNoRows
	}
	s.rowsApproval[i] = a
	return a, nil
}

// ExpireDueApprovals expires pending approvals past their expiry, like the SQL.
func (s *MemStore) ExpireDueApprovals(_ context.Context, maxRows int32) ([]queries.Approval, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []queries.Approval
	for i, a := range s.rowsApproval {
		if len(out) < int(maxRows) && a.Status == "pending" && !a.ExpiresAt.After(time.Now()) {
			a.Status, a.Arguments = "expired", nil
			s.rowsApproval[i] = a
			out = append(out, a)
		}
	}
	return out, nil
}

// ExpireDueApprovalsFor expires one account's overdue pending approvals of
// a tool, like the SQL.
func (s *MemStore) ExpireDueApprovalsFor(_ context.Context, p queries.ExpireDueApprovalsForParams) ([]queries.Approval, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []queries.Approval
	for i, a := range s.rowsApproval {
		if a.RequesterAccount == p.RequesterAccount && a.ToolName == p.ToolName &&
			a.Status == "pending" && !a.ExpiresAt.After(time.Now()) {
			a.Status, a.Arguments = "expired", nil
			s.rowsApproval[i] = a
			out = append(out, a)
		}
	}
	return out, nil
}

// AbandonStuckApprovals fails approved rows decided longer ago than the
// grace, like the SQL.
func (s *MemStore) AbandonStuckApprovals(_ context.Context, p queries.AbandonStuckApprovalsParams) ([]queries.Approval, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cutoff := time.Now().Add(-time.Duration(p.GraceSeconds * float64(time.Second)))
	var out []queries.Approval
	for i, a := range s.rowsApproval {
		if len(out) < int(p.MaxRows) && a.Status == "approved" && a.DecidedAt.Valid && a.DecidedAt.Time.Before(cutoff) {
			a.Status, a.ExecutedAt, a.Arguments = "failed", now(), nil
			s.rowsApproval[i] = a
			out = append(out, a)
		}
	}
	return out, nil
}

// ThreadHasOpenApproval answers like the SQL does.
func (s *MemStore) ThreadHasOpenApproval(ctx context.Context, p queries.ThreadHasOpenApprovalParams) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := cmp.Or(ctx.Err(), s.ApprovalErr); err != nil {
		return false, err
	}
	for _, a := range s.rowsApproval {
		if a.SlackChannelID == p.SlackChannelID && a.SlackThreadTs == p.SlackThreadTs && a.ToolName == p.ToolName &&
			(a.Status == "pending" || a.Status == "approved") {
			return true, nil
		}
	}
	return false, nil
}

// FindPendingApproval answers like the SQL does.
func (s *MemStore) FindPendingApproval(ctx context.Context, p queries.FindPendingApprovalParams) (queries.Approval, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := cmp.Or(ctx.Err(), s.ApprovalErr); err != nil {
		return queries.Approval{}, err
	}
	if s.HidePending {
		return queries.Approval{}, pgx.ErrNoRows
	}
	for _, a := range slices.Backward(s.rowsApproval) {
		if a.RequesterAccount == p.RequesterAccount && a.ToolName == p.ToolName && a.Status == "pending" && a.ExpiresAt.After(time.Now()) {
			return a, nil
		}
	}
	return queries.Approval{}, pgx.ErrNoRows
}

// ListApprovalsSince answers like the SQL does.
func (s *MemStore) ListApprovalsSince(_ context.Context, p queries.ListApprovalsSinceParams) ([]queries.Approval, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []queries.Approval
	for _, a := range s.rowsApproval {
		if len(out) < int(p.MaxRows) && !a.CreatedAt.Before(p.Since) {
			out = append(out, a)
		}
	}
	return out, nil
}

// MarkApprovalExecuted moves an approved row to executed, like the SQL.
func (s *MemStore) MarkApprovalExecuted(_ context.Context, id uuid.UUID) (queries.Approval, error) {
	return s.endApproved(id, "executed")
}

// MarkApprovalFailed moves an approved row to failed, like the SQL.
func (s *MemStore) MarkApprovalFailed(_ context.Context, id uuid.UUID) (queries.Approval, error) {
	return s.endApproved(id, "failed")
}

func (s *MemStore) endApproved(id uuid.UUID, status string) (queries.Approval, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.approvalIndex(id)
	if i < 0 || s.rowsApproval[i].Status != "approved" {
		return queries.Approval{}, pgx.ErrNoRows
	}
	a := s.rowsApproval[i]
	a.Status, a.ExecutedAt, a.Arguments = status, now(), nil
	s.rowsApproval[i] = a
	return a, nil
}

// ExecutionOutcomeForApproval answers like the SQL does.
func (s *MemStore) ExecutionOutcomeForApproval(_ context.Context, p queries.ExecutionOutcomeForApprovalParams) (queries.ExecutionOutcomeForApprovalRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range slices.Backward(s.rows) {
		if r.Kind == "outcome" && r.ApprovalID == p.ApprovalID && r.Outcome.String != "pending_approval" &&
			(r.ToolName == p.ToolName || r.ToolName == "approval.abandon") {
			return queries.ExecutionOutcomeForApprovalRow{Outcome: r.Outcome, Error: r.Error, Result: r.Result}, nil
		}
	}
	return queries.ExecutionOutcomeForApprovalRow{}, pgx.ErrNoRows
}

func text(s string) pgtype.Text { return pgtype.Text{String: s, Valid: s != ""} }

func now() pgtype.Timestamptz { return pgtype.Timestamptz{Time: time.Now(), Valid: true} }

// CountRecentOutcomes counts like the SQL does, or fails with CountErr.
func (s *MemStore) CountRecentOutcomes(ctx context.Context, p queries.CountRecentOutcomesParams) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := cmp.Or(ctx.Err(), s.CountErr); err != nil {
		return 0, err
	}
	var n int64
	for _, r := range s.rows {
		if r.Kind == "outcome" && r.Outcome == p.Outcome && r.ToolName == p.ToolName &&
			r.RequesterSlackID == p.RequesterSlackID && r.OccurredAt.After(p.Since) {
			n++
		}
	}
	return n, nil
}

// ThreadHasOutcome answers like the SQL does, or fails with ThreadErr.
func (s *MemStore) ThreadHasOutcome(ctx context.Context, p queries.ThreadHasOutcomeParams) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := cmp.Or(ctx.Err(), s.ThreadErr); err != nil {
		return false, err
	}
	for _, r := range s.rows {
		if r.Kind == "outcome" && r.Outcome == p.Outcome && r.ToolName == p.ToolName &&
			r.SlackChannelID == p.SlackChannelID && r.SlackThreadTs == p.SlackThreadTs {
			return true, nil
		}
	}
	return false, nil
}

// ThreadTicketID answers like the SQL does (the newest ok ticket_create's
// numeric result.ticket_id, else 0), or fails with ThreadErr.
func (s *MemStore) ThreadTicketID(ctx context.Context, p queries.ThreadTicketIDParams) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := cmp.Or(ctx.Err(), s.ThreadErr); err != nil {
		return 0, err
	}
	for _, r := range slices.Backward(s.rows) {
		if r.Kind != "outcome" || r.Outcome.String != "ok" || r.ToolName != "ticket_create" ||
			r.SlackChannelID != p.SlackChannelID || r.SlackThreadTs != p.SlackThreadTs {
			continue
		}
		var res map[string]any
		_ = json.Unmarshal(r.Result, &res)
		id, _ := res["ticket_id"].(float64) // jsonb_typeof = 'number'
		return int64(id), nil
	}
	return 0, nil
}

// Backdate moves every row stored so far back by d, to test time windows.
func (s *MemStore) Backdate(d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.rows {
		s.rows[i].OccurredAt = s.rows[i].OccurredAt.Add(-d)
	}
}

// InTx runs fn on the store itself; nothing rolls back.
func (s *MemStore) InTx(_ context.Context, fn func(audit.Queries) error) error { return fn(s) }

// All returns every row, in insert order.
func (s *MemStore) All() []queries.AuditLog {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]queries.AuditLog(nil), s.rows...)
}

// Directory is a fake directory that is also the writer.
type Directory struct {
	mu       sync.Mutex
	Users    []ldap.User
	Locked   map[string]time.Time // DN -> lock end; a zero end is locked until an admin clears it
	Groups   map[string]ldap.Group
	NestedIn map[string][]string // group name -> parent DNs at any depth
	// UserIn is user DN -> group DNs the user is in at any depth.
	UserIn    map[string][]string
	Err       error // returned by every read
	CallCount int   // reads made
	clears    []string
	KeepLock  bool // ClearLockout succeeds but the lock stays (not confirmed)
	WriterErr error
	// Stall makes FindUsersByMail wait for its context to end, then fail
	// with the context's error, as a hung directory connection does.
	Stall bool
	// Slow makes FindUsersByMail answer after this long, even past the
	// context's deadline.
	Slow time.Duration
}

// FindUsersByMail matches mail ignoring case, like the directory, or fails with Err.
func (f *Directory) FindUsersByMail(ctx context.Context, mail string) ([]ldap.User, error) {
	if f.Stall {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	time.Sleep(f.Slow)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.CallCount++
	if f.Err != nil {
		return nil, f.Err
	}
	var out []ldap.User
	for _, u := range f.Users {
		if strings.EqualFold(u.Mail, mail) {
			out = append(out, u)
		}
	}
	return out, nil
}

// FindUserByAccount finds a user by exact account name, enabled or not, or
// fails with Err.
func (f *Directory) FindUserByAccount(_ context.Context, name string) (ldap.User, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.CallCount++
	if f.Err != nil {
		return ldap.User{}, false, f.Err
	}
	for _, u := range f.Users {
		if u.Username == name {
			return u, true, nil
		}
	}
	return ldap.User{}, false, nil
}

// Lock reports the lock in Locked, or fails with Err.
func (f *Directory) Lock(_ context.Context, dn string) (ldap.Lock, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.CallCount++
	if f.Err != nil {
		return ldap.Lock{}, f.Err
	}
	end, locked := f.Locked[dn]
	return ldap.Lock{Locked: locked, EndsAt: end}, nil
}

// GroupsByName resolves names ignoring case, or fails with Err.
func (f *Directory) GroupsByName(_ context.Context, names []string) (map[string]ldap.Group, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.CallCount++
	if f.Err != nil {
		return nil, f.Err
	}
	out := map[string]ldap.Group{}
	for _, n := range names {
		for name, g := range f.Groups {
			if strings.EqualFold(name, n) {
				out[name] = g
			}
		}
	}
	return out, nil
}

// InChainMemberOf reports a nesting listed in NestedIn, or fails with Err.
func (f *Directory) InChainMemberOf(_ context.Context, name string, parents []string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.CallCount++
	if f.Err != nil {
		return false, f.Err
	}
	for _, p := range f.NestedIn[name] {
		for _, want := range parents {
			if strings.EqualFold(p, want) {
				return true, nil
			}
		}
	}
	return false, nil
}

// UserInChainMemberOf reports a membership listed in UserIn, or fails with Err.
func (f *Directory) UserInChainMemberOf(_ context.Context, dn string, parents []string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.CallCount++
	if f.Err != nil {
		return false, f.Err
	}
	for _, g := range f.UserIn[dn] {
		for _, want := range parents {
			if strings.EqualFold(g, want) {
				return true, nil
			}
		}
	}
	return false, nil
}

// ClearLockout records the write and unlocks unless KeepLock, or fails with WriterErr.
func (f *Directory) ClearLockout(_ context.Context, dn string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.WriterErr != nil {
		return f.WriterErr
	}
	f.clears = append(f.clears, dn)
	if !f.KeepLock {
		delete(f.Locked, dn)
	}
	return nil
}

// Cleared lists the DNs ClearLockout wrote, in order.
func (f *Directory) Cleared() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.clears...)
}

// UserDN is a staff user DN in a department OU.
func UserDN(cn, dept string) string { return "CN=" + cn + ",OU=" + dept + ",OU=Users,OU=Noord," + Base }

// GroupDN is a group DN in OU=Groups.
func GroupDN(name string) string { return "CN=" + name + ",OU=Groups,OU=Noord," + Base }

// NewDirectory holds a small Noord: eva (locked), ruben, an admin account with
// a mail, a disabled user, and the groups the fence tests need.
func NewDirectory() *Directory {
	return &Directory{
		Users: []ldap.User{
			{DN: UserDN("Eva Peters", "Sales"), Username: "eva.peters", DisplayName: "Eva Peters", Department: "Sales", Mail: "eva.peters@noord.example", Enabled: true},
			{DN: UserDN("Ruben Mulder", "IT"), Username: "ruben.mulder", DisplayName: "Ruben Mulder", Department: "IT", Mail: "ruben.mulder@noord.example", Enabled: true},
			{DN: "CN=adm.ruben.mulder,OU=Admin Accounts,OU=Noord," + Base, Username: "adm.ruben.mulder", Mail: "adm@noord.example", Enabled: true},
			{DN: UserDN("Old Leaver", "Sales"), Username: "old.leaver", Mail: "old.leaver@noord.example", Enabled: false},
		},
		Locked: map[string]time.Time{UserDN("Eva Peters", "Sales"): time.Date(2026, 10, 18, 12, 15, 0, 0, time.UTC)},
		Groups: map[string]ldap.Group{
			"SG-Finance":          {DN: GroupDN("SG-Finance"), Name: "SG-Finance"},
			"APP-Helpdesk-Agents": {DN: GroupDN("APP-Helpdesk-Agents"), Name: "APP-Helpdesk-Agents"},
			"SYNC-Entra-Users":    {DN: GroupDN("SYNC-Entra-Users"), Name: "SYNC-Entra-Users"},
			"SG-IT-Admins":        {DN: "CN=SG-IT-Admins,OU=Admin Accounts,OU=Noord," + Base, Name: "SG-IT-Admins"},
			"Domain Admins":       {DN: "CN=Domain Admins,CN=Users," + Base, Name: "Domain Admins"},
			"T-Nested":            {DN: GroupDN("T-Nested"), Name: "T-Nested"},
			"Outside-Group":       {DN: "CN=Outside-Group,OU=Elsewhere," + Base, Name: "Outside-Group"},
		},
		NestedIn: map[string][]string{
			"T-Nested": {"CN=SG-IT-Admins,OU=Admin Accounts,OU=Noord," + Base, "CN=Domain Admins,CN=Users," + Base},
		},
	}
}
