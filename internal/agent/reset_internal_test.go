package agent

import (
	"testing"

	"github.com/toyinogun/AIITsupport/internal/mcpclient"
)

// covers: spec 0011 AC-5. stripResetLink removes reset_url from an ok
// password_reset_link result and hands the link back separately, without
// changing the result it was given; anything else passes through untouched,
// and a link it cannot post (no minutes, not a string, empty) is still
// removed but not returned.
func TestStripResetLink(t *testing.T) {
	const url = "https://reset.noord.test/reset/TOKEN"
	ok := func(data map[string]any) mcpclient.Result {
		return mcpclient.Result{Text: "Reset link sent to the requester.", Data: data}
	}

	in := ok(map[string]any{"link_id": "1", "expires_in_minutes": float64(15), resetURLKey: url})
	out, link := stripResetLink(toolPasswordResetLink, in)
	if _, has := out.Data[resetURLKey]; has || out.Data["link_id"] != "1" {
		t.Errorf("stripped data = %v", out.Data)
	}
	if link == nil || link.url != url || link.minutes != 15 {
		t.Errorf("link = %+v, want the url and 15 minutes", link)
	}
	if in.Data[resetURLKey] != url {
		t.Error("stripResetLink changed the result it was given")
	}

	passThrough := map[string]struct {
		tool string
		r    mcpclient.Result
	}{
		"another tool": {"user_lookup", ok(map[string]any{resetURLKey: url})},
		"a refusal":    {toolPasswordResetLink, mcpclient.Result{IsError: true, Code: "rate_limited", Data: map[string]any{resetURLKey: url}}},
		"no link":      {toolPasswordResetLink, ok(map[string]any{"link_id": "1"})},
	}
	for name, c := range passThrough {
		t.Run(name, func(t *testing.T) {
			out, link := stripResetLink(c.tool, c.r)
			if link != nil || len(out.Data) != len(c.r.Data) {
				t.Errorf("out = %v, link = %+v; want it untouched", out.Data, link)
			}
		})
	}

	unpostable := map[string]map[string]any{
		"no minutes":     {resetURLKey: url},
		"zero minutes":   {resetURLKey: url, "expires_in_minutes": float64(0)},
		"partial minute": {resetURLKey: url, "expires_in_minutes": 1.5},
		"url not text":   {resetURLKey: 42, "expires_in_minutes": float64(15)},
		"empty url":      {resetURLKey: "", "expires_in_minutes": float64(15)},
	}
	for name, data := range unpostable {
		t.Run(name, func(t *testing.T) {
			out, link := stripResetLink(toolPasswordResetLink, ok(data))
			if _, has := out.Data[resetURLKey]; has || link != nil {
				t.Errorf("out = %v, link = %+v; want reset_url gone and no link", out.Data, link)
			}
		})
	}
}
