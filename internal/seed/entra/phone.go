package entra

import (
	"crypto/rand"
	"fmt"
	"slices"
	"strings"

	"github.com/toyinogun/AIITsupport/internal/seed/directory"
)

// generatedPhonePrefix is the fictional range every generated number uses (AC-5).
const generatedPhonePrefix = "+31 60000"

// ExpectedPhones is every staff user's expected mobile number: the demo
// file's number for demo users, else +31 60000NNNN where NNNN is the user's
// 1 based position in the bytewise sorted staff usernames (AC-5).
func ExpectedPhones(m directory.Model, demo Demo) map[string]string {
	names := make([]string, 0, len(m.Users))
	for _, u := range m.Users {
		names = append(names, u.Username)
	}
	slices.Sort(names)
	out := make(map[string]string, len(names))
	for i, name := range names {
		out[name] = fmt.Sprintf("%s%04d", generatedPhonePrefix, i+1)
	}
	for _, d := range demo.Users {
		out[d.Username] = d.MFAPhone
	}
	return out
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
