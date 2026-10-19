package ldap

import (
	"context"
	"encoding/binary"
	"fmt"
	"strconv"
	"time"
	"unicode/utf16"

	goldap "github.com/go-ldap/ldap/v3"
)

// Password and policy attributes (spec 0011).
const (
	attrUnicodePwd       = "unicodePwd"
	attrPwdLastSet       = "pwdLastSet"
	attrMinPwdLength     = "minPwdLength"
	attrPwdProperties    = "pwdProperties"
	attrPwdHistoryLength = "pwdHistoryLength"
)

// pwdComplex is the complexity bit in the domain's pwdProperties.
const pwdComplex = 0x1

// oidPolicyHints is AD's password policy hints control: with it, a reset is
// held to the password history like a change (spec 0011, AC-11).
const oidPolicyHints = "1.2.840.113556.1.4.2239"

// policyHintsOn is the control's value, BER SEQUENCE { INTEGER 1 }.
var policyHintsOn = string([]byte{0x30, 0x03, 0x02, 0x01, 0x01})

// PasswordPolicy is the domain's password rules the reset page shows.
type PasswordPolicy struct {
	MinLength     int
	Complex       bool
	HistoryLength int
}

// PasswordPolicy reads the domain root's minPwdLength, pwdProperties, and
// pwdHistoryLength.
func (c *Client) PasswordPolicy(ctx context.Context) (PasswordPolicy, error) {
	var p PasswordPolicy
	err := c.session(ctx, func(conn *goldap.Conn) error {
		e, err := ReadOne(conn, c.cfg.BaseDN, attrMinPwdLength, attrPwdProperties, attrPwdHistoryLength)
		if err != nil {
			return err
		}
		if e == nil {
			return fmt.Errorf("domain root %s not found", c.cfg.BaseDN)
		}
		minLen, err1 := strconv.Atoi(e.GetAttributeValue(attrMinPwdLength))
		props, err2 := strconv.Atoi(e.GetAttributeValue(attrPwdProperties))
		hist, err3 := strconv.Atoi(e.GetAttributeValue(attrPwdHistoryLength))
		if err1 != nil || err2 != nil || err3 != nil {
			return fmt.Errorf("read the password policy of %s: not a number", c.cfg.BaseDN)
		}
		p = PasswordPolicy{MinLength: minLen, Complex: props&pwdComplex != 0, HistoryLength: hist}
		return nil
	})
	return p, err
}

// PasswordLastSet reads a user's pwdLastSet; zero when it is 0 or unset.
func (c *Client) PasswordLastSet(ctx context.Context, dn string) (time.Time, error) {
	var at time.Time
	err := c.session(ctx, func(conn *goldap.Conn) error {
		e, err := ReadOne(conn, dn, attrPwdLastSet)
		if err != nil {
			return err
		}
		if e == nil {
			return fmt.Errorf("%s not found", dn)
		}
		at, err = fileTime(e.GetAttributeValue(attrPwdLastSet))
		return err
	})
	return at, err
}

// FindUserByAccount finds the one user whose sAMAccountName equals name,
// searched from the base DN, enabled or not; ok is false when there is none.
// More than one match is ErrTooManyMatches.
func (c *Client) FindUserByAccount(ctx context.Context, name string) (u User, ok bool, err error) {
	err = c.session(ctx, func(conn *goldap.Conn) error {
		filter := "(&(objectClass=user)(objectCategory=person)(sAMAccountName=" + goldap.EscapeFilter(name) + "))"
		res, err := conn.Search(goldap.NewSearchRequest(c.cfg.BaseDN, goldap.ScopeWholeSubtree, goldap.NeverDerefAliases,
			2, 0, false, filter, userAttrs, nil))
		if goldap.IsErrorWithCode(err, goldap.LDAPResultSizeLimitExceeded) || (err == nil && len(res.Entries) > 1) {
			return ErrTooManyMatches
		}
		if err != nil {
			return fmt.Errorf("search user by account: %w", err)
		}
		if len(res.Entries) == 1 {
			u, ok = toUser(res.Entries[0]), true
		}
		return nil
	})
	return u, ok, err
}

// UserInChainMemberOf reports whether the user at dn is a member, at any
// depth of nesting, of any of the groups in parents (DNs).
func (c *Client) UserInChainMemberOf(ctx context.Context, dn string, parents []string) (bool, error) {
	var found bool
	err := c.session(ctx, func(conn *goldap.Conn) error {
		for _, parent := range parents {
			filter := fmt.Sprintf("(memberOf:%s:=%s)", ruleInChain, goldap.EscapeFilter(parent))
			res, err := conn.Search(goldap.NewSearchRequest(dn, goldap.ScopeBaseObject, goldap.NeverDerefAliases,
				1, 0, false, filter, []string{"1.1"}, nil))
			if err != nil {
				return fmt.Errorf("search nested membership: %w", err)
			}
			if len(res.Entries) > 0 {
				found = true
				return nil
			}
		}
		return nil
	})
	return found, err
}

// PasswordWriter holds the directory's password reset, made as svc-pwreset.
// Only the reset page holds one (spec 0011, AC-15).
type PasswordWriter struct {
	cfg Config
}

// NewPasswordWriter returns the reset writer; cfg binds as svc-pwreset.
func NewPasswordWriter(cfg Config) *PasswordWriter { return &PasswordWriter{cfg: cfg} }

// SetPassword resets the user's password and clears any lockout in one
// modify, with the policy hints control so AD applies the history rule.
func (w *PasswordWriter) SetPassword(ctx context.Context, dn, password string) error {
	return session(ctx, w.cfg, func(conn *goldap.Conn) error {
		req := goldap.NewModifyRequest(dn, []goldap.Control{goldap.NewControlString(oidPolicyHints, false, policyHintsOn)})
		req.Replace(attrUnicodePwd, []string{encodePassword(password)})
		req.Replace(attrLockoutTime, []string{"0"})
		if err := conn.Modify(req); err != nil {
			return fmt.Errorf("set the password: %w", err)
		}
		return nil
	})
}

// IsPolicyRefusal reports whether AD refused a password for its rules:
// constraintViolation (19) or unwillingToPerform (53).
func IsPolicyRefusal(err error) bool {
	return goldap.IsErrorAnyOf(err, goldap.LDAPResultConstraintViolation, goldap.LDAPResultUnwillingToPerform)
}

// encodePassword is unicodePwd's form: the password in double quotes, UTF-16LE.
func encodePassword(password string) string {
	units := utf16.Encode([]rune(`"` + password + `"`))
	b := make([]byte, 2*len(units))
	for i, u := range units {
		binary.LittleEndian.PutUint16(b[2*i:], u)
	}
	return string(b)
}
