package agent

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
)

// kbMarker is the model's citation of a knowledge base article (spec 0014,
// AC-10): [[kb:12]], with any spacing and case.
var kbMarker = regexp.MustCompile(`(?i)\[\[\s*kb\s*:\s*(\d+)\s*\]\]`)

// replyLink is a link code did not build: a Slack link with a label, a
// bare Slack link to a web address, or a raw web address.
var replyLink = regexp.MustCompile(`(?i)<[^<>\n]*\|[^<>\n]*>|<https?:[^<>\s]*>|\bhttps?://\S+`)

// builtLink stands in for a link code built while the link filter runs, so
// the filter never removes it. Model text never holds a NUL; it is removed first.
const builtLink = "\x00"

var spaceRun = regexp.MustCompile(` {2,}`)

// kbRef is one knowledge base result a citation may point to.
type kbRef struct {
	ID    int    `json:"id"`
	Title string `json:"title"`
	URL   string `json:"url"`
}

// link is the reply's text for the result: a Slack link, or the title in
// bold when the result has no url (ZAMMAD_PUBLIC_URL unset).
func (r kbRef) link() string {
	if r.URL == "" {
		return "*" + slackEscape(r.Title) + "*"
	}
	return "<" + slackEscape(r.URL) + "|" + slackEscape(r.Title) + ">"
}

// citedReply is the model's answer as the user sees it (spec 0014, AC-10):
// each allowed marker becomes a link built from a stored kb_search result,
// every other marker goes, and then every link code did not build goes, in
// every reply. linked reports whether a link with a url was built, so the
// post goes out with unfurling off. The stored assistant message keeps the model's text.
func (t *turn) citedReply(answer string) (text string, linked bool) {
	text = strings.ReplaceAll(answer, builtLink, "")
	var built []string
	markers := 0
	if strings.Contains(text, "[[") {
		allowed := t.kbRefs()
		text = kbMarker.ReplaceAllStringFunc(text, func(m string) string {
			id, err := strconv.Atoi(kbMarker.FindStringSubmatch(m)[1])
			ref, ok := allowed[id]
			if err != nil || !ok {
				markers++
				return ""
			}
			built = append(built, ref.link())
			linked = linked || ref.URL != ""
			return builtLink
		})
	}
	links := 0
	text = replyLink.ReplaceAllStringFunc(text, func(string) string {
		links++
		return ""
	})
	if markers > 0 {
		t.a.deps.Logger.Warn("agent_kb_marker_dropped", "dropped", markers)
	}
	if links > 0 {
		t.a.deps.Logger.Warn("agent_reply_link_dropped", "dropped", links)
	}
	if markers+links > 0 { // close the gaps the removals left
		text = strings.TrimSpace(spaceRun.ReplaceAllString(text, " "))
	}
	for _, link := range built {
		text = strings.Replace(text, builtLink, link, 1)
	}
	return text, linked
}

// kbRefs are the results of every ok kb_search stored in this conversation,
// by answer id. A history that cannot be read allows none, so every marker
// is removed.
func (t *turn) kbRefs() map[int]kbRef {
	ctx, cancel := t.a.detached(t.ctx, t.a.cfg.ToolTimeout)
	defer cancel()
	msgs, err := t.a.deps.Store.ListMessages(ctx, t.conv.ID)
	if err != nil {
		t.a.deps.Logger.Warn("agent_history_read_failed", "conversation_id", t.conv.ID, "cause", cause(err))
		return nil
	}
	refs := map[int]kbRef{}
	for _, result := range storedKBResults(msgs) {
		for _, r := range result {
			refs[r.ID] = r
		}
	}
	return refs
}

// storedKBResults lists the results of each ok kb_search in a stored
// history, oldest first, pairing each tool_use with its tool_result by id
// as storedCalls does. A purged or unreadable result holds none.
func storedKBResults(msgs []StoredMessage) [][]kbRef {
	var out [][]kbRef
	searches := map[string]bool{}
	for _, m := range msgs {
		var blocks []anthropic.ContentBlockParamUnion
		if m.Content == nil || json.Unmarshal(m.Content, &blocks) != nil {
			continue
		}
		for _, b := range blocks {
			switch {
			case b.OfToolUse != nil && b.OfToolUse.Name == toolKBSearch:
				searches[b.OfToolUse.ID] = true
			case b.OfToolResult != nil && searches[b.OfToolResult.ToolUseID] && !b.OfToolResult.IsError.Value:
				out = append(out, kbResultsFromText(resultText(b.OfToolResult)))
			}
		}
	}
	return out
}

// resultText is a stored tool result's first text block.
func resultText(r *anthropic.ToolResultBlockParam) string {
	for _, c := range r.Content {
		if c.OfText != nil {
			return c.OfText.Text
		}
	}
	return ""
}

// kbResultsFromText reads the results from a stored kb_search result: the
// JSON after the first newline (toolResultText).
func kbResultsFromText(text string) []kbRef {
	_, data, ok := strings.Cut(text, "\n")
	if !ok {
		return nil
	}
	var out struct {
		Results []kbRef `json:"results"`
	}
	if json.Unmarshal([]byte(data), &out) != nil {
		return nil
	}
	return out.Results
}

// kbFallbackLink is the link for TextKBAnswerFallback (spec 0014, AC-14a):
// the first result of the last ok kb_search stored in this conversation;
// ok is false when there is none.
func (t *turn) kbFallbackLink() (string, bool) {
	ctx, cancel := t.a.detached(t.ctx, t.a.cfg.ToolTimeout)
	defer cancel()
	msgs, err := t.a.deps.Store.ListMessages(ctx, t.conv.ID)
	if err != nil {
		t.a.deps.Logger.Warn("agent_history_read_failed", "conversation_id", t.conv.ID, "cause", cause(err))
		return "", false
	}
	results := storedKBResults(msgs)
	if len(results) == 0 || len(results[len(results)-1]) == 0 {
		return "", false
	}
	return results[len(results)-1][0].link(), true
}
