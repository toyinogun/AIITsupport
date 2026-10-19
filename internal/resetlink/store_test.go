package resetlink_test

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/toyinogun/AIITsupport/internal/db/dbtest"
	"github.com/toyinogun/AIITsupport/internal/resetlink"
)

func issue(account string) resetlink.Issue {
	return resetlink.Issue{
		Account: account, RequesterSlackID: "U0EVA", ChannelID: "D0EVA", ThreadTS: "1.0",
		TicketID: 42, IssueCallID: uuid.New(), TTL: 15 * time.Minute,
	}
}

// covers: spec 0011 AC-4. A token is 43 base64url characters, never reused,
// and only its sha256 is stored.
func TestTokenShape(t *testing.T) {
	a, b := resetlink.NewToken(), resetlink.NewToken()
	if len(a) != resetlink.TokenLen || !resetlink.WellFormed(a) || a == b {
		t.Fatalf("tokens %q, %q: want two different well formed 43 character tokens", a, b)
	}
	for _, bad := range []string{"", "short", strings.Repeat("A", 42), strings.Repeat("A", 44), strings.Repeat("+", 43), a[:42] + "="} {
		if resetlink.WellFormed(bad) {
			t.Errorf("WellFormed(%q) = true, want false", bad)
		}
	}
	if len(resetlink.Hash(a)) != 32 {
		t.Errorf("hash is %d bytes, want 32", len(resetlink.Hash(a)))
	}
}

// covers: spec 0011 AC-4, AC-7, AC-11. Issue stores only the hash; a
// second link voids the first; Valid changes nothing; Claim works once.
func TestIssueVoidValidClaim(t *testing.T) {
	tdb := dbtest.New(t)
	s := resetlink.NewStore(tdb.ToolServer)
	ctx := t.Context()

	first, err := s.Issue(ctx, issue("eva.peters"))
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if first.Voided != 0 || !resetlink.WellFormed(first.Token) {
		t.Fatalf("first = %+v", first)
	}
	if d := time.Until(first.ExpiresAt); d < 14*time.Minute || d > 16*time.Minute {
		t.Errorf("expires in %s, want about 15m", d)
	}
	var plain int
	if err := tdb.ToolServer.QueryRow(ctx, "SELECT count(*) FROM reset_links WHERE position($1::bytea IN token_hash) > 0", []byte(first.Token)).Scan(&plain); err != nil || plain != 0 {
		t.Errorf("plain token found in the table: %d, %v", plain, err)
	}

	second, err := s.Issue(ctx, issue("eva.peters"))
	if err != nil || second.Voided != 1 {
		t.Fatalf("second Issue = %+v, %v; want one voided", second, err)
	}
	if _, err := s.Valid(ctx, first.Token); !errors.Is(err, resetlink.ErrNotValid) {
		t.Errorf("Valid(voided) = %v, want ErrNotValid", err)
	}
	for range 2 { // a preview fetch does not burn it
		if l, err := s.Valid(ctx, second.Token); err != nil || l.Account != "eva.peters" || l.TicketID != 42 {
			t.Fatalf("Valid = %+v, %v", l, err)
		}
	}
	claimed, err := s.Claim(ctx, second.Token)
	if err != nil || claimed.UsedAt.IsZero() {
		t.Fatalf("Claim = %+v, %v", claimed, err)
	}
	if _, err := s.Claim(ctx, second.Token); !errors.Is(err, resetlink.ErrNotValid) {
		t.Errorf("second Claim = %v, want ErrNotValid", err)
	}
	if n, err := s.CountRecent(ctx, "eva.peters", time.Hour); err != nil || n != 2 {
		t.Errorf("CountRecent = %d, %v; want 2", n, err)
	}
}

// covers: spec 0011 AC-12. A revert puts the claim back only for its own
// claim, and the link can be claimed again.
func TestRevertClaim(t *testing.T) {
	tdb := dbtest.New(t)
	s := resetlink.NewStore(tdb.ToolServer)
	ctx := t.Context()
	issued, err := s.Issue(ctx, issue("eva.peters"))
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := s.Claim(ctx, issued.Token)
	if err != nil {
		t.Fatal(err)
	}
	stale := claimed
	stale.UsedAt = claimed.UsedAt.Add(-time.Second)
	if ok, err := s.Revert(ctx, stale); err != nil || ok {
		t.Errorf("Revert with another used_at = %v, %v; want false", ok, err)
	}
	if ok, err := s.Revert(ctx, claimed); err != nil || !ok {
		t.Fatalf("Revert = %v, %v; want true", ok, err)
	}
	if _, err := s.Claim(ctx, issued.Token); err != nil {
		t.Errorf("Claim after revert: %v", err)
	}
}

// covers: spec 0011 AC-7. An expired link is not valid and cannot be claimed.
func TestExpiredLink(t *testing.T) {
	tdb := dbtest.New(t)
	s := resetlink.NewStore(tdb.ToolServer)
	ctx := t.Context()
	issued, err := s.Issue(ctx, issue("eva.peters"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tdb.Owner.Exec(ctx, "UPDATE reset_links SET expires_at = now() - interval '1 second'"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Valid(ctx, issued.Token); !errors.Is(err, resetlink.ErrNotValid) {
		t.Errorf("Valid(expired) = %v", err)
	}
	if _, err := s.Claim(ctx, issued.Token); !errors.Is(err, resetlink.ErrNotValid) {
		t.Errorf("Claim(expired) = %v", err)
	}
}

// covers: spec 0011 AC-4, AC-13. Concurrent issues leave one live link, and
// concurrent claims give exactly one winner.
func TestConcurrentIssueAndClaim(t *testing.T) {
	tdb := dbtest.New(t)
	s := resetlink.NewStore(tdb.ToolServer)
	ctx := t.Context()

	var wg sync.WaitGroup
	tokens := make([]string, 4)
	for i := range tokens {
		wg.Go(func() {
			issued, err := s.Issue(ctx, issue("eva.peters"))
			if err != nil {
				t.Errorf("Issue: %v", err)
				return
			}
			tokens[i] = issued.Token
		})
	}
	wg.Wait()
	var live int
	if err := tdb.ToolServer.QueryRow(ctx, "SELECT count(*) FROM reset_links WHERE status = 'live'").Scan(&live); err != nil || live != 1 {
		t.Fatalf("live links = %d, %v; want 1", live, err)
	}
	var token string
	for _, tok := range tokens {
		if _, err := s.Valid(ctx, tok); err == nil {
			token = tok
		}
	}

	var mu sync.Mutex
	wins := 0
	for range 4 {
		wg.Go(func() {
			if _, err := s.Claim(ctx, token); err == nil {
				mu.Lock()
				wins++
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	if wins != 1 {
		t.Errorf("claims won = %d, want 1", wins)
	}
}

// covers: spec 0011 AC-18. The cleanup deletes only links that expired
// longer ago than the retention.
func TestDeleteExpired(t *testing.T) {
	tdb := dbtest.New(t)
	s := resetlink.NewStore(tdb.ToolServer)
	ctx := t.Context()
	if _, err := s.Issue(ctx, issue("old.user")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Issue(ctx, issue("eva.peters")); err != nil {
		t.Fatal(err)
	}
	if _, err := tdb.Owner.Exec(ctx, "UPDATE reset_links SET expires_at = now() - interval '31 days' WHERE directory_account = 'old.user'"); err != nil {
		t.Fatal(err)
	}
	if n, err := s.DeleteExpired(ctx, 30); err != nil || n != 1 {
		t.Errorf("DeleteExpired = %d, %v; want 1", n, err)
	}
}

// covers: spec 0011 AC-3. CountRecent counts every link for the account
// created inside the window, whatever its status, and no other account's.
func TestCountRecent(t *testing.T) {
	tdb := dbtest.New(t)
	s := resetlink.NewStore(tdb.ToolServer)
	ctx := t.Context()
	for _, account := range []string{"eva.peters", "eva.peters", "eva.peters", "ruben.mulder"} {
		if _, err := s.Issue(ctx, issue(account)); err != nil {
			t.Fatal(err)
		}
	}
	if n, err := s.CountRecent(ctx, "eva.peters", 24*time.Hour); err != nil || n != 3 {
		t.Fatalf("CountRecent = %d, %v; want 3 (voided links count too)", n, err)
	}
	if _, err := tdb.Owner.Exec(ctx, `UPDATE reset_links SET created_at = now() - interval '25 hours'
		WHERE id = (SELECT id FROM reset_links WHERE directory_account = 'eva.peters' ORDER BY created_at LIMIT 1)`); err != nil {
		t.Fatal(err)
	}
	if n, err := s.CountRecent(ctx, "eva.peters", 24*time.Hour); err != nil || n != 2 {
		t.Errorf("CountRecent after one aged out = %d, %v; want 2", n, err)
	}
}

// covers: spec 0011 AC-12. A claim on a link that expired meanwhile is not
// reverted: the link stays used.
func TestRevertSkipsAnExpiredLink(t *testing.T) {
	tdb := dbtest.New(t)
	s := resetlink.NewStore(tdb.ToolServer)
	ctx := t.Context()
	issued, err := s.Issue(ctx, issue("eva.peters"))
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := s.Claim(ctx, issued.Token)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tdb.Owner.Exec(ctx, "UPDATE reset_links SET expires_at = now() - interval '1 second'"); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.Revert(ctx, claimed); err != nil || ok {
		t.Errorf("Revert(expired) = %v, %v; want false", ok, err)
	}
	var status string
	if err := tdb.ToolServer.QueryRow(ctx, "SELECT status FROM reset_links WHERE id = $1", claimed.ID).Scan(&status); err != nil || status != "used" {
		t.Errorf("status = %q, %v; want used", status, err)
	}
}
