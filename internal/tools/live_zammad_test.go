package tools

import (
	"log/slog"
	"net/http"
	"os"
	"slices"
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
	if os.Getenv("TEST_ZAMMAD") != "1" {
		t.Skip("set TEST_ZAMMAD=1 to run against the running Zammad (after make helpdesk-reset)")
	}
	client, err := zammad.New(zammad.Options{
		BaseURL: os.Getenv("ZAMMAD_URL"), Auth: zammad.TokenAuth(os.Getenv("ZAMMAD_TOKEN")), Timeout: 15 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	desk := NewHelpdesk(client, os.Getenv("ZAMMAD_DEFAULT_GROUP"))

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
	reg, err := NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	d, err := NewDispatcher(reg, testConfig(), Systems{
		Directory: dir, Writer: dir, Helpdesk: desk, Recorder: rec, KillSwitch: NewKillSwitch("", true),
	}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	r := Requester{SlackID: "U01LIVE", Email: "eva.peters@noord.example", ChannelID: "C01LIVE", ThreadTS: "1760000500.000100"}

	created := d.Call(t.Context(), Request{Tool: ToolTicketCreate, Args: []byte(`{"title":"Live test: locked out","body":"Created by the tool layer's live test."}`), Requester: r})
	if created.IsError {
		t.Fatalf("ticket_create = %+v (outcome %+v)", created, lastRow(store))
	}
	id := created.Data["ticket_id"].(int)
	r.TicketID = int64(id)

	var tags struct {
		Tags []string `json:"tags"`
	}
	err = client.Do(t.Context(), zammad.Request{Method: http.MethodGet, Path: "/api/v1/tags?object=Ticket&o_id=" + ticketTarget(r.TicketID), Out: &tags})
	t.Logf("tags on create: %v (err %v)", tags.Tags, err)
	if err != nil || !slices.Contains(tags.Tags, assistantTag) {
		t.Errorf("ticket %d tags = %v, %v; want %s", id, tags.Tags, err, assistantTag)
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
	err = client.Do(t.Context(), zammad.Request{Method: http.MethodGet, Path: "/api/v1/ticket_articles/by_ticket/" + ticketTarget(r.TicketID), Out: &articles})
	if err != nil || len(articles) != 3 || articles[0].Internal || articles[0].Sender != zammad.SenderCustomer ||
		!articles[2].Internal || articles[2].Sender != zammad.SenderAgent || articles[2].Body != notePrefix+"Unlocked and confirmed." {
		t.Errorf("articles = %+v, %v; want the customer's first article and two internal agent notes", articles, err)
	}
	resp := d.Call(t.Context(), Request{Tool: ToolTicketUpdate, Args: []byte(`{"note":"more"}`), Requester: r})
	if resp.Code != CodeTicketClosed {
		t.Errorf("update on a closed ticket = %+v, want ticket_closed", resp)
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
