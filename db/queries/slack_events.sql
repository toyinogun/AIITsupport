-- Slack event dedup (spec 0003). The agent marks an event seen before it
-- processes it: at most once.

-- name: MarkEventSeen :execrows
-- MarkEventSeen affects 1 row for a new event and 0 for a duplicate.
INSERT INTO slack_events (event_id) VALUES (@event_id)
ON CONFLICT (event_id) DO NOTHING;

-- name: PurgeSeenEventsBefore :execrows
DELETE FROM slack_events WHERE received_at < @cutoff;
