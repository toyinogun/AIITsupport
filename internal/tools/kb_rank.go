package tools

import (
	"cmp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// minKBWordRunes is the shortest query word the score counts (spec 0014, AC-3).
const minKBWordRunes = 3

// Scores of one query word found in an article (spec 0014, AC-3).
const (
	kbTitleScore = 3
	kbBodyScore  = 1
)

// kbStopwords are query words too common to rank by, fixed in code.
var kbStopwords = map[string]bool{
	"the": true, "and": true, "for": true, "how": true, "what": true, "can": true, "you": true,
	"from": true, "with": true, "does": true, "are": true, "get": true, "into": true, "this": true,
	"that": true, "when": true, "why": true, "not": true, "has": true, "have": true,
}

// kbWords lowercases s (Unicode) and splits it on anything that is not a
// letter or a digit.
func kbWords(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
}

// kbQueryWords are the query's scorable words: three runes or longer, not a
// stopword, each counted once, in the order they first appear.
func kbQueryWords(query string) []string {
	var out []string
	for _, w := range kbWords(query) {
		if utf8.RuneCountInString(w) >= minKBWordRunes && !kbStopwords[w] && !slices.Contains(out, w) {
			out = append(out, w)
		}
	}
	return out
}

// kbScore scores an article for the query words: each word adds
// kbTitleScore when it starts a word of the title and kbBodyScore when it
// starts a word of the body text, so "lock" finds "locked" but not "block",
// the way Zammad matches word prefixes. body is the full plain text, scored
// before the KB_BODY_MAX_RUNES cut.
func kbScore(queryWords []string, title, body string) int {
	titleWords, bodyWords := kbWords(title), kbWords(body)
	startsAWord := func(words []string, w string) bool {
		return slices.ContainsFunc(words, func(word string) bool { return strings.HasPrefix(word, w) })
	}
	score := 0
	for _, w := range queryWords {
		if startsAWord(titleWords, w) {
			score += kbTitleScore
		}
		if startsAWord(bodyWords, w) {
			score += kbBodyScore
		}
	}
	return score
}

// kbCandidate is one published answer read for a search, with its rank inputs.
type kbCandidate struct {
	read  kbRead
	text  string // the full plain text body
	score int
	order int // its place among the candidates, in Zammad's order
}

// kbRank orders candidates by score, then the later updated_at (the full
// timestamp), then Zammad's order (spec 0014, AC-3), and keeps the first
// limit. A zero score still ranks: Zammad matched it.
func kbRank(candidates []kbCandidate, limit int) []kbCandidate {
	ranked := slices.Clone(candidates)
	slices.SortFunc(ranked, func(a, b kbCandidate) int {
		if c := cmp.Compare(b.score, a.score); c != 0 {
			return c
		}
		if c := b.read.tr.UpdatedAt.Compare(a.read.tr.UpdatedAt); c != 0 {
			return c
		}
		return cmp.Compare(a.order, b.order)
	})
	return ranked[:min(limit, len(ranked))]
}
