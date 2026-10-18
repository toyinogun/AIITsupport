package slack

import (
	"context"

	slackapi "github.com/slack-go/slack"
	"github.com/slack-go/slack/socketmode"
)

// socket adapts slack-go's Socket Mode client.
type socket struct {
	client *socketmode.Client
}

func (s socket) Events() <-chan socketmode.Event  { return s.client.Events }
func (s socket) Ack(req socketmode.Request) error { return s.client.Ack(req) }
func (s socket) Run(ctx context.Context) error    { return s.client.RunContext(ctx) }

// poster adapts slack-go's Web API client.
type poster struct {
	api *slackapi.Client
}

func (p poster) Check(ctx context.Context) error {
	_, err := p.api.AuthTestContext(ctx)
	return err
}

func (p poster) Reply(ctx context.Context, channel, threadTS, text string) error {
	_, _, err := p.api.PostMessageContext(ctx, channel,
		slackapi.MsgOptionText(text, false), slackapi.MsgOptionTS(threadTS))
	return err
}
