// Package graphtest is an in memory fake of the Microsoft Graph calls the
// Noord sync and the tool server use (spec 0005), served over httptest. It
// keeps live users, the recycle bin, managers, and phone methods, counts
// writes, and lets a test inject failures. It models Graph's behavior only as
// far as those calls need, not the real service.
package graphtest

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// Token is the bearer token the fake accepts.
const Token = "graphtest-token"

// Graph's fixed phone method ids, one per phone type.
var phoneIDs = map[string]string{
	"mobile":          "3179e48a-750b-4051-897c-87b9720928f7",
	"alternateMobile": "b6332ec1-7057-4abe-9331-3d72feddfe41",
	"office":          "e37fc753-ff3b-4958-9484-eaa9425c82bc",
}

const passwordMethodID = "28c10230-6103-485e-b985-444c60001490" //nolint:gosec // G101: Graph's fixed method id, not a secret

// Phone is a stored phone method.
type Phone struct {
	ID          string `json:"id"`
	PhoneNumber string `json:"phoneNumber"`
	PhoneType   string `json:"phoneType"`
}

// Fault is an injected answer for one request.
type Fault struct {
	Status     int
	RetryAfter string
	Code       string
	Message    string
}

// Fake is the in memory Graph.
type Fake struct {
	Server *httptest.Server
	// PageSize splits user listings so tests cover paging.
	PageSize int
	// DeletedHoldUPN makes a create clash with a user in the recycle bin.
	DeletedHoldUPN bool
	// Intercept, when set, may answer a request instead of the fake (return
	// ok false to let it through). It runs before anything is changed.
	Intercept func(r *http.Request) (Fault, bool)

	mu       sync.Mutex
	nextID   int
	users    map[string]map[string]any
	deleted  map[string]map[string]any
	managers map[string]string
	phones   map[string][]Phone
	writes   int
	requests []string
}

// New starts a fake Graph that stops when the test ends.
func New(t testing.TB) *Fake {
	t.Helper()
	f := &Fake{
		PageSize: 2,
		users:    map[string]map[string]any{},
		deleted:  map[string]map[string]any{},
		managers: map[string]string{},
		phones:   map[string][]Phone{},
	}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.Server.Close)
	return f
}

// URL is the base URL to point a graph client at.
func (f *Fake) URL() string { return f.Server.URL }

// Writes counts every request that is not a GET and that succeeded.
func (f *Fake) Writes() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.writes
}

// Requests lists every request seen, as "METHOD /path".
func (f *Fake) Requests() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.requests)
}

// ResetCounts clears the write count and request log.
func (f *Fake) ResetCounts() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.writes = 0
	f.requests = nil
}

// AddUser stores a live user directly (no write counted) and returns its id.
func (f *Fake) AddUser(fields map[string]any) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	id := f.newID()
	u := maps.Clone(fields)
	u["id"] = id
	f.users[id] = u
	return id
}

// AddDeleted stores a user straight into the recycle bin.
func (f *Fake) AddDeleted(fields map[string]any) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	id := f.newID()
	u := maps.Clone(fields)
	u["id"] = id
	f.deleted[id] = u
	return id
}

// User returns a copy of a live user's fields by mailNickname.
func (f *Fake) User(nickname string) (map[string]any, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, u := range f.users {
		if u["mailNickname"] == nickname {
			return maps.Clone(u), true
		}
	}
	return nil, false
}

// Password returns the password a create or PATCH last set on a live user.
func (f *Fake) Password(id string) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.users[id]["passwordProfile"].(map[string]any)
	if !ok {
		return "", false
	}
	pw, ok := p["password"].(string)
	return pw, ok
}

// Deleted reports whether a user with that mailNickname is in the recycle bin.
func (f *Fake) Deleted(nickname string) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for id, u := range f.deleted {
		if u["mailNickname"] == nickname {
			return id, true
		}
	}
	return "", false
}

// Patch edits a live user's fields directly, to simulate drift.
func (f *Fake) Patch(id string, fields map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	maps.Copy(f.users[id], fields)
}

// SoftDelete moves a live user to the recycle bin directly.
func (f *Fake) SoftDelete(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleted[id] = f.users[id]
	delete(f.users, id)
	delete(f.managers, id)
}

// Manager returns the manager id of a live user.
func (f *Fake) Manager(id string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.managers[id]
}

// SetManager sets a manager directly.
func (f *Fake) SetManager(id, managerID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.managers[id] = managerID
}

// Phones returns a user's phone methods.
func (f *Fake) Phones(id string) []Phone {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.phones[id])
}

// SetPhones replaces a user's phone methods directly.
func (f *Fake) SetPhones(id string, phones []Phone) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.phones[id] = slices.Clone(phones)
}

// PhoneID is Graph's fixed method id for a phone type.
func PhoneID(phoneType string) string { return phoneIDs[phoneType] }

func (f *Fake) newID() string {
	f.nextID++
	return fmt.Sprintf("00000000-0000-0000-0000-%012d", f.nextID)
}

func (f *Fake) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.requests = append(f.requests, r.Method+" "+r.URL.Path)
	f.mu.Unlock()

	if r.Header.Get("Authorization") != "Bearer "+Token {
		writeError(w, http.StatusUnauthorized, "InvalidAuthenticationToken", "bad token")
		return
	}
	if f.Intercept != nil {
		if fault, ok := f.Intercept(r); ok {
			if fault.RetryAfter != "" {
				w.Header().Set("Retry-After", fault.RetryAfter)
			}
			writeError(w, fault.Status, fault.Code, fault.Message)
			return
		}
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	status, body := f.route(r)
	if status < 300 && r.Method != http.MethodGet {
		f.writes++
	}
	if status >= 300 {
		e, _ := body.(apiError)
		writeError(w, status, e.code, e.message)
		return
	}
	if body == nil {
		w.WriteHeader(status)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

type apiError struct{ code, message string }

func fail(status int, code, message string) (int, any) {
	return status, apiError{code: code, message: message}
}

func (f *Fake) route(r *http.Request) (int, any) {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	switch {
	case len(parts) == 1 && parts[0] == "users":
		switch r.Method {
		case http.MethodGet:
			return f.listUsers(r)
		case http.MethodPost:
			return f.createUser(r)
		}
	case len(parts) == 2 && parts[0] == "users":
		return f.user(r, parts[1])
	case len(parts) == 4 && parts[0] == "users" && parts[2] == "manager" && parts[3] == "$ref":
		return f.manager(r, parts[1])
	case len(parts) >= 4 && parts[0] == "users" && parts[2] == "authentication":
		return f.methods(r, parts[1], parts[3:])
	case len(parts) == 3 && parts[0] == "directory" && parts[1] == "deletedItems" && parts[2] == "microsoft.graph.user" && r.Method == http.MethodGet:
		return http.StatusOK, map[string]any{"value": sorted(f.deleted)}
	case len(parts) == 4 && parts[0] == "directory" && parts[1] == "deletedItems" && parts[3] == "restore" && r.Method == http.MethodPost:
		return f.restore(parts[2])
	}
	return fail(http.StatusNotFound, "Request_ResourceNotFound", "no such route in the fake")
}

func (f *Fake) listUsers(r *http.Request) (int, any) {
	q := r.URL.Query()
	all := sorted(f.users)
	if filter := q.Get("$filter"); filter != "" {
		nick, ok := parseNicknameFilter(filter)
		if !ok {
			return fail(http.StatusBadRequest, "Request_UnsupportedQuery", "the fake supports only mailNickname eq")
		}
		all = slices.DeleteFunc(all, func(u map[string]any) bool { return u["mailNickname"] != nick })
	}
	expand := strings.HasPrefix(q.Get("$expand"), "manager")
	start, _ := strconv.Atoi(q.Get("$skiptoken"))
	end := min(start+f.PageSize, len(all))
	if f.PageSize <= 0 {
		end = len(all)
	}
	page := make([]map[string]any, 0, end-start)
	for _, u := range all[start:end] {
		out := public(u)
		if m, ok := f.managers[u["id"].(string)]; ok && expand {
			out["manager"] = map[string]any{"id": m}
		}
		page = append(page, out)
	}
	resp := map[string]any{"value": page}
	if end < len(all) {
		next := q
		next.Set("$skiptoken", strconv.Itoa(end))
		resp["@odata.nextLink"] = f.Server.URL + "/users?" + next.Encode()
	}
	return http.StatusOK, resp
}

func parseNicknameFilter(filter string) (string, bool) {
	rest, ok := strings.CutPrefix(filter, "mailNickname eq '")
	if !ok || !strings.HasSuffix(rest, "'") {
		return "", false
	}
	return strings.ReplaceAll(strings.TrimSuffix(rest, "'"), "''", "'"), true
}

func (f *Fake) createUser(r *http.Request) (int, any) {
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		return fail(http.StatusBadRequest, "Request_BadRequest", "bad JSON")
	}
	for _, key := range []string{"displayName", "mailNickname", "userPrincipalName", "passwordProfile", "accountEnabled"} {
		if _, ok := body[key]; !ok {
			return fail(http.StatusBadRequest, "Request_BadRequest", "missing "+key)
		}
	}
	pools := []map[string]map[string]any{f.users}
	if f.DeletedHoldUPN {
		pools = append(pools, f.deleted)
	}
	for _, pool := range pools {
		for _, u := range pool {
			if strings.EqualFold(str(u["userPrincipalName"]), str(body["userPrincipalName"])) {
				return fail(http.StatusBadRequest, "Request_BadRequest",
					"Another object with the same value for property userPrincipalName already exists.")
			}
		}
	}
	id := f.newID()
	body["id"] = id
	f.users[id] = body
	return http.StatusCreated, public(body)
}

func (f *Fake) user(r *http.Request, id string) (int, any) {
	u, ok := f.users[id]
	if !ok {
		return fail(http.StatusNotFound, "Request_ResourceNotFound", "user "+id+" does not exist")
	}
	switch r.Method {
	case http.MethodGet:
		return http.StatusOK, public(u)
	case http.MethodPatch:
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			return fail(http.StatusBadRequest, "Request_BadRequest", "bad JSON")
		}
		if _, ok := body["id"]; ok {
			return fail(http.StatusBadRequest, "Request_BadRequest", "id is read only")
		}
		maps.Copy(u, body)
		return http.StatusNoContent, nil
	case http.MethodDelete:
		f.deleted[id] = u
		delete(f.users, id)
		delete(f.managers, id)
		return http.StatusNoContent, nil
	}
	return fail(http.StatusMethodNotAllowed, "Request_BadRequest", r.Method)
}

func (f *Fake) manager(r *http.Request, id string) (int, any) {
	if _, ok := f.users[id]; !ok {
		return fail(http.StatusNotFound, "Request_ResourceNotFound", "user "+id+" does not exist")
	}
	switch r.Method {
	case http.MethodPut:
		var body struct {
			Ref string `json:"@odata.id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			return fail(http.StatusBadRequest, "Request_BadRequest", "bad JSON")
		}
		i := strings.LastIndex(body.Ref, "/users/")
		if i < 0 {
			return fail(http.StatusBadRequest, "Request_BadRequest", "bad @odata.id")
		}
		managerID := body.Ref[i+len("/users/"):]
		if _, ok := f.users[managerID]; !ok {
			return fail(http.StatusNotFound, "Request_ResourceNotFound", "manager "+managerID+" does not exist")
		}
		f.managers[id] = managerID
		return http.StatusNoContent, nil
	case http.MethodDelete:
		if _, ok := f.managers[id]; !ok {
			return fail(http.StatusNotFound, "Request_ResourceNotFound", "no manager")
		}
		delete(f.managers, id)
		return http.StatusNoContent, nil
	}
	return fail(http.StatusMethodNotAllowed, "Request_BadRequest", r.Method)
}

func (f *Fake) methods(r *http.Request, id string, rest []string) (int, any) {
	if _, ok := f.users[id]; !ok {
		return fail(http.StatusNotFound, "Request_ResourceNotFound", "user "+id+" does not exist")
	}
	switch {
	case len(rest) == 1 && rest[0] == "methods" && r.Method == http.MethodGet:
		out := []map[string]any{{"@odata.type": "#microsoft.graph.passwordAuthenticationMethod", "id": passwordMethodID}}
		for _, p := range f.phones[id] {
			out = append(out, map[string]any{
				"@odata.type": "#microsoft.graph.phoneAuthenticationMethod",
				"id":          p.ID, "phoneNumber": p.PhoneNumber, "phoneType": p.PhoneType,
			})
		}
		return http.StatusOK, map[string]any{"value": out}
	case len(rest) == 1 && rest[0] == "phoneMethods":
		return f.phoneCollection(r, id)
	case len(rest) == 2 && rest[0] == "phoneMethods":
		return f.phoneMethod(r, id, rest[1])
	}
	return fail(http.StatusNotFound, "Request_ResourceNotFound", "no such method route in the fake")
}

func (f *Fake) phoneCollection(r *http.Request, id string) (int, any) {
	switch r.Method {
	case http.MethodGet:
		return http.StatusOK, map[string]any{"value": f.phones[id]}
	case http.MethodPost:
		var p Phone
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil || phoneIDs[p.PhoneType] == "" {
			return fail(http.StatusBadRequest, "Request_BadRequest", "bad phone method")
		}
		for _, have := range f.phones[id] {
			if have.PhoneType == p.PhoneType {
				return fail(http.StatusBadRequest, "Request_BadRequest", "A phone method of this type already exists.")
			}
		}
		p.ID = phoneIDs[p.PhoneType]
		f.phones[id] = append(f.phones[id], p)
		return http.StatusCreated, p
	}
	return fail(http.StatusMethodNotAllowed, "Request_BadRequest", r.Method)
}

func (f *Fake) phoneMethod(r *http.Request, id, methodID string) (int, any) {
	i := slices.IndexFunc(f.phones[id], func(p Phone) bool { return p.ID == methodID })
	if i < 0 {
		return fail(http.StatusNotFound, "Request_ResourceNotFound", "no phone method "+methodID)
	}
	switch r.Method {
	case http.MethodPatch:
		var p Phone
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			return fail(http.StatusBadRequest, "Request_BadRequest", "bad phone method")
		}
		f.phones[id][i].PhoneNumber = p.PhoneNumber
		return http.StatusNoContent, nil
	case http.MethodDelete:
		f.phones[id] = slices.Delete(f.phones[id], i, i+1)
		return http.StatusNoContent, nil
	}
	return fail(http.StatusMethodNotAllowed, "Request_BadRequest", r.Method)
}

func (f *Fake) restore(id string) (int, any) {
	u, ok := f.deleted[id]
	if !ok {
		return fail(http.StatusNotFound, "Request_ResourceNotFound", "deleted item "+id+" does not exist")
	}
	for _, live := range f.users {
		if strings.EqualFold(str(live["userPrincipalName"]), str(u["userPrincipalName"])) {
			return fail(http.StatusBadRequest, "Request_BadRequest", "userPrincipalName already exists")
		}
	}
	f.users[id] = u
	delete(f.deleted, id)
	return http.StatusOK, public(u)
}

// public drops what Graph never returns, such as the password.
func public(u map[string]any) map[string]any {
	out := maps.Clone(u)
	delete(out, "passwordProfile")
	return out
}

func sorted(pool map[string]map[string]any) []map[string]any {
	out := make([]map[string]any, 0, len(pool))
	for _, id := range slices.Sorted(maps.Keys(pool)) {
		out = append(out, public(pool[id]))
	}
	return out
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": code, "message": message}})
}
