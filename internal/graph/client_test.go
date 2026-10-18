package graph

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// testClient points a client at handler and records every backoff instead
// of sleeping.
func testClient(t *testing.T, handler http.HandlerFunc, retries int) (*Client, *[]time.Duration) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	c, err := New(Options{
		BaseURL: srv.URL, Token: StaticToken("t"), MaxRetries: retries,
		Timeout: 2 * time.Second, ClientIDSetting: "SEED_GRAPH_CLIENT_ID",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	var waits []time.Duration
	c.sleep = func(_ context.Context, d time.Duration) error {
		waits = append(waits, d)
		return nil
	}
	return c, &waits
}

// sequence answers each request with the next status, then 200 forever.
func sequence(calls *atomic.Int32, statuses ...int) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		n := int(calls.Add(1)) - 1
		if n < len(statuses) {
			if statuses[n] == http.StatusTooManyRequests {
				w.Header().Set("Retry-After", "7")
			}
			w.WriteHeader(statuses[n])
			fmt.Fprint(w, `{"error":{"code":"x","message":"try again"}}`)
			return
		}
		fmt.Fprint(w, `{"value":[{"id":"a"}]}`)
	}
}

// covers: AC-12. A 429 waits Retry-After, then the call succeeds.
func TestRetryAfterThenSuccess(t *testing.T) {
	var calls atomic.Int32
	c, waits := testClient(t, sequence(&calls, http.StatusTooManyRequests), 5)
	var out struct{ Value []ObjectRef }
	if err := c.Do(t.Context(), Request{Method: http.MethodGet, Path: "/users", Out: &out}); err != nil {
		t.Fatalf("Do: %v", err)
	}
	if calls.Load() != 2 || len(out.Value) != 1 {
		t.Errorf("calls = %d, value = %v; want 2 calls and one item", calls.Load(), out.Value)
	}
	if len(*waits) != 1 || (*waits)[0] != 7*time.Second {
		t.Errorf("waits = %v, want [7s] from Retry-After", *waits)
	}
}

// covers: AC-12. An endless 503 fails after the configured retries.
func TestEndless503FailsAfterMaxRetries(t *testing.T) {
	var calls atomic.Int32
	c, waits := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}, 3)
	err := c.Do(t.Context(), Request{Method: http.MethodGet, Path: "/users"})
	if Status(err) != http.StatusServiceUnavailable || !strings.Contains(err.Error(), "after 3 retries") {
		t.Fatalf("Do = %v, want a 503 after 3 retries", err)
	}
	if calls.Load() != 4 || len(*waits) != 3 {
		t.Errorf("calls = %d, waits = %d; want 4 calls and 3 waits", calls.Load(), len(*waits))
	}
	for _, w := range *waits {
		if w <= 0 || w > maxBackoff {
			t.Errorf("backoff %s is outside (0, %s]", w, maxBackoff)
		}
	}
}

// covers: AC-12. A 404 right after a create is retried only when asked.
func TestNotFoundRetriedOnlyWhenAsked(t *testing.T) {
	var calls atomic.Int32
	c, _ := testClient(t, sequence(&calls, http.StatusNotFound), 5)
	if err := c.Do(t.Context(), Request{Method: http.MethodPost, Path: "/users/a/authentication/phoneMethods", RetryNotFound: true}); err != nil {
		t.Fatalf("Do with RetryNotFound: %v", err)
	}

	calls.Store(0)
	c2, _ := testClient(t, sequence(&calls, http.StatusNotFound), 5)
	err := c2.Do(t.Context(), Request{Method: http.MethodGet, Path: "/users/a"})
	if !IsNotFound(err) || calls.Load() != 1 {
		t.Errorf("Do = %v after %d calls, want one 404 and no retry", err, calls.Load())
	}
}

// covers: AC-12. A rejected secret names the setting, never the value.
func TestUnauthorizedNamesTheSetting(t *testing.T) {
	var calls atomic.Int32
	c, _ := testClient(t, sequence(&calls, http.StatusUnauthorized), 5)
	err := c.Do(t.Context(), Request{Method: http.MethodGet, Path: "/users"})
	if err == nil || !strings.Contains(err.Error(), "client secret for SEED_GRAPH_CLIENT_ID rejected or expired") {
		t.Fatalf("Do = %v, want the setting named", err)
	}
	if calls.Load() != 1 {
		t.Errorf("calls = %d, a 401 must not be retried", calls.Load())
	}
}

func TestTokenFailureNamesTheSetting(t *testing.T) {
	c, _ := testClient(t, func(http.ResponseWriter, *http.Request) {}, 0)
	c.token = failingToken{}
	err := c.Do(t.Context(), Request{Method: http.MethodGet, Path: "/users"})
	if err == nil || !strings.Contains(err.Error(), "SEED_GRAPH_CLIENT_ID") {
		t.Fatalf("Do = %v, want the setting named", err)
	}
}

type failingToken struct{}

func (failingToken) Token(context.Context) (string, error) { return "", errors.New("no network") }

// covers: AC-12. Each request has its own timeout.
func TestPerRequestTimeout(t *testing.T) {
	release := make(chan struct{})
	c, _ := testClient(t, func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}, 0)
	defer close(release)
	c.timeout = 50 * time.Millisecond
	err := c.Do(t.Context(), Request{Method: http.MethodGet, Path: "/users"})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Do = %v, want a deadline error", err)
	}
}

func TestListFollowsNextLink(t *testing.T) {
	var base string
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") == "2" {
			fmt.Fprint(w, `{"value":[{"id":"b"}]}`)
			return
		}
		fmt.Fprintf(w, `{"value":[{"id":"a"}],"@odata.nextLink":%q}`, base+"/users?page=2")
	}, 0)
	base = c.Base()
	got, err := ListAll[ObjectRef](t.Context(), c, "/users")
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	if len(got) != 2 || got[0].ID != "a" || got[1].ID != "b" {
		t.Errorf("got %v, want a then b", got)
	}
}

// The token only ever goes to Graph: a nextLink to another host is refused.
func TestRefusesLinkToAnotherHost(t *testing.T) {
	var calls atomic.Int32
	c, _ := testClient(t, sequence(&calls), 0)
	err := c.Do(t.Context(), Request{Method: http.MethodGet, Path: "https://evil.example/v1.0/users"})
	if err == nil || calls.Load() != 0 {
		t.Errorf("Do = %v after %d calls, want a refusal before any request", err, calls.Load())
	}
}

func TestIsConflict(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{&Error{Status: http.StatusConflict}, true},
		{&Error{Status: http.StatusBadRequest, Code: "ObjectConflict"}, true},
		{&Error{Status: http.StatusBadRequest, Message: "Another object with the same value for property userPrincipalName already exists."}, true},
		{&Error{Status: http.StatusBadRequest, Message: "Invalid value"}, false},
		{errors.New("plain"), false},
	}
	for _, c := range cases {
		if got := IsConflict(c.err); got != c.want {
			t.Errorf("IsConflict(%v) = %t, want %t", c.err, got, c.want)
		}
	}
}

func TestRetryAfterParsing(t *testing.T) {
	if got := retryAfter("3"); got != 3*time.Second {
		t.Errorf("retryAfter(3) = %s", got)
	}
	if got := retryAfter("soon"); got != 0 {
		t.Errorf("retryAfter(soon) = %s, want 0", got)
	}
	if got := backoff(0, 90*time.Second); got != maxBackoff {
		t.Errorf("backoff caps Retry-After at %s, got %s", maxBackoff, got)
	}
}

func TestErrorTextHasNoQuery(t *testing.T) {
	err := newError(http.MethodGet, "/users?$filter=mailNickname eq 'eva.peters'", response{status: 400})
	if strings.Contains(err.Error(), "eva.peters") {
		t.Errorf("error %q leaks the query", err)
	}
}
