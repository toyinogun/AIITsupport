package tools

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestLiveKBSearchOnZammad runs kb_search against the running Zammad as the
// noord-assistant token (spec 0014, the AC-17 probe as a test). It reads
// only, so any seeded Zammad works:
//
//	set -a && . ./.env && set +a && TEST_ZAMMAD=1 go test -race -run LiveKB -v ./internal/tools/
//
// covers: spec 0014 AC-2, AC-3, AC-4, AC-17
func TestLiveKBSearchOnZammad(t *testing.T) {
	d, _, store := liveDispatcher(t)
	d.cfg.KB.StaleAsOf = time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC) // the seed's as_of
	r := Requester{SlackID: "U01LIVE", Email: "eva.peters@noord.example", ChannelID: "C01LIVE", ThreadTS: "1760000700.000100"}
	search := func(query string) []map[string]any {
		t.Helper()
		resp := d.Call(t.Context(), Request{Tool: ToolKBSearch, Args: []byte(`{"query":"` + query + `"}`), Requester: r})
		if resp.IsError {
			t.Fatalf("kb_search %q = %+v (outcome %+v)", query, resp, lastRow(store))
		}
		raw, err := json.Marshal(resp.Data["results"])
		if err != nil {
			t.Fatal(err)
		}
		var out []map[string]any
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatal(err)
		}
		for _, a := range out {
			t.Logf("%q -> %v %q (%s, %s, stale %v, %d runes)", query, a["id"], a["title"], a["category"], a["updated"], a["stale"],
				len([]rune(a["body"].(string))))
		}
		return out
	}
	titles := func(results []map[string]any) []string {
		var out []string
		for _, a := range results {
			out = append(out, a["title"].(string))
		}
		return out
	}

	// The ranked order (AC-3): title matches outrank newer body only matches.
	vpn := search("vpn")
	if len(vpn) < 2 || vpn[0]["title"] != "Connect to the VPN (WireGuard)" || vpn[0]["stale"] != false ||
		vpn[1]["title"] != "Install the VPN client" || vpn[1]["stale"] != true {
		t.Errorf("vpn = %v, want WireGuard (current) then SecureLink (stale) first", titles(vpn))
	}
	for _, query := range []string{"locked", "lock"} {
		got := search(query)
		if len(got) < 2 || got[0]["title"] != "Locked out of your account" || got[1]["title"] != "My account is locked" || got[1]["stale"] != true {
			t.Errorf("%s = %v, want both lockout articles first, the 2020 one stale", query, titles(got))
		}
	}
	if none := search("meeting room"); len(none) != 0 {
		t.Errorf("meeting room = %v, want nothing", titles(none))
	}
	for _, a := range append(vpn, search("password")...) {
		body := a["body"].(string)
		if strings.Contains(body, "<") || strings.Contains(body, "http") {
			t.Errorf("%q body holds markup or a URL: %q", a["title"], body)
		}
	}

	// The portal link opens without sign in and shows the article.
	url, _ := vpn[0]["url"].(string)
	if url == "" {
		t.Fatal("no url; set ZAMMAD_PUBLIC_URL")
	}
	resp, err := http.Get(url) //nolint:gosec // a live test reading the dev portal
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	page, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(page), "WireGuard") {
		t.Errorf("GET %s = %d, want 200 with the article", url, resp.StatusCode)
	}
	if o := lastRow(store); o == nil {
		t.Error("no audit rows")
	}
}
