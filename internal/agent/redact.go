package agent

import (
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

// Placeholders the name rule puts in the IT post (spec 0009, AC-9).
const (
	placeholderName       = "[name]"
	placeholderDepartment = "[department]"
)

// minNamePartLetters is the fewest letters a display name token or part
// needs to be removed on its own.
const minNamePartLetters = 3

// minDepartmentRunes is the fewest runes a department needs to be removed.
// It keeps two letter departments such as IT and HR, which are everyday
// words in an IT request, while R&D still counts.
const minDepartmentRunes = 3

// redactTerm is one value the name rule looks for, and what replaces it.
// A wholeWord term never matches inside an identifier such as a group name
// (SG-Transport, it_admins), which IT needs to read as written.
type redactTerm struct {
	text        []rune
	placeholder string
	wholeWord   bool
}

// redactRequester is the AC-9 name rule: it replaces the requester's display
// name, each of its parts with 3 or more letters, and the department when it
// has 3 or more runes, in the model's summary, so the IT post never names
// the person or the department. Matching is NFC, ignores case, needs a non
// letter, non digit rune (or the edge) on both sides, and runs in one pass,
// longest term first, so replaced text is never matched again. The
// department is never matched inside an identifier. It applies to the IT
// post only.
func redactRequester(summary, displayName, department string) string {
	terms := redactTerms(collapse(displayName), collapse(department))
	if len(terms) == 0 {
		return summary
	}
	text := []rune(norm.NFC.String(summary))
	var b strings.Builder
	for i := 0; i < len(text); {
		if t, ok := termAt(text, i, terms); ok {
			b.WriteString(t.placeholder)
			i += len(t.text)
			continue
		}
		b.WriteRune(text[i])
		i++
	}
	return b.String()
}

// redactTerms lists the full name, the department, and the name's tokens
// and letter only parts, longest first, each once.
func redactTerms(displayName, department string) []redactTerm {
	var terms []redactTerm
	add := func(s, placeholder string, wholeWord bool) {
		if s == "" || slices.ContainsFunc(terms, func(t redactTerm) bool { return strings.EqualFold(string(t.text), s) }) {
			return
		}
		terms = append(terms, redactTerm{text: []rune(s), placeholder: placeholder, wholeWord: wholeWord})
	}
	add(norm.NFC.String(displayName), placeholderName, false)
	if dept := norm.NFC.String(department); utf8.RuneCountInString(dept) >= minDepartmentRunes {
		add(dept, placeholderDepartment, true)
	}
	name := norm.NFC.String(displayName)
	tokens := strings.Fields(name)
	parts := strings.FieldsFunc(name, func(r rune) bool { return !unicode.IsLetter(r) })
	for _, part := range append(tokens, parts...) {
		if letters(part) >= minNamePartLetters {
			add(part, placeholderName, false)
		}
	}
	slices.SortStableFunc(terms, func(a, b redactTerm) int { return len(b.text) - len(a.text) })
	return terms
}

// termAt returns the longest term that matches at i with a word boundary on
// both sides.
func termAt(text []rune, i int, terms []redactTerm) (redactTerm, bool) {
	if i > 0 && isWordRune(text[i-1]) {
		return redactTerm{}, false
	}
	for _, t := range terms {
		end := i + len(t.text)
		if end > len(text) || (end < len(text) && isWordRune(text[end])) {
			continue
		}
		if t.wholeWord && inIdentifier(text, i, end) {
			continue
		}
		if foldEqual(text[i:end], t.text) {
			return t, true
		}
	}
	return redactTerm{}, false
}

// inIdentifier reports whether text[i:end] is joined to a letter or digit by
// an identifier joiner on either side, as in SG-Transport or it_admins.
func inIdentifier(text []rune, i, end int) bool {
	before := i >= 2 && isJoiner(text[i-1]) && isWordRune(text[i-2])
	after := end+1 < len(text) && isJoiner(text[end]) && isWordRune(text[end+1])
	return before || after
}

// isJoiner reports whether r joins the words of a group, account, or path name.
func isJoiner(r rune) bool { return strings.ContainsRune(`-_./\`, r) }

// foldEqual reports whether a and b are equal under Unicode simple case
// folding, rune by rune, like strings.EqualFold without the string copies.
func foldEqual(a, b []rune) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !runeFoldEqual(a[i], b[i]) {
			return false
		}
	}
	return true
}

func runeFoldEqual(r1, r2 rune) bool {
	if r1 == r2 {
		return true
	}
	for r := unicode.SimpleFold(r1); r != r1; r = unicode.SimpleFold(r) {
		if r == r2 {
			return true
		}
	}
	return false
}

// letters counts the letter runes in s.
func letters(s string) int {
	n := 0
	for _, r := range s {
		if unicode.IsLetter(r) {
			n++
		}
	}
	return n
}

func isWordRune(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }

// collapse turns every whitespace run into one space and trims the ends.
func collapse(s string) string { return strings.Join(strings.Fields(s), " ") }
