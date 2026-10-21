package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/toyinogun/AIITsupport/internal/zammad"
	"github.com/toyinogun/AIITsupport/internal/zammad/zammadtest"
)

func kbDay(s string) time.Time {
	t, err := time.Parse(time.DateOnly, s)
	if err != nil {
		panic(err)
	}
	return t.Add(7 * time.Hour) // morning UTC, the same date in Amsterdam
}

func kbPtr(t time.Time) *time.Time { return &t }

// kbAnswer is a published answer in category 3.
func kbAnswer(title, body, updated string) zammadtest.KBAnswer {
	return zammadtest.KBAnswer{CategoryID: 3, Title: title, Body: body, Updated: kbDay(updated), Published: kbPtr(kbDay("2020-01-01"))}
}

// results decodes a kb_search response's results the way the model gets
// them: through JSON.
func results(t *testing.T, resp Response) []map[string]any {
	t.Helper()
	raw, err := json.Marshal(resp.Data)
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Results []map[string]any `json:"results"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out.Results
}

// covers: spec 0014 AC-2, AC-3, AC-4. One search returns published answers
// ranked by score (a tie goes to the later update) with plain text bodies,
// the Amsterdam date, the code computed stale flag, and a portal link; the
// audit row keeps only count, answer_ids, and stale_ids.
func TestKBSearch(t *testing.T) {
	h := newHarness(t)
	h.zammad.AddKBCategory(3, "VPN and remote work")
	old := h.zammad.AddKBAnswer(kbAnswer("Install the VPN client",
		`<p>To work from home you need the <a href="https://evil.example/x">SecureLink</a> VPN client.</p><img src="https://evil.example/i.png">`, "2021-02-08"))
	current := h.zammad.AddKBAnswer(kbAnswer("Connect to the VPN (WireGuard)",
		"<p>Since October 2024 the Noord VPN is WireGuard.</p><ol><li>Install WireGuard.</li><li>Ask IT for your tunnel file.</li></ol>", "2026-04-20"))

	resp := h.call(ToolKBSearch, `{"query":"  vpn  "}`, eva())
	if resp.IsError || resp.Text != "Found 2 knowledge base articles." {
		t.Fatalf("response = %+v", resp)
	}
	if resp.Data["count"] != 2 || resp.Data["stale_after_days"] != 730 {
		t.Errorf("data = %+v", resp.Data)
	}
	if _, ok := resp.Data["answer_ids"]; ok {
		t.Error("the model sees answer_ids")
	}
	if _, ok := resp.Data["stale_ids"]; ok {
		t.Error("the model sees stale_ids")
	}
	got := results(t, resp)
	want := []map[string]any{
		{
			"id": float64(current.ID), "title": "Connect to the VPN (WireGuard)", "category": "VPN and remote work",
			"updated": "2026-04-20", "stale": false,
			"body": "Since October 2024 the Noord VPN is WireGuard.\n- Install WireGuard.\n- Ask IT for your tunnel file.",
			"url":  "http://zammad.test/help/en-us/3/" + strconv.Itoa(current.ID),
		},
		{
			"id": float64(old.ID), "title": "Install the VPN client", "category": "VPN and remote work",
			"updated": "2021-02-08", "stale": true,
			"body": "To work from home you need the SecureLink VPN client.",
			"url":  "http://zammad.test/help/en-us/3/" + strconv.Itoa(old.ID),
		},
	}
	if len(got) != len(want) {
		t.Fatalf("results = %+v", got)
	}
	for i := range want {
		for k, v := range want[i] {
			if got[i][k] != v {
				t.Errorf("result %d %s = %#v, want %#v", i, k, got[i][k], v)
			}
		}
		if len(got[i]) != len(want[i]) {
			t.Errorf("result %d keys = %v", i, got[i])
		}
	}
	for _, r := range got {
		if strings.Contains(r["body"].(string), "evil.example") {
			t.Errorf("an article URL reached the model: %q", r["body"])
		}
	}

	o := h.lastOutcome()
	if o.Outcome.String != "ok" || o.Target.Valid || string(h.lastIntent().Arguments) != `{"_omitted":1}` {
		t.Errorf("outcome %s target %v arguments %s", o.Outcome.String, o.Target, h.lastIntent().Arguments)
	}
	wantResult := `{"_omitted":2,"answer_ids":[` + strconv.Itoa(current.ID) + `,` + strconv.Itoa(old.ID) + `],"count":2,"stale_ids":[` + strconv.Itoa(old.ID) + `]}`
	if string(o.Result) != wantResult {
		t.Errorf("stored result = %s, want %s", o.Result, wantResult)
	}
	for _, r := range h.zammad.Requests() {
		if r.Path == "/api/v1/knowledge_bases/search" && r.Body["query"] != "vpn" {
			t.Errorf("query sent = %q, want the trimmed query", r.Body["query"])
		}
	}
}

// covers: spec 0014 AC-2, AC-8. No match is an ok outcome with count 0.
func TestKBSearchNoMatch(t *testing.T) {
	h := newHarness(t)
	h.zammad.AddKBAnswer(kbAnswer("Connect to the VPN", "<p>x</p>", "2026-04-20"))
	resp := h.call(ToolKBSearch, `{"query":"meeting room"}`, eva())
	if resp.IsError || resp.Data["count"] != 0 || resp.Text != "No knowledge base articles found." || len(results(t, resp)) != 0 {
		t.Fatalf("response = %+v", resp)
	}
	if o := h.lastOutcome(); o.Outcome.String != "ok" || string(o.Result) != `{"_omitted":2,"answer_ids":[],"count":0,"stale_ids":[]}` {
		t.Errorf("outcome %s result %s", o.Outcome.String, o.Result)
	}
}

// covers: spec 0014 AC-3 (the security model). Drafts, internal, scheduled,
// and archived answers never reach the model, whatever the search returns.
func TestKBSearchPublishedOnly(t *testing.T) {
	h := newHarness(t)
	now := h.d.now()
	past, future := kbPtr(now.Add(-time.Hour)), kbPtr(now.Add(time.Hour))
	keep := h.zammad.AddKBAnswer(kbAnswer("Printer published", "<p>printer</p>", "2026-01-01"))
	h.zammad.AddKBAnswer(zammadtest.KBAnswer{CategoryID: 3, Title: "Printer draft", Body: "printer", Updated: kbDay("2026-09-01")})
	h.zammad.AddKBAnswer(zammadtest.KBAnswer{CategoryID: 3, Title: "Printer internal", Body: "printer", Updated: kbDay("2026-09-02"), Internal: past})
	// Once internal, now published: Zammad's rule ignores internal_at.
	wasInternal := h.zammad.AddKBAnswer(zammadtest.KBAnswer{CategoryID: 3, Title: "Printer was internal", Body: "printer", Updated: kbDay("2025-06-01"), Internal: past, Published: past})
	h.zammad.AddKBAnswer(zammadtest.KBAnswer{CategoryID: 3, Title: "Printer scheduled", Body: "printer", Updated: kbDay("2026-09-03"), Published: future})
	h.zammad.AddKBAnswer(zammadtest.KBAnswer{CategoryID: 3, Title: "Printer archived", Body: "printer", Updated: kbDay("2026-09-04"), Published: past, Archived: past})
	// Archived only from tomorrow: still published today.
	later := h.zammad.AddKBAnswer(zammadtest.KBAnswer{CategoryID: 3, Title: "Printer until tomorrow", Body: "printer", Updated: kbDay("2025-01-01"), Published: past, Archived: future})

	h.d.cfg.KB.SearchLimit = 5
	ids := resultIDs(t, h.call(ToolKBSearch, `{"query":"printer"}`, eva()))
	if want := []int{keep.ID, wasInternal.ID, later.ID}; !slices.Equal(ids, want) {
		t.Errorf("ids = %v, want %v", ids, want)
	}
}

// answersRead counts the distinct answers a search read.
func answersRead(h *harness) int {
	read := map[string]bool{}
	for _, r := range h.zammad.Requests() {
		if r.Method == http.MethodGet && strings.Contains(r.Path, "/answers/") {
			read[r.Path] = true
		}
	}
	return len(read)
}

// resultIDs are the answer ids of a kb_search response, in order.
func resultIDs(t *testing.T, resp Response) []int {
	t.Helper()
	var ids []int
	for _, r := range results(t, resp) {
		ids = append(ids, int(r["id"].(float64)))
	}
	return ids
}

// covers: spec 0014 AC-2, AC-3. At most KB_SEARCH_LIMIT answers come back,
// but every candidate is read and ranked, and Zammad is asked for twice
// KB_SEARCH_CANDIDATES hits.
func TestKBSearchLimit(t *testing.T) {
	h := newHarness(t)
	for i := range 5 {
		h.zammad.AddKBAnswer(kbAnswer("Email "+strconv.Itoa(i), "<p>mail</p>", "2026-0"+strconv.Itoa(i+1)+"-01"))
	}
	resp := h.call(ToolKBSearch, `{"query":"mail"}`, eva())
	if resp.Data["count"] != 3 || len(results(t, resp)) != 3 {
		t.Fatalf("response = %+v", resp)
	}
	if n := answersRead(h); n != 5 {
		t.Errorf("answers read = %d, want all 5 candidates", n)
	}
	for _, r := range h.zammad.Requests() {
		if r.Path == "/api/v1/knowledge_bases/search" && r.Body["limit"] != float64(20) {
			t.Errorf("search limit = %v, want 2 x KB_SEARCH_CANDIDATES", r.Body["limit"])
		}
	}
}

// covers: spec 0014 AC-3. Candidates are the first KB_SEARCH_CANDIDATES
// hits, counted before any read: a skipped one (404) frees no slot, so an
// older match past the cap is never read, however well it would score.
func TestKBSearchCandidateCap(t *testing.T) {
	h := newHarness(t)
	h.d.cfg.KB.SearchCandidates = 3
	best := h.zammad.AddKBAnswer(kbAnswer("Mail rules", "<p>mail</p>", "2020-01-01")) // oldest: fourth in Zammad's order
	a := h.zammad.AddKBAnswer(kbAnswer("One", "<p>mail</p>", "2026-03-01"))
	gone := h.zammad.AddKBAnswer(kbAnswer("Two", "<p>mail</p>", "2026-02-01"))
	b := h.zammad.AddKBAnswer(kbAnswer("Three", "<p>mail</p>", "2026-01-01"))
	h.zammad.Intercept = func(r *http.Request) (zammadtest.Fault, bool) {
		return zammadtest.Fault{Status: http.StatusNotFound}, r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/answers/"+strconv.Itoa(gone.ID))
	}
	resp := h.call(ToolKBSearch, `{"query":"mail"}`, eva())
	if got := resultIDs(t, resp); !slices.Equal(got, []int{a.ID, b.ID}) {
		t.Errorf("ids = %v, want %v (never %d)", got, []int{a.ID, b.ID}, best.ID)
	}
	if n := answersRead(h); n != 3 {
		t.Errorf("answers read = %d, want 3", n)
	}
}

// covers: spec 0014 AC-3. The seeded lock and vpn cases: title matches
// outrank newer body only matches, "lock" starts "locked" but not "block",
// and an answer Zammad matched that scores 0 still ranks, last.
func TestKBSearchRanking(t *testing.T) {
	h := newHarness(t)
	h.d.cfg.KB.SearchLimit = 5
	lockout := h.zammad.AddKBAnswer(kbAnswer("Locked out of your account", "<p>Your account locks after five tries.</p>", "2026-02-02"))
	wfh := h.zammad.AddKBAnswer(kbAnswer("Working from home", "<p>Lock your screen. Use the VPN.</p>", "2025-08-25"))
	forgot := h.zammad.AddKBAnswer(kbAnswer("Forgot your password?", "<p>Do not lock yourself out.</p>", "2025-06-02"))
	blocked := h.zammad.AddKBAnswer(kbAnswer("Blocked senders", "<p>Mail from a blocked sender is dropped.</p>", "2026-09-01"))
	old := h.zammad.AddKBAnswer(kbAnswer("My account is locked", "<p>The lockout lasts 30 minutes.</p>", "2020-01-13"))

	// The fake matches substrings, so "Blocked senders" is a hit Zammad would
	// not make; its score is 0 ("lock" does not start "blocked").
	got := resultIDs(t, h.call(ToolKBSearch, `{"query":"lock"}`, eva()))
	if want := []int{lockout.ID, old.ID, wfh.ID, forgot.ID, blocked.ID}; !slices.Equal(got, want) {
		t.Errorf("lock: ids = %v, want %v", got, want)
	}

	wireguard := h.zammad.AddKBAnswer(kbAnswer("Connect to the VPN (WireGuard)", "<p>The VPN is WireGuard.</p>", "2026-04-20"))
	policy := h.zammad.AddKBAnswer(kbAnswer("Password policy (2025)", "<p>Also for the VPN.</p>", "2025-09-01"))
	securelink := h.zammad.AddKBAnswer(kbAnswer("Install the VPN client", "<p>The SecureLink VPN client.</p>", "2021-02-08"))
	h.d.cfg.KB.SearchLimit = 3
	got = resultIDs(t, h.call(ToolKBSearch, `{"query":"vpn"}`, eva()))
	if want := []int{wireguard.ID, securelink.ID, policy.ID}; !slices.Equal(got, want) {
		t.Errorf("vpn: ids = %v, want %v", got, want)
	}
}

// covers: spec 0014 AC-3. A hit in another locale never reaches the model
// and is never read.
func TestKBSearchDropsOtherLocales(t *testing.T) {
	h := newHarness(t)
	dutch := kbAnswer("VPN thuis", "<p>vpn</p>", "2026-05-01")
	dutch.Locale = "nl-nl"
	h.zammad.AddKBAnswer(dutch)
	english := h.zammad.AddKBAnswer(kbAnswer("VPN from home", "<p>vpn</p>", "2026-04-01"))

	resp := h.call(ToolKBSearch, `{"query":"vpn"}`, eva())
	if got := resultIDs(t, resp); !slices.Equal(got, []int{english.ID}) {
		t.Errorf("ids = %v, want only the en-us answer %d", got, english.ID)
	}
	if n := answersRead(h); n != 1 {
		t.Errorf("answers read = %d, want 1", n)
	}
}

// covers: spec 0014 AC-3. Candidates are the first n distinct en-us answer
// hits in Zammad's order: a second hit for the same answer and a hit in
// another locale take no slot.
func TestKBCandidateHits(t *testing.T) {
	hits := []zammad.KBHit{
		{TranslationID: 11, AnswerID: 1, Locale: "en-us"},
		{TranslationID: 12, AnswerID: 1, Locale: "en-us"}, // the same answer again
		{TranslationID: 21, AnswerID: 2, Locale: "nl-nl"},
		{TranslationID: 31, AnswerID: 3, Locale: "en-us"},
		{TranslationID: 41, AnswerID: 4, Locale: "en-us"},
	}
	got := kbCandidateHits(hits, 2)
	if want := []zammad.KBHit{hits[0], hits[3]}; !slices.Equal(got, want) {
		t.Errorf("candidates = %+v, want %+v", got, want)
	}
	if got := kbCandidateHits(hits, 10); len(got) != 3 {
		t.Errorf("with room for all: %d candidates, want 3", len(got))
	}
}

// covers: spec 0014 AC-3. When the score and the full updated_at tie, the
// candidate Zammad listed first ranks first, whatever order they arrive in.
func TestKBRankTieKeepsZammadOrder(t *testing.T) {
	same := kbDay("2026-04-01")
	cand := func(id, order int) kbCandidate {
		return kbCandidate{read: kbRead{tr: zammad.AnswerTranslation{AnswerID: id, UpdatedAt: same}, ok: true}, score: 3, order: order}
	}
	ranked := kbRank([]kbCandidate{cand(30, 2), cand(10, 0), cand(40, 3), cand(20, 1)}, 3)
	var ids []int
	for _, c := range ranked {
		ids = append(ids, c.read.tr.AnswerID)
	}
	if want := []int{10, 20, 30}; !slices.Equal(ids, want) {
		t.Errorf("ids = %v, want %v", ids, want)
	}
}

// bodilessAnswers is a Zammad that answers GetAnswer with the translation
// but never its content.
type bodilessAnswers struct {
	HelpdeskAPI
	contentIDs []int
}

func (b *bodilessAnswers) GetAnswer(_ context.Context, _, answerID int, contentIDs ...int) (zammad.Answer, error) {
	b.contentIDs = append(b.contentIDs, contentIDs...)
	raw := func(s string) json.RawMessage { return json.RawMessage(s) }
	return zammad.Answer{ID: answerID, Assets: map[string]json.RawMessage{
		"KnowledgeBaseAnswer": raw(`{"7":{"id":7,"category_id":3,"published_at":"2020-01-01T00:00:00Z"}}`),
		"KnowledgeBaseAnswerTranslation": raw(`{"70":{"id":70,"title":"VPN","kb_locale_id":1,"answer_id":7,` +
			`"content_id":700,"updated_at":"2026-04-01T07:00:00Z"}}`),
	}}, nil
}

// covers: spec 0014 AC-12. An answer that sends no body, even when asked
// for its translation's own content, fails the read (Zammad changed shape)
// instead of reaching the model empty.
func TestKBAnswerWithoutBody(t *testing.T) {
	api := &bodilessAnswers{}
	h := NewHelpdesk(api, "IT Support", "")
	_, _, err := h.answerTranslation(t.Context(), zammadtest.KBID, zammad.KBHit{TranslationID: 70, AnswerID: 7, Locale: "en-us"})
	if err == nil {
		t.Fatal("answerTranslation accepted an answer with no body")
	}
	if want := []int{70, 700}; !slices.Equal(api.contentIDs, want) {
		t.Errorf("content ids asked = %v, want the translation id, then its content_id %v", api.contentIDs, want)
	}
}

// covers: spec 0014 AC-3. With no scorable query word every score is 0, so
// the later update wins; a body word past KB_BODY_MAX_RUNES still scores.
func TestKBSearchZeroScores(t *testing.T) {
	h := newHarness(t)
	older := h.zammad.AddKBAnswer(kbAnswer("Laptop", "<p>pc pc</p>", "2025-01-01"))
	newer := h.zammad.AddKBAnswer(kbAnswer("Desktop", "<p>pc</p>", "2026-01-01"))
	if got := resultIDs(t, h.call(ToolKBSearch, `{"query":"pc"}`, eva())); !slices.Equal(got, []int{newer.ID, older.ID}) {
		t.Errorf("pc: ids = %v", got)
	}

	h.d.cfg.KB.BodyMaxRunes = 200
	long := h.zammad.AddKBAnswer(kbAnswer("Old printer notes", "<p>"+strings.Repeat("filler ", 60)+"toner</p>", "2019-01-01"))
	h.zammad.AddKBAnswer(kbAnswer("Printer basics", "<p>Paper goes in tray two.</p>", "2026-05-01"))
	resp := h.call(ToolKBSearch, `{"query":"printer toner"}`, eva())
	got := results(t, resp)
	if len(got) == 0 || int(got[0]["id"].(float64)) != long.ID || strings.Contains(got[0]["body"].(string), "toner") {
		t.Errorf("printer toner: results = %+v", got)
	}
}

func TestKBQueryWordsAndScore(t *testing.T) {
	if got := kbQueryWords("How do I get the VPN, vpn and Café-WiFi into 2FA?"); !slices.Equal(got, []string{"vpn", "café", "wifi", "2fa"}) {
		t.Errorf("query words = %q", got)
	}
	if got := kbQueryWords("pc is ok"); len(got) != 0 {
		t.Errorf("short words kept: %q", got)
	}
	words := kbQueryWords("lock vpn")
	for _, tc := range []struct {
		title, body string
		want        int
	}{
		{"Locked out", "your account locks", 4},
		{"Locked out", "nothing", 3},
		{"Working from home", "Lock the screen, use the VPN", 2},
		{"Blocked senders", "unblock them", 0},
		{"VPN lockout", "", 6},
	} {
		if got := kbScore(words, tc.title, tc.body); got != tc.want {
			t.Errorf("score(%q, %q) = %d, want %d", tc.title, tc.body, got, tc.want)
		}
	}
}

// covers: spec 0014 AC-3. An answer whose category is not in the cached map
// has an empty category and no link; the init read is cached.
func TestKBSearchUnknownCategoryAndCache(t *testing.T) {
	h := newHarness(t)
	h.zammad.AddKBAnswer(zammadtest.KBAnswer{CategoryID: 9, Title: "New one", Body: "<p>label</p>", Updated: kbDay("2026-01-01"), Published: kbPtr(kbDay("2026-01-01"))})
	for range 2 {
		got := results(t, h.call(ToolKBSearch, `{"query":"label"}`, eva()))
		if len(got) != 1 || got[0]["category"] != "" || got[0]["url"] != nil {
			t.Fatalf("results = %+v", got)
		}
	}
	inits := 0
	for _, r := range h.zammad.Requests() {
		if r.Path == "/api/v1/knowledge_bases/init" {
			inits++
		}
	}
	if inits != 1 {
		t.Errorf("init reads = %d, want 1 (cached)", inits)
	}
}

// covers: spec 0014 AC-12. A Zammad error on the init read, the search, or
// an answer read fails the call with unavailable and no partial results; a
// 404 on one hit skips it; a failed init read is tried again next call.
func TestKBSearchFailures(t *testing.T) {
	// failOn fails the requests match picks; second is the older answer's id.
	failOn := func(t *testing.T, match func(r *http.Request, second int) bool) (*harness, Response) {
		h := newHarness(t)
		h.zammad.AddKBCategory(3, "VPN and remote work")
		h.zammad.AddKBAnswer(kbAnswer("VPN one", "<p>vpn</p>", "2026-04-20"))
		second := h.zammad.AddKBAnswer(kbAnswer("VPN two", "<p>vpn</p>", "2025-04-20")).ID
		h.zammad.Intercept = func(r *http.Request) (zammadtest.Fault, bool) {
			return zammadtest.Fault{Status: http.StatusInternalServerError, Message: "boom"}, match(r, second)
		}
		return h, h.call(ToolKBSearch, `{"query":"vpn"}`, eva())
	}
	for name, match := range map[string]func(*http.Request, int) bool{
		"init":   func(r *http.Request, _ int) bool { return r.URL.Path == "/api/v1/knowledge_bases/init" },
		"search": func(r *http.Request, _ int) bool { return r.URL.Path == "/api/v1/knowledge_bases/search" },
		// The first answer reads fine; the second fails, so nothing partial comes back.
		"answer": func(r *http.Request, second int) bool {
			return r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/answers/"+strconv.Itoa(second))
		},
	} {
		t.Run(name, func(t *testing.T) {
			h, resp := failOn(t, match)
			wantCode(t, resp, CodeUnavailable)
			if len(resp.Data) != 1 {
				t.Errorf("partial results leaked: %+v", resp.Data)
			}
			if o := h.lastOutcome(); o.Outcome.String != "error" || o.Error.String != reasonHelpdeskDown {
				t.Errorf("outcome %s %q", o.Outcome.String, o.Error.String)
			}
			if !strings.Contains(h.logs.String(), `"cause":"zammad_500"`) {
				t.Errorf("logs lack the cause token: %s", h.logs)
			}
		})
	}

	t.Run("init retried", func(t *testing.T) {
		h, _ := failOn(t, func(r *http.Request, _ int) bool { return r.URL.Path == "/api/v1/knowledge_bases/init" })
		h.zammad.Intercept = nil
		if resp := h.call(ToolKBSearch, `{"query":"vpn"}`, eva()); resp.IsError || resp.Data["count"] != 2 {
			t.Errorf("after a failed init: %+v", resp)
		}
	})

	t.Run("404 skipped", func(t *testing.T) {
		h := newHarness(t)
		gone := h.zammad.AddKBAnswer(kbAnswer("VPN gone", "<p>vpn</p>", "2026-05-01"))
		kept := h.zammad.AddKBAnswer(kbAnswer("VPN kept", "<p>vpn</p>", "2026-04-01"))
		h.zammad.Intercept = func(r *http.Request) (zammadtest.Fault, bool) {
			return zammadtest.Fault{Status: http.StatusNotFound}, r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/answers/"+strconv.Itoa(gone.ID))
		}
		got := results(t, h.call(ToolKBSearch, `{"query":"vpn"}`, eva()))
		if len(got) != 1 || int(got[0]["id"].(float64)) != kept.ID {
			t.Errorf("results = %+v", got)
		}
	})

	t.Run("timeout on the last candidate", func(t *testing.T) {
		h := newHarness(t)
		h.zammad.AddKBAnswer(kbAnswer("VPN one", "<p>vpn</p>", "2026-04-20"))
		last := h.zammad.AddKBAnswer(kbAnswer("VPN last", "<p>nothing</p>", "2019-04-20")).ID
		h.zammad.Intercept = func(r *http.Request) (zammadtest.Fault, bool) {
			if strings.HasSuffix(r.URL.Path, "/answers/"+strconv.Itoa(last)) {
				<-r.Context().Done()
				return zammadtest.Fault{Status: http.StatusGatewayTimeout}, true
			}
			return zammadtest.Fault{}, false
		}
		resp := h.call(ToolKBSearch, `{"query":"vpn"}`, eva())
		wantCode(t, resp, CodeUnavailable)
		if len(resp.Data) != 1 {
			t.Errorf("partial results leaked: %+v", resp.Data)
		}
	})

	t.Run("timeout", func(t *testing.T) {
		h := newHarness(t)
		h.zammad.AddKBAnswer(kbAnswer("VPN one", "<p>vpn</p>", "2026-04-20"))
		h.zammad.Intercept = func(r *http.Request) (zammadtest.Fault, bool) {
			if strings.Contains(r.URL.Path, "/answers/") {
				<-r.Context().Done() // the client's ZAMMAD_TIMEOUT ends it
				return zammadtest.Fault{Status: http.StatusGatewayTimeout}, true
			}
			return zammadtest.Fault{}, false
		}
		wantCode(t, h.call(ToolKBSearch, `{"query":"vpn"}`, eva()), CodeUnavailable)
		if !strings.Contains(h.logs.String(), `"cause":"deadline"`) {
			t.Errorf("logs lack the deadline cause: %s", h.logs)
		}
	})
}

// covers: spec 0014 AC-2. The query is 2 to 100 runes after trimming, one
// line, and never stored.
func TestKBSearchQueryRules(t *testing.T) {
	h := newHarness(t)
	for name, args := range map[string]string{
		"missing":  `{}`,
		"short":    `{"query":" v "}`,
		"long":     `{"query":"` + strings.Repeat("a", 101) + `"}`,
		"newline":  `{"query":"vpn\nhome"}`,
		"not text": `{"query":3}`,
		"extra":    `{"query":"vpn","limit":5}`,
	} {
		wantCode(t, h.call(ToolKBSearch, args, eva()), CodeInvalidArguments)
		if strings.Contains(string(h.lastIntent().Arguments), "vpn") {
			t.Errorf("%s: the query was stored: %s", name, h.lastIntent().Arguments)
		}
	}
	if resp := h.call(ToolKBSearch, `{"query":"`+strings.Repeat("a", 100)+`"}`, eva()); resp.IsError {
		t.Errorf("100 runes refused: %+v", resp)
	}
}

// covers: spec 0014 AC-4. With the seeded KB, the default 730 days, and
// today 2026-10-16, exactly the five planted old articles are stale.
func TestKBStaleSeededArticles(t *testing.T) {
	dir := filepath.Join("..", "..", "seed", "helpdesk", "kb")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	s := KBSettings{StaleAfterDays: 730, StaleAsOf: kbDay("2026-10-16"), Zone: amsterdam()}
	today := kbToday(s, time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)) // KB_STALE_AS_OF wins over the clock
	var stale []string
	for _, e := range entries {
		slug := strings.TrimSuffix(e.Name(), ".md")
		updated := seededUpdated(t, filepath.Join(dir, e.Name()))
		if kbStale(civilDate(updated, s.Zone), today, s.StaleAfterDays) {
			stale = append(stale, slug)
		}
	}
	want := []string{"account-lockout-old", "mfa-sms-registration", "new-starter-checklist", "password-policy-2019", "vpn-securelink-setup"}
	if !slices.Equal(stale, want) {
		t.Errorf("stale = %v, want %v", stale, want)
	}
}

// seededUpdated reads an article's updated date from its header, as the
// seed backdates the translation to it (spec 0006).
func seededUpdated(t *testing.T, path string) time.Time {
	t.Helper()
	f, err := os.Open(path) //nolint:gosec // G304: a test reading the repo's own seed files
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if v, ok := strings.CutPrefix(sc.Text(), "updated: "); ok {
			return kbDay(strings.TrimSpace(v))
		}
	}
	t.Fatalf("%s has no updated date", path)
	return time.Time{}
}

// covers: spec 0014 AC-4. Today is the dispatcher's clock read in
// Amsterdam when KB_STALE_AS_OF is unset, and an article exactly
// KB_STALE_AFTER_DAYS old is not yet stale.
func TestKBStaleBoundaries(t *testing.T) {
	s := KBSettings{StaleAfterDays: 730, Zone: amsterdam()}
	// 23:30 UTC on 15 October is already 16 October in Amsterdam.
	today := kbToday(s, time.Date(2026, 10, 15, 23, 30, 0, 0, time.UTC))
	if today.Format(time.DateOnly) != "2026-10-16" {
		t.Fatalf("today = %s, want the Amsterdam date", today)
	}
	for updated, want := range map[string]bool{"2024-10-15": true, "2024-10-16": false, "2024-10-17": false} {
		if got := kbStale(civilDate(kbDay(updated), s.Zone), today, 730); got != want {
			t.Errorf("updated %s: stale %t, want %t", updated, got, want)
		}
	}
	// An updated_at late in the UTC evening is the next day in Amsterdam.
	late := time.Date(2024, 10, 15, 22, 30, 0, 0, time.UTC)
	if civilDate(late, s.Zone).Format(time.DateOnly) != "2024-10-16" {
		t.Errorf("updated date = %s, want the Amsterdam date", civilDate(late, s.Zone))
	}
}

// covers: spec 0014 AC-4. The tool reads KB_STALE_AS_OF through the
// dispatcher, and stale_after_days echoes the setting.
func TestKBSearchStaleAsOf(t *testing.T) {
	h := newHarness(t)
	h.d.cfg.KB.StaleAsOf = kbDay("2026-11-19")
	h.d.cfg.KB.StaleAfterDays = 730
	h.zammad.AddKBAnswer(kbAnswer("Reset your password", "<p>password</p>", "2024-11-18"))
	got := results(t, h.call(ToolKBSearch, `{"query":"password"}`, eva()))
	if len(got) != 1 || got[0]["stale"] != true {
		t.Errorf("results = %+v", got)
	}
}

func TestLoadKBZone(t *testing.T) {
	zone, err := LoadKBZone()
	if err != nil || zone.String() != "Europe/Amsterdam" {
		t.Fatalf("zone = %v, %v", zone, err)
	}
}

// covers: spec 0014 AC-3. The HTML to text rules.
func TestArticleText(t *testing.T) {
	cases := map[string]struct {
		html string
		max  int
		want string
	}{
		"blocks and lists": {
			"<h2>Steps</h2><p>Do   this\tnow.</p><ul><li>One</li><li>Two <b>bold</b></li></ul><p>Done.</p>", 2000,
			"Steps\nDo this now.\n- One\n- Two bold\nDone.",
		},
		"anchors and images": {
			`<p>See <a href="https://x.example/a">the portal</a> <img src="https://x.example/i.png" alt="pic"> or https://raw.example/path now.</p>`, 2000,
			"See the portal or now.",
		},
		"entities and breaks": {"<p>Tom &amp; Jerry&nbsp;&lt;3<br>next</p>", 2000, "Tom & Jerry <3\nnext"},
		"script and style":    {"<p>a</p><script>alert(1)</script><style>p{}</style><p>b</p>", 2000, "a\nb"},
		"cut":                 {"<p>" + strings.Repeat("word ", 100) + "</p>", 20, "word word word word…"},
		"plain":               {"just text", 2000, "just text"},
	}
	for name, tc := range cases {
		if got := articleText(tc.html, tc.max); got != tc.want {
			t.Errorf("%s: %q, want %q", name, got, tc.want)
		}
	}
	if got := articleText("<p>"+strings.Repeat("é", 50)+"</p>", 10); len([]rune(got)) != 10 || !strings.HasSuffix(got, "…") {
		t.Errorf("cut counts runes: %q", got)
	}
}

// covers: spec 0014 AC-11. kb_review is a boolean in the schema.
func TestTicketUpdateKBReviewSchema(t *testing.T) {
	reg, err := NewRegistry(8 * time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	tool, _ := reg.Lookup(ToolTicketUpdate)
	props := tool.InputSchema()["properties"].(map[string]any)
	if p := props["kb_review"].(map[string]any); p["type"] != "boolean" {
		t.Errorf("kb_review schema = %v", p)
	}
}

// covers: spec 0014 AC-11. kb_review true needs a note, accepts only a JSON
// boolean, tags the ticket only after the update, and is stored.
func TestTicketUpdateKBReview(t *testing.T) {
	h := newHarness(t)
	created := h.call(ToolTicketCreate, `{"title":"VPN question","body":"how do I connect"}`, eva())
	r := eva()
	r.TicketID = int64(created.Data["ticket_id"].(int))
	id := int(r.TicketID)

	wantCode(t, h.call(ToolTicketUpdate, `{"state":"closed","kb_review":true}`, r), CodeInvalidArguments)
	wantCode(t, h.call(ToolTicketUpdate, `{"note":"x","kb_review":"true"}`, r), CodeInvalidArguments)
	wantCode(t, h.call(ToolTicketUpdate, `{"kb_review":true}`, r), CodeInvalidArguments)
	if slices.Contains(h.zammad.Tags(id), "kb-review") {
		t.Fatal("a refused call tagged the ticket")
	}

	resp := h.call(ToolTicketUpdate, `{"note":"Answered from WireGuard (14); SecureLink (13) is older.","kb_review":false}`, r)
	if resp.IsError || resp.Data["kb_review_tagged"] != false || slices.Contains(h.zammad.Tags(id), "kb-review") {
		t.Fatalf("kb_review false: %+v, tags %v", resp, h.zammad.Tags(id))
	}

	resp = h.call(ToolTicketUpdate, `{"state":"closed","note":"Answered from WireGuard (14); SecureLink (13) is older.","kb_review":true}`, r)
	if resp.IsError || resp.Data["state"] != "closed" || resp.Data["kb_review_tagged"] != true {
		t.Fatalf("response = %+v", resp)
	}
	if !slices.Contains(h.zammad.Tags(id), "kb-review") {
		t.Errorf("tags = %v", h.zammad.Tags(id))
	}
	reqs := h.zammad.Requests()
	if last := reqs[len(reqs)-1]; last.Path != "/api/v1/tags/add" || reqs[len(reqs)-2].Method != http.MethodPut {
		t.Errorf("the tag did not follow the update: %+v", reqs[len(reqs)-2:])
	}
	if got := string(h.lastIntent().Arguments); got != `{"_omitted":1,"kb_review":"true","state":"closed"}` {
		t.Errorf("stored arguments = %s", got)
	}
	if got := string(h.lastOutcome().Result); got != `{"kb_review_tagged":true,"state":"closed","ticket_id":`+strconv.Itoa(id)+`}` {
		t.Errorf("stored result = %s", got)
	}

	// A refused update (the ticket is closed now) tags nothing.
	before := len(h.zammad.Requests())
	wantCode(t, h.call(ToolTicketUpdate, `{"note":"again","kb_review":true}`, r), CodeTicketClosed)
	for _, s := range h.zammad.Requests()[before:] {
		if s.Path == "/api/v1/tags/add" {
			t.Error("a refused update added a tag")
		}
	}
}

// covers: spec 0014 AC-11. A failed tag fails nothing: the update stands,
// kb_review_tagged is false, and one line logs only the cause.
func TestTicketUpdateKBReviewTagFails(t *testing.T) {
	h := newHarness(t)
	created := h.call(ToolTicketCreate, `{"title":"t","body":"b"}`, eva())
	r := eva()
	r.TicketID = int64(created.Data["ticket_id"].(int))
	h.zammad.Intercept = func(req *http.Request) (zammadtest.Fault, bool) {
		return zammadtest.Fault{Status: http.StatusInternalServerError, Message: "tag store down"}, req.URL.Path == "/api/v1/tags/add"
	}
	resp := h.call(ToolTicketUpdate, `{"note":"stale article","kb_review":true}`, r)
	if resp.IsError || resp.Data["kb_review_tagged"] != false {
		t.Fatalf("response = %+v", resp)
	}
	if h.lastOutcome().Outcome.String != "ok" {
		t.Errorf("outcome = %s", h.lastOutcome().Outcome.String)
	}
	var line map[string]any
	for l := range strings.SplitSeq(strings.TrimSpace(h.logs.String()), "\n") {
		var m map[string]any
		if json.Unmarshal([]byte(l), &m) == nil && m["msg"] == "kb_review_tag_failed" {
			line = m
		}
	}
	if line == nil || line["cause"] != "zammad_500" {
		t.Fatalf("kb_review_tag_failed line = %v in %s", line, h.logs)
	}
	for k := range line {
		if !slices.Contains([]string{"time", "level", "msg", "cause"}, k) {
			t.Errorf("kb_review_tag_failed carries %q", k)
		}
	}
}
