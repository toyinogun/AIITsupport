package zammad_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/toyinogun/AIITsupport/internal/zammad"
	"github.com/toyinogun/AIITsupport/internal/zammad/zammadtest"
)

func day(s string) time.Time {
	t, err := time.Parse(time.DateOnly, s)
	if err != nil {
		panic(err)
	}
	return t.Add(7 * time.Hour)
}

func ptr(t time.Time) *time.Time { return &t }

// covers: spec 0014 AC-3. The init read gives the knowledge base id and the
// category titles per KB locale.
func TestKBInit(t *testing.T) {
	f := zammadtest.New(t)
	f.AddKBCategory(3, "VPN and remote work")
	meta, err := tokenClient(t, f).KBInit(t.Context())
	if err != nil {
		t.Fatalf("KBInit: %v", err)
	}
	if meta.ID != zammadtest.KBID || meta.Categories[3][zammadtest.KBLocaleID] != "VPN and remote work" {
		t.Errorf("meta = %+v", meta)
	}
}

func TestKBInitFails(t *testing.T) {
	f := zammadtest.New(t)
	f.Intercept = func(r *http.Request) (zammadtest.Fault, bool) {
		return zammadtest.Fault{Status: http.StatusServiceUnavailable, Message: "down"}, r.URL.Path == "/api/v1/knowledge_bases/init"
	}
	if _, err := tokenClient(t, f).KBInit(t.Context()); zammad.Status(err) != http.StatusServiceUnavailable {
		t.Errorf("err = %v, want 503", err)
	}
}

// rawZammad is a Zammad that answers every request with body, for response
// shapes the fake never sends.
func rawZammad(t *testing.T, body string) *zammad.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	c, err := zammad.New(zammad.Options{BaseURL: srv.URL, Auth: zammad.TokenAuth(zammadtest.Token), Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("zammad.New: %v", err)
	}
	return c
}

// covers: spec 0014 AC-3, AC-12. The init read takes the one active
// knowledge base and skips an inactive one; none active, or two, is an
// unexpected shape and fails.
func TestKBInitShapes(t *testing.T) {
	kb := func(id int, active bool) string {
		return `"` + strconv.Itoa(id) + `":{"id":` + strconv.Itoa(id) + `,"active":` + strconv.FormatBool(active) + `}`
	}
	cats := `"KnowledgeBaseCategoryTranslation":{"9":{"title":"Printers","kb_locale_id":1,"category_id":5}}`

	meta, err := rawZammad(t, `{"KnowledgeBase":{`+kb(1, false)+`,`+kb(2, true)+`},`+cats+`}`).KBInit(t.Context())
	if err != nil || meta.ID != 2 || meta.Categories[5][1] != "Printers" {
		t.Errorf("one active of two: meta %+v, err %v", meta, err)
	}
	for name, kbs := range map[string]string{
		"none active": kb(1, false),
		"no kb":       ``,
		"two active":  kb(1, true) + `,` + kb(2, true),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := rawZammad(t, `{"KnowledgeBase":{`+kbs+`},`+cats+`}`).KBInit(t.Context()); err == nil {
				t.Error("KBInit accepted it")
			}
		})
	}
}

// covers: spec 0014 AC-12. A hit whose translation id or answer id is not a
// positive number fails the search, like a portal path that does not parse.
func TestSearchKBUnexpectedID(t *testing.T) {
	for name, detail := range map[string]string{
		"translation id 0": `{"id":0,"type":"KnowledgeBase::Answer::Translation","url":"/help/en-us/3/14"}`,
		"answer id 0":      `{"id":70,"type":"KnowledgeBase::Answer::Translation","url":"/help/en-us/3/0"}`,
		"answer id too big": `{"id":70,"type":"KnowledgeBase::Answer::Translation","url":"/help/en-us/3/` +
			`99999999999999999999"}`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := rawZammad(t, `{"result":[],"details":[`+detail+`]}`).SearchKB(t.Context(), zammadtest.KBID, "en-us", "vpn", 20)
			if err == nil {
				t.Error("SearchKB accepted the hit")
			}
		})
	}
}

// covers: spec 0014 AC-3, AC-17. Search keeps answer translation hits in
// Zammad's order (newest first), maps each to its answer id and locale from
// the portal path, and drops category hits.
func TestSearchKB(t *testing.T) {
	f := zammadtest.New(t)
	f.AddKBCategory(3, "VPN and remote work")
	old := f.AddKBAnswer(zammadtest.KBAnswer{CategoryID: 3, Title: "Install the VPN client", Body: "<p>SecureLink</p>", Updated: day("2021-02-08")})
	current := f.AddKBAnswer(zammadtest.KBAnswer{CategoryID: 3, Title: "Connect to the VPN (WireGuard)", Body: "<p>WireGuard</p>", Updated: day("2026-04-20")})
	f.AddKBAnswer(zammadtest.KBAnswer{CategoryID: 3, Title: "Printer", Body: "<p>labels</p>", Updated: day("2026-01-01")})

	hits, err := tokenClient(t, f).SearchKB(t.Context(), zammadtest.KBID, "en-us", "vpn", 20)
	if err != nil {
		t.Fatalf("SearchKB: %v", err)
	}
	want := []zammad.KBHit{
		{TranslationID: current.TranslationID, AnswerID: current.ID, Locale: "en-us"},
		{TranslationID: old.TranslationID, AnswerID: old.ID, Locale: "en-us"},
	}
	if len(hits) != len(want) || hits[0] != want[0] || hits[1] != want[1] {
		t.Errorf("hits = %+v, want %+v", hits, want)
	}
	req := f.Requests()[0]
	if req.Method != http.MethodPost || req.Path != "/api/v1/knowledge_bases/search" ||
		req.Body["flavor"] != "public" || req.Body["locale"] != "en-us" || req.Body["query"] != "vpn" ||
		req.Body["index"] != "KnowledgeBase::Answer::Translation" || req.Body["limit"] != float64(20) {
		t.Errorf("request = %+v", req)
	}

	// The limit is Zammad's: the fake cuts its list like the real one.
	hits, err = tokenClient(t, f).SearchKB(t.Context(), zammadtest.KBID, "en-us", "vpn", 1)
	if err != nil || len(hits) != 1 || hits[0] != want[0] {
		t.Errorf("limit 1: hits = %+v, %v", hits, err)
	}
}

// covers: spec 0014 AC-12. A portal path that does not parse fails the
// search instead of silently dropping the hit, and the error never holds
// the query.
func TestSearchKBUnexpectedPath(t *testing.T) {
	f := zammadtest.New(t)
	f.AddKBCategory(3, "VPN and remote work")
	f.AddKBAnswer(zammadtest.KBAnswer{CategoryID: 3, Title: "VPN", Body: "x", Updated: day("2026-04-20"), Locale: "EN_US"})
	_, err := tokenClient(t, f).SearchKB(t.Context(), zammadtest.KBID, "en-us", "vpn", 20)
	if err == nil {
		t.Fatal("SearchKB accepted a hit with an unexpected portal path")
	}
	f2 := zammadtest.New(t)
	f2.Intercept = func(*http.Request) (zammadtest.Fault, bool) {
		return zammadtest.Fault{Status: http.StatusInternalServerError}, true
	}
	_, err = tokenClient(t, f2).SearchKB(t.Context(), zammadtest.KBID, "en-us", "vpn secretquery", 20)
	if zammad.Status(err) != http.StatusInternalServerError || strings.Contains(err.Error(), "secretquery") {
		t.Errorf("err = %v", err)
	}
}

// covers: spec 0014 AC-3. GetAnswer asks for the named contents only, and
// Translation reads the answer's publishing fields, the translation, and
// its body.
func TestGetAnswerTranslation(t *testing.T) {
	f := zammadtest.New(t)
	published := day("2024-10-07")
	a := f.AddKBAnswer(zammadtest.KBAnswer{
		CategoryID: 3, Title: "Connect to the VPN", Body: "<p>WireGuard</p>", Updated: day("2026-04-20"), Published: &published,
	})
	c := tokenClient(t, f)

	got, err := c.GetAnswer(t.Context(), zammadtest.KBID, a.ID)
	if err != nil {
		t.Fatalf("GetAnswer: %v", err)
	}
	tr, ok, err := got.Translation(a.TranslationID)
	if err != nil || !ok || tr.HasBody {
		t.Fatalf("Translation without contents = %+v, %t, %v", tr, ok, err)
	}

	got, err = c.GetAnswer(t.Context(), zammadtest.KBID, a.ID, a.ContentID)
	if err != nil {
		t.Fatalf("GetAnswer: %v", err)
	}
	if p := f.Requests()[1].Path; p != "/api/v1/knowledge_bases/1/answers/"+strconv.Itoa(a.ID) {
		t.Errorf("path = %q", p)
	}
	tr, ok, err = got.Translation(a.TranslationID)
	if err != nil || !ok {
		t.Fatalf("Translation = %t, %v", ok, err)
	}
	if tr.AnswerID != a.ID || tr.CategoryID != 3 || tr.Title != "Connect to the VPN" || !tr.HasBody || tr.Body != "<p>WireGuard</p>" ||
		tr.KBLocaleID != zammadtest.KBLocaleID || !tr.UpdatedAt.Equal(day("2026-04-20")) ||
		tr.PublishedAt == nil || !tr.PublishedAt.Equal(published) || tr.ArchivedAt != nil {
		t.Errorf("translation = %+v", tr)
	}
	if _, ok, err := got.Translation(a.TranslationID + 1); ok || err != nil {
		t.Errorf("unknown translation: ok %t, err %v", ok, err)
	}
}

func TestGetAnswerNotFound(t *testing.T) {
	f := zammadtest.New(t)
	a := f.AddKBAnswer(zammadtest.KBAnswer{CategoryID: 3, Title: "x", Body: "x", Updated: day("2026-04-20"), Published: ptr(day("2026-04-20"))})
	f.RemoveKBAnswer(a.ID)
	if _, err := tokenClient(t, f).GetAnswer(t.Context(), zammadtest.KBID, a.ID, a.ContentID); zammad.Status(err) != http.StatusNotFound {
		t.Errorf("err = %v, want 404", err)
	}
}

func TestTranslationRejectsBadAssets(t *testing.T) {
	a := zammad.Answer{ID: 4, Assets: map[string]json.RawMessage{}}
	if _, _, err := a.Translation(4); err == nil {
		t.Error("an answer missing from its own assets was accepted")
	}
	a.Assets["KnowledgeBaseAnswer"] = json.RawMessage(`"not an object"`)
	if _, _, err := a.Translation(4); err == nil {
		t.Error("malformed assets were accepted")
	}
}
