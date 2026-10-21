package slack

import (
	"context"
	"errors"
	"regexp"
	"strconv"
	"strings"

	slackapi "github.com/slack-go/slack"
	"github.com/slack-go/slack/socketmode"

	"github.com/toyinogun/AIITsupport/internal/agent"
	"github.com/toyinogun/AIITsupport/internal/cause"
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
		return "", wrapErr(err)
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
	return p.post(ctx, channel, threadTS, slackapi.MsgOptionText(text, false))
}

func (p poster) ReplyNoUnfurl(ctx context.Context, channel, threadTS, text string) error {
	return p.post(ctx, channel, threadTS, slackapi.MsgOptionText(text, false),
		slackapi.MsgOptionDisableLinkUnfurl(), slackapi.MsgOptionDisableMediaUnfurl())
}

func (p poster) post(ctx context.Context, channel, threadTS string, opts ...slackapi.MsgOption) error {
	if threadTS != "" { // an empty thread posts to the channel itself (the IT channel, spec 0009)
		opts = append(opts, slackapi.MsgOptionTS(threadTS))
	}
	_, _, err := p.api.PostMessageContext(ctx, channel, opts...)
	return wrapErr(err)
}

// users adapts users.info to the fields the agent's Slack gate reads (spec 0008, AC-3).
type users struct {
	api *slackapi.Client
}

func (u users) UserInfo(ctx context.Context, id string) (agent.SlackUser, error) {
	user, err := u.api.GetUserInfoContext(ctx, id)
	if err != nil {
		return agent.SlackUser{}, wrapErr(err)
	}
	return toSlackUser(user), nil
}

// LookupByEmail is users.lookupByEmail (spec 0013, AC-10): users_not_found
// is agent.ErrSlackUserNotFound, every other error keeps its cause token.
func (u users) LookupByEmail(ctx context.Context, email string) (agent.SlackUser, error) {
	user, err := u.api.GetUserByEmailContext(ctx, email)
	var resp slackapi.SlackErrorResponse
	if errors.As(err, &resp) && resp.Err == "users_not_found" {
		return agent.SlackUser{}, agent.ErrSlackUserNotFound
	}
	if err != nil {
		return agent.SlackUser{}, wrapErr(err)
	}
	return toSlackUser(user), nil
}

func toSlackUser(u *slackapi.User) agent.SlackUser {
	return agent.SlackUser{
		ID:                u.ID,
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

// slackError hands the agent a Slack Web API error's fixed cause token
// (cause.Causer), so its logs say why a call failed without the error text.
// Error and Unwrap keep the original error.
type slackError struct {
	err   error
	cause string
}

var _ cause.Causer = slackError{}

func (e slackError) Error() string { return e.err.Error() }
func (e slackError) Unwrap() error { return e.err }
func (e slackError) Cause() string { return e.cause }

// slackCode is the shape of a Slack API error code such as "missing_scope".
var slackCode = regexp.MustCompile(`^[a-z0-9_]{1,50}$`)

// wrapErr gives a Slack Web API error its cause token: "slack_<code>" for an
// API error code, "slack_rate_limited", or "slack_<status>" for an HTTP
// status. Any other error is returned unchanged.
func wrapErr(err error) error {
	var (
		resp slackapi.SlackErrorResponse
		rate *slackapi.RateLimitedError
		code slackapi.StatusCodeError
	)
	switch {
	case err == nil:
		return nil
	case errors.As(err, &resp):
		if slackCode.MatchString(resp.Err) {
			return slackError{err: err, cause: "slack_" + resp.Err}
		}
		return slackError{err: err, cause: "slack_api"}
	case errors.As(err, &rate):
		return slackError{err: err, cause: "slack_rate_limited"}
	case errors.As(err, &code):
		return slackError{err: err, cause: "slack_" + strconv.Itoa(code.Code)}
	}
	return err
}

// PostCard posts an approval card as a new message and returns the channel
// it landed in and its ts. A user id as channel opens that person's DM
// (spec 0013, AC-10); chat:write covers it.
func (p poster) PostCard(ctx context.Context, channel string, c agent.Card) (string, string, error) {
	posted, ts, err := p.api.PostMessageContext(ctx, channel, cardOptions(c)...)
	if err != nil {
		return "", "", wrapErr(err)
	}
	return posted, ts, nil
}

// UpdateCard replaces a posted card (chat.update).
func (p poster) UpdateCard(ctx context.Context, channel, ts string, c agent.Card) error {
	_, _, _, err := p.api.UpdateMessageContext(ctx, channel, ts, cardOptions(c)...)
	return wrapErr(err)
}

// Ephemeral shows text to one user only (chat.postEphemeral).
func (p poster) Ephemeral(ctx context.Context, channel, userID, text string) error {
	_, err := p.api.PostEphemeralContext(ctx, channel, userID, slackapi.MsgOptionText(text, false))
	return wrapErr(err)
}

// cardOptions is the card's fallback text and its blocks.
func cardOptions(c agent.Card) []slackapi.MsgOption {
	return []slackapi.MsgOption{slackapi.MsgOptionText(c.Fallback, false), slackapi.MsgOptionBlocks(cardBlocks(c)...)}
}

// cardBlocks renders a card as Block Kit: one section with its lines, then,
// while it can be decided, the Approve and Deny buttons in an actions block
// whose id names the approval, Approve behind the card's confirm text. Only
// code builds the lines (spec 0012, AC-7).
func cardBlocks(c agent.Card) []slackapi.Block {
	blocks := []slackapi.Block{
		slackapi.NewSectionBlock(slackapi.NewTextBlockObject(slackapi.MarkdownType, strings.Join(c.Lines, "\n"), false, true), nil, nil),
	}
	if c.ApprovalID != "" {
		approve := slackapi.NewButtonBlockElement(agent.ActionApprove, c.ApprovalID,
			slackapi.NewTextBlockObject(slackapi.PlainTextType, "Approve", false, false))
		approve.Style = slackapi.StylePrimary
		approve.Confirm = slackapi.NewConfirmationBlockObject(
			slackapi.NewTextBlockObject(slackapi.PlainTextType, "Approve", false, false),
			slackapi.NewTextBlockObject(slackapi.PlainTextType, c.Confirm, false, false),
			slackapi.NewTextBlockObject(slackapi.PlainTextType, "Approve", false, false),
			slackapi.NewTextBlockObject(slackapi.PlainTextType, "Cancel", false, false))
		deny := slackapi.NewButtonBlockElement(agent.ActionDeny, c.ApprovalID,
			slackapi.NewTextBlockObject(slackapi.PlainTextType, "Deny", false, false))
		deny.Style = slackapi.StyleDanger
		blocks = append(blocks, slackapi.NewActionBlock("approval:"+c.ApprovalID, approve, deny))
	}
	return blocks
}
