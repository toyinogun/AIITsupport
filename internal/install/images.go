package install

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// Images are the four images CI pushes for every commit on the default
// branch (AC-19); the install needs each at the cluster's image.tag (AC-2).
var Images = []string{"agent", "toolserver", "seed", "samba"}

// manifestTypes are the manifest kinds a multi arch push produces.
var manifestTypes = strings.Join([]string{
	"application/vnd.oci.image.index.v1+json",
	"application/vnd.docker.distribution.manifest.list.v2+json",
	"application/vnd.oci.image.manifest.v1+json",
	"application/vnd.docker.distribution.manifest.v2+json",
}, ", ")

// Registry checks that public images exist, with the registry's anonymous
// token flow (the Docker Registry HTTP API v2).
type Registry struct {
	Client *http.Client
	Scheme string // https, or http in tests
}

// Exists reports whether host/repo:tag has a manifest.
func (r Registry) Exists(ctx context.Context, host, repo, tag string) (bool, error) {
	manifest := fmt.Sprintf("%s://%s/v2/%s/manifests/%s", r.scheme(), host, repo, url.PathEscape(tag))
	status, challenge, err := r.head(ctx, manifest, "")
	if err != nil {
		return false, err
	}
	if status == http.StatusUnauthorized {
		token, err := r.token(ctx, challenge)
		if errors.Is(err, errNoPull) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if status, _, err = r.head(ctx, manifest, token); err != nil {
			return false, err
		}
	}
	switch status {
	case http.StatusOK:
		return true, nil
	case http.StatusNotFound, http.StatusUnauthorized, http.StatusForbidden:
		// A private or missing package looks the same anonymously.
		return false, nil
	default:
		return false, fmt.Errorf("registry %s answered %d for %s", host, status, repo)
	}
}

func (r Registry) scheme() string {
	if r.Scheme == "" {
		return "https"
	}
	return r.Scheme
}

// head asks for a manifest and returns the status and any login challenge.
func (r Registry) head(ctx context.Context, target, token string) (status int, challenge string, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, target, http.NoBody)
	if err != nil {
		return 0, "", fmt.Errorf("build the registry request: %w", err)
	}
	req.Header.Set("Accept", manifestTypes)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := r.Client.Do(req)
	if err != nil {
		return 0, "", fmt.Errorf("reach the registry: %w", err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode, resp.Header.Get("WWW-Authenticate"), nil
}

// errNoPull is a token service refusing an anonymous pull: GHCR answers 403
// for a package that does not exist or is private.
var errNoPull = errors.New("no anonymous pull")

// token gets an anonymous pull token from the realm a 401 names.
func (r Registry) token(ctx context.Context, challenge string) (string, error) {
	params := parseChallenge(challenge)
	realm := params["realm"]
	if realm == "" {
		return "", errors.New("the registry asked for a login without naming a token realm")
	}
	u, err := url.Parse(realm)
	if err != nil {
		return "", fmt.Errorf("the registry's token realm does not parse: %w", err)
	}
	q := u.Query()
	for _, k := range []string{"service", "scope"} {
		if v := params[k]; v != "" {
			q.Set(k, v)
		}
	}
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), http.NoBody)
	if err != nil {
		return "", fmt.Errorf("build the token request: %w", err)
	}
	resp, err := r.Client.Do(req)
	if err != nil {
		return "", fmt.Errorf("get a registry token: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound:
		return "", errNoPull
	default:
		return "", fmt.Errorf("the registry's token service answered %d", resp.StatusCode)
	}
	var body struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body); err != nil {
		return "", fmt.Errorf("parse the registry token: %w", err)
	}
	if body.Token != "" {
		return body.Token, nil
	}
	return body.AccessToken, nil
}

// parseChallenge reads `Bearer realm="...",service="...",scope="..."`.
func parseChallenge(h string) map[string]string {
	params := map[string]string{}
	_, rest, ok := strings.Cut(h, " ")
	if !ok || !strings.EqualFold(strings.Fields(h)[0], "bearer") {
		return params
	}
	for part := range strings.SplitSeq(rest, ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
		if ok {
			params[strings.ToLower(k)] = strings.Trim(v, `"`)
		}
	}
	return params
}
