package graph

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

// Sentinel errors for the managed marker fence (AC-10).
var (
	// ErrNotFound means no user has that mailNickname.
	ErrNotFound = errors.New("not found in Entra")
	// ErrNotManaged means the user exists but the sync does not manage it,
	// such as the tenant admin, so nothing here may touch it.
	ErrNotManaged = errors.New("not managed by the Noord sync")
)

// Error is a Graph error response.
type Error struct {
	Method  string
	Path    string
	Status  int
	Code    string
	Message string
}

func (e *Error) Error() string {
	msg := fmt.Sprintf("graph %s %s: %d %s", e.Method, e.Path, e.Status, http.StatusText(e.Status))
	if e.Code != "" {
		msg += " (" + e.Code + ")"
	}
	if e.Message != "" {
		msg += ": " + e.Message
	}
	return msg
}

// Cause is the fixed token cause.Of logs for a Graph error: graph_<status>,
// or graph when there is no status. It never holds the message.
func (e *Error) Cause() string {
	if e.Status <= 0 {
		return "graph"
	}
	return "graph_" + strconv.Itoa(e.Status)
}

func newError(method, path string, resp response) *Error {
	var body struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(resp.body, &body) // a body that is not Graph's shape still has a status
	return &Error{
		Method:  method,
		Path:    stripQuery(path),
		Status:  resp.status,
		Code:    body.Error.Code,
		Message: body.Error.Message,
	}
}

// stripQuery keeps $filter values (usernames) out of error text.
func stripQuery(path string) string {
	p, _, _ := strings.Cut(path, "?")
	return p
}

// Status returns the HTTP status of a Graph error, or 0.
func Status(err error) int {
	var gerr *Error
	if errors.As(err, &gerr) {
		return gerr.Status
	}
	return 0
}

// IsNotFound reports a 404.
func IsNotFound(err error) bool { return Status(err) == http.StatusNotFound }

// IsForbidden reports a 403: the app lacks a permission (or a licence).
func IsForbidden(err error) bool { return Status(err) == http.StatusForbidden }

// IsConflict reports Graph's "object already exists" answer, which comes as
// a 409 or as a 400 naming the clash.
func IsConflict(err error) bool {
	var gerr *Error
	if !errors.As(err, &gerr) {
		return false
	}
	if gerr.Status == http.StatusConflict || gerr.Code == "ObjectConflict" {
		return true
	}
	return gerr.Status == http.StatusBadRequest && strings.Contains(strings.ToLower(gerr.Message), "already exist")
}
