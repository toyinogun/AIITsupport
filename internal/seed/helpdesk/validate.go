package helpdesk

import (
	"fmt"
	"regexp"
	"slices"
	"time"

	"github.com/toyinogun/AIITsupport/internal/seed/directory"
)

var (
	slugPattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
	iconPattern = regexp.MustCompile(`^f[0-9a-f]{3}$`)
	keyPattern  = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
)

var allowedStates = []string{StateClosed, StateOpen, StatePending}

// validate runs every helpdesk rule (spec 0006, AC-10) and returns every
// problem found.
func validate(c Content, m directory.Model) []error {
	v := validator{c: c, m: m}
	v.kb()
	v.categories()
	v.articles()
	v.tickets()
	return v.problems
}

type validator struct {
	c        Content
	m        directory.Model
	problems []error
}

func (v *validator) add(format string, args ...any) {
	v.problems = append(v.problems, fmt.Errorf(format, args...))
}

// endOfAsOf is the first moment after the fiction's today.
func (v *validator) endOfAsOf() time.Time {
	d := v.c.AsOf
	return time.Date(d.Year(), d.Month(), d.Day()+1, 0, 0, 0, 0, Amsterdam)
}

func (v *validator) kb() {
	if v.c.KBName != KBTitle {
		v.add("%s: name %q must be %q (bootstrap.rb titles the KB with it)", KBFile, v.c.KBName, KBTitle)
	}
	if v.c.AsOf.IsZero() {
		return
	}
	for _, u := range v.m.Users {
		if u.Left != nil && u.Left.After(v.c.AsOf) {
			v.add("user %s left on %s, after as_of %s", u.Username, u.Left.Format(dateLayout), v.c.AsOf.Format(dateLayout))
		}
	}
}

func (v *validator) categories() {
	if len(v.c.Categories) == 0 {
		v.add("%s: no categories", KBFile)
	}
	seen := map[string]bool{}
	for _, cat := range v.c.Categories {
		switch {
		case cat.Name == "":
			v.add("%s: a category has no name", KBFile)
		case seen[cat.Name]:
			v.add("%s: category %q appears twice", KBFile, cat.Name)
		}
		seen[cat.Name] = true
		if !iconPattern.MatchString(cat.Icon) {
			v.add("%s: category %q icon %q must be a Font Awesome code such as f023", KBFile, cat.Name, cat.Icon)
		}
		used := slices.ContainsFunc(v.c.Articles, func(a Article) bool { return a.Category == cat.Name })
		if cat.Name != "" && !used {
			v.add("%s: category %q has no articles", KBFile, cat.Name)
		}
	}
}

func (v *validator) articles() {
	if len(v.c.Articles) == 0 {
		v.add("%s: no articles", KBDir)
	}
	for _, a := range v.c.Articles {
		v.article(a)
	}
}

func (v *validator) article(a Article) {
	name := "article " + a.Slug
	if !slugPattern.MatchString(a.Slug) {
		v.add("%s: the file name must be lower case words joined by -", name)
	}
	if a.Title == "" {
		v.add("%s: title is required", name)
	}
	if a.Body == "" {
		v.add("%s: the body is empty", name)
	}
	if !slices.ContainsFunc(v.c.Categories, func(c Category) bool { return c.Name == a.Category }) {
		v.add("%s: category %q is not in %s", name, a.Category, KBFile)
	}
	if !a.Created.IsZero() && !a.Updated.IsZero() && a.Updated.Before(a.Created) {
		v.add("%s: updated is before created", name)
	}
	if !a.Updated.IsZero() && !v.c.AsOf.IsZero() && !a.Updated.Before(v.endOfAsOf()) {
		v.add("%s: updated is after as_of", name)
	}
	if a.Status != StatusCurrent && a.Status != StatusOutdated {
		v.add("%s: status %q must be %s or %s", name, a.Status, StatusCurrent, StatusOutdated)
	}
	v.contradicts(a)
	v.duplicate(a)
	v.deadContact(a)
}

// contradicts: held by the outdated article, pointing at a current one.
func (v *validator) contradicts(a Article) {
	if len(a.Contradicts) > 0 && a.Status != StatusOutdated {
		v.add("article %s: contradicts is held by the outdated article only", a.Slug)
	}
	for _, slug := range a.Contradicts {
		target, ok := v.c.Article(slug)
		switch {
		case !ok:
			v.add("article %s: contradicts %q, which does not exist", a.Slug, slug)
		case target.Status != StatusCurrent:
			v.add("article %s: contradicts %q, which must be current", a.Slug, slug)
		}
	}
}

// duplicate: held by the newer article, pointing at an older one.
func (v *validator) duplicate(a Article) {
	if a.DuplicateOf == "" {
		return
	}
	target, ok := v.c.Article(a.DuplicateOf)
	switch {
	case !ok:
		v.add("article %s: duplicate_of %q does not exist", a.Slug, a.DuplicateOf)
	case !target.Created.Before(a.Created):
		v.add("article %s: duplicate_of %q must be older than it", a.Slug, a.DuplicateOf)
	}
}

// deadContact: someone who left after the article was last updated.
func (v *validator) deadContact(a Article) {
	if a.DeadContact == "" {
		return
	}
	u, ok := v.m.User(a.DeadContact)
	switch {
	case !ok:
		v.add("article %s: dead_contact %q is not a user", a.Slug, a.DeadContact)
	case u.Left == nil:
		v.add("article %s: dead_contact %q has not left", a.Slug, a.DeadContact)
	case !u.Left.After(a.Updated):
		v.add("article %s: dead_contact %q left before the article was updated", a.Slug, a.DeadContact)
	}
}

func (v *validator) tickets() {
	if len(v.c.Tickets) == 0 {
		v.add("%s: no tickets", TicketsFile)
	}
	seen := map[string]bool{}
	for _, t := range v.c.Tickets {
		if !keyPattern.MatchString(t.Key) {
			v.add("ticket %q: key must be lower case words joined by -", t.Key)
		}
		if seen[t.Key] {
			v.add("ticket %s: key appears twice", t.Key)
		}
		seen[t.Key] = true
		v.ticket(t)
	}
}

func (v *validator) ticket(t Ticket) {
	name := "ticket " + t.Key
	if t.Title == "" {
		v.add("%s: title is required", name)
	}
	if _, ok := v.m.User(t.Customer); !ok {
		v.add("%s: customer %q is not a user", name, t.Customer)
	}
	if !slices.Contains(allowedStates, t.State) {
		v.add("%s: state %q must be one of %v", name, t.State, allowedStates)
	}
	if (t.State == StateClosed) != (t.Closed != nil) {
		v.add("%s: closed is required exactly when the state is closed", name)
	}
	if len(t.Articles) == 0 {
		v.add("%s: needs at least one article", name)
		return
	}
	if t.Articles[0].From != t.Customer {
		v.add("%s: the first article must be from the customer", name)
	}
	prev := t.Created
	for i, a := range t.Articles {
		if _, ok := v.m.User(a.From); !ok {
			v.add("%s: article %d is from %q, who is not a user", name, i+1, a.From)
		} else if a.From != t.Customer && a.From != Agent {
			v.add("%s: article %d is from %q; only the customer or %s may write", name, i+1, a.From, Agent)
		}
		if a.Body == "" {
			v.add("%s: article %d has no body", name, i+1)
		}
		if !a.At.IsZero() && !prev.IsZero() && a.At.Before(prev) {
			v.add("%s: article %d is before the time before it", name, i+1)
		}
		if !a.At.IsZero() {
			prev = a.At
		}
	}
	last := prev
	if t.Closed != nil {
		if !t.Closed.IsZero() && t.Closed.Before(last) {
			v.add("%s: closed is before the last article", name)
		}
		last = *t.Closed
	}
	if !v.c.AsOf.IsZero() && !last.Before(v.endOfAsOf()) {
		v.add("%s: a time is after as_of", name)
	}
}
