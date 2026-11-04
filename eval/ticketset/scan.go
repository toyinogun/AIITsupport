package ticketset

import (
	"regexp"
	"strings"
)

const fictionalDomain = "noord.example"

var (
	emailRe = regexp.MustCompile(`[A-Za-z0-9._%+-]+@([A-Za-z0-9-]+(?:\.[A-Za-z0-9-]+)*)`)
	urlRe   = regexp.MustCompile(`https?://[^\s/"'<>)]+`)
)

// scanFictional keeps real addresses and hosts out of the public file (AC-11).
func (c *checker) scanFictional(id string, t Ticket) {
	texts := append([]string{t.Message}, t.Expect.ReplyMustNot...)
	for _, text := range texts {
		for _, m := range emailRe.FindAllStringSubmatch(text, -1) {
			if strings.ToLower(m[1]) != fictionalDomain {
				c.add(id, "an email address does not end in @%s", fictionalDomain)
			}
		}
		for _, u := range urlRe.FindAllString(text, -1) {
			if !fictionalHost(hostOf(u)) {
				c.add(id, "a link host does not end in %s", fictionalDomain)
			}
		}
		for _, tok := range strings.Fields(text) {
			if strings.Contains(strings.ToLower(tok), "www.") && !strings.Contains(tok, "://") {
				if !fictionalHost(hostOf(tok)) {
					c.add(id, "a www host does not end in %s", fictionalDomain)
				}
			}
		}
	}
}

// hostOf takes the host out of a link or bare host token.
func hostOf(s string) string {
	s = strings.TrimPrefix(strings.TrimPrefix(s, "https://"), "http://")
	if i := strings.IndexAny(s, "/?#"); i >= 0 {
		s = s[:i]
	}
	if i := strings.LastIndex(s, "@"); i >= 0 {
		s = s[i+1:]
	}
	if i := strings.Index(s, ":"); i >= 0 {
		s = s[:i]
	}
	return strings.ToLower(strings.TrimRight(s, ".,;:!?)\"'"))
}

func fictionalHost(h string) bool {
	return h == fictionalDomain || strings.HasSuffix(h, "."+fictionalDomain)
}
