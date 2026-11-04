# eval

## Overview

What the agent and the eval harness share (specs 0003, 0008, 0017). `pricing.go` holds the per model prices (read from `pricing.yaml`, never hardcoded). `tickets.yaml` is the fixed list of 50 eval tickets, and `ticketset/` loads and checks it. Feature #20 (the harness) scores against the ticket set and imports only `eval/ticketset`.

## Key files

| File | Owns |
|---|---|
| `pricing.go`, `pricing.yaml` | model prices and `BudgetTokens`; imported by the agent |
| `tickets.yaml` | the 50 tickets, ids `ev-001` to `ev-050`; matches the case table in `docs/specs/0017-eval-ticket-set/rationale.md` (AC-13) |
| `ticketset/load.go`, `check.go`, `lists.go`, `scan.go` | `Load` (strict decode, sha256 `Hash`, one aggregated error), the per ticket and set checks against the directory truth and `internal/tools`, the closed lists, the fictional data scan |
| `ticketset/lock.go` | `CheckLock`: compares the file hash with `eval/tickets.lock` (the lock does not exist until the first saved run in #21) |

## Conventions

- Package `eval` and the agent must never import `eval/ticketset` or `internal/seed`. `cmd/agent/boundary_test.go` checks it.
- The file is public: fictional data only, emails and links end in `noord.example` (the loader scans for it).
- After the first saved run the file is frozen: change means a new numbered set (`tickets_v2.yaml`) and a journal entry, never an edit.
- A new tool or refusal code needs `ticketset/lists.go` and `internal/tools/refusalcode.go` updated; tests fail until it is.
- The expected tool lists were written from the specs, not from a run. Expect #21 to correct some before the freeze.

## Commands

```bash
go test -race ./eval/...
```
