# 0002. Local dev ports already taken on the laptop

**Date:** 2026-10-17 · **Week:** 0 (before week 1) · **Feature:** #3 Stack & architecture
**Time lost:** about 3 minutes (estimate) · **Story worthy:** no

## What I saw
Before writing the Compose and `.env.example` files, a port check showed the spec's defaults would collide on this machine: a local Postgres already listens on 5432, and Docker already holds 8081 and 8082. Both binaries also default to `:8080`, which is fine in containers but clashes when `make dev` runs both on the host.

## What I thought it was
TODO (you): what did you first think it was?

## What it actually was
The laptop already runs other services on the obvious ports. Container defaults are not safe defaults for running two binaries side by side on the host.

## How I fixed it
Dev Postgres maps to `127.0.0.1:55432`. In `.env.example`, the agent listens on `:18080` and the tool server on `:18081`, and `TOOLSERVER_URL` points at `:18081`. The defaults in `internal/config` stay `:8080` for the cluster. Not committed yet.

## What I would tell a team doing this for real
(draft) Keep the container defaults the spec chose, and give local dev its own uncommon ports in `.env.example` so `make dev` works on a busy laptop.

## Evidence
- Error text or screenshot: `lsof` showed `postgres` on `localhost:5432` and Docker on `*:8081`, `*:8082`
- Commit or PR: not committed yet
- Eval run (if any): none
- Clip file and timestamp: TODO (you): clip file and timestamp
