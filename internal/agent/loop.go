package agent

import (
	"math"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"

	"github.com/toyinogun/AIITsupport/eval"
	"github.com/toyinogun/AIITsupport/internal/agent/prompt"
	"github.com/toyinogun/AIITsupport/internal/mcpclient"
)

// loop is turn step 10: budget checks, history check, model call, store the
// assistant message, run its one tool call, store the results; until the
// model ends its turn with text, which loop returns (AC-5).
func (t *turn) loop() (string, *failure) {
	for {
		if t.deadlinePassed() {
			return "", fail(ReasonMessageTimeout)
		}
		if t.modelCalls >= t.a.cfg.MaxTurns {
			return "", fail(ReasonTurnLimit)
		}
		params, f := t.request()
		if f != nil {
			return "", f
		}
		resp, f := t.callModel(params)
		if f != nil {
			return "", f
		}
		switch resp.StopReason {
		case anthropic.StopReasonToolUse:
			if f := t.runToolStep(resp); f != nil {
				return "", f
			}
		case anthropic.StopReasonEndTurn:
			text := strings.TrimSpace(responseText(resp))
			if text == "" {
				return "", fail(ReasonModelUnusable)
			}
			if f := t.storeAssistant(resp); f != nil {
				return "", f
			}
			return text, nil
		default:
			return "", fail(ReasonModelUnusable)
		}
	}
}

// request builds the next model request after the token budget and the
// history check (spec 0008, The model loop).
func (t *turn) request() (anthropic.MessageNewParams, *failure) {
	used, err := t.a.deps.Store.SumTokens(t.ctx, t.conv.ID)
	if err != nil {
		return anthropic.MessageNewParams{}, t.storeFailure(err)
	}
	if used >= t.a.cfg.MaxTokensPerTicket {
		return anthropic.MessageNewParams{}, fail(ReasonTokenBudget)
	}
	stored, err := t.a.deps.Store.ListMessages(t.ctx, t.conv.ID)
	if err != nil {
		return anthropic.MessageNewParams{}, t.storeFailure(err)
	}
	msgs, err := buildHistory(stored)
	if err != nil {
		return anthropic.MessageNewParams{}, fail(ReasonHistoryInvalid)
	}
	defs, err := t.a.toolDefs.get(t.ctx, t.a.deps.Tools)
	if err != nil {
		if t.deadlinePassed() {
			return anthropic.MessageNewParams{}, failErr(ReasonMessageTimeout, err)
		}
		return anthropic.MessageNewParams{}, failErr(ReasonToolServerUnavailable, err)
	}
	choice := anthropic.ToolChoiceUnionParam{OfAuto: &anthropic.ToolChoiceAutoParam{DisableParallelToolUse: anthropic.Bool(true)}}
	if t.resolved || t.escalated {
		none := anthropic.NewToolChoiceNoneParam()
		choice = anthropic.ToolChoiceUnionParam{OfNone: &none}
	}
	return anthropic.MessageNewParams{
		Model:     anthropic.Model(t.a.cfg.Model),
		MaxTokens: t.a.cfg.MaxResponseTokens,
		System: []anthropic.TextBlockParam{
			{Text: prompt.System, CacheControl: anthropic.NewCacheControlEphemeralParam()},
			{Text: requesterBlock(t.lookup, t.a.deps.Now())},
		},
		Tools:      requestTools(defs),
		ToolChoice: choice,
		Messages:   msgs,
	}, nil
}

// callModel makes one model call in an llm.call span and records its usage.
func (t *turn) callModel(params anthropic.MessageNewParams) (*anthropic.Message, *failure) {
	ctx, span := tracer.Start(t.ctx, "llm.call")
	defer span.End()
	span.SetAttributes(attribute.String("model", t.a.cfg.Model))
	t.modelCalls++
	resp, err := t.a.deps.Model.New(ctx, params)
	if err != nil {
		span.SetStatus(codes.Error, "model call failed")
		span.SetAttributes(attribute.String("cause", cause(err)))
		if t.deadlinePassed() {
			return nil, failErr(ReasonMessageTimeout, err)
		}
		return nil, failErr(ReasonModelUnavailable, err)
	}
	u := resp.Usage
	span.SetAttributes(
		attribute.String("stop_reason", string(resp.StopReason)),
		attribute.Int64("input_tokens", u.InputTokens),
		attribute.Int64("output_tokens", u.OutputTokens),
		attribute.Int64("cache_read_tokens", u.CacheReadInputTokens),
		attribute.Int64("cache_write_tokens", u.CacheCreationInputTokens),
	)
	if f := t.recordUsage(resp); f != nil {
		return nil, f
	}
	return resp, nil
}

// recordUsage writes the call's llm_usage row on a detached context, so a
// call that returned just before the deadline is still counted (AC-15).
func (t *turn) recordUsage(resp *anthropic.Message) *failure {
	tokens := eval.Tokens{
		Input:      resp.Usage.InputTokens,
		Output:     resp.Usage.OutputTokens,
		CacheRead:  resp.Usage.CacheReadInputTokens,
		CacheWrite: resp.Usage.CacheCreationInputTokens,
	}
	ctx, cancel := t.a.detached(t.ctx, t.a.cfg.ToolTimeout)
	defer cancel()
	err := t.a.deps.Store.InsertUsage(ctx, Usage{
		ConversationID: t.conv.ID,
		Model:          t.a.cfg.Model,
		Tokens:         tokens,
		CostMicroUSD:   t.a.cfg.Price.CostMicroUSD(tokens),
		PricingDate:    t.a.cfg.Price.PricingDate,
	})
	if err != nil {
		return failErr(ReasonStoreUnavailable, err)
	}
	return nil
}

// storeAssistant stores the response's content as the next assistant message.
func (t *turn) storeAssistant(resp *anthropic.Message) *failure {
	content, err := encodeBlocks(assistantContent(resp))
	if err != nil {
		return failErr(ReasonStoreUnavailable, err)
	}
	if err := t.a.deps.Store.AppendMessage(t.ctx, t.conv.ID, RoleAssistant, content); err != nil {
		return t.storeFailure(err)
	}
	return nil
}

// runToolStep stores the assistant message, runs its first tool_use block,
// answers any others with an error, and stores all results in one message
// together with the status change the result causes (AC-5, AC-6, AC-11).
func (t *turn) runToolStep(resp *anthropic.Message) *failure {
	uses := toolUses(resp)
	if len(uses) == 0 {
		return fail(ReasonModelUnusable)
	}
	if f := t.storeAssistant(resp); f != nil {
		return f
	}
	if t.deadlinePassed() {
		return fail(ReasonMessageTimeout)
	}
	first := uses[0]
	res, callErr := t.a.callTool(t.ctx, *t.requester, first.name, first.input)
	res, link := stripResetLink(first.name, res) // before anything else sees it (spec 0011, AC-5)
	answered := callErr == nil
	t.toolCalls++

	blocks := []anthropic.ContentBlockParamUnion{anthropic.NewToolResultBlock(first.id, toolResultText(res), res.IsError)}
	for _, u := range uses[1:] {
		blocks = append(blocks, anthropic.NewToolResultBlock(u.id, oneToolPerStep, true))
	}
	content, err := encodeBlocks(blocks)
	if err != nil {
		return &failure{reason: ReasonStoreUnavailable, cause: cause(err), unknown: !answered}
	}
	change := statusChangeFor(first.name, res, t.conv.TicketID)
	ctx, cancel := t.a.detached(t.ctx, t.a.cfg.ToolTimeout)
	defer cancel()
	card := t.a.pendingCard(first.name, res)
	var cardCreated bool
	if card != nil { // the card row goes in the result's transaction (spec 0012, AC-6)
		cardCreated, err = t.a.deps.Store.AppendToolResultWithCard(ctx, t.conv.ID, content, *card)
	} else {
		err = t.a.deps.Store.AppendToolResult(ctx, t.conv.ID, content, change)
	}
	if err != nil {
		return &failure{reason: ReasonStoreUnavailable, cause: cause(err), unknown: !answered}
	}
	if cardCreated {
		t.a.postCard(t.ctx, card.ApprovalID, requestCard(factsFromResult(t.conv, res.Data, card.ExpiresAt), stringOf(res.Data["methods"]), card.ApprovalID))
	}
	if change != nil {
		if change.TicketID != 0 {
			t.conv.TicketID = change.TicketID
			t.requester.TicketID = change.TicketID
		}
		switch change.Status {
		case StatusResolved:
			t.conv.Status = StatusResolved
			t.resolved = true
			if first.name == toolPasswordResetLink {
				t.postResetLink(link)
			}
		case StatusEscalated:
			t.modelEscalated(first.input, res)
		}
	}
	if !answered {
		return &failure{reason: ReasonToolServerUnavailable, cause: cause(callErr), unknown: true}
	}
	return nil
}

// statusChangeFor is the conversation update a tool result causes (AC-6):
// an ok ticket_create stores its ticket id, an ok ticket_update that closed
// the ticket or an ok password_reset_link (spec 0011, AC-5) resolves the
// conversation, keeping its ticket, and an ok escalate ends it as
// escalated, storing its ticket only when the conversation has none
// (spec 0009, AC-8). current is the conversation's ticket id.
func statusChangeFor(tool string, r mcpclient.Result, current int64) *StatusChange {
	if r.IsError {
		return nil
	}
	switch tool {
	case toolEscalate:
		ch := &StatusChange{Status: StatusEscalated}
		if id, ok := ticketID(r.Data["ticket_id"]); ok && current == 0 {
			ch.TicketID = id
		}
		return ch
	case toolTicketCreate:
		if id, ok := ticketID(r.Data["ticket_id"]); ok {
			return &StatusChange{Status: StatusOpen, TicketID: id}
		}
	case toolTicketUpdate:
		if state, _ := r.Data["state"].(string); state == "closed" {
			return &StatusChange{Status: StatusResolved}
		}
	case toolPasswordResetLink:
		return &StatusChange{Status: StatusResolved}
	}
	return nil
}

// modelEscalated follows the model's ok escalate once its transaction
// committed: the conversation is escalated, and IT hears at once, before the
// next model call, so a later failure in the turn cannot swallow the post
// (spec 0009, AC-8 and AC-9).
func (t *turn) modelEscalated(input map[string]any, res mcpclient.Result) {
	t.conv.Status, t.escalated = StatusEscalated, true
	reason, ok := resultReason(res.Data)
	if !ok {
		t.a.deps.Logger.Warn("agent_escalate_reason_unknown", "event_id", t.in.EventID)
	}
	t.reason = Reason(reason)
	t.a.logEscalated(t.ctx, t.in.EventID, t.reason, true, cause(nil))
	calls := t.a.callsOrNone(t.ctx, t.conv.ID) // a failed read leaves Ran: empty; the post still goes
	summary, _ := input["summary"].(string)
	displayName, _ := t.lookup["display_name"].(string)
	department, _ := t.lookup["department"].(string)
	post := itPost{
		slackID: t.requester.SlackID, account: t.accountName(),
		ran: ranLine(calls, false), result: res.Data, summary: modelSummary(summary, displayName, department),
	}
	t.a.postIT(t.ctx, t.in.EventID, kindITEscalation, post.text())
}

// maxTicketID is the largest id JSON carries exactly (2^53 - 1).
const maxTicketID = 1<<53 - 1

// ticketID reads a ticket id from decoded JSON (a float64 from the MCP
// client, an int from a fake).
func ticketID(v any) (int64, bool) {
	switch n := v.(type) {
	case float64:
		if n >= 1 && n <= maxTicketID && n == math.Trunc(n) {
			return int64(n), true
		}
	case int:
		return int64(n), n >= 1
	case int64:
		return n, n >= 1
	}
	return 0, false
}
