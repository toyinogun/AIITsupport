package tools

import (
	"regexp"
	"strings"
	"unicode/utf8"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// kbBodyEllipsis ends a body cut at KB_BODY_MAX_RUNES.
const kbBodyEllipsis = "…"

// blockAtoms start and end a line in an article's text.
var blockAtoms = map[atom.Atom]bool{
	atom.P: true, atom.Div: true, atom.Br: true, atom.Li: true, atom.Ul: true, atom.Ol: true,
	atom.H1: true, atom.H2: true, atom.H3: true, atom.H4: true, atom.H5: true, atom.H6: true,
	atom.Table: true, atom.Tr: true, atom.Blockquote: true, atom.Pre: true, atom.Hr: true,
	atom.Section: true, atom.Article: true, atom.Header: true, atom.Footer: true,
	atom.Dl: true, atom.Dt: true, atom.Dd: true,
}

// skippedAtoms are dropped with everything inside them.
var skippedAtoms = map[atom.Atom]bool{atom.Script: true, atom.Style: true, atom.Template: true, atom.Noscript: true}

// rawURL is a URL written as text in an article; it is dropped like an href.
var rawURL = regexp.MustCompile(`(?i)\b(?:https?|ftp)://\S*`)

// articleText turns an answer's HTML into the plain text the model reads
// (spec 0014, AC-3), cut at maxRunes with a trailing ellipsis.
func articleText(body string, maxRunes int) string {
	return cutText(plainText(body), maxRunes)
}

// plainText is an answer's HTML as plain text, uncut: block elements become
// line breaks, list items keep a "- " prefix, anchor text stays while every
// href, image, and URL written as text goes, and whitespace runs fold.
// Article text is untrusted, so nothing of its markup survives.
func plainText(body string) string {
	z := html.NewTokenizer(strings.NewReader(body))
	var b strings.Builder
	skip := 0
	for {
		tt := z.Next()
		if tt == html.ErrorToken {
			break
		}
		tok := z.Token()
		switch tt {
		case html.StartTagToken, html.SelfClosingTagToken:
			switch {
			case skippedAtoms[tok.DataAtom] && tt == html.StartTagToken:
				skip++
			case tok.DataAtom == atom.Li:
				b.WriteString("\n- ")
			case blockAtoms[tok.DataAtom]:
				b.WriteString("\n")
			case tok.DataAtom == atom.Td || tok.DataAtom == atom.Th:
				b.WriteString(" ")
			}
		case html.EndTagToken:
			switch {
			case skippedAtoms[tok.DataAtom]:
				skip = max(skip-1, 0)
			case blockAtoms[tok.DataAtom]:
				b.WriteString("\n")
			}
		case html.TextToken:
			if skip == 0 {
				b.WriteString(tok.Data)
			}
		}
	}
	return foldWhitespace(rawURL.ReplaceAllString(b.String(), ""))
}

// foldWhitespace folds each run of spaces and tabs into one space, trims
// every line, and drops empty lines.
func foldWhitespace(s string) string {
	var lines []string
	for line := range strings.SplitSeq(s, "\n") {
		line = strings.Join(strings.Fields(line), " ")
		if line != "" && line != "-" {
			lines = append(lines, line)
		}
	}
	return strings.Join(lines, "\n")
}

// cutText cuts s to maxRunes runes, the last of them the ellipsis.
func cutText(s string, maxRunes int) string {
	if utf8.RuneCountInString(s) <= maxRunes {
		return s
	}
	runes := []rune(s)
	return strings.TrimRight(string(runes[:maxRunes-1]), " \n") + kbBodyEllipsis
}
