# Runbook: data retention

This page says what personal data the assistant keeps, for how long, and how you check that the purges ran (spec [0010](../specs/0010-pii-redaction-retention/index.md)). The data processing note (#22) reuses it.

One setting drives both purges: `TICKET_RETENTION_DAYS` (default `30`, from 1 to 365). The clock starts when a conversation or ticket *ends*, not when it starts.

## What is purged, where, and when

| What | Where | When | Who does it |
|---|---|---|---|
| Idle conversations | `conversations` in our Postgres | an `open` conversation with no activity for N days becomes `resolved`, with `closed_at` set to its last activity | the agent's retention pass, at start and then hourly |
| Message text | `conversation_messages.content` | N days after its conversation ended (closed, or idle closed above): `content` becomes null and `content_purged_at` is set, once | the same pass, right after the idle close |
| Assistant tickets | Zammad | tickets tagged `noord-assistant` only: closed ones N days after their last close (`last_close_at`), merged ones N days after the merge (`updated_at`); the whole ticket and its history are deleted | the `noord-retention` scheduler inside Zammad, every hour at minute 0 |

Chat text can expire while IT still has the ticket open: the two clocks start at different events. The other way round, an assistant ticket IT never closes keeps its text for as long as it stays open: retention counts from the end of a ticket, never from its start, so live work is never deleted. A ticket IT reopens is not `closed` any more, so it is safe until it is closed again, and then its clock starts over. Noord's other helpdesk tickets (the seeded history, anything not tagged `noord-assistant`) are never touched.

After the purge, a later message in that Slack thread gets the `closed_thread` reply (spec 0008), so purged text is never sent to the model again.

## What is kept, for how long, and why

| What | Kept for | Why |
|---|---|---|
| `audit_log` (Slack ids, account names, allow listed tool arguments) | the whole engagement; it is append only and cannot be purged | it is the record of every action on a client system (spec 0003) |
| `approvals` (same kind of data; the full call only while pending or approved) | the whole engagement | approvals are audit evidence (spec 0003) |
| `conversations` rows (Slack channel and thread ids, the requester's Slack id, the directory account name, the ticket id) | the whole engagement | they link the audit log to a thread; they hold no names, emails, or message text |
| `llm_usage` (token counts and cost per model call) | the whole engagement | cost reporting; no text |

These rows are personal data kept without a time limit. That is a deliberate tradeoff (spec 0003, spec 0010 consequences), and the data processing note states it.

Logs and traces hold no names, emails, or message text: every value outside an allow list of ids, counts, and fixed tokens prints as `[redacted]`. One thing to know: a DM `channel_id` in a log line is a stable pseudonym for one user, because Slack gives each person one DM channel with the bot.

## Outside our control (documented, not enforced)

- **Slack history** follows the workspace plan's retention. The free plan hides messages older than 90 days and has no per channel setting. That covers the escalation summaries in the IT channel (names and departments are already masked there, spec 0009) and the replies in DMs.
- **The model provider** keeps API request data under its own retention terms. The EU region follow up from spec 0001 still applies.
- **Zammad's own Rails logs** are Zammad's to rotate.
- **A binary's exit line.** When the agent or the tool server stops on an error, `main` prints `<service>: <error>` to stderr, outside the log handler. It is meant for setting errors, which name the setting and never the value, but an error from Slack or Postgres that ends `run()` is printed as is.

## Change the window

1. Set `TICKET_RETENTION_DAYS` in `.env` (in the cluster: the agent's settings and the job that runs `helpdesk-seed`, so the two cannot drift).
2. Rerun the helpdesk seed, which updates the same `noord-retention` scheduler:

   ```bash
   make helpdesk-seed
   ```

   It prints `retention scheduler` with `days` and `tickets_matching_now`, the tickets the next hourly run would delete. Ticket deletion cannot be undone, so read that number before the next full hour.
3. Restart both binaries (`make dev`, or roll the deployments). The agent reads the window at start.

A Noord admin can edit or disable `noord-retention` in Zammad's scheduler UI. The next `make helpdesk-seed` restores it; nothing alerts in between.

## Check that both purges ran

**Postgres.** Count messages that still hold text in conversations that ended before the cutoff. Expect `0` (use your window in place of `30`):

```bash
docker compose exec -T postgres psql -U noord -d noord -c "SELECT count(*) FROM conversation_messages m JOIN conversations c ON c.id = m.conversation_id WHERE c.status <> 'open' AND c.closed_at < now() - interval '30 days' AND m.content IS NOT NULL;"
```

The agent's log shows each pass: `retention_purged` with `kind` (`idle_conversations` or `messages`) and `rows`, or `retention_purge_failed` with a `cause` token (the next hourly tick tries again).

**Zammad.** Print the scheduler's last run and how many tickets match now:

```bash
echo "j = Job.find_by!(name: 'noord-retention'); puts({last_run_at: j.last_run_at, next_run_at: j.next_run_at, active: j.active, matching_now: Ticket.selectors(j.condition, limit: 1, access: 'ignore').first.to_i}.to_json)" | docker compose --profile helpdesk exec -T zammad-railsserver bundle exec rails r -
```

`matching_now` right after a run should be `0`. A closed assistant ticket with no `last_close_at` is never deleted (fail safe); check there are none (expect `0`):

```bash
echo "ids = Tag.where(tag_object: Tag::Object.lookup(name: 'Ticket'), tag_item: Tag::Item.lookup(name: 'noord-assistant')).pluck(:o_id); puts Ticket.where(id: ids, last_close_at: nil).joins(:state).where(ticket_states: {name: 'closed'}).count" | docker compose --profile helpdesk exec -T zammad-railsserver bundle exec rails r -
```
