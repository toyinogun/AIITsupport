// Package directory loads Noord's seed file and builds the directory from it
// over LDAPS (spec 0004). The loaded Model is also the eval's record of the
// truth behind the directory: who has left, who is locked, who is unsynced.
//
// Load validates everything before anything is written; Apply fills a freshly
// provisioned Samba AD; Lock and State handle account lockouts.
package directory

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Group kinds a seed file may use.
const (
	KindDepartment   = "department"
	KindApp          = "app"
	KindShare        = "share"
	KindDistribution = "distribution"
	KindPrivileged   = "privileged"
)

// SyncGroup is generated from every user with synced: true (AC-10).
const SyncGroup = "SYNC-Entra-Users"

// AD groupType values: global security and global distribution (AC-6).
const (
	groupTypeGlobalSecurity     = "-2147483646"
	groupTypeGlobalDistribution = "2"
)

// userAccountControl values (spec 0004 data model).
const (
	uacDisabled            = "514"
	uacEnabled             = "512"
	uacEnabledNeverExpires = "66048"
)

// Model is a validated seed file with the overlay applied.
type Model struct {
	MailDomain  string
	Departments []string
	Users       []User
	Admins      []Admin
	Groups      []Group
	// Marker is written to OU=Noord last, so a rerun can tell whether the
	// directory already holds exactly this seed (AC-13).
	Marker string
}

// User is one Noord staff member, current or left.
type User struct {
	Username    string
	GivenName   string
	Surname     string
	Department  string
	Title       string
	Manager     string // username, empty only at the top
	Description string
	Locked      bool
	Synced      bool
	// Left is the truth about leavers. It lives in the seed file only and is
	// never written to AD (AC-4).
	Left *time.Time
	Mail string
}

// DisplayName is the user's cn and displayName.
func (u User) DisplayName() string { return u.GivenName + " " + u.Surname }

// Admin is a separate privileged account belonging to a staff member.
type Admin struct {
	Username string
	Person   string // the staff username it belongs to
}

// Group is one directory group.
type Group struct {
	Name        string
	Kind        string
	Owner       string // username, empty when nobody owns it
	Members     []string
	Description string
	NestedInto  []string // built in groups; privileged only
	// Departments tag a requestable group for the access request (spec
	// 0013, AC-3), written to the group's info attribute.
	Departments []string
}

// User returns the user with the given username.
func (m Model) User(username string) (User, bool) {
	for _, u := range m.Users {
		if u.Username == username {
			return u, true
		}
	}
	return User{}, false
}

// Admin returns the admin account with the given username.
func (m Model) Admin(username string) (Admin, bool) {
	for _, a := range m.Admins {
		if a.Username == username {
			return a, true
		}
	}
	return Admin{}, false
}

// Group returns the group with the given name, including the generated sync group.
func (m Model) Group(name string) (Group, bool) {
	for _, g := range m.Groups {
		if g.Name == name {
			return g, true
		}
	}
	return Group{}, false
}

// file mirrors the YAML shape. Optional values are pointers so the loader can
// tell "absent" from "empty".
type file struct {
	Version     int         `yaml:"version"`
	MailDomain  string      `yaml:"mail_domain"`
	Departments []string    `yaml:"departments"`
	Users       []fileUser  `yaml:"users"`
	Admins      []fileAdmin `yaml:"admins"`
	Groups      []fileGroup `yaml:"groups"`
}

type fileUser struct {
	Username    string  `yaml:"username"`
	GivenName   string  `yaml:"given_name"`
	Surname     string  `yaml:"surname"`
	Department  string  `yaml:"department"`
	Title       string  `yaml:"title"`
	Manager     *string `yaml:"manager"`
	Description *string `yaml:"description"`
	Locked      bool    `yaml:"locked"`
	Synced      *bool   `yaml:"synced"`
	Left        *string `yaml:"left"`
}

type fileAdmin struct {
	Username string `yaml:"username"`
	Person   string `yaml:"person"`
}

type fileGroup struct {
	Name        string   `yaml:"name"`
	Kind        string   `yaml:"kind"`
	Owner       *string  `yaml:"owner"`
	Members     []string `yaml:"members"`
	Description *string  `yaml:"description"`
	NestedInto  []string `yaml:"nested_into"`
	Departments []string `yaml:"departments"`
}

type overlayFile struct {
	Users []overlayUser `yaml:"users"`
}

type overlayUser struct {
	Username string `yaml:"username"`
	Mail     string `yaml:"mail"`
}

// Load reads and validates the seed file and the optional overlay. A missing
// overlay file is fine. Every problem found is reported in one error.
func Load(seedPath, overlayPath string) (Model, error) {
	seedBytes, err := os.ReadFile(seedPath) //nolint:gosec // G304: the path comes from config
	if err != nil {
		return Model{}, fmt.Errorf("read seed file: %w", err)
	}
	var overlayBytes []byte
	if overlayPath != "" {
		overlayBytes, err = os.ReadFile(overlayPath) //nolint:gosec // G304: the path comes from config
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return Model{}, fmt.Errorf("read overlay file: %w", err)
		}
	}
	m, err := Parse(seedBytes, overlayBytes)
	if err != nil {
		return Model{}, fmt.Errorf("seed file %s: %w", seedPath, err)
	}
	return m, nil
}

// Parse validates seed and overlay bytes. A nil overlay means none.
func Parse(seedBytes, overlayBytes []byte) (Model, error) {
	var f file
	decodeErr := decodeStrict(seedBytes, &f)
	var ov overlayFile
	var overlayErr error
	if overlayBytes != nil {
		if err := decodeStrict(overlayBytes, &ov); err != nil {
			overlayErr = fmt.Errorf("overlay: %w", err)
		}
	}

	m, problems := build(f, ov)
	problems = append([]error{decodeErr, overlayErr}, problems...)
	if err := errors.Join(problems...); err != nil {
		return Model{}, err
	}
	m.Marker = marker(seedBytes, overlayBytes)
	return m, nil
}

// decodeStrict rejects unknown fields. yaml.v3 keeps decoding after a type
// error, so every unknown field is listed, not just the first.
func decodeStrict(data []byte, dst any) error {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	err := dec.Decode(dst)
	if errors.Is(err, io.EOF) {
		return errors.New("file is empty")
	}
	if err != nil {
		return fmt.Errorf("decode: %w", err)
	}
	return nil
}

// build turns the decoded file into a Model and runs every check.
func build(f file, ov overlayFile) (Model, []error) {
	m := Model{MailDomain: f.MailDomain, Departments: f.Departments}
	var problems []error
	add := func(format string, args ...any) { problems = append(problems, fmt.Errorf(format, args...)) }

	if f.Version != 1 {
		add("version is %d, want 1", f.Version)
	}
	if f.MailDomain == "" {
		add("mail_domain is empty")
	}

	for _, fu := range f.Users {
		u := User{
			Username:    fu.Username,
			GivenName:   fu.GivenName,
			Surname:     fu.Surname,
			Department:  fu.Department,
			Title:       fu.Title,
			Manager:     deref(fu.Manager),
			Description: deref(fu.Description),
			Locked:      fu.Locked,
			Synced:      fu.Synced == nil || *fu.Synced,
			Mail:        fu.Username + "@" + f.MailDomain,
		}
		if fu.Left != nil {
			left, err := time.Parse(time.DateOnly, *fu.Left)
			if err != nil {
				add("user %s: left %q is not a date such as 2026-08-31", fu.Username, *fu.Left)
			} else {
				u.Left = &left
			}
		}
		m.Users = append(m.Users, u)
	}
	for _, fa := range f.Admins {
		m.Admins = append(m.Admins, Admin(fa))
	}
	for _, fg := range f.Groups {
		m.Groups = append(m.Groups, Group{
			Name:        fg.Name,
			Kind:        fg.Kind,
			Owner:       deref(fg.Owner),
			Members:     fg.Members,
			Description: deref(fg.Description),
			NestedInto:  fg.NestedInto,
			Departments: fg.Departments,
		})
	}

	problems = append(problems, applyOverlay(&m, ov)...)
	m.Groups = append(m.Groups, syncGroup(m.Users))
	problems = append(problems, validate(m)...)
	return m, problems
}

func applyOverlay(m *Model, ov overlayFile) []error {
	var problems []error
	for _, o := range ov.Users {
		i := indexOfUser(m.Users, o.Username)
		switch {
		case i < 0:
			problems = append(problems, fmt.Errorf("overlay: unknown user %q", o.Username))
		case !strings.Contains(o.Mail, "@"):
			problems = append(problems, fmt.Errorf("overlay: user %s: mail %q is not an address", o.Username, o.Mail))
		default:
			m.Users[i].Mail = o.Mail
		}
	}
	return problems
}

func syncGroup(users []User) Group {
	g := Group{
		Name:        SyncGroup,
		Kind:        KindApp,
		Description: "Users in scope for Entra Cloud Sync",
	}
	for _, u := range users {
		if u.Synced {
			g.Members = append(g.Members, u.Username)
		}
	}
	return g
}

func indexOfUser(users []User, username string) int {
	for i, u := range users {
		if u.Username == username {
			return i
		}
	}
	return -1
}

// marker is seed:sha256:<seed hash>:<overlay hash or none> (AC-13).
func marker(seedBytes, overlayBytes []byte) string {
	sum := sha256.Sum256(seedBytes)
	overlay := "none"
	if overlayBytes != nil {
		o := sha256.Sum256(overlayBytes)
		overlay = hex.EncodeToString(o[:])
	}
	return "seed:sha256:" + hex.EncodeToString(sum[:]) + ":" + overlay
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
