package ldap

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	goldap "github.com/go-ldap/ldap/v3"
)

// Matching rules AD and Samba support in extensible filters.
const (
	// ruleBitAnd tests a bit in an integer attribute.
	ruleBitAnd = "1.2.840.113556.1.4.803"
	// ruleInChain follows group nesting to any depth.
	ruleInChain = "1.2.840.113556.1.4.1941"
)

// ufAccountDisable is the disabled bit in userAccountControl.
const ufAccountDisable = 0x2

// maxMailMatches caps the identity search; more is treated as ambiguous.
const maxMailMatches = 50

// ErrTooManyMatches means a search hit its size limit.
var ErrTooManyMatches = errors.New("ldap: too many matching entries")

// Config is how the tool server reaches the directory as svc-itsupport.
type Config struct {
	Dialer   Dialer
	BindDN   string // LDAP_BIND_DN
	Password string // LDAP_BIND_PASSWORD
	BaseDN   string // LDAP_BASE_DN
}

// Client makes the tool server's directory reads. Each call dials, binds,
// runs, and closes; there is no shared connection. It cannot write: the one
// write lives on Writer.
type Client struct {
	cfg Config
}

// NewClient returns a read only directory client.
func NewClient(cfg Config) *Client { return &Client{cfg: cfg} }

// User is a directory user as the tool server sees it.
type User struct {
	DN          string
	Username    string // sAMAccountName
	DisplayName string
	Department  string
	Mail        string
	Enabled     bool
}

// Lock is a user's lock state with when it ends.
type Lock struct {
	Locked bool
	// EndsAt is when the domain unlocks the account by itself; zero when it
	// is not locked or stays locked until an admin clears it.
	EndsAt time.Time
}

// Group is a directory group.
type Group struct {
	DN   string
	Name string // sAMAccountName
}

var userAttrs = []string{"sAMAccountName", "displayName", "department", "mail", "userAccountControl"}

// FindUsersByMail returns the enabled users whose mail matches, searched
// from the base DN. The value is escaped; the caller still compares case and
// checks the OU. More than 50 matches is ErrTooManyMatches.
func (c *Client) FindUsersByMail(ctx context.Context, mail string) ([]User, error) {
	var users []User
	err := c.session(ctx, func(conn *goldap.Conn) error {
		var err error
		users, err = usersByMail(conn, c.cfg.BaseDN, mail)
		return err
	})
	return users, err
}

// usersByMail runs the identity search on conn. Hitting the size limit is
// ErrTooManyMatches, even when go-ldap also returns the partial result.
func usersByMail(conn goldap.Client, base, mail string) ([]User, error) {
	filter := fmt.Sprintf("(&(objectClass=user)(objectCategory=person)(mail=%s)(!(userAccountControl:%s:=%d)))",
		goldap.EscapeFilter(mail), ruleBitAnd, ufAccountDisable)
	res, err := conn.Search(goldap.NewSearchRequest(base, goldap.ScopeWholeSubtree, goldap.NeverDerefAliases,
		maxMailMatches, 0, false, filter, userAttrs, nil))
	if goldap.IsErrorWithCode(err, goldap.LDAPResultSizeLimitExceeded) {
		return nil, ErrTooManyMatches
	}
	if err != nil {
		return nil, fmt.Errorf("search users by mail: %w", err)
	}
	users := make([]User, 0, len(res.Entries))
	for _, e := range res.Entries {
		users = append(users, toUser(e))
	}
	return users, nil
}

// GetUser reads one user by DN; ok is false when it does not exist.
func (c *Client) GetUser(ctx context.Context, dn string) (u User, ok bool, err error) {
	err = c.session(ctx, func(conn *goldap.Conn) error {
		e, err := ReadOne(conn, dn, userAttrs...)
		if err != nil || e == nil {
			return err
		}
		u, ok = toUser(e), true
		return nil
	})
	return u, ok, err
}

// Lock reads a user's lock state and, when locked, when the lock ends (from
// lockoutTime and the domain's lockoutDuration).
func (c *Client) Lock(ctx context.Context, dn string) (Lock, error) {
	var lock Lock
	err := c.session(ctx, func(conn *goldap.Conn) error {
		state, err := State(conn, dn)
		if err != nil || !state.Locked {
			return err
		}
		lock.Locked = true
		duration, forever, err := LockoutDuration(conn, c.cfg.BaseDN)
		if err != nil {
			return err
		}
		if !forever && !state.LockedAt.IsZero() {
			lock.EndsAt = state.LockedAt.Add(duration)
		}
		return nil
	})
	return lock, err
}

// GroupsByName resolves group sAMAccountNames to groups, in one search.
// Names that do not exist are absent from the map.
func (c *Client) GroupsByName(ctx context.Context, names []string) (map[string]Group, error) {
	groups := make(map[string]Group, len(names))
	if len(names) == 0 {
		return groups, nil
	}
	filter := "(&(objectClass=group)(|"
	for _, n := range names {
		filter += "(sAMAccountName=" + goldap.EscapeFilter(n) + ")"
	}
	filter += "))"
	err := c.session(ctx, func(conn *goldap.Conn) error {
		res, err := conn.Search(goldap.NewSearchRequest(c.cfg.BaseDN, goldap.ScopeWholeSubtree, goldap.NeverDerefAliases,
			0, 0, false, filter, []string{"sAMAccountName"}, nil))
		if err != nil {
			return fmt.Errorf("search groups by name: %w", err)
		}
		for _, e := range res.Entries {
			g := Group{DN: e.DN, Name: e.GetAttributeValue("sAMAccountName")}
			groups[g.Name] = g
		}
		return nil
	})
	return groups, err
}

// InChainMemberOf reports whether the group named name is a member, at any
// depth of nesting, of any of the groups in parents. It runs one search per
// parent with the in chain matching rule, on one connection.
func (c *Client) InChainMemberOf(ctx context.Context, name string, parents []string) (bool, error) {
	var found bool
	err := c.session(ctx, func(conn *goldap.Conn) error {
		for _, parent := range parents {
			filter := fmt.Sprintf("(&(objectClass=group)(sAMAccountName=%s)(memberOf:%s:=%s))",
				goldap.EscapeFilter(name), ruleInChain, goldap.EscapeFilter(parent))
			res, err := conn.Search(goldap.NewSearchRequest(c.cfg.BaseDN, goldap.ScopeWholeSubtree, goldap.NeverDerefAliases,
				1, 0, false, filter, []string{"1.1"}, nil))
			switch {
			case goldap.IsErrorWithCode(err, goldap.LDAPResultSizeLimitExceeded):
				found = true // more than one match is still a match
			case err != nil:
				return fmt.Errorf("search nested membership: %w", err)
			default:
				found = len(res.Entries) > 0
			}
			if found {
				return nil
			}
		}
		return nil
	})
	return found, err
}

// session dials, binds as the service account, runs fn, and closes.
func (c *Client) session(ctx context.Context, fn func(*goldap.Conn) error) error {
	return session(ctx, c.cfg, fn)
}

func session(ctx context.Context, cfg Config, fn func(*goldap.Conn) error) error {
	conn, closeConn, err := cfg.Dialer.DialContext(ctx)
	if err != nil {
		return err
	}
	defer closeConn()
	if err := conn.Bind(cfg.BindDN, cfg.Password); err != nil {
		return fmt.Errorf("bind as the service account: %w", err)
	}
	return fn(conn)
}

func toUser(e *goldap.Entry) User {
	uac, _ := strconv.ParseInt(e.GetAttributeValue("userAccountControl"), 10, 64) // absent reads as enabled; the filter decides
	return User{
		DN:          e.DN,
		Username:    e.GetAttributeValue("sAMAccountName"),
		DisplayName: e.GetAttributeValue("displayName"),
		Department:  e.GetAttributeValue("department"),
		Mail:        e.GetAttributeValue("mail"),
		Enabled:     uac&ufAccountDisable == 0,
	}
}

// Writer holds the directory's only write, clearing a lockout. Only the
// tool layer's account change dependencies hold one (spec 0007, AC-21).
type Writer struct {
	cfg Config
}

// NewWriter returns the directory writer.
func NewWriter(cfg Config) *Writer { return &Writer{cfg: cfg} }

// ClearLockout sets lockoutTime to 0, the one way AD lets you unlock.
func (w *Writer) ClearLockout(ctx context.Context, dn string) error {
	return session(ctx, w.cfg, func(conn *goldap.Conn) error {
		req := goldap.NewModifyRequest(dn, nil)
		req.Replace(attrLockoutTime, []string{"0"})
		if err := conn.Modify(req); err != nil {
			return fmt.Errorf("clear lockoutTime: %w", err)
		}
		return nil
	})
}
