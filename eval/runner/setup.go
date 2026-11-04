package runner

import (
	"context"
	"fmt"
	"strings"

	"github.com/toyinogun/AIITsupport/eval/env"
	"github.com/toyinogun/AIITsupport/eval/ticketset"
	"github.com/toyinogun/AIITsupport/internal/mcpclient"
	"github.com/toyinogun/AIITsupport/internal/seed/directory"
)

// authenticatorID is the one fake Authenticator method the `mfa_methods` setup
// registers for the sender.
const authenticatorID = "eval-authenticator-1"

// otherThread is the thread the second MFA request of `pending_mfa_other_thread`
// is made in.
const otherThread = "1700000000.000200"

// applySetup applies the ticket's starting state (spec 0017, AC-8; spec 0018,
// AC-3). Directory and Entra state is undone by the reset step; the pending
// approval lives in the attempt database and goes with it.
func (r *Runner) applySetup(ctx context.Context, at *env.Attempt, t ticketset.Ticket, sender string) error {
	for _, s := range t.Setup {
		var err error
		switch {
		case s == "lock":
			err = r.lock(ctx, t.Sender.User)
		case strings.HasPrefix(s, "lock:"):
			err = r.lock(ctx, strings.TrimPrefix(s, "lock:"))
		case s == "mfa_methods":
			err = r.registerAuthenticator(t.Sender.User)
		case s == "pending_mfa_other_thread":
			err = r.pendingMFA(ctx, at, t, sender)
		default:
			err = fmt.Errorf("unknown setup %q", s)
		}
		if err != nil {
			return fmt.Errorf("ticket %s: setup %s: %w", t.ID, s, err)
		}
	}
	return nil
}

func (r *Runner) lock(ctx context.Context, user string) error {
	return r.Env.Samba.Dir.Lock(ctx, r.Env.Samba.Model, user)
}

func (r *Runner) registerAuthenticator(user string) error {
	id, ok := r.Env.Entra.UserIDs[user]
	if !ok {
		// A user the sync never reached (synced: false) has no Entra account,
		// so there is nothing to register; the tool refuses with not_in_entra.
		return nil
	}
	r.Env.Entra.Fake.SetAuthenticators(id, authenticatorID)
	return nil
}

// pendingMFA leaves one pending mfa_clear approval for the sender, made in a
// second thread through the MCP client with the sender's identity.
func (r *Runner) pendingMFA(ctx context.Context, at *env.Attempt, t ticketset.Ticket, sender string) error {
	mail, ok := Mail(r.Env.Samba.Model, t.Sender.User)
	if !ok {
		return fmt.Errorf("%s is not in the directory", t.Sender.User)
	}
	client, err := mcpclient.New(at.Child.URL+"/mcp", r.Env.Token)
	if err != nil {
		return err
	}
	req := mcpclient.Requester{SlackID: sender, Email: mail, ChannelID: evalChannel, ThreadTS: otherThread}
	created, err := client.Call(ctx, req, "ticket_create", map[string]any{"title": "MFA reset", "body": "New phone, please reset my MFA."})
	if err != nil {
		return fmt.Errorf("create the first ticket: %w", err)
	}
	if created.IsError {
		return fmt.Errorf("create the first ticket: refused with code %q", created.Code)
	}
	id, ok := created.Data["ticket_id"].(float64)
	if !ok {
		return fmt.Errorf("the first ticket has no id")
	}
	req.TicketID = int64(id)
	asked, err := client.Call(ctx, req, "mfa_clear", nil)
	if err != nil {
		return fmt.Errorf("ask for the first MFA clear: %w", err)
	}
	if asked.IsError {
		return fmt.Errorf("ask for the first MFA clear: refused with code %q", asked.Code)
	}
	return nil
}

// Mail is a directory user's mail address.
func Mail(m directory.Model, username string) (string, bool) {
	for _, u := range m.Users {
		if u.Username == username {
			return u.Mail, true
		}
	}
	return "", false
}
