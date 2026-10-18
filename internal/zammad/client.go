// Package zammad is a thin client for Zammad's REST API (spec 0006): the
// helpdesk's tickets, articles, and knowledge base. The tool server uses it
// with the noord-assistant token (#9); cmd/seed uses it with the local
// admin to fill the helpdesk.
//
// It never writes a Zammad user. Zammad lets every ticket.agent create and
// update users, so the wall is ours: the transport refuses any request that
// is not GET to a user, organization, or user token path, whatever calls it.
package zammad

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"
)

const maxResponseSize = 8 << 20

// Auth adds credentials to a request.
type Auth interface {
	apply(req *http.Request)
}

// TokenAuth is an API token, such as the noord-assistant's ZAMMAD_TOKEN.
type TokenAuth string

func (t TokenAuth) apply(req *http.Request) {
	req.Header.Set("Authorization", "Token token="+string(t))
}

// BasicAuth is a login and password, used only by cmd/seed's local admin.
type BasicAuth struct {
	Login, Password string
}

func (b BasicAuth) apply(req *http.Request) {
	req.SetBasicAuth(b.Login, b.Password)
}

// Options configures a Client.
type Options struct {
	// BaseURL is Zammad's root, such as http://127.0.0.1:58080 (ZAMMAD_URL).
	BaseURL string
	Auth    Auth
	// HTTPClient defaults to a plain client; its transport is wrapped by the
	// user write guard either way.
	HTTPClient *http.Client
	// Timeout applies to each request (ZAMMAD_TIMEOUT).
	Timeout time.Duration
}

// Client sends one Zammad request at a time.
type Client struct {
	base    string
	auth    Auth
	http    *http.Client
	timeout time.Duration
}

// New builds a client. The base URL must be http or https with a host.
func New(opts Options) (*Client, error) {
	if opts.Auth == nil {
		return nil, errors.New("zammad client: no credentials")
	}
	if opts.Timeout <= 0 {
		return nil, errors.New("zammad client: the request timeout must be positive")
	}
	base := strings.TrimSuffix(opts.BaseURL, "/")
	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, errors.New("zammad client: ZAMMAD_URL must be an http or https URL with a host")
	}
	hc := &http.Client{}
	if opts.HTTPClient != nil {
		copied := *opts.HTTPClient
		hc = &copied
	}
	hc.Transport = guard{next: hc.Transport}
	// A redirect would resend the credentials somewhere we did not choose.
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{base: base, auth: opts.Auth, http: hc, timeout: opts.Timeout}, nil
}

// Request is one Zammad call.
type Request struct {
	Method string
	// Path is relative to the base and starts with /api/v1/. It may carry a query.
	Path string
	Body any
	// Out receives the decoded JSON response, when not nil.
	Out any
	// From makes Zammad act as that user (login, email, or id). The caller
	// needs admin rights; cmd/seed uses it to author tickets and articles.
	From string
}

// Do sends the request once, under the per request timeout.
func (c *Client) Do(ctx context.Context, req Request) error {
	if !strings.HasPrefix(req.Path, "/api/v1/") {
		return fmt.Errorf("zammad: path %q must start with /api/v1/", stripQuery(req.Path))
	}
	var reader io.Reader
	if req.Body != nil {
		data, err := json.Marshal(req.Body)
		if err != nil {
			return fmt.Errorf("zammad %s %s: encode body: %w", req.Method, stripQuery(req.Path), err)
		}
		reader = bytes.NewReader(data)
	}

	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	hreq, err := http.NewRequestWithContext(ctx, req.Method, c.base+req.Path, reader)
	if err != nil {
		return fmt.Errorf("zammad %s %s: build request: %w", req.Method, stripQuery(req.Path), err)
	}
	c.auth.apply(hreq)
	hreq.Header.Set("Accept", "application/json")
	if reader != nil {
		hreq.Header.Set("Content-Type", "application/json")
	}
	if req.From != "" {
		hreq.Header.Set("From", req.From)
	}

	resp, err := c.http.Do(hreq)
	if err != nil {
		return fmt.Errorf("zammad %s %s: %w", req.Method, stripQuery(req.Path), unwrapURLError(err))
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
	if err != nil {
		return fmt.Errorf("zammad %s %s: read response: %w", req.Method, stripQuery(req.Path), err)
	}
	if resp.StatusCode >= 300 {
		return newError(req.Method, req.Path, resp.StatusCode, data)
	}
	if req.Out == nil || len(bytes.TrimSpace(data)) == 0 {
		return nil
	}
	if err := json.Unmarshal(data, req.Out); err != nil {
		return fmt.Errorf("zammad %s %s: decode response: %w", req.Method, stripQuery(req.Path), err)
	}
	return nil
}

// ErrUserWrite is the guard's refusal: this client never writes a Zammad
// user, organization, or user token (spec 0006, AC-6).
var ErrUserWrite = errors.New("zammad: this client never writes users, organizations, or user tokens")

// guardedPrefixes are the paths only GET may reach.
var guardedPrefixes = []string{"/api/v1/users", "/api/v1/organizations", "/api/v1/user_access_token"}

// guard is the transport wall behind ErrUserWrite.
type guard struct {
	next http.RoundTripper
}

func (g guard) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Method != http.MethodGet && Guarded(req.URL.Path) {
		if req.Body != nil {
			_ = req.Body.Close()
		}
		return nil, ErrUserWrite
	}
	next := g.next
	if next == nil {
		next = http.DefaultTransport
	}
	return next.RoundTrip(req)
}

// Guarded reports whether a request path reaches users, organizations, or
// user tokens. The path is normalised first: case, dot segments, repeated
// and trailing slashes, and a Rails format suffix such as .json.
func Guarded(p string) bool {
	p = stripQuery(p)
	if p == "" {
		return false
	}
	p = path.Clean("/" + strings.ToLower(p))
	for _, prefix := range guardedPrefixes {
		if p == prefix || strings.HasPrefix(p, prefix+"/") || strings.HasPrefix(p, prefix+".") {
			return true
		}
	}
	return false
}

// unwrapURLError drops net/http's *url.Error wrapper, whose text repeats
// the full URL (query included), and keeps the cause.
func unwrapURLError(err error) error {
	var uerr *url.Error
	if errors.As(err, &uerr) {
		return uerr.Err
	}
	return err
}
