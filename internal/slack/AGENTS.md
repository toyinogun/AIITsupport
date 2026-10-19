# internal/slack

## Overview

The agent's Slack strand (spec 0006): a Socket Mode listener that hears a DM (`message.im`) or an @mention outside a DM (`app_mention`), acknowledges it at once, drops duplicates through the `slack_events` table, and answers in the message's thread. Every accepted event goes to the agent (`internal/agent`, spec 0008) through a non blocking `Submit`; the agent writes the answer. Socket Mode dials out, so nothing listens on the internet. `cmd/agent` starts it only when both `SLACK_BOT_TOKEN` and `SLACK_APP_TOKEN` are set.

## Key files

| File | Owns |
|---|---|
| `listener.go` | `Listener`, `New`, `CheckBotToken` (auth.test with backoff; returns the workspace team id), `Run` (the socket, event loop, and purge loop in one errgroup), the `Handler.Submit` hand off, the event filter `toMessage`, `ErrBotTokenRejected`, `ErrAppTokenRejected` |
| `adapters.go` | the thin adapters over slack-go's `socketmode.Client` (`Socket`) and Web API client (`Poster`, and `users` for `users.info` as `agent.SlackUser`), plus `wrapErr` |
| `../../deploy/slack/manifest.yaml` | the Slack app (scopes, events, Socket Mode); setup in [the Slack runbook](../../docs/runbooks/slack-workspace.md) |

## Conventions

- **At most once (spec 0003).** `MarkEventSeen` runs before any reply. A duplicate (0 rows) gets nothing; a dedup error logs `slack_event_dedup_failed` and sends nothing. `PurgeSeenEventsBefore` runs at start and hourly with cutoff now minus `SLACK_EVENT_DEDUP_TTL`.
- **What gets answered:** `message` with channel type `im`, or `app_mention` in a channel whose id does not start with `D`; never a `bot_id`, never a subtype (edits, deletes, joins). A DM that mentions the bot arrives as both events and gets one reply.
- **Logs** (`slack_event_received` and friends) carry the event id, channel id, and event type only, never the message text or a user id.
- **Refused tokens end `run()`.** `invalid_auth`, `not_authed`, `token_revoked`, or `account_inactive` from auth.test or `apps.connections.open` returns an error naming the setting, never the value. Other connect errors retry with backoff; slack-go reconnects a dropped socket itself and emits an event per attempt (`slack_connect_failed`).
- Web API errors leave the adapters through `wrapErr`, which gives them a fixed `slack_<code>` cause token (`cause.Causer`) for the agent's logs; the error text never reaches a log.
- `Poster.ReplyNoUnfurl` posts with link and media unfurling off, so Slack never fetches the link; the password reset link goes out only through it (spec 0011, AC-5).
- `Poster.Reply` with an empty `threadTS` posts to the channel itself, not a thread: that is how the agent posts to `SLACK_IT_CHANNEL` (spec 0009).
- Tests drive the listener through the `Socket`, `Poster`, and `Dedup` interfaces with fakes; no network.

## Gotchas

- The bot has no `channels:history` scope by design, so it cannot read channel history; timing checks for channel replies come from the agent log, DM timing from `conversations.history` on the DM.
- slack-go's `socketmode.Client` already retries the first connect with its own backoff (up to 5 minutes); the listener does not wrap that in a second loop.

## Commands

```bash
go test -race ./internal/slack/...   # unit tests with a fake socket, poster, and dedup store
make dev                             # with both tokens in .env: the agent logs slack_connected
```

## Related specs

- [0006 helpdesk, KB, and Slack](../../docs/specs/0006-helpdesk-kb-slack/index.md) (AC-11 to AC-14, with [verify.md](../../docs/specs/0006-helpdesk-kb-slack/verify.md))
- [0003 data model](../../docs/specs/0003-data-model/index.md) (the `slack_events` table and at most once delivery)
- [0009 refusal and escalation](../../docs/specs/0009-refusal-escalation/index.md) (the IT channel post)

_Drafted by /sync from the introducing change, worth a quick human pass._
