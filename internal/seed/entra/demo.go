// Package entra makes Noord's Entra tenant match the synced part of its AD
// (spec 0005). It reads the members of SYNC-Entra-Users from Samba, plans the
// difference against Entra as a pure function, and applies it over Microsoft
// Graph one call at a time with the seed app. Only users carrying the
// managed marker are ever written.
package entra

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"regexp"
	"strings"
	"unicode"

	"gopkg.in/yaml.v3"

	"github.com/toyinogun/AIITsupport/internal/seed/directory"
)

// maxDemoUsers caps how many users may carry a real phone (AC-8).
const maxDemoUsers = 2

// demoPhonePattern is the number shape a demo file may hold (AC-8).
var demoPhonePattern = regexp.MustCompile(`^\+\d{1,3} \d{6,12}$`)

// DemoUser is a synced user you sign in as for the video, with your phone.
type DemoUser struct {
	Username string
	MFAPhone string
}

// Demo is the validated demo file. Empty means no demo users.
type Demo struct {
	Users []DemoUser
}

// Phone returns a demo user's MFA phone.
func (d Demo) Phone(username string) (string, bool) {
	for _, u := range d.Users {
		if u.Username == username {
			return u.MFAPhone, true
		}
	}
	return "", false
}

// IsDemo reports whether username is a demo user.
func (d Demo) IsDemo(username string) bool {
	_, ok := d.Phone(username)
	return ok
}

type demoFile struct {
	Users []struct {
		Username string `yaml:"username"`
		MFAPhone string `yaml:"mfa_phone"`
	} `yaml:"users"`
}

// LoadDemo reads and validates the demo file. A missing file means no demo
// users.
func LoadDemo(path string, m directory.Model) (Demo, error) {
	data, err := os.ReadFile(path) //nolint:gosec // G304: the path comes from config
	if errors.Is(err, fs.ErrNotExist) {
		return Demo{}, nil
	}
	if err != nil {
		return Demo{}, fmt.Errorf("read demo file: %w", err)
	}
	d, err := ParseDemo(data, m)
	if err != nil {
		return Demo{}, fmt.Errorf("demo file %s: %w", path, err)
	}
	return d, nil
}

// ParseDemo validates demo file bytes against the model. Every problem is
// reported in one error; numbers are never echoed back.
func ParseDemo(data []byte, m directory.Model) (Demo, error) {
	var f demoFile
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil && !errors.Is(err, io.EOF) {
		return Demo{}, fmt.Errorf("decode: %w", err)
	}

	var problems []error
	add := func(format string, args ...any) { problems = append(problems, fmt.Errorf(format, args...)) }
	if len(f.Users) > maxDemoUsers {
		add("%d demo users, at most %d allowed", len(f.Users), maxDemoUsers)
	}
	var d Demo
	seenUser := map[string]bool{}
	seenPhone := map[string]string{}
	for _, fu := range f.Users {
		u, ok := m.User(fu.Username)
		switch {
		case !ok:
			add("demo user %q is not a user in the seed file", fu.Username)
		case !u.Synced:
			add("demo user %s has synced: false, so it never reaches Entra", fu.Username)
		}
		if seenUser[fu.Username] {
			add("demo user %s is listed twice", fu.Username)
		}
		seenUser[fu.Username] = true
		if !demoPhonePattern.MatchString(fu.MFAPhone) {
			add("demo user %s: mfa_phone must be a + country code, a space, then 6 to 12 digits", fu.Username)
		} else if other, dup := seenPhone[fu.MFAPhone]; dup {
			add("demo users %s and %s share an mfa_phone", other, fu.Username)
		}
		seenPhone[fu.MFAPhone] = fu.Username
		d.Users = append(d.Users, DemoUser{Username: fu.Username, MFAPhone: fu.MFAPhone})
	}
	if err := errors.Join(problems...); err != nil {
		return Demo{}, err
	}
	return d, nil
}

const (
	minDemoPasswordLen = 12
	minDemoClasses     = 3
	minNameToken       = 3
)

// CheckDemoPassword applies AD's complexity rule to SEED_ENTRA_DEMO_PASSWORD
// (AC-8): at least 12 characters, 3 of upper, lower, digit, and symbol, and
// no 3+ letter part of any staff name. It never echoes the password.
func CheckDemoPassword(pw string, m directory.Model) error {
	if pw == "" {
		return errors.New("SEED_ENTRA_DEMO_PASSWORD is required when the demo file lists users")
	}
	if len([]rune(pw)) < minDemoPasswordLen {
		return fmt.Errorf("SEED_ENTRA_DEMO_PASSWORD must be at least %d characters", minDemoPasswordLen)
	}
	var upper, lower, digit, symbol bool
	for _, r := range pw {
		switch {
		case unicode.IsUpper(r):
			upper = true
		case unicode.IsLower(r):
			lower = true
		case unicode.IsDigit(r):
			digit = true
		default:
			symbol = true
		}
	}
	if count(upper, lower, digit, symbol) < minDemoClasses {
		return errors.New("SEED_ENTRA_DEMO_PASSWORD needs 3 of: upper case, lower case, digit, symbol")
	}
	lowered := strings.ToLower(pw)
	for _, token := range nameTokens(m) {
		if strings.Contains(lowered, token) {
			return errors.New("SEED_ENTRA_DEMO_PASSWORD contains part of a staff name")
		}
	}
	return nil
}

func count(flags ...bool) int {
	n := 0
	for _, f := range flags {
		if f {
			n++
		}
	}
	return n
}

// nameTokens are the lower cased 3+ letter runs in every staff member's
// given name, surname, and username.
func nameTokens(m directory.Model) []string {
	var tokens []string
	for _, u := range m.Users {
		for _, part := range []string{u.GivenName, u.Surname, u.Username} {
			for _, tok := range strings.FieldsFunc(strings.ToLower(part), func(r rune) bool { return !unicode.IsLetter(r) }) {
				if len([]rune(tok)) >= minNameToken {
					tokens = append(tokens, tok)
				}
			}
		}
	}
	return tokens
}
