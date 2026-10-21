# 0059. Every knowledge base answer used nearly the whole token budget

**Date:** 2026-10-21 · **Week:** 1 · **Feature:** #16 Knowledge base answers
**Time lost:** ~15 min (estimate) · **Story worthy:** post

## What I saw
Spec 0014 AC-14 says the KB happy path and the conflict path must each stay at or under 27,000 tokens (10 percent under `AGENT_MAX_TOKENS_PER_TICKET` of 30,000). The live runs (`TestLiveKBAnswers`, real model, real tool server and Zammad) came in at:

```
RESULT vpn:      status resolved, model calls 4, tokens 29918 of 30000
RESULT password: status resolved, model calls 4, tokens 29940 of 30000
RESULT sms:      status resolved, model calls 4, tokens 30489 of 30000
RESULT starter:  status open,     model calls 4, tokens 29709 of 30000
RESULT nomatch:  status open,     model calls 4, tokens 28330 of 30000
RESULT lockout:  status resolved, model calls 5, tokens 37712 of 30000
```

Every call read `cache read 6661` tokens. The lockout run searched twice (`lockout` found nothing, then `lock`), which took a fifth call.

## What I thought it was
The three article bodies (about 450 runes each) pushing the history up.

## What it actually was
Mostly the fixed prefix, not the articles. The cached system prompt plus tool definitions is now about 6,660 tokens (it was about 5,300 in journal 0058), and the budget counts cache reads in full. Four model calls read it four times, which is about 26,600 tokens before any history or output. The new prompt section, the `kb_search` tool, and the `ticket_update` field added about 1,300 tokens to every call.

## How I fixed it
Not fixed. The spec says a miss is a spec change, not a silent budget raise, so it goes to `/architect`. The options are counting cache reads differently, a larger budget, or a shorter prompt prefix (a smaller `KB_BODY_MAX_RUNES` or `KB_SEARCH_LIMIT` barely helps). The counts are in spec 0014's `verify.md`. Not committed yet.

## What I would tell a team doing this for real
(draft) Every new tool and prompt section grows the prefix that every model call pays for, so measure the per call prefix whenever the prompt changes, not only the conversation total.

## Evidence
- Error text or screenshot: the RESULT lines above
- Commit or PR: not committed yet
- Eval run (if any): none (live harness `TestLiveKBAnswers`, 2026-10-21)
- Clip file and timestamp: TODO (you): clip file and timestamp
