package zammad

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// User is a Zammad user as the API returns it. This package only reads users.
type User struct {
	ID             int    `json:"id"`
	Login          string `json:"login"`
	Firstname      string `json:"firstname"`
	Lastname       string `json:"lastname"`
	Email          string `json:"email"`
	Active         bool   `json:"active"`
	OrganizationID *int   `json:"organization_id"`
	RoleIDs        []int  `json:"role_ids"`
}

// Me returns the user the credentials belong to.
func (c *Client) Me(ctx context.Context) (User, error) {
	var u User
	err := c.Do(ctx, Request{Method: http.MethodGet, Path: "/api/v1/users/me", Out: &u})
	return u, err
}

// maxSearchLimit is the most users one search returns.
const maxSearchLimit = 100

// SearchUsers runs Zammad's user search (a login, name, or email). Zammad's
// search index can lag a fresh LDAP import, so the seed never relies on it.
func (c *Client) SearchUsers(ctx context.Context, query string, limit int) ([]User, error) {
	if limit <= 0 || limit > maxSearchLimit {
		limit = maxSearchLimit
	}
	q := url.Values{"query": {query}, "limit": {fmt.Sprint(limit)}}
	var users []User
	err := c.Do(ctx, Request{Method: http.MethodGet, Path: "/api/v1/users/search?" + q.Encode(), Out: &users})
	return users, err
}

// UsersByLogin returns the active users whose login equals login exactly.
// It searches for the quoted login, then filters client side, because
// Zammad's search matches loosely. The search index lags a fresh LDAP import,
// so a new user can be missing for a while (spec 0006).
func (c *Client) UsersByLogin(ctx context.Context, login string) ([]User, error) {
	quoted := strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(login)
	found, err := c.SearchUsers(ctx, `login:"`+quoted+`"`, maxSearchLimit)
	if err != nil {
		return nil, err
	}
	var out []User
	for _, u := range found {
		if u.Login == login && u.Active {
			out = append(out, u)
		}
	}
	return out, nil
}
