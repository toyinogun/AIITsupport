# 0058. READ_ONLY in .env, then the token budget, ended a live run early

**Date:** 2026-10-21 · **Week:** 1 · **Feature:** #15 Access requests via group owner
**Time lost:** ~10 min (estimate) · **Story worthy:** post

## What I saw
During the live run after the review fixes (Noor asks for finance, Ilse approves), Noor's choice of `SG-Finance` did not reach Ilse:

```
tool_call group_access_request tier medium outcome read_only
agent_escalated reason token_budget
```

Noor's thread ended with "I couldn't finish this one, so I've passed it to IT in a helpdesk ticket."

## What I thought it was
At startup the tool server was taken to be read write, without checking its `kill_switch_file_unset` line, which said `read_only: true`.

## What it actually was
Two things. `.env` has `READ_ONLY=true`, and with `KILL_SWITCH_FILE` empty that decides, so every account change is paused. The refusal then cost the model two more steps (`ticket_update`, `escalate`), and the next model call hit `AGENT_MAX_TOKENS_PER_TICKET` (30000). That budget counts cached prompt tokens in full: each call reads about 5,300 cached tokens, so a thread is over 30,000 after five calls (this one: 30,963). The happy path fits exactly (this morning's run ended at 30,720), but any extra step tips it over.

## How I fixed it
For the live session only, the binaries were started directly with `READ_ONLY=false` exported after loading `.env` (`make dev` reloads `.env`, which would undo it); `.env` unchanged. A fresh thread then ran end to end: search, request, Ilse's approve, add, ticket closed, settle. No code change; not committed. The token budget is left as is; whether it should count cache reads is TODO (you): a decision for later. (Answered 2026-10-21 by spec 0014 AC-18: the budget now counts a cache read at its price over the input price, a tenth today, so the cap stays 30000; see journal 0059.)

## What I would tell a team doing this for real
(draft) Before a live run, read the startup line that says whether writes are paused, and size a per conversation token budget from real runs with prompt caching on.

## Evidence
- Error text: above; audit rows 819 and 820 (`group_access_request`, `read_only`, `account changes are paused`); `llm_usage` for conversation 71
- Commit or PR: none
- Eval run (if any): none
- Clip file and timestamp: TODO (you): clip file and timestamp
