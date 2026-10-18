package graph

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

// ManagedMarker is the employeeType every user the Noord sync manages
// carries. Only marked users are ever written, and the tool server refuses
// any other user (AC-10).
const ManagedMarker = "noord-sync"

// UserSelect is every user field the sync maps or the fence reads.
const UserSelect = "id,mailNickname,userPrincipalName,displayName,givenName,surname," +
	"department,jobTitle,accountEnabled,companyName,employeeType,usageLocation"

// User is the slice of an Entra user the sync and the fence need. Nullable
// fields Graph may return as null are pointers where null differs from "".
type User struct {
	ID                string  `json:"id"`
	MailNickname      string  `json:"mailNickname"`
	UserPrincipalName string  `json:"userPrincipalName"`
	DisplayName       string  `json:"displayName"`
	GivenName         string  `json:"givenName"`
	Surname           string  `json:"surname"`
	Department        *string `json:"department"`
	JobTitle          *string `json:"jobTitle"`
	AccountEnabled    bool    `json:"accountEnabled"`
	CompanyName       string  `json:"companyName"`
	EmployeeType      string  `json:"employeeType"`
	UsageLocation     string  `json:"usageLocation"`
	// Manager is set only when the read asked for $expand=manager.
	Manager *ObjectRef `json:"manager,omitempty"`
}

// ObjectRef is a directory object known only by id.
type ObjectRef struct {
	ID string `json:"id"`
}

// DeletedUser is a user in the recycle bin.
type DeletedUser struct {
	ID                string `json:"id"`
	MailNickname      string `json:"mailNickname"`
	UserPrincipalName string `json:"userPrincipalName"`
	EmployeeType      string `json:"employeeType"`
}

// ManagedUser is a user that passed the marker fence. It can only come from
// Managed or UserByUsername, so every auth method call is fenced by type.
type ManagedUser struct {
	user User
}

// User returns the fenced user's fields.
func (m ManagedUser) User() User { return m.user }

// ID is the user's Entra object id.
func (m ManagedUser) ID() string { return m.user.ID }

// Managed fences a user: ErrNotManaged unless it carries the marker.
func Managed(u User) (ManagedUser, error) {
	if u.ID == "" {
		return ManagedUser{}, fmt.Errorf("user %q has no id", u.MailNickname)
	}
	if u.EmployeeType != ManagedMarker {
		return ManagedUser{}, fmt.Errorf("user %q: %w", u.MailNickname, ErrNotManaged)
	}
	return ManagedUser{user: u}, nil
}

// usernamePattern is the sAMAccountName shape Noord uses; anything else is
// refused before it reaches a $filter.
var usernamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// UserByUsername finds a user by mailNickname, the sync's anchor, and fences
// it: ErrNotFound for none, ErrNotManaged for a user without the marker.
func (c *Client) UserByUsername(ctx context.Context, username string) (ManagedUser, error) {
	users, err := c.UsersByNickname(ctx, username)
	if err != nil {
		return ManagedUser{}, err
	}
	switch len(users) {
	case 0:
		return ManagedUser{}, fmt.Errorf("user %q: %w", username, ErrNotFound)
	case 1:
		return Managed(users[0])
	default:
		return ManagedUser{}, fmt.Errorf("user %q: %d Entra users share this mailNickname", username, len(users))
	}
}

// UsersByNickname returns every live user with that mailNickname, managed
// or not, unfenced. The sync uses it to detect collisions.
func (c *Client) UsersByNickname(ctx context.Context, username string) ([]User, error) {
	if !usernamePattern.MatchString(username) {
		return nil, fmt.Errorf("%q is not a username", username)
	}
	filter := "mailNickname eq '" + strings.ReplaceAll(username, "'", "''") + "'"
	path := "/users" + Query("$filter", filter, "$select", UserSelect)
	var page struct {
		Value []User `json:"value"`
	}
	if err := c.Do(ctx, Request{Method: http.MethodGet, Path: path, Out: &page}); err != nil {
		return nil, fmt.Errorf("look up user %q: %w", username, err)
	}
	return page.Value, nil
}

// Query builds a query string from name, value pairs, with spaces as %20.
func Query(pairs ...string) string {
	var b strings.Builder
	for i := 0; i+1 < len(pairs); i += 2 {
		if i == 0 {
			b.WriteByte('?')
		} else {
			b.WriteByte('&')
		}
		b.WriteString(pairs[i])
		b.WriteByte('=')
		b.WriteString(strings.ReplaceAll(url.QueryEscape(pairs[i+1]), "+", "%20"))
	}
	return b.String()
}

// UserPath is /users/{id} with the id escaped.
func UserPath(id string) string { return "/users/" + url.PathEscape(id) }
