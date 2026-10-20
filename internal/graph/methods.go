package graph

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// Phone types Graph allows: one of each per user.
const (
	PhoneMobile          = "mobile"
	PhoneAlternateMobile = "alternateMobile"
	PhoneOffice          = "office"
)

// AuthMethod is one registered sign in method.
type AuthMethod struct {
	ID string
	// Type is the short Graph type, such as "phone", "password", or "microsoftAuthenticator".
	Type string
	// PhoneNumber and PhoneType are set only for phone methods.
	PhoneNumber string
	PhoneType   string
}

// PhoneMethod is one phone authentication method.
type PhoneMethod struct {
	ID          string `json:"id"`
	PhoneNumber string `json:"phoneNumber"`
	PhoneType   string `json:"phoneType"`
}

type rawMethod struct {
	ODataType   string `json:"@odata.type"`
	ID          string `json:"id"`
	PhoneNumber string `json:"phoneNumber"`
	PhoneType   string `json:"phoneType"`
}

// AuthMethods lists every method a managed user has registered.
func (c *Client) AuthMethods(ctx context.Context, u ManagedUser) ([]AuthMethod, error) {
	if u.ID() == "" {
		return nil, fmt.Errorf("auth methods: %w", ErrNotManaged)
	}
	raw, err := ListAll[rawMethod](ctx, c, methodsPath(u)+"/methods")
	if err != nil {
		return nil, fmt.Errorf("list auth methods of %s: %w", u.user.MailNickname, err)
	}
	out := make([]AuthMethod, 0, len(raw))
	for _, m := range raw {
		out = append(out, AuthMethod{ID: m.ID, Type: shortType(m.ODataType), PhoneNumber: m.PhoneNumber, PhoneType: m.PhoneType})
	}
	return out, nil
}

// PhoneMethods lists a managed user's phone methods.
func (c *Client) PhoneMethods(ctx context.Context, u ManagedUser) ([]PhoneMethod, error) {
	if u.ID() == "" {
		return nil, fmt.Errorf("phone methods: %w", ErrNotManaged)
	}
	methods, err := ListAll[PhoneMethod](ctx, c, methodsPath(u)+"/phoneMethods")
	if err != nil {
		return nil, fmt.Errorf("list phone methods of %s: %w", u.user.MailNickname, err)
	}
	return methods, nil
}

// DeletePhoneMethod removes one phone method from a managed user.
func (c *Client) DeletePhoneMethod(ctx context.Context, u ManagedUser, methodID string) error {
	if u.ID() == "" {
		return fmt.Errorf("delete phone method: %w", ErrNotManaged)
	}
	path := methodsPath(u) + "/phoneMethods/" + url.PathEscape(methodID)
	if err := c.Do(ctx, Request{Method: http.MethodDelete, Path: path}); err != nil {
		return fmt.Errorf("delete phone method of %s: %w", u.user.MailNickname, err)
	}
	return nil
}

// AuthenticatorMethod is one Microsoft Authenticator registration. Only the
// id is kept: the device name never leaves Graph's answer (spec 0012, AC-22).
type AuthenticatorMethod struct {
	ID string `json:"id"`
}

// Short method types from AuthMethods (spec 0012).
const (
	MethodPassword      = "password"
	MethodPhone         = "phone"
	MethodAuthenticator = "microsoftAuthenticator"
)

// AuthenticatorMethods lists a managed user's Microsoft Authenticator methods.
func (c *Client) AuthenticatorMethods(ctx context.Context, u ManagedUser) ([]AuthenticatorMethod, error) {
	if u.ID() == "" {
		return nil, fmt.Errorf("authenticator methods: %w", ErrNotManaged)
	}
	methods, err := ListAll[AuthenticatorMethod](ctx, c, methodsPath(u)+"/microsoftAuthenticatorMethods")
	if err != nil {
		return nil, fmt.Errorf("list authenticator methods of %s: %w", u.user.MailNickname, err)
	}
	return methods, nil
}

// DeleteAuthenticatorMethod removes one Microsoft Authenticator method from a managed user.
func (c *Client) DeleteAuthenticatorMethod(ctx context.Context, u ManagedUser, methodID string) error {
	if u.ID() == "" {
		return fmt.Errorf("delete authenticator method: %w", ErrNotManaged)
	}
	path := methodsPath(u) + "/microsoftAuthenticatorMethods/" + url.PathEscape(methodID)
	if err := c.Do(ctx, Request{Method: http.MethodDelete, Path: path}); err != nil {
		return fmt.Errorf("delete authenticator method of %s: %w", u.user.MailNickname, err)
	}
	return nil
}

// SignInPreference is the user's preferred second factor as Graph names it
// (signInPreferences.userPreferredMethodForSecondaryAuthentication), such as
// "push", "sms", or "voiceOffice"; empty when none is set.
func (c *Client) SignInPreference(ctx context.Context, u ManagedUser) (string, error) {
	if u.ID() == "" {
		return "", fmt.Errorf("sign in preference: %w", ErrNotManaged)
	}
	var out struct {
		Preferred string `json:"userPreferredMethodForSecondaryAuthentication"`
	}
	if err := c.Do(ctx, Request{Method: http.MethodGet, Path: methodsPath(u) + "/signInPreferences", Out: &out}); err != nil {
		return "", fmt.Errorf("read sign in preference of %s: %w", u.user.MailNickname, err)
	}
	return out.Preferred, nil
}

func methodsPath(u ManagedUser) string { return UserPath(u.ID()) + "/authentication" }

// shortType turns "#microsoft.graph.phoneAuthenticationMethod" into "phone".
func shortType(odataType string) string {
	t := strings.TrimPrefix(odataType, "#microsoft.graph.")
	return strings.TrimSuffix(t, "AuthenticationMethod")
}

// MaskPhone keeps only the last 2 digits of a phone number (AC-9, AC-15).
func MaskPhone(number string) string {
	digits := 0
	for _, r := range number {
		if r >= '0' && r <= '9' {
			digits++
		}
	}
	var b strings.Builder
	seen := 0
	for _, r := range number {
		if r >= '0' && r <= '9' {
			seen++
			if seen <= digits-2 {
				r = '*'
			}
		}
		b.WriteRune(r)
	}
	return b.String()
}

// MarshalJSON keeps a full phone number out of any JSON log of a method.
func (m AuthMethod) MarshalJSON() ([]byte, error) {
	type masked AuthMethod
	m.PhoneNumber = MaskPhone(m.PhoneNumber)
	return json.Marshal(masked(m))
}
