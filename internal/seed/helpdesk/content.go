// Package helpdesk builds Noord's Zammad helpdesk from the files in
// seed/helpdesk/ (spec 0006): a Rails bootstrap for what REST cannot set,
// the REST fill of tickets and the knowledge base, then a Rails backdate
// step that writes the marker last.
//
// The loaded Content is also the eval's record of the truth behind the KB:
// which articles are outdated, what they contradict, which systems are
// retired, and who has left. None of it is ever sent to Zammad.
package helpdesk

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/toyinogun/AIITsupport/internal/seed/directory"
)

// Article status values (eval truth).
const (
	StatusCurrent  = "current"
	StatusOutdated = "outdated"
)

// Ticket states a seed file may use.
const (
	StateClosed  = "closed"
	StateOpen    = "open"
	StatePending = "pending"
)

// KBTitle is the knowledge base title; bootstrap.rb sets the same constant.
const KBTitle = "Noord IT Help"

// Agent is Noord's only IT person: the author of every reply, the owner of
// closed and pending tickets, and the only Zammad agent besides the seed
// admin (through APP-Helpdesk-Agents).
const Agent = "ruben.mulder"

// ZammadBindDN is svc-zammad, Zammad's read only LDAP account. The base DN
// is fixed by the realm in compose.yaml (spec 0004).
const ZammadBindDN = "CN=svc-zammad,OU=Service Accounts,OU=Noord,DC=ad,DC=noord,DC=example"

// Seed file names inside the helpdesk directory.
const (
	KBFile      = "kb.yaml"
	KBDir       = "kb"
	TicketsFile = "tickets.yaml"
)

// Amsterdam is the fiction's time zone: every time in the seed files is
// Europe/Amsterdam local time.
var Amsterdam = mustLoadLocation("Europe/Amsterdam")

func mustLoadLocation(name string) *time.Location {
	loc, err := time.LoadLocation(name)
	if err != nil {
		panic(fmt.Sprintf("load time zone %s: %v", name, err))
	}
	return loc
}

// Content is a validated helpdesk seed.
type Content struct {
	// AsOf is the fiction's "today", 09:00 Amsterdam on the as_of date.
	AsOf       time.Time
	KBName     string
	Categories []Category
	Articles   []Article
	Tickets    []Ticket
}

// Category is one KB category, in file order.
type Category struct {
	Name string
	Icon string
}

// Article is one KB answer. The truth fields stay in the seed files.
type Article struct {
	Slug     string
	Title    string
	Category string
	Created  time.Time
	Updated  time.Time
	// Body is Markdown.
	Body string

	Status        string
	Contradicts   []string
	RetiredSystem string
	DeadContact   string
	DuplicateOf   string
}

// Ticket is one old ticket.
type Ticket struct {
	Key      string
	Customer string
	Title    string
	State    string
	Created  time.Time
	Closed   *time.Time
	Articles []TicketArticle
}

// TicketArticle is one message on a ticket.
type TicketArticle struct {
	From string
	At   time.Time
	Body string
}

// Last returns the time of the ticket's last article.
func (t Ticket) Last() time.Time { return t.Articles[len(t.Articles)-1].At }

// localTime is a seed file time: "2026-09-14 08:12" or a bare date
// (09:00), in Amsterdam. It remembers what was written so validation can
// spot a time that does not exist (the spring DST gap).
type localTime struct {
	raw  string
	time time.Time
	err  error
}

func (l *localTime) UnmarshalYAML(node *yaml.Node) error {
	l.raw = node.Value
	l.time, l.err = parseLocal(node.Value)
	return nil
}

const (
	dateLayout     = "2006-01-02"
	dateTimeLayout = "2006-01-02 15:04"
	bareDateHour   = 9
)

// parseLocal reads an Amsterdam local time and refuses one the clock skips.
func parseLocal(s string) (time.Time, error) {
	if d, err := time.Parse(dateLayout, s); err == nil {
		return time.Date(d.Year(), d.Month(), d.Day(), bareDateHour, 0, 0, 0, Amsterdam), nil
	}
	naive, err := time.Parse(dateTimeLayout, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("time %q is not YYYY-MM-DD or YYYY-MM-DD HH:MM", s)
	}
	t := time.Date(naive.Year(), naive.Month(), naive.Day(), naive.Hour(), naive.Minute(), 0, 0, Amsterdam)
	if t.Hour() != naive.Hour() || t.Minute() != naive.Minute() {
		return time.Time{}, fmt.Errorf("time %q does not exist in Europe/Amsterdam (daylight saving gap)", s)
	}
	return t, nil
}

type kbFile struct {
	Name       string `yaml:"name"`
	AsOf       string `yaml:"as_of"`
	Categories []struct {
		Name string `yaml:"name"`
		Icon string `yaml:"icon"`
	} `yaml:"categories"`
}

type articleHeader struct {
	Title         string    `yaml:"title"`
	Category      string    `yaml:"category"`
	Created       localTime `yaml:"created"`
	Updated       localTime `yaml:"updated"`
	Status        string    `yaml:"status"`
	Contradicts   []string  `yaml:"contradicts"`
	RetiredSystem string    `yaml:"retired_system"`
	DeadContact   string    `yaml:"dead_contact"`
	DuplicateOf   string    `yaml:"duplicate_of"`
}

type ticketsFile struct {
	AsOf    string `yaml:"as_of"`
	Tickets []struct {
		Key      string     `yaml:"key"`
		Customer string     `yaml:"customer"`
		Title    string     `yaml:"title"`
		State    string     `yaml:"state"`
		Created  localTime  `yaml:"created"`
		Closed   *localTime `yaml:"closed"`
		Articles []struct {
			From string    `yaml:"from"`
			At   localTime `yaml:"at"`
			Body string    `yaml:"body"`
		} `yaml:"articles"`
	} `yaml:"tickets"`
}

// Load reads and validates the helpdesk directory against the directory
// model (for usernames and leave dates). It needs no network. Every problem
// found is reported in one error.
func Load(dir string, m directory.Model) (Content, error) {
	var problems []error
	add := func(err error) { problems = append(problems, err) }

	var kb kbFile
	if err := decodeFile(filepath.Join(dir, KBFile), &kb); err != nil {
		add(err)
	}
	var tf ticketsFile
	if err := decodeFile(filepath.Join(dir, TicketsFile), &tf); err != nil {
		add(err)
	}
	articles, errs := loadArticles(filepath.Join(dir, KBDir))
	problems = append(problems, errs...)

	c, errs := build(kb, tf, articles)
	problems = append(problems, errs...)
	problems = append(problems, validate(c, m)...)
	if err := errors.Join(problems...); err != nil {
		return Content{}, fmt.Errorf("helpdesk files in %s: %w", dir, err)
	}
	return c, nil
}

func decodeFile(path string, dst any) error {
	data, err := os.ReadFile(path) //nolint:gosec // G304: the path comes from config
	if err != nil {
		return fmt.Errorf("read %s: %w", filepath.Base(path), err)
	}
	if err := decodeStrict(data, dst); err != nil {
		return fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	return nil
}

func decodeStrict(data []byte, dst any) error {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(dst); err != nil {
		return fmt.Errorf("decode: %w", err)
	}
	return nil
}

type rawArticle struct {
	slug   string
	header articleHeader
	body   string
}

// loadArticles reads every kb/<slug>.md, in name order.
func loadArticles(dir string) ([]rawArticle, []error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, []error{fmt.Errorf("read %s: %w", KBDir, err)}
	}
	var out []rawArticle
	var problems []error
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || strings.HasPrefix(name, ".") {
			continue
		}
		if !strings.HasSuffix(name, ".md") {
			problems = append(problems, fmt.Errorf("%s/%s: only .md files belong here", KBDir, name))
			continue
		}
		a, err := readArticle(filepath.Join(dir, name))
		if err != nil {
			problems = append(problems, fmt.Errorf("%s/%s: %w", KBDir, name, err))
			continue
		}
		out = append(out, a)
	}
	return out, problems
}

const frontMatter = "---\n"

func readArticle(path string) (rawArticle, error) {
	data, err := os.ReadFile(path) //nolint:gosec // G304: inside the configured helpdesk directory
	if err != nil {
		return rawArticle{}, err
	}
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	if !strings.HasPrefix(text, frontMatter) {
		return rawArticle{}, errors.New("must start with a --- YAML header")
	}
	header, body, ok := strings.Cut(text[len(frontMatter):], "\n"+frontMatter)
	if !ok {
		return rawArticle{}, errors.New("the YAML header has no closing ---")
	}
	var h articleHeader
	if err := decodeStrict([]byte(header), &h); err != nil {
		return rawArticle{}, fmt.Errorf("header: %w", err)
	}
	slug := strings.TrimSuffix(filepath.Base(path), ".md")
	return rawArticle{slug: slug, header: h, body: strings.TrimSpace(body)}, nil
}

// build converts the decoded files, reporting unparseable times.
func build(kb kbFile, tf ticketsFile, raws []rawArticle) (Content, []error) {
	var problems []error
	add := func(format string, args ...any) { problems = append(problems, fmt.Errorf(format, args...)) }

	c := Content{KBName: kb.Name}
	if kb.AsOf != tf.AsOf {
		add("%s as_of %q and %s as_of %q differ", KBFile, kb.AsOf, TicketsFile, tf.AsOf)
	}
	if d, err := time.Parse(dateLayout, kb.AsOf); err != nil {
		add("%s: as_of %q is not a date", KBFile, kb.AsOf)
	} else {
		c.AsOf = time.Date(d.Year(), d.Month(), d.Day(), bareDateHour, 0, 0, 0, Amsterdam)
	}
	for _, cat := range kb.Categories {
		c.Categories = append(c.Categories, Category{Name: cat.Name, Icon: cat.Icon})
	}

	for _, r := range raws {
		h := r.header
		for field, lt := range map[string]localTime{"created": h.Created, "updated": h.Updated} {
			if lt.raw == "" {
				add("article %s: %s is required", r.slug, field)
			} else if lt.err != nil {
				add("article %s: %s: %w", r.slug, field, lt.err)
			}
		}
		c.Articles = append(c.Articles, Article{
			Slug: r.slug, Title: h.Title, Category: h.Category, Created: h.Created.time, Updated: h.Updated.time,
			Body: r.body, Status: h.Status, Contradicts: h.Contradicts, RetiredSystem: h.RetiredSystem,
			DeadContact: h.DeadContact, DuplicateOf: h.DuplicateOf,
		})
	}

	for _, rt := range tf.Tickets {
		t := Ticket{Key: rt.Key, Customer: rt.Customer, Title: rt.Title, State: rt.State, Created: rt.Created.time}
		checkTime := func(field string, lt localTime) {
			if lt.raw == "" {
				add("ticket %s: %s is required", rt.Key, field)
			} else if lt.err != nil {
				add("ticket %s: %s: %w", rt.Key, field, lt.err)
			}
		}
		checkTime("created", rt.Created)
		if rt.Closed != nil {
			checkTime("closed", *rt.Closed)
			closed := rt.Closed.time
			t.Closed = &closed
		}
		for i, ra := range rt.Articles {
			checkTime(fmt.Sprintf("article %d at", i+1), ra.At)
			t.Articles = append(t.Articles, TicketArticle{From: ra.From, At: ra.At.time, Body: strings.TrimSpace(ra.Body)})
		}
		c.Tickets = append(c.Tickets, t)
	}
	return c, problems
}

// Article returns the article with that slug.
func (c Content) Article(slug string) (Article, bool) {
	i := slices.IndexFunc(c.Articles, func(a Article) bool { return a.Slug == slug })
	if i < 0 {
		return Article{}, false
	}
	return c.Articles[i], true
}
