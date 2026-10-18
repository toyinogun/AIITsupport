package zammad

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Article types and senders this package writes.
const (
	ArticleNote    = "note"
	ArticleWeb     = "web"
	SenderAgent    = "Agent"
	SenderCustomer = "Customer"
)

// Ticket is a Zammad ticket as the API returns it.
type Ticket struct {
	ID         int    `json:"id"`
	Number     string `json:"number"`
	Title      string `json:"title"`
	GroupID    int    `json:"group_id"`
	StateID    int    `json:"state_id"`
	CustomerID int    `json:"customer_id"`
	OwnerID    int    `json:"owner_id"`
	ArticleIDs []int  `json:"article_ids"`
}

// NewTicket creates a ticket with its first article. The customer is a
// numeric id, never an email or login, so Zammad never creates a user on
// the fly.
type NewTicket struct {
	Title      string     `json:"title"`
	Group      string     `json:"group"`
	CustomerID int        `json:"customer_id"`
	Article    NewArticle `json:"article"`
	// Tags are joined with commas, as Zammad expects.
	Tags []string `json:"-"`
}

// NewArticle is one article. TicketID is set when posting to an existing ticket.
type NewArticle struct {
	TicketID    int    `json:"ticket_id,omitempty"`
	Subject     string `json:"subject,omitempty"`
	Body        string `json:"body"`
	ContentType string `json:"content_type"`
	Type        string `json:"type"`
	Sender      string `json:"sender"`
	Internal    bool   `json:"internal"`
}

// Article is a ticket article as the API returns it.
type Article struct {
	ID       int `json:"id"`
	TicketID int `json:"ticket_id"`
}

// TicketUpdate changes a ticket's state, owner, or pending reminder, and can
// add an article in the same request, so the two succeed or fail together.
// Empty fields are left alone.
type TicketUpdate struct {
	State       string      `json:"state,omitempty"`
	OwnerID     int         `json:"owner_id,omitempty"`
	PendingTime *time.Time  `json:"pending_time,omitempty"`
	Article     *NewArticle `json:"article,omitempty"`
}

// TicketState is one of Zammad's ticket states, such as open or closed.
type TicketState struct {
	ID     int    `json:"id"`
	Name   string `json:"name"`
	Active bool   `json:"active"`
}

type newTicketBody struct {
	NewTicket
	Tags string `json:"tags,omitempty"`
}

// CreateTicket creates a ticket. from, when set, is the user Zammad acts as;
// a customer may only create a ticket in state new.
func (c *Client) CreateTicket(ctx context.Context, t NewTicket, from string) (Ticket, error) {
	var out Ticket
	body := newTicketBody{NewTicket: t, Tags: strings.Join(t.Tags, ",")}
	err := c.Do(ctx, Request{Method: http.MethodPost, Path: "/api/v1/tickets", Body: body, Out: &out, From: from})
	return out, err
}

// UpdateTicket changes a ticket, acting as from when it is set.
func (c *Client) UpdateTicket(ctx context.Context, id int, u TicketUpdate, from string) (Ticket, error) {
	var out Ticket
	err := c.Do(ctx, Request{Method: http.MethodPut, Path: fmt.Sprintf("/api/v1/tickets/%d", id), Body: u, Out: &out, From: from})
	return out, err
}

// CreateArticle posts an article to an existing ticket, acting as from when it is set.
func (c *Client) CreateArticle(ctx context.Context, a NewArticle, from string) (Article, error) {
	var out Article
	err := c.Do(ctx, Request{Method: http.MethodPost, Path: "/api/v1/ticket_articles", Body: a, Out: &out, From: from})
	return out, err
}

// GetTicket reads one ticket.
func (c *Client) GetTicket(ctx context.Context, id int) (Ticket, error) {
	var out Ticket
	err := c.Do(ctx, Request{Method: http.MethodGet, Path: fmt.Sprintf("/api/v1/tickets/%d", id), Out: &out})
	return out, err
}

// TicketStates lists Zammad's ticket states, to map names to ids.
func (c *Client) TicketStates(ctx context.Context) ([]TicketState, error) {
	var out []TicketState
	err := c.Do(ctx, Request{Method: http.MethodGet, Path: "/api/v1/ticket_states", Out: &out})
	return out, err
}
