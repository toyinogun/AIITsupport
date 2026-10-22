package zammad

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

// Error is a Zammad error response. It names the method, path, and status,
// never the credentials or the query string.
type Error struct {
	Method  string
	Path    string
	Status  int
	Message string
}

func (e *Error) Error() string {
	msg := fmt.Sprintf("zammad %s %s: %d %s", e.Method, e.Path, e.Status, http.StatusText(e.Status))
	if e.Message != "" {
		msg += ": " + e.Message
	}
	return msg
}

// Cause is the fixed token cause.Of logs for a Zammad error: zammad_<status>.
// It never holds the message.
func (e *Error) Cause() string { return "zammad_" + strconv.Itoa(e.Status) }

// maxMessage caps the error text taken from a response body.
const maxMessage = 300

func newError(method, path string, status int, body []byte) *Error {
	var parsed struct {
		Error      string `json:"error"`
		ErrorHuman string `json:"error_human"`
	}
	_ = json.Unmarshal(body, &parsed) // a body that is not Zammad's shape still has a status
	msg := parsed.ErrorHuman
	if msg == "" {
		msg = parsed.Error
	}
	if len(msg) > maxMessage {
		msg = msg[:maxMessage] + "..."
	}
	return &Error{Method: method, Path: stripQuery(path), Status: status, Message: msg}
}

// stripQuery keeps search terms (logins, names) out of error text.
func stripQuery(p string) string {
	before, _, _ := strings.Cut(p, "?")
	return before
}

// Status returns the HTTP status of a Zammad error, or 0.
func Status(err error) int {
	var zerr *Error
	if errors.As(err, &zerr) {
		return zerr.Status
	}
	return 0
}

// IsForbidden reports a 403.
func IsForbidden(err error) bool { return Status(err) == http.StatusForbidden }

// IsUnauthorized reports a 401: wrong credentials.
func IsUnauthorized(err error) bool { return Status(err) == http.StatusUnauthorized }
