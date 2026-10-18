package entra

import (
	"crypto/rand"
	"fmt"
	"slices"
	"strings"

	"github.com/toyinogun/AIITsupport/internal/seed/directory"
)

// Generated numbers come from the North American 555-0100 to 555-0199
// range, reserved for fiction, so none ever reaches a real phone (AC-5).
// Graph validates numbers like Google's libphonenumber and rejects
// unallocated ranges, which ruled out +31 60 and Ofcom's +44 7700 900
// (journal 0013); this one it accepts.
const (
	generatedPhonePrefix = "+1 20255501"
	maxGeneratedPhones   = 99
)

// GeneratedPhones is every staff user's generated mobile number:
// +1 20255501NN, where NN is the user's 1 based position in the bytewise
// sorted staff usernames, compared as the exact strings in the seed file.
func GeneratedPhones(m directory.Model) (map[string]string, error) {
	if len(m.Users) > maxGeneratedPhones {
		return nil, fmt.Errorf("%d staff users, but the fictional phone range holds only %d numbers", len(m.Users), maxGeneratedPhones)
	}
	names := make([]string, 0, len(m.Users))
	for _, u := range m.Users {
		names = append(names, u.Username)
	}
	slices.Sort(names)
	out := make(map[string]string, len(names))
	for i, name := range names {
		out[name] = fmt.Sprintf("%s%02d", generatedPhonePrefix, i+1)
	}
	return out, nil
}

// ExpectedPhones is every staff user's expected mobile number: the demo
// file's number for demo users, else the generated one (AC-5).
func ExpectedPhones(m directory.Model, demo Demo) (map[string]string, error) {
	out, err := GeneratedPhones(m)
	if err != nil {
		return nil, err
	}
	for _, d := range demo.Users {
		out[d.Username] = d.MFAPhone
	}
	return out, nil
}

// samePhone compares numbers ignoring spacing, since Graph may store them
// with different separators.
func samePhone(a, b string) bool {
	clean := func(s string) string {
		return strings.Map(func(r rune) rune {
			if r == ' ' || r == '-' || r == '(' || r == ')' {
				return -1
			}
			return r
		}, s)
	}
	return clean(a) == clean(b)
}

const (
	passwordLen      = 32
	passwordAlphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_!"
)

// RandomPassword is a fresh 32 character password with at least one upper,
// one lower, and one digit. It is used once at create and never kept.
func RandomPassword() string {
	for {
		b := make([]byte, passwordLen)
		for i := range b {
			b[i] = passwordAlphabet[randIndex(len(passwordAlphabet))]
		}
		pw := string(b)
		if strings.ContainsAny(pw, "ABCDEFGHIJKLMNOPQRSTUVWXYZ") &&
			strings.ContainsAny(pw, "abcdefghijklmnopqrstuvwxyz") &&
			strings.ContainsAny(pw, "0123456789") {
			return pw
		}
	}
}

// randIndex is an unbiased index below n from crypto/rand.
func randIndex(n int) int {
	limit := 256 - 256%n
	var b [1]byte
	for {
		_, _ = rand.Read(b[:]) // crypto/rand.Read never fails
		if int(b[0]) < limit {
			return int(b[0]) % n
		}
	}
}
