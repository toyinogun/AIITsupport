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

	"github.com/toyinogun/AIITsupport/internal/audit"
	"github.com/toyinogun/AIITsupport/internal/db/queries"
	"github.com/toyinogun/AIITsupport/internal/ldap"
)

// Base is the fake directory's base DN.
const Base = "DC=ad,DC=noord,DC=example"

// ErrNotUsed answers the approval reads the tool layer never makes.
var ErrNotUsed = errors.New("not used by this test")

// MemStore is an in memory audit.Store. It answers the history reads the
// same way the SQL does and, like Postgres, fails a query whose context has
// ended.
type MemStore struct {
	mu         sync.Mutex
	rows       []queries.AuditLog
	Approvals  []queries.CreateApprovalParams
	IntentErr  error // fails every intent insert
	OutcomeErr error // fails every outcome insert
	CountErr   error // fails the rate limit count
	ThreadErr  error // fails the thread reads (outcomes and ticket id)
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

// CreateApproval records the approval request.
func (s *MemStore) CreateApproval(_ context.Context, p queries.CreateApprovalParams) (queries.Approval, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Approvals = append(s.Approvals, p)
	return queries.Approval{ID: uuid.New(), CallID: p.CallID, ToolName: p.ToolName, Status: "pending"}, nil
}

// GetApproval is not used by the tool layer.
func (s *MemStore) GetApproval(context.Context, uuid.UUID) (queries.Approval, error) {
	return queries.Approval{}, ErrNotUsed
}

// ApproveApproval is not used by the tool layer.
func (s *MemStore) ApproveApproval(context.Context, queries.ApproveApprovalParams) (queries.Approval, error) {
	return queries.Approval{}, ErrNotUsed
}

// DenyApproval is not used by the tool layer.
func (s *MemStore) DenyApproval(context.Context, queries.DenyApprovalParams) (queries.Approval, error) {
	return queries.Approval{}, ErrNotUsed
}

// ExpireDueApprovals is not used by the tool layer.
func (s *MemStore) ExpireDueApprovals(context.Context, int32) ([]queries.Approval, error) {
	return nil, ErrNotUsed
}

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
