-- name: Ping :one
-- Ping proves the database answers a real query at startup.
SELECT 1::int4 AS ok;
