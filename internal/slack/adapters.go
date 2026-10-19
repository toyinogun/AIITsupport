package slack

import (
	"context"
	"errors"

	slackapi "github.com/slack-go/slack"
	"github.com/slack-go/slack/socketmode"

	"github.com/toyinogun/AIITsupport/internal/agent"
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

func (p poster) Check(ctx context.Context) (string, error) {
	res, err := p.api.AuthTestContext(ctx)
	if err != nil {
		return "", err
	}
	if res.TeamID == "" {
		return "", errNoTeam
	}
	return res.TeamID, nil
}

// errNoTeam means auth.test answered without a team id; CheckBotToken
// retries it like any other connect error.
var errNoTeam = errors.New("auth.test returned no team id")

func (p poster) Reply(ctx context.Context, channel, threadTS, text string) error {
	_, _, err := p.api.PostMessageContext(ctx, channel,
		slackapi.MsgOptionText(text, false), slackapi.MsgOptionTS(threadTS))
	return err
}

// users adapts users.info to the fields the agent's Slack gate reads (spec 0008, AC-3).
type users struct {
	api *slackapi.Client
}

func (u users) UserInfo(ctx context.Context, id string) (agent.SlackUser, error) {
	user, err := u.api.GetUserInfoContext(ctx, id)
	if err != nil {
		return agent.SlackUser{}, err
	}
	return toSlackUser(user), nil
}

func toSlackUser(u *slackapi.User) agent.SlackUser {
	return agent.SlackUser{
		TeamID:            u.TeamID,
		Deleted:           u.Deleted,
		IsBot:             u.IsBot,
		IsAppUser:         u.IsAppUser,
		IsRestricted:      u.IsRestricted,
		IsUltraRestricted: u.IsUltraRestricted,
		IsStranger:        u.IsStranger,
		IsEmailConfirmed:  u.IsEmailConfirmed,
		Email:             u.Profile.Email,
	}
}
