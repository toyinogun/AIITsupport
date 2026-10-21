package tools

import (
	"log/slog"
	"net/http"
	"os"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/toyinogun/AIITsupport/internal/audit"
	"github.com/toyinogun/AIITsupport/internal/tools/toolstest"
	"github.com/toyinogun/AIITsupport/internal/zammad"
)

// TestLiveTicketsOnZammad runs the ticket tools against the running Zammad
// as the noord-assistant token (spec 0007, build step 4). Run it right after
// make helpdesk-reset, with .env loaded, so it also measures the search lag:
//
//	set -a && . ./.env && set +a && TEST_ZAMMAD=1 go test -race -run LiveTickets -v ./internal/tools/
//
// It leaves a closed ticket behind; run make helpdesk-reset afterwards.
//
// covers: AC-10, AC-11, AC-19
func TestLiveTicketsOnZammad(t *testing.T) {
	d, client, store := liveDispatcher(t)
	r := Requester{SlackID: "U01LIVE", Email: "eva.peters@noord.example", ChannelID: "C01LIVE", ThreadTS: "1760000500.000100"}

	created := d.Call(t.Context(), Request{Tool: ToolTicketCreate, Args: []byte(`{"title":"Live test: locked out","body":"Created by the tool layer's live test."}`), Requester: r})
	if created.IsError {
		t.Fatalf("ticket_create = %+v (outcome %+v)", created, lastRow(store))
	}
	id := created.Data["ticket_id"].(int)
	r.TicketID = int64(id)

	if tags := liveTags(t, client, id); !slices.Contains(tags, assistantTag) {
		t.Errorf("ticket %d tags = %v; want %s", id, tags, assistantTag)
	}

	pending := time.Now().Add(48 * time.Hour).UTC().Format(time.RFC3339)
	for _, args := range []string{
		`{"state":"pending","pending_until":"` + pending + `","note":"Waiting for the user."}`,
		`{"state":"closed","note":"Unlocked and confirmed."}`,
	} {
		resp := d.Call(t.Context(), Request{Tool: ToolTicketUpdate, Args: []byte(args), Requester: r})
		if resp.IsError {
			t.Fatalf("ticket_update %s = %+v (outcome %+v)", args, resp, lastRow(store))
		}
		t.Logf("ticket_update %s -> state %v", args, resp.Data["state"])
	}
	// A plain GET on a ticket omits article_ids; read the articles instead.
	var articles []struct {
		Internal bool   `json:"internal"`
		Sender   string `json:"sender"`
		Body     string `json:"body"`
	}
	err := client.Do(t.Context(), zammad.Request{Method: http.MethodGet, Path: "/api/v1/ticket_articles/by_ticket/" + ticketTarget(r.TicketID), Out: &articles})
	if err != nil || len(articles) != 3 || articles[0].Internal || articles[0].Sender != zammad.SenderCustomer ||
		!articles[2].Internal || articles[2].Sender != zammad.SenderAgent || articles[2].Body != notePrefix+"Unlocked and confirmed." {
		t.Errorf("articles = %+v, %v; want the customer's first article and two internal agent notes", articles, err)
	}
	resp := d.Call(t.Context(), Request{Tool: ToolTicketUpdate, Args: []byte(`{"note":"more"}`), Requester: r})
	if resp.Code != CodeTicketClosed {
		t.Errorf("update on a closed ticket = %+v, want ticket_closed", resp)
	}
}

// liveDispatcher is the production registry over the running Zammad (as the
// noord-assistant token), a fake directory, and an in memory audit store.
// It waits until eva.peters shows up in Zammad's user search.
func liveDispatcher(t *testing.T) (*Dispatcher, *zammad.Client, *toolstest.MemStore) {
	t.Helper()
	if os.Getenv("TEST_ZAMMAD") != "1" {
		t.Skip("set TEST_ZAMMAD=1 to run against the running Zammad (after make helpdesk-reset)")
	}
	client, err := zammad.New(zammad.Options{
		BaseURL: os.Getenv("ZAMMAD_URL"), Auth: zammad.TokenAuth(os.Getenv("ZAMMAD_TOKEN")), Timeout: 15 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	desk := NewHelpdesk(client, os.Getenv("ZAMMAD_DEFAULT_GROUP"), os.Getenv("ZAMMAD_PUBLIC_URL"))

	// Zammad's search index lags a fresh LDAP import; measure it.
	start := time.Now()
	for {
		_, ok, err := desk.customer(t.Context(), "eva.peters")
		if err != nil {
			t.Fatalf("search eva.peters: %v", err)
		}
		if ok {
			break
		}
		if time.Since(start) > 5*time.Minute {
			t.Fatal("eva.peters never appeared in Zammad's user search")
		}
		time.Sleep(time.Second)
	}
	t.Logf("search lag after reset: eva.peters found after %s", time.Since(start).Round(time.Second))

	store := &toolstest.MemStore{}
	dir := toolstest.NewDirectory()
	rec, err := audit.NewRecorder(store, audit.Config{WriteTimeout: time.Second, MaxPayloadBytes: 4096}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	reg, err := NewRegistry(8 * time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	d, err := NewDispatcher(reg, testConfig(), Systems{
		Directory: dir, Groups: dir, Reader: dir, Writer: dir, Helpdesk: desk, Recorder: rec, KillSwitch: NewKillSwitch("", true),
	}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	return d, client, store
}

// liveTags reads a ticket's tags from Zammad.
func liveTags(t *testing.T, client *zammad.Client, id int) []string {
	t.Helper()
	var tags struct {
		Tags []string `json:"tags"`
	}
	if err := client.Do(t.Context(), zammad.Request{Method: http.MethodGet, Path: "/api/v1/tags?object=Ticket&o_id=" + strconv.Itoa(id), Out: &tags}); err != nil {
		t.Fatalf("read tags of ticket %d: %v", id, err)
	}
	return tags.Tags
}

// TestLiveEscalateOnZammad runs escalate against the running Zammad (spec
// 0009, build step 1): tags and an internal note on the thread's ticket,
// and a new ticket with three tags after a blocked attempt. Like the test
// above, run it after make helpdesk-reset with .env loaded; it leaves two
// open tickets behind.
//
//	set -a && . ./.env && set +a && TEST_ZAMMAD=1 go test -race -run LiveEscalate -v ./internal/tools/
//
// covers: spec 0009 AC-4
func TestLiveEscalateOnZammad(t *testing.T) {
	d, client, store := liveDispatcher(t)
	call := func(tool, args string, r Requester) Response {
		t.Helper()
		resp := d.Call(t.Context(), Request{Tool: tool, Args: []byte(args), Requester: r})
		t.Logf("%s -> %+v", tool, resp.Data)
		return resp
	}
	thread := Requester{SlackID: "U01LIVE", Email: "eva.peters@noord.example", ChannelID: "C01LIVE", ThreadTS: "1760000600.000100"}
	created := call(ToolTicketCreate, `{"title":"Live test: Eva's account","body":"Created by the escalate live test."}`, thread)
	if created.IsError {
		t.Fatalf("ticket_create = %+v (outcome %+v)", created, lastRow(store))
	}
	id := created.Data["ticket_id"].(int)

	// No _meta id: the ticket is found in the audit log, tagged, and noted.
	resp := call(ToolEscalate, `{"reason":"other_person","title":"Live test","summary":"Asked to unlock a colleague."}`, thread)
	if resp.IsError || resp.Data["ticket_id"] != id || resp.Data["created"] != false {
		t.Fatalf("escalate = %+v (outcome %+v)", resp, lastRow(store))
	}
	if got := liveTags(t, client, id); !slices.Contains(got, tagEscalated) || slices.Contains(got, tagPrivileged) {
		t.Errorf("tags = %v, want escalated and not privileged-request", got)
	}
	var articles []struct {
		Internal bool   `json:"internal"`
		Body     string `json:"body"`
	}
	err := client.Do(t.Context(), zammad.Request{Method: http.MethodGet, Path: "/api/v1/ticket_articles/by_ticket/" + strconv.Itoa(id), Out: &articles})
	if err != nil || len(articles) != 2 || !articles[1].Internal ||
		articles[1].Body != "Escalated by the Noord assistant (other_person):\nAsked to unlock a colleague." {
		t.Errorf("articles = %+v, %v; want the internal escalation note", articles, err)
	}

	// The printer hook: blocked, then escalate with no ticket creates one.
	printer := thread
	printer.ThreadTS = "1760000600.000200"
	if resp := call(ToolPrivilegedAccessChange, `{"summary":"Domain Admin"}`, printer); resp.Code != CodeBlocked {
		t.Fatalf("privileged_access_change = %+v", resp)
	}
	resp = call(ToolEscalate, `{"reason":"privileged_request","title":"Live test: printer on the 2nd floor","summary":"Asked for Domain Admin to fix the printer. Refused."}`, printer)
	if resp.IsError || resp.Data["created"] != true || resp.Data["privileged"] != true {
		t.Fatalf("privileged escalate = %+v (outcome %+v)", resp, lastRow(store))
	}
	got := liveTags(t, client, resp.Data["ticket_id"].(int))
	for _, want := range []string{assistantTag, tagEscalated, tagPrivileged} {
		if !slices.Contains(got, want) {
			t.Errorf("new ticket tags = %v, want %s", got, want)
		}
	}
}

func lastRow(s *toolstest.MemStore) any {
	rows := s.All()
	if len(rows) == 0 {
		return nil
	}
	last := rows[len(rows)-1]
	return map[string]any{"outcome": last.Outcome.String, "error": last.Error.String}
}
