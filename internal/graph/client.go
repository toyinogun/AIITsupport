// Package graph talks to Microsoft Graph, Entra ID's REST API (spec 0005).
// It holds the tool server side: the token, a transport that retries
// throttling and replication lag, paging, error mapping, and the reads and
// phone method writes the tool server may make. Every user lookup goes
// through the managed marker fence, so nothing here can reach a user the
// sync does not manage. User writes live in internal/seed/entra only.
package graph

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
)

// BaseURL is Graph v1.0.
const BaseURL = "https://graph.microsoft.com/v1.0"

const (
	graphScope      = "https://graph.microsoft.com/.default"
	maxResponseSize = 8 << 20
	firstBackoff    = time.Second
	maxBackoff      = 30 * time.Second
	runbook         = "docs/runbooks/entra-tenant.md"
)

// TokenSource returns a bearer token for Graph.
type TokenSource interface {
	Token(ctx context.Context) (string, error)
}

// StaticToken is a fixed token, for tests against a fake Graph.
type StaticToken string

// Token returns the fixed token.
func (s StaticToken) Token(context.Context) (string, error) { return string(s), nil }

// secretSource gets app only tokens with a client secret. The credential
// caches tokens until they expire.
type secretSource struct {
	cred *azidentity.ClientSecretCredential
}

func (s secretSource) Token(ctx context.Context) (string, error) {
	tok, err := s.cred.GetToken(ctx, policy.TokenRequestOptions{Scopes: []string{graphScope}})
	if err != nil {
		return "", err
	}
	return tok.Token, nil
}

// Options configures a Client.
type Options struct {
	// BaseURL defaults to Graph v1.0; tests point it at a fake.
	BaseURL string
	Token   TokenSource
	// HTTPClient defaults to a plain client; the per request Timeout applies either way.
	HTTPClient *http.Client
	MaxRetries int
	Timeout    time.Duration
	// ClientIDSetting names the setting that holds the client id, so a
	// rejected secret is reported by name, never by value.
	ClientIDSetting string
}

// Credentials are one app registration's client secret credentials.
type Credentials struct {
	TenantID, ClientID, ClientSecret string
}

// Client sends one Graph request at a time.
type Client struct {
	base       string
	token      TokenSource
	http       *http.Client
	maxRetries int
	timeout    time.Duration
	setting    string
	sleep      func(context.Context, time.Duration) error
}

// New builds a client from options.
func New(opts Options) (*Client, error) {
	if opts.Token == nil {
		return nil, errors.New("graph client: no token source")
	}
	if opts.Timeout <= 0 {
		return nil, errors.New("graph client: the request timeout must be positive")
	}
	if opts.MaxRetries < 0 {
		return nil, errors.New("graph client: max retries must not be negative")
	}
	c := &Client{
		base:       strings.TrimSuffix(opts.BaseURL, "/"),
		token:      opts.Token,
		http:       opts.HTTPClient,
		maxRetries: opts.MaxRetries,
		timeout:    opts.Timeout,
		setting:    opts.ClientIDSetting,
		sleep:      sleepCtx,
	}
	if c.base == "" {
		c.base = BaseURL
	}
	if c.http == nil {
		c.http = &http.Client{}
	}
	if c.setting == "" {
		c.setting = "the client id setting"
	}
	return c, nil
}

// NewWithSecret builds a client that authenticates as the app registration.
func NewWithSecret(creds Credentials, opts Options) (*Client, error) {
	cred, err := azidentity.NewClientSecretCredential(creds.TenantID, creds.ClientID, creds.ClientSecret, nil)
	if err != nil {
		return nil, fmt.Errorf("graph credential for %s: %w", opts.ClientIDSetting, err)
	}
	opts.Token = secretSource{cred: cred}
	return New(opts)
}

// Base is the Graph root this client talks to, for building @odata.id references.
func (c *Client) Base() string { return c.base }

// Request is one Graph call.
type Request struct {
	Method string
	// Path is relative to the base ("/users"), or an @odata.nextLink under it.
	Path string
	Body any
	// Out receives the decoded JSON response, when not nil.
	Out any
	// RetryNotFound retries a 404, for an object created or restored
	// moments ago that Graph has not replicated yet.
	RetryNotFound bool
}

// Do sends the request, retrying 429 and 503 (and 404 when asked) up to
// the configured count.
func (c *Client) Do(ctx context.Context, req Request) error {
	url, err := c.url(req.Path)
	if err != nil {
		return err
	}
	var body []byte
	if req.Body != nil {
		if body, err = json.Marshal(req.Body); err != nil {
			return fmt.Errorf("graph %s %s: encode body: %w", req.Method, req.Path, err)
		}
	}
	for attempt := 0; ; attempt++ {
		resp, err := c.once(ctx, req.Method, url, body)
		if err != nil {
			return err
		}
		if resp.status < 300 {
			return decode(req, resp.body)
		}
		gerr := newError(req.Method, req.Path, resp)
		if !c.retryable(req, resp.status) || attempt >= c.maxRetries {
			return c.final(gerr, attempt)
		}
		if err := c.sleep(ctx, backoff(attempt, resp.retryAfter)); err != nil {
			return fmt.Errorf("graph %s %s: %w", req.Method, req.Path, err)
		}
	}
}

// List pages through a collection, calling each for every page's items.
func List[T any](ctx context.Context, c *Client, path string, each func([]T) error) error {
	for path != "" {
		var page struct {
			Value    []T    `json:"value"`
			NextLink string `json:"@odata.nextLink"`
		}
		if err := c.Do(ctx, Request{Method: http.MethodGet, Path: path, Out: &page}); err != nil {
			return err
		}
		if err := each(page.Value); err != nil {
			return err
		}
		path = page.NextLink
	}
	return nil
}

// ListAll collects every item of a collection.
func ListAll[T any](ctx context.Context, c *Client, path string) ([]T, error) {
	var all []T
	err := List(ctx, c, path, func(items []T) error {
		all = append(all, items...)
		return nil
	})
	return all, err
}

// url resolves a path, refusing an absolute link to any other host so the
// token is only ever sent to Graph.
func (c *Client) url(path string) (string, error) {
	if strings.HasPrefix(path, "https://") || strings.HasPrefix(path, "http://") {
		if !strings.HasPrefix(path, c.base+"/") {
			return "", fmt.Errorf("graph: refusing a link outside %s", c.base)
		}
		return path, nil
	}
	if !strings.HasPrefix(path, "/") {
		return "", fmt.Errorf("graph: path %q must start with /", path)
	}
	return c.base + path, nil
}

type response struct {
	status     int
	body       []byte
	retryAfter time.Duration
}

// once sends one attempt under its own timeout.
func (c *Client) once(ctx context.Context, method, url string, body []byte) (response, error) {
	token, err := c.token.Token(ctx)
	if err != nil {
		var authErr *azidentity.AuthenticationFailedError
		if errors.As(err, &authErr) {
			return response{}, fmt.Errorf("client secret for %s rejected or expired, see %s: %w", c.setting, runbook, err)
		}
		return response{}, fmt.Errorf("get a Graph token for %s: %w", c.setting, err)
	}

	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, reader)
	if err != nil {
		return response{}, fmt.Errorf("graph %s: build request: %w", method, err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return response{}, fmt.Errorf("graph %s %s: %w", method, req.URL.Path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
	if err != nil {
		return response{}, fmt.Errorf("graph %s %s: read response: %w", method, req.URL.Path, err)
	}
	return response{status: resp.StatusCode, body: data, retryAfter: retryAfter(resp.Header.Get("Retry-After"))}, nil
}

func (c *Client) retryable(req Request, status int) bool {
	switch status {
	case http.StatusTooManyRequests, http.StatusServiceUnavailable:
		return true
	case http.StatusNotFound:
		return req.RetryNotFound
	}
	return false
}

// final names the setting on a 401 and says how often it tried.
func (c *Client) final(err *Error, attempt int) error {
	if err.Status == http.StatusUnauthorized {
		return fmt.Errorf("client secret for %s rejected or expired, see %s: %w", c.setting, runbook, err)
	}
	if attempt > 0 {
		return fmt.Errorf("after %d retries: %w", attempt, err)
	}
	return err
}

func decode(req Request, body []byte) error {
	if req.Out == nil || len(bytes.TrimSpace(body)) == 0 {
		return nil
	}
	if err := json.Unmarshal(body, req.Out); err != nil {
		return fmt.Errorf("graph %s %s: decode response: %w", req.Method, req.Path, err)
	}
	return nil
}

// backoff waits Retry-After when Graph sent one, else doubles from one
// second with jitter, capped at 30 seconds.
func backoff(attempt int, retryAfter time.Duration) time.Duration {
	if retryAfter > 0 {
		return min(retryAfter, maxBackoff)
	}
	d := min(firstBackoff<<attempt, maxBackoff)
	return d/2 + rand.N(d/2+1) //nolint:gosec // G404: jitter, not a secret
}

// retryAfter parses seconds or an HTTP date; anything else means none.
func retryAfter(v string) time.Duration {
	if v == "" {
		return 0
	}
	if s, err := strconv.Atoi(v); err == nil && s > 0 {
		return time.Duration(s) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		return time.Until(t)
	}
	return 0
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
