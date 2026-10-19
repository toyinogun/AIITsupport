// Package agenttest holds in memory fakes for every interface the agent
// talks through (spec 0008), so unit tests and the eval (#20) drive the
// agent with no Slack, no network, and a scripted model.
package agenttest

import (
	"context"
	"errors"
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
}

// Poster is a fake chat.postMessage that records every post.
type Poster struct {
	mu    sync.Mutex
	posts []Post
	Err   error
}

// Reply records the post.
func (p *Poster) Reply(_ context.Context, channel, thread, text string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.posts = append(p.posts, Post{channel, thread, text})
	return p.Err
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
