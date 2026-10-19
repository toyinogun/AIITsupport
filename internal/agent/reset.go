package agent

import (
	"maps"

	"github.com/toyinogun/AIITsupport/internal/mcpclient"
)

// resetURLKey is the password_reset_link result key holding the link
// (spec 0011, AC-5). Agent code removes it before anything else sees the result.
const resetURLKey = "reset_url"

// resetLink is a link held only in the turn's memory until it is posted.
type resetLink struct {
	url     string
	minutes int64
}

// stripResetLink removes reset_url from an ok password_reset_link result,
// right after the call returns: the stored result, the model, and the IT
// post's Ran: list never see it. It returns a copy; r is left as it was.
// link is nil for any other tool, a refusal, or a result without a link.
func stripResetLink(tool string, r mcpclient.Result) (mcpclient.Result, *resetLink) {
	if tool != toolPasswordResetLink || r.IsError {
		return r, nil
	}
	url, hasURL := r.Data[resetURLKey].(string)
	if _, present := r.Data[resetURLKey]; !present {
		return r, nil
	}
	out := r
	out.Data = maps.Clone(r.Data)
	delete(out.Data, resetURLKey)
	minutes, ok := wholeNumber(r.Data["expires_in_minutes"])
	if !hasURL || url == "" || !ok || minutes < 1 {
		return out, nil
	}
	return out, &resetLink{url: url, minutes: minutes}
}

// postResetLink posts the link in the thread with unfurling off, once the
// result's transaction committed (spec 0011, AC-5). A failed post makes the
// turn end with reset_link_failed instead of the model's reply.
func (t *turn) postResetLink(link *resetLink) {
	if link == nil {
		t.resetPostFailed = true
		return
	}
	ctx, cancel := t.a.detached(t.ctx, t.a.cfg.PostTimeout)
	defer cancel()
	if err := t.a.deps.Poster.ReplyNoUnfurl(ctx, t.in.ChannelID, t.in.ThreadTS, ResetLinkText(link.minutes, link.url)); err != nil {
		t.a.deps.Logger.Warn("agent_post_failed", "event_id", t.in.EventID, "kind", "reset_link", "cause", cause(err))
		t.resetPostFailed = true
		return
	}
	t.resetPosted = true
}
