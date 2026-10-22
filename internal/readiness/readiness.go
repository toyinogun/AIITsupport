// Package readiness is the /readyz report both binaries serve and the smoke
// check reads (spec 0015, AC-16): one status and one fixed cause token per
// check, never error text. It is not a Kubernetes probe; liveness and
// readiness stay on /healthz.
package readiness

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/toyinogun/AIITsupport/internal/cause"
)

// Path is where both binaries serve the report.
const Path = "/readyz"

// CheckTimeout bounds each check, so one slow dependency cannot hold the
// report (the server sets no write timeout).
const CheckTimeout = 5 * time.Second

// maxBody caps a fetched report; ours are a few hundred bytes.
const maxBody = 64 << 10

// Status is one check's outcome.
type Status string

// The three statuses a check can have.
const (
	OK            Status = "ok"
	Unavailable   Status = "unavailable"
	NotConfigured Status = "not_configured"
)

// Result is one check in the report: its status and cause.Of of its error.
type Result struct {
	Status Status `json:"status"`
	Cause  string `json:"cause"`
}

// Report is the /readyz body.
type Report struct {
	Checks map[string]Result `json:"checks"`
}

// Ready reports whether every configured check is ok.
func (r Report) Ready() bool {
	for _, c := range r.Checks {
		if c.Status == Unavailable {
			return false
		}
	}
	return true
}

// Check is one named dependency. A nil Probe reports not_configured.
type Check struct {
	Name  string
	Probe func(context.Context) error
}

// Run runs every check at once, each bounded by timeout.
func Run(ctx context.Context, timeout time.Duration, checks []Check) Report {
	report := Report{Checks: make(map[string]Result, len(checks))}
	var (
		mu sync.Mutex
		wg sync.WaitGroup
	)
	for _, c := range checks {
		if c.Probe == nil {
			mu.Lock()
			report.Checks[c.Name] = Result{Status: NotConfigured, Cause: cause.Of(nil)}
			mu.Unlock()
			continue
		}
		wg.Go(func() {
			cctx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()
			err := c.Probe(cctx)
			res := Result{Status: OK, Cause: cause.Of(err)}
			if err != nil {
				res.Status = Unavailable
			}
			mu.Lock()
			report.Checks[c.Name] = res
			mu.Unlock()
		})
	}
	wg.Wait()
	return report
}

// Handler serves the report: 200 when Ready, else 503.
func Handler(checks ...Check) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		report := Run(r.Context(), CheckTimeout, checks)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		if !report.Ready() {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
		_ = json.NewEncoder(w).Encode(report) // a failed write means the client is gone
	})
}

// URL is the /readyz address on the same scheme and host as endpoint, such
// as the tool server's TOOLSERVER_URL (which ends in /mcp).
func URL(endpoint string) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("%q is not an http URL with a host", endpoint)
	}
	return (&url.URL{Scheme: u.Scheme, Host: u.Host, Path: Path}).String(), nil
}

// StatusError is a /readyz answer other than 200. Its cause token is
// http_<status>, so a 503 report and a refused token read differently.
type StatusError struct {
	Code int
}

func (e StatusError) Error() string { return fmt.Sprintf("readyz answered %d", e.Code) }

// Cause is the fixed token cause.Of logs: http_<status>.
func (e StatusError) Cause() string { return "http_" + strconv.Itoa(e.Code) }

// Fetch GETs a /readyz URL, with a bearer token when token is set. It
// returns the report it could decode and a StatusError for any status but
// 200, so a caller can show which check failed.
func Fetch(ctx context.Context, client *http.Client, url, token string) (Report, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Report{}, fmt.Errorf("build the readyz request: %w", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := client.Do(req)
	if err != nil {
		return Report{}, fmt.Errorf("call readyz: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	var report Report
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if readErr == nil && resp.StatusCode != http.StatusUnauthorized {
		_ = json.Unmarshal(body, &report) // a body that is not a report still has a status
	}
	if resp.StatusCode != http.StatusOK {
		return report, StatusError{Code: resp.StatusCode}
	}
	if readErr != nil {
		return report, fmt.Errorf("read readyz: %w", readErr)
	}
	return report, nil
}
