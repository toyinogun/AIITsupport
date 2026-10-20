// Package agenttest holds in memory fakes for every interface the agent
// talks through (spec 0008), so unit tests and the eval (#20) drive the
// agent with no Slack, no network, and a scripted model.
package agenttest

import (
	"context"
	"errors"
	"strconv"
	"sync"

	"github.com/toyinogun/AIITsupport/internal/agent"
)

// Team is the workspace the fakes belong to.
const Team = "T0NOORD"

// Staff returns a Slack user who passes the gate.
func Staff(email string) agent.SlackUser {
	return agent.SlackUser{TeamID: Team, IsEmailConfirmed: true, Email: email}
}

// Users is a fake users.info.
type Users struct {
	mu    sync.Mutex
	users map[string]agent.SlackUser
	Err   error // returned by every call when set
	calls int
}

// NewUsers returns a fake with the given users by Slack id.
func NewUsers(users map[string]agent.SlackUser) *Users {
	return &Users{users: users}
}

// Set adds or replaces a user.
func (u *Users) Set(id string, user agent.SlackUser) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.users[id] = user
}

// UserInfo returns the user, or an error for an unknown id.
func (u *Users) UserInfo(_ context.Context, id string) (agent.SlackUser, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.calls++
	if u.Err != nil {
		return agent.SlackUser{}, u.Err
	}
	user, ok := u.users[id]
	if !ok {
		return agent.SlackUser{}, errors.New("user_not_found")
	}
	return user, nil
}

// Calls is how many users.info calls were made.
func (u *Users) Calls() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.calls
}

// Post is one chat.postMessage.
type Post struct {
	Channel, Thread, Text string
	NoUnfurl              bool // sent with ReplyNoUnfurl
}

// Poster is a fake chat.postMessage that records every post.
type Poster struct {
	mu          sync.Mutex
	posts       []Post
	Err         error
	NoUnfurlErr error // fails ReplyNoUnfurl only
	CardErr     error // fails PostCard and UpdateCard
	cards       []CardPost
	ephemerals  []Ephemeral
}

// Reply records the post.
func (p *Poster) Reply(_ context.Context, channel, thread, text string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.posts = append(p.posts, Post{Channel: channel, Thread: thread, Text: text})
	return p.Err
}

// ReplyNoUnfurl records the post as sent with unfurling off, or fails with
// NoUnfurlErr when set (else Err).
func (p *Poster) ReplyNoUnfurl(_ context.Context, channel, thread, text string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.posts = append(p.posts, Post{Channel: channel, Thread: thread, Text: text, NoUnfurl: true})
	if p.NoUnfurlErr != nil {
		return p.NoUnfurlErr
	}
	return p.Err
}

// CardPost is one PostCard or UpdateCard.
type CardPost struct {
	Channel, TS string
	Card        agent.Card
	Update      bool
}

// Ephemeral is one chat.postEphemeral.
type Ephemeral struct{ Channel, User, Text string }

// PostCard records the card and returns a ts, or fails with CardErr.
func (p *Poster) PostCard(_ context.Context, channel string, c agent.Card) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.CardErr != nil {
		return "", p.CardErr
	}
	ts := "1792400100." + strconv.Itoa(100000+len(p.cards))
	p.cards = append(p.cards, CardPost{Channel: channel, TS: ts, Card: c})
	return ts, nil
}

// UpdateCard records the update, or fails with CardErr.
func (p *Poster) UpdateCard(_ context.Context, channel, ts string, c agent.Card) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.CardErr != nil {
		return p.CardErr
	}
	p.cards = append(p.cards, CardPost{Channel: channel, TS: ts, Card: c, Update: true})
	return nil
}

// Ephemeral records the message.
func (p *Poster) Ephemeral(_ context.Context, channel, user, text string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.ephemerals = append(p.ephemerals, Ephemeral{Channel: channel, User: user, Text: text})
	return p.Err
}

// Cards returns every card post and update so far.
func (p *Poster) Cards() []CardPost {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]CardPost(nil), p.cards...)
}

// Ephemerals returns every ephemeral message so far.
func (p *Poster) Ephemerals() []Ephemeral {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]Ephemeral(nil), p.ephemerals...)
}

// Posts returns every post so far.
func (p *Poster) Posts() []Post {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]Post(nil), p.posts...)
}

// Texts returns the text of every post so far.
func (p *Poster) Texts() []string {
	var out []string
	for _, post := range p.Posts() {
		out = append(out, post.Text)
	}
	return out
}
