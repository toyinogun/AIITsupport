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
}

// New starts a fake Zammad that stops when the test ends. User 1 is the
// assistant the token belongs to; user 2 is the admin.
func New(t testing.TB) *Fake {
	t.Helper()
	f := &Fake{
		nextID:   100,
		users:    map[int]map[string]any{},
		tickets:  map[int]map[string]any{},
		articles: map[int]map[string]any{},
		answers:  map[int]map[string]any{},
		cats:     map[int]map[string]any{},
	}
	f.users[1] = map[string]any{"id": 1, "login": "noord-assistant", "active": true}
	f.users[2] = map[string]any{"id": 2, "login": AdminLogin, "active": true}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.Server.Close)
	return f
}

// URL is the base URL to point a client at.
func (f *Fake) URL() string { return f.Server.URL }

// AddUser stores a user and returns its id.
func (f *Fake) AddUser(login string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextID++
	f.users[f.nextID] = map[string]any{"id": f.nextID, "login": login, "active": true}
	return f.nextID
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
	case r.Method == http.MethodPost && categoryPath.MatchString(p):
		f.kbWrite(w, who, func() map[string]any { return f.store(f.cats, body) })
	case r.Method == http.MethodPost && answersPath.MatchString(p):
		f.kbWrite(w, who, func() map[string]any { return f.store(f.answers, body) })
	case r.Method == http.MethodPost && publishPath.MatchString(p):
		f.kbWrite(w, who, func() map[string]any { return f.publish(idFrom(publishPath, p, 2)) })
	case r.Method == http.MethodGet && answerPath.MatchString(p):
		f.getAnswer(w, idFrom(answerPath, p, 2))
	default:
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "no route " + r.Method + " " + p})
	}
}

func (f *Fake) searchUsers(w http.ResponseWriter, query string) {
	var out []map[string]any
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
	t["article_ids"] = []any{art["id"]}
	t["state"] = "new"
	writeJSON(w, http.StatusCreated, t)
}

func (f *Fake) updateTicket(w http.ResponseWriter, id int, body map[string]any) {
	t, ok := f.tickets[id]
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "no ticket"})
		return
	}
	for k, v := range body {
		t[k] = v
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

func (f *Fake) getAnswer(w http.ResponseWriter, id int) {
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
