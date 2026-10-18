package helpdesk

import (
	"context"
	"fmt"
	"time"

	"github.com/toyinogun/AIITsupport/internal/zammad"
)

// backdatePayload is backdate.rb's stdin: every id the REST fill made with
// the dates from the seed files, as RFC 3339 UTC.
type backdatePayload struct {
	Marker  string        `json:"marker"`
	Tickets []ticketDates `json:"tickets"`
	Answers []answerDates `json:"answers"`
}

type ticketDates struct {
	ID                    int            `json:"id"`
	Key                   string         `json:"key"`
	CreatedAt             time.Time      `json:"created_at"`
	UpdatedAt             time.Time      `json:"updated_at"`
	CloseAt               *time.Time     `json:"close_at"`
	FirstResponseAt       *time.Time     `json:"first_response_at"`
	LastContactAt         time.Time      `json:"last_contact_at"`
	LastContactCustomerAt *time.Time     `json:"last_contact_customer_at"`
	LastContactAgentAt    *time.Time     `json:"last_contact_agent_at"`
	Articles              []articleDates `json:"articles"`
}

type articleDates struct {
	ID int       `json:"id"`
	At time.Time `json:"at"`
}

type answerDates struct {
	ID        int       `json:"id"`
	Slug      string    `json:"slug"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// filler runs the REST fill and collects the backdate payload.
type filler struct {
	s          Seeder
	boot       bootstrapResult
	now        time.Time
	payload    backdatePayload
	articles   int
	categories int
}

func (f *filler) userID(login string) (int, error) {
	u, ok := f.boot.Users[login]
	if !ok {
		return 0, fmt.Errorf("user %s is not in Zammad", login)
	}
	return u.ID, nil
}

// tickets creates each ticket as its customer, posts every further article
// as its author while the ticket is still new, then sets the state and
// owner as Ruben (a customer article on a closed ticket would reopen it).
func (f *filler) tickets(ctx context.Context) error {
	agentID, err := f.userID(Agent)
	if err != nil {
		return err
	}
	for _, t := range f.s.Content.Tickets {
		dates, err := f.ticket(ctx, t, agentID)
		if err != nil {
			return fmt.Errorf("ticket %s: %w", t.Key, err)
		}
		f.payload.Tickets = append(f.payload.Tickets, dates)
	}
	return nil
}

func (f *filler) ticket(ctx context.Context, t Ticket, agentID int) (ticketDates, error) {
	customerID, err := f.userID(t.Customer)
	if err != nil {
		return ticketDates{}, err
	}
	first := t.Articles[0]
	created, err := f.s.Admin.CreateTicket(ctx, zammad.NewTicket{
		Title: t.Title, Group: f.s.Group, CustomerID: customerID, Tags: []string{t.Key},
		Article: article(t, first, 0),
	}, t.Customer)
	if err != nil {
		return ticketDates{}, err
	}
	if len(created.ArticleIDs) != 1 {
		return ticketDates{}, fmt.Errorf("zammad made %d first articles, want 1", len(created.ArticleIDs))
	}
	dates := ticketDates{ID: created.ID, Key: t.Key, Articles: []articleDates{{ID: created.ArticleIDs[0], At: first.At.UTC()}}}
	f.articles++

	for _, a := range t.Articles[1:] {
		posted, err := f.s.Admin.CreateArticle(ctx, article(t, a, created.ID), a.From)
		if err != nil {
			return ticketDates{}, err
		}
		dates.Articles = append(dates.Articles, articleDates{ID: posted.ID, At: a.At.UTC()})
		f.articles++
	}

	if update, ok := f.stateUpdate(t, agentID); ok {
		if _, err := f.s.Admin.UpdateTicket(ctx, created.ID, update, Agent); err != nil {
			return ticketDates{}, err
		}
	}
	fillTicketDates(&dates, t)
	return dates, nil
}

// article builds one article: customer articles are public web articles,
// Ruben's replies are notes visible to the customer.
func article(t Ticket, a TicketArticle, ticketID int) zammad.NewArticle {
	na := zammad.NewArticle{TicketID: ticketID, Subject: t.Title, Body: a.Body, ContentType: "text/plain"}
	if a.From == Agent {
		na.Type, na.Sender = zammad.ArticleNote, zammad.SenderAgent
	} else {
		na.Type, na.Sender = zammad.ArticleWeb, zammad.SenderCustomer
	}
	return na
}

func (f *filler) stateUpdate(t Ticket, agentID int) (zammad.TicketUpdate, bool) {
	switch t.State {
	case StateClosed:
		return zammad.TicketUpdate{State: StateClosed, OwnerID: agentID}, true
	case StatePending:
		reminder := f.now.Add(pendingFor).UTC().Truncate(time.Minute)
		return zammad.TicketUpdate{State: zammadPending, OwnerID: agentID, PendingTime: &reminder}, true
	case StateOpen:
		// Zammad keeps a fresh ticket in state new; an open one has no owner.
		return zammad.TicketUpdate{State: StateOpen}, true
	}
	return zammad.TicketUpdate{}, false
}

// fillTicketDates derives the backdated ticket columns from the file.
func fillTicketDates(d *ticketDates, t Ticket) {
	d.CreatedAt = t.Created.UTC()
	last := t.Last().UTC()
	d.LastContactAt = last
	d.UpdatedAt = last
	if t.Closed != nil {
		closed := t.Closed.UTC()
		d.CloseAt = &closed
		d.UpdatedAt = closed
	}
	for _, a := range t.Articles {
		at := a.At.UTC()
		if a.From == Agent {
			if d.FirstResponseAt == nil {
				d.FirstResponseAt = &at
			}
			d.LastContactAgentAt = &at
		} else {
			d.LastContactCustomerAt = &at
		}
	}
}

// knowledgeBase creates the categories in order, then each answer, and
// publishes it.
func (f *filler) knowledgeBase(ctx context.Context) error {
	catIDs := map[string]int{}
	for _, c := range f.s.Content.Categories {
		id, err := f.s.Admin.CreateCategory(ctx, f.boot.KBID, zammad.NewCategory{Title: c.Name, Icon: c.Icon, KBLocaleID: f.boot.KBLocaleID})
		if err != nil {
			return fmt.Errorf("KB category %q: %w", c.Name, err)
		}
		catIDs[c.Name] = id
		f.categories++
	}
	for _, a := range f.s.Content.Articles {
		html, err := ToHTML(a.Body)
		if err != nil {
			return fmt.Errorf("KB article %s: %w", a.Slug, err)
		}
		id, err := f.s.Admin.CreateAnswer(ctx, f.boot.KBID, zammad.NewAnswer{
			CategoryID: catIDs[a.Category], Title: a.Title, Body: html, KBLocaleID: f.boot.KBLocaleID,
		})
		if err != nil {
			return fmt.Errorf("KB article %s: %w", a.Slug, err)
		}
		if err := f.s.Admin.PublishAnswer(ctx, f.boot.KBID, id); err != nil {
			return fmt.Errorf("publish KB article %s: %w", a.Slug, err)
		}
		f.payload.Answers = append(f.payload.Answers, answerDates{ID: id, Slug: a.Slug, CreatedAt: a.Created.UTC(), UpdatedAt: a.Updated.UTC()})
	}
	return nil
}
