-- At most one pending approval per account and tool (spec 0012): the
-- precheck reuses or refuses, and this index stops two requests that pass
-- the precheck at the same moment. Defer expires the account's overdue
-- pending rows first, so a row the tick has not reached yet never blocks a
-- new request. A null account never collides.

-- +goose Up
CREATE UNIQUE INDEX approvals_one_pending_idx ON approvals (requester_account, tool_name)
    WHERE status = 'pending';

-- +goose Down
DROP INDEX approvals_one_pending_idx;
