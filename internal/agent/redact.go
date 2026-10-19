package agent

import (
	"slices"
	"strings"
	"unicode"

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

// redactTerm is one value the name rule looks for, and what replaces it.
type redactTerm struct {
	text        []rune
	placeholder string
}

// redactRequester is the AC-9 name rule: it replaces the requester's display
// name, each of its parts with 3 or more letters, and the department in the
// model's summary, so the IT post never names the person or the department.
// Matching is NFC, ignores case, needs a non letter, non digit rune (or the
// edge) on both sides, and runs in one pass, longest term first, so replaced
// text is never matched again. It applies to the IT post only.
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
	add := func(s, placeholder string) {
		if s == "" || slices.ContainsFunc(terms, func(t redactTerm) bool { return strings.EqualFold(string(t.text), s) }) {
			return
		}
		terms = append(terms, redactTerm{text: []rune(s), placeholder: placeholder})
	}
	add(norm.NFC.String(displayName), placeholderName)
	add(norm.NFC.String(department), placeholderDepartment)
	name := norm.NFC.String(displayName)
	tokens := strings.Fields(name)
	parts := strings.FieldsFunc(name, func(r rune) bool { return !unicode.IsLetter(r) })
	for _, part := range append(tokens, parts...) {
		if letters(part) >= minNamePartLetters {
			add(part, placeholderName)
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
		if strings.EqualFold(string(text[i:end]), string(t.text)) {
			return t, true
		}
	}
	return redactTerm{}, false
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
