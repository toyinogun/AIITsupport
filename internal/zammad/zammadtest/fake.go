// Package zammadtest is an in memory fake of the Zammad REST calls the seed
// and the tool server make (spec 0006), served over httptest. It checks the
// credentials, keeps users, tickets, articles, and knowledge base objects,
// records every request with its From header, and lets a test inject
// failures. It models Zammad only as far as those calls need.
package zammadtest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// Credentials the fake accepts.
const (
	Token         = "zammadtest-token-0123456789abcdef0123"
	AdminLogin    = "admin@noord.example"
	AdminPassword = "zammadtest-admin"
)

// Seen is one request as the fake received it.
type Seen struct {
	Method string
	Path   string
	From   string
	Body   map[string]any
}

// Fault is an injected answer for one request.
type Fault struct {
	Status  int
	Message string
}

// Fake is the in memory Zammad.
type Fake struct {
	Server *httptest.Server
	// Intercept, when set, may answer a request instead of the fake.
	Intercept func(r *http.Request) (Fault, bool)

	mu       sync.Mutex
	nextID   int
	users    map[int]map[string]any
	tickets  map[int]map[string]any
	articles map[int]map[string]any
	answers  map[int]map[string]any
	cats     map[int]map[string]any
	requests []Seen

	// The knowledge base the read paths serve (init, search, GetAnswer).
	kbCats    map[int]string
	kbAnswers map[int]KBAnswer
}

// New starts a fake Zammad that stops when the test ends. User 1 is the
// assistant the token belongs to; user 2 is the admin.
func New(t testing.TB) *Fake {
	t.Helper()
	f := &Fake{
		nextID:    100,
		users:     map[int]map[string]any{},
		tickets:   map[int]map[string]any{},
		articles:  map[int]map[string]any{},
		answers:   map[int]map[string]any{},
		cats:      map[int]map[string]any{},
		kbCats:    map[int]string{},
		kbAnswers: map[int]KBAnswer{},
	}
	f.users[1] = map[string]any{"id": 1, "login": "noord-assistant", "active": true}
	f.users[2] = map[string]any{"id": 2, "login": AdminLogin, "active": true}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.Server.Close)
	return f
}

// URL is the base URL to point a client at.
func (f *Fake) URL() string { return f.Server.URL }

// AddUser stores an active user and returns its id.
func (f *Fake) AddUser(login string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextID++
	f.users[f.nextID] = map[string]any{"id": f.nextID, "login": login, "active": true}
	return f.nextID
}

// Deactivate marks a user inactive, as Zammad does for a disabled LDAP user.
func (f *Fake) Deactivate(id int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.users[id]["active"] = false
}

// States are the fake's ticket states, with Zammad's default names.
var States = []map[string]any{
	{"id": 1, "name": "new", "active": true},
	{"id": 2, "name": "open", "active": true},
	{"id": 3, "name": "pending reminder", "active": true},
	{"id": 4, "name": "closed", "active": true},
	{"id": 5, "name": "merged", "active": true},
	{"id": 6, "name": "pending close", "active": true},
}

func stateID(name string) (int, bool) {
	for _, s := range States {
		if s["name"] == name {
			return s["id"].(int), true
		}
	}
	return 0, false
}

// Ticket returns a copy of one stored ticket.
func (f *Fake) Ticket(id int) (map[string]any, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.tickets[id]
	if !ok {
		return nil, false
	}
	return copyMap(t), true
}

// Articles returns copies of a ticket's articles, oldest first.
func (f *Fake) Articles(ticketID int) []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.tickets[ticketID]
	if !ok {
		return nil
	}
	var out []map[string]any
	for _, id := range t["article_ids"].([]any) {
		out = append(out, copyMap(f.articles[toInt(id)]))
	}
	return out
}

// Requests lists every request seen.
func (f *Fake) Requests() []Seen {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.requests)
}

// Tickets returns a copy of every stored ticket.
func (f *Fake) Tickets() []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []map[string]any
	for _, t := range f.tickets {
		out = append(out, copyMap(t))
	}
	return out
}

var (
	ticketPath   = regexp.MustCompile(`^/api/v1/tickets/(\d+)$`)
	categoryPath = regexp.MustCompile(`^/api/v1/knowledge_bases/(\d+)/categories$`)
	answersPath  = regexp.MustCompile(`^/api/v1/knowledge_bases/(\d+)/answers$`)
	answerPath   = regexp.MustCompile(`^/api/v1/knowledge_bases/(\d+)/answers/(\d+)$`)
	publishPath  = regexp.MustCompile(`^/api/v1/knowledge_bases/(\d+)/answers/(\d+)/publish$`)
)

// KBID and KBLocaleID are the fake knowledge base's id and its one locale.
const (
	KBID       = 1
	KBLocaleID = 1
)

func (f *Fake) serve(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&body)
	}
	f.mu.Lock()
	f.requests = append(f.requests, Seen{Method: r.Method, Path: r.URL.Path, From: r.Header.Get("From"), Body: body})
	f.mu.Unlock()

	if f.Intercept != nil {
		if fault, ok := f.Intercept(r); ok {
			writeJSON(w, fault.Status, map[string]any{"error": fault.Message})
			return
		}
	}
	who := f.auth(r)
	if who == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "authentication failed"})
		return
	}
	f.route(w, r, who, body)
}

// auth returns "assistant", "admin", or "" for bad credentials.
func (f *Fake) auth(r *http.Request) string {
	if r.Header.Get("Authorization") == "Token token="+Token {
		return "assistant"
	}
	if login, pass, ok := r.BasicAuth(); ok && login == AdminLogin && pass == AdminPassword {
		return "admin"
	}
	return ""
}

func (f *Fake) route(w http.ResponseWriter, r *http.Request, who string, body map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := r.URL.Path
	switch {
	case r.Method == http.MethodGet && p == "/api/v1/users/me":
		id := 1
		if who == "admin" {
			id = 2
		}
		writeJSON(w, http.StatusOK, f.users[id])
	case r.Method == http.MethodGet && p == "/api/v1/users/search":
		f.searchUsers(w, r.URL.Query().Get("query"))
	case r.Method == http.MethodGet && p == "/api/v1/ticket_states":
		writeJSON(w, http.StatusOK, States)
	case r.Method == http.MethodGet && p == "/api/v1/settings":
		if who != "admin" {
			writeJSON(w, http.StatusForbidden, map[string]any{"error": "Not authorized"})
			return
		}
		writeJSON(w, http.StatusOK, []any{})
	case r.Method == http.MethodPost && p == "/api/v1/tickets":
		f.createTicket(w, body)
	case r.Method == http.MethodPut && ticketPath.MatchString(p):
		f.updateTicket(w, idFrom(ticketPath, p, 1), body)
	case r.Method == http.MethodGet && ticketPath.MatchString(p):
		f.getTicket(w, idFrom(ticketPath, p, 1))
	case r.Method == http.MethodPost && p == "/api/v1/ticket_articles":
		f.createArticle(w, body)
	case r.Method == http.MethodPost && p == "/api/v1/tags/add":
		f.addTag(w, body)
	case r.Method == http.MethodPost && categoryPath.MatchString(p):
		f.kbWrite(w, who, func() map[string]any { return f.store(f.cats, body) })
	case r.Method == http.MethodPost && answersPath.MatchString(p):
		f.kbWrite(w, who, func() map[string]any { return f.store(f.answers, body) })
	case r.Method == http.MethodPost && publishPath.MatchString(p):
		f.kbWrite(w, who, func() map[string]any { return f.publish(idFrom(publishPath, p, 2)) })
	case r.Method == http.MethodGet && answerPath.MatchString(p):
		f.getAnswer(w, idFrom(answerPath, p, 2), r.URL.Query().Get("include_contents"))
	case r.Method == http.MethodPost && p == "/api/v1/knowledge_bases/init":
		f.kbInit(w)
	case r.Method == http.MethodPost && p == "/api/v1/knowledge_bases/search":
		f.searchKB(w, body)
	default:
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "no route " + r.Method + " " + p})
	}
}

// searchUsers matches loosely, like Zammad: a login:"x" query matches every
// login containing x, so the client must filter exactly.
func (f *Fake) searchUsers(w http.ResponseWriter, query string) {
	if q, ok := strings.CutPrefix(query, `login:"`); ok {
		query = strings.TrimSuffix(q, `"`)
	}
	out := []map[string]any{}
	for _, u := range f.users {
		if strings.Contains(u["login"].(string), query) {
			out = append(out, u)
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (f *Fake) createTicket(w http.ResponseWriter, body map[string]any) {
	if body["title"] == nil || body["group"] == nil || body["article"] == nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": "missing title, group, or article"})
		return
	}
	cid, ok := body["customer_id"].(float64)
	if !ok || f.users[int(cid)] == nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": "customer_id must name an existing user"})
		return
	}
	t := f.store(f.tickets, body)
	art := f.store(f.articles, body["article"].(map[string]any))
	art["ticket_id"] = t["id"]
	delete(t, "article")
	t["article_ids"] = []any{art["id"]}
	t["state"] = "new"
	t["state_id"] = 1
	t["number"] = fmt.Sprintf("%d", 70000+t["id"].(int))
	writeJSON(w, http.StatusCreated, t)
}

func (f *Fake) updateTicket(w http.ResponseWriter, id int, body map[string]any) {
	t, ok := f.tickets[id]
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "no ticket"})
		return
	}
	if name, ok := body["state"].(string); ok {
		id, known := stateID(name)
		if !known {
			writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": "unknown state"})
			return
		}
		t["state_id"] = id
	}
	if a, ok := body["article"].(map[string]any); ok {
		art := f.store(f.articles, a)
		art["ticket_id"] = id
		t["article_ids"] = append(t["article_ids"].([]any), art["id"])
	}
	for k, v := range body {
		if k != "article" {
			t[k] = v
		}
	}
	writeJSON(w, http.StatusOK, t)
}

func (f *Fake) getTicket(w http.ResponseWriter, id int) {
	t, ok := f.tickets[id]
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "no ticket"})
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (f *Fake) createArticle(w http.ResponseWriter, body map[string]any) {
	tid, _ := body["ticket_id"].(float64)
	t, ok := f.tickets[int(tid)]
	if !ok {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": "no ticket"})
		return
	}
	art := f.store(f.articles, body)
	t["article_ids"] = append(t["article_ids"].([]any), art["id"])
	writeJSON(w, http.StatusCreated, art)
}

// addTag adds a tag to a ticket's comma separated tags, as Zammad's tag
// API does; adding one it already has is not an error.
func (f *Fake) addTag(w http.ResponseWriter, body map[string]any) {
	t, ok := f.tickets[toInt(body["o_id"])]
	item, _ := body["item"].(string)
	if body["object"] != "Ticket" || !ok || item == "" {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": "no such ticket or tag"})
		return
	}
	tags := splitTags(t["tags"])
	if !slices.Contains(tags, item) {
		tags = append(tags, item)
	}
	t["tags"] = strings.Join(tags, ",")
	writeJSON(w, http.StatusCreated, true)
}

// Tags returns a ticket's tags in the order they were added.
func (f *Fake) Tags(ticketID int) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.tickets[ticketID]
	if !ok {
		return nil
	}
	return splitTags(t["tags"])
}

func splitTags(v any) []string {
	s, _ := v.(string)
	var out []string
	for tag := range strings.SplitSeq(s, ",") {
		if tag = strings.TrimSpace(tag); tag != "" {
			out = append(out, tag)
		}
	}
	return out
}

// kbWrite refuses the assistant (knowledge_base.reader only), as Zammad does.
func (f *Fake) kbWrite(w http.ResponseWriter, who string, write func() map[string]any) {
	if who != "admin" {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "Not authorized"})
		return
	}
	obj := write()
	if obj == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "not found"})
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": obj["id"], "assets": map[string]any{}})
}

func (f *Fake) publish(id int) map[string]any {
	a, ok := f.answers[id]
	if !ok {
		return nil
	}
	a["published"] = true
	return a
}

func (f *Fake) getAnswer(w http.ResponseWriter, id int, includeContents string) {
	if a, ok := f.kbAnswers[id]; ok {
		f.getKBAnswer(w, a, includeContents)
		return
	}
	a, ok := f.answers[id]
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "no answer"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "assets": map[string]any{"KnowledgeBaseAnswer": map[string]any{fmt.Sprint(id): a}}})
}

// store saves a copy of obj under a new id. The caller holds the lock.
func (f *Fake) store(into map[int]map[string]any, obj map[string]any) map[string]any {
	f.nextID++
	c := copyMap(obj)
	c["id"] = f.nextID
	into[f.nextID] = c
	return c
}

func toInt(v any) int {
	switch n := v.(type) {
	case int:
		return n
	case float64:
		return int(n)
	}
	return 0
}

func idFrom(re *regexp.Regexp, p string, group int) int {
	var id int
	_, _ = fmt.Sscan(re.FindStringSubmatch(p)[group], &id)
	return id
}

func copyMap(m map[string]any) map[string]any {
	c := make(map[string]any, len(m))
	for k, v := range m {
		c[k] = v
	}
	return c
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// KBAnswer is an answer in the fake knowledge base's read paths. Unlike the
// answers CreateAnswer stores, these are found by search and read in full.
type KBAnswer struct {
	CategoryID int
	Title      string
	Body       string // HTML
	Updated    time.Time
	// Published, Archived, and Internal are Zammad's publishing timestamps;
	// nil is unset, so an answer with none is a draft.
	Published, Archived, Internal *time.Time
	Locale                        string // the portal locale; default en-us
	KBLocaleID                    int    // default KBLocaleID

	// Set by AddKBAnswer: the translation and content ids differ from the
	// answer id, as they can in Zammad.
	ID, TranslationID, ContentID int
}

// AddKBCategory adds a category title in the fake knowledge base's locale.
func (f *Fake) AddKBCategory(id int, title string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.kbCats[id] = title
}

// AddKBAnswer adds an answer to the read paths and returns it with its ids.
func (f *Fake) AddKBAnswer(a KBAnswer) KBAnswer {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextID++
	a.ID, a.TranslationID, a.ContentID = f.nextID, f.nextID+1000, f.nextID+2000
	if a.Locale == "" {
		a.Locale = "en-us"
	}
	if a.KBLocaleID == 0 {
		a.KBLocaleID = KBLocaleID
	}
	f.kbAnswers[a.ID] = a
	return a
}

// RemoveKBAnswer deletes an answer, so reading it is a 404.
func (f *Fake) RemoveKBAnswer(id int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.kbAnswers, id)
}

func (f *Fake) kbInit(w http.ResponseWriter) {
	cats := map[string]any{}
	for id, title := range f.kbCats {
		cats[fmt.Sprint(id)] = map[string]any{"id": id, "title": title, "kb_locale_id": KBLocaleID, "category_id": id}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"KnowledgeBase":                    map[string]any{fmt.Sprint(KBID): map[string]any{"id": KBID, "active": true}},
		"KnowledgeBaseLocale":              map[string]any{fmt.Sprint(KBLocaleID): map[string]any{"id": KBLocaleID, "system_locale_id": 1, "primary": true}},
		"KnowledgeBaseCategoryTranslation": cats,
	})
}

var htmlTag = regexp.MustCompile(`<[^>]*>`)

// searchKB matches like Zammad's REST search: every query word must occur
// (here as a substring of the title or the body text), hits come newest
// first, and a positive limit cuts the list. Unlike Zammad's public flavor
// it does not filter by publishing state, so a test sees the tool's own
// filter. A category whose title holds the query is listed too, whatever
// the index, so the client must drop it.
func (f *Fake) searchKB(w http.ResponseWriter, body map[string]any) {
	query, _ := body["query"].(string)
	if toInt(body["knowledge_base_id"]) != KBID || strings.TrimSpace(query) == "" {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": "missing knowledge_base_id or query"})
		return
	}
	words := strings.Fields(strings.ToLower(query))
	var found []KBAnswer
	for _, a := range f.kbAnswers {
		text := strings.ToLower(a.Title + " " + htmlTag.ReplaceAllString(a.Body, " "))
		if !slices.ContainsFunc(words, func(word string) bool { return !strings.Contains(text, word) }) {
			found = append(found, a)
		}
	}
	slices.SortFunc(found, func(a, b KBAnswer) int {
		if c := b.Updated.Compare(a.Updated); c != 0 {
			return c
		}
		return a.ID - b.ID
	})
	details := []map[string]any{}
	for _, a := range found {
		details = append(details, map[string]any{
			"id": a.TranslationID, "type": "KnowledgeBase::Answer::Translation",
			"url":   fmt.Sprintf("/help/%s/%d-%s/%d-%s", a.Locale, a.CategoryID, slug(f.kbCats[a.CategoryID]), a.ID, slug(a.Title)),
			"title": a.Title, "date": a.Updated,
		})
	}
	for id, title := range f.kbCats {
		if strings.Contains(strings.ToLower(title), strings.ToLower(query)) {
			details = append(details, map[string]any{
				"id": id, "type": "KnowledgeBase::Category::Translation", "url": fmt.Sprintf("/help/en-us/%d-%s", id, slug(title)),
			})
		}
	}
	if limit := toInt(body["limit"]); limit > 0 && len(details) > limit {
		details = details[:limit]
	}
	writeJSON(w, http.StatusOK, map[string]any{"result": []any{}, "details": details})
}

var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

func slug(s string) string {
	return strings.Trim(nonSlug.ReplaceAllString(strings.ToLower(s), "-"), "-")
}

func (f *Fake) getKBAnswer(w http.ResponseWriter, a KBAnswer, includeContents string) {
	assets := map[string]any{
		"KnowledgeBaseAnswer": map[string]any{fmt.Sprint(a.ID): map[string]any{
			"id": a.ID, "category_id": a.CategoryID,
			"published_at": a.Published, "archived_at": a.Archived, "internal_at": a.Internal,
			"translation_ids": []int{a.TranslationID},
		}},
		"KnowledgeBaseAnswerTranslation": map[string]any{fmt.Sprint(a.TranslationID): map[string]any{
			"id": a.TranslationID, "title": a.Title, "kb_locale_id": a.KBLocaleID, "answer_id": a.ID,
			"content_id": a.ContentID, "updated_at": a.Updated,
		}},
	}
	if slices.Contains(strings.Split(includeContents, ","), fmt.Sprint(a.ContentID)) {
		assets["KnowledgeBaseAnswerTranslationContent"] = map[string]any{
			fmt.Sprint(a.ContentID): map[string]any{"id": a.ContentID, "body": a.Body},
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": a.ID, "assets": assets})
}
