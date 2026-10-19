package helpdesk

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/toyinogun/AIITsupport/internal/zammad"
)

// assistantTag is the tag the tool server puts on every assistant ticket
// (spec 0007 AC-10, spec 0009 AC-4); internal/tools is not imported here.
const assistantTag = "noord-assistant"

// covers: AC-9, AC-10 (spec 0010). After one forced run of noord-retention,
// the assistant's closed ticket past the window and its merged ticket past
// the window are gone (404). These remain: its closed ticket inside the
// window, a ticket whose first close (close_at) is past the window but whose
// last close is inside it (so the test fails if the condition reads
// close_at), a ticket merged inside the window, its open ticket with an old
// creation date, the merge target, and every seeded closed ticket.
func TestLiveRetentionScheduler(t *testing.T) {
	e := live(t)
	ctx := t.Context()
	days := e.cfg.TicketRetentionDays
	if _, err := runStep(ctx, e.runner, StepRetention, map[string]string{"NOORD_RETENTION_DAYS": strconv.Itoa(days)}, nil); err != nil {
		t.Fatalf("retention step: %v", err)
	}
	seededClosed := closedTitles(e.report(t))

	customer := evaPeters(t, e)
	create := func(title string) int {
		t.Helper()
		tk, err := e.assistant.CreateTicket(ctx, zammad.NewTicket{
			Title: title, Group: e.cfg.ZammadDefaultGroup, CustomerID: customer, Tags: []string{assistantTag, "live-test"},
			Article: zammad.NewArticle{Body: "Created by TestLiveRetentionScheduler.", ContentType: "text/plain", Type: zammad.ArticleNote, Sender: zammad.SenderAgent, Internal: true},
		}, "")
		if err != nil {
			t.Fatalf("assistant create %q: %v", title, err)
		}
		t.Cleanup(func() {
			err := e.admin.Do(context.Background(), zammad.Request{Method: http.MethodDelete, Path: fmt.Sprintf("/api/v1/tickets/%d", tk.ID)})
			if err != nil && zammad.Status(err) != http.StatusNotFound {
				t.Errorf("delete live test ticket %d: %v", tk.ID, err)
			}
		})
		return tk.ID
	}
	closeTicket := func(id int) {
		t.Helper()
		if _, err := e.assistant.UpdateTicket(ctx, id, zammad.TicketUpdate{State: "closed"}, ""); err != nil {
			t.Fatalf("close ticket %d: %v", id, err)
		}
	}
	closedOld, closedNew, openOld := create("retention closed old"), create("retention closed new"), create("retention open old")
	mergedOld, target := create("retention merged old"), create("retention merge target")
	reclosed, mergedNew := create("retention reclosed"), create("retention merged new")
	closeTicket(closedOld)
	closeTicket(closedNew)
	closeTicket(reclosed)

	// Set the times through Rails, then force one run of the scheduler.
	script := fmt.Sprintf(`
UserInfo.current_user_id = 1
old, recent = %[1]d.days.ago, %[2]d.days.ago
Ticket.find(%[3]d).update_columns(close_at: old, last_close_at: old)
Ticket.find(%[4]d).update_columns(close_at: recent, last_close_at: recent)
Ticket.find(%[5]d).update_columns(created_at: old, updated_at: old)
Ticket.find(%[6]d).merge_to(ticket_id: %[7]d, user_id: 1)
Ticket.find(%[6]d).update_columns(updated_at: old)
Ticket.find(%[8]d).update_columns(close_at: old, last_close_at: recent)
Ticket.find(%[9]d).merge_to(ticket_id: %[7]d, user_id: 1)
Ticket.find(%[9]d).update_columns(updated_at: recent)
Job.find_by!(name: 'noord-retention').run(true)
puts 'NOORD_RESULT {}'
`, days+5, max(days-5, 0), closedOld, closedNew, openOld, mergedOld, target, reclosed, mergedNew)
	railsEval(t, e, script)

	want := map[int]bool{
		closedOld: true, mergedOld: true,
		closedNew: false, reclosed: false, mergedNew: false, openOld: false, target: false,
	}
	for id, wantGone := range want {
		var got map[string]any
		err := e.admin.Do(ctx, zammad.Request{Method: http.MethodGet, Path: fmt.Sprintf("/api/v1/tickets/%d", id), Out: &got})
		gone := zammad.Status(err) == http.StatusNotFound
		if err != nil && !gone {
			t.Fatalf("read ticket %d: %v", id, err)
		}
		if gone != wantGone {
			t.Errorf("ticket %d gone = %v, want %v", id, gone, wantGone)
		}
	}
	if after := closedTitles(e.report(t)); strings.Join(after, "|") != strings.Join(seededClosed, "|") {
		t.Errorf("seeded closed tickets changed:\nbefore %v\nafter  %v", seededClosed, after)
	}
}

// closedTitles lists the seeded closed tickets (never tagged for the
// assistant) in the report's order.
func closedTitles(r report) []string {
	var out []string
	for _, tk := range r.Tickets {
		if tk.State == "closed" && !strings.Contains(strings.Join(tk.Tags, ","), assistantTag) {
			out = append(out, tk.Title)
		}
	}
	return out
}

// evaPeters is the customer id of eva.peters; the user search index lags a
// fresh import, so it waits up to 3 minutes.
func evaPeters(t *testing.T, e liveEnv) int {
	t.Helper()
	for range 90 {
		users, err := e.admin.SearchUsers(t.Context(), "eva.peters", 5)
		if err == nil && len(users) > 0 {
			return users[0].ID
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatal("search eva.peters: no result")
	return 0
}

// railsEval runs Ruby inside zammad-railsserver through `rails r -`, for
// test setup only; the seed's own steps go through ComposeRunner.
func railsEval(t *testing.T, e liveEnv, script string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), e.runner.Limit)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", "compose", "--profile", "helpdesk", "exec", "-T",
		"zammad-railsserver", "bundle", "exec", "rails", "r", "-")
	cmd.Dir = e.runner.Dir
	cmd.Stdin = strings.NewReader(script)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	if _, ok := findResult(out.Bytes()); err != nil || !ok {
		t.Fatalf("rails eval: %v\n%s", err, out.String())
	}
}
