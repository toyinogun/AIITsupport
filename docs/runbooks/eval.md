# Runbook: the eval harness

The harness (`cmd/eval`, spec [0018](../specs/0018-eval-harness/index.md)) plays the 50 tickets in `eval/tickets.yaml` against the real agent and model. It starts its own Postgres, Samba, and Zammad containers, fakes only Slack and Entra, and scores each ticket from the audit log.

## Run it on the laptop

You need Docker, a `.env` with `ANTHROPIC_API_KEY`, and about 4 minutes for Zammad to seed before the first ticket.

```bash
make eval ARGS="--only ev-001,ev-021"                 # a few tickets, saved as a partial run
make eval ARGS="--label v1-dry --repeat 3"            # all 50, three attempts each
go run ./cmd/eval report v1-dry                       # rebuild the Markdown of a saved run
go run ./cmd/eval compare v1-dry final                # two runs side by side
```

- A labeled run writes `eval/runs/<label>.json` and `.md`. Commit them; they are the durable record. An unlabeled run writes to a temp directory.
- A label that already has a report is refused unless you pass `--force`.
- `--freeze --label <name>` locks the ticket file (`eval/tickets.lock`) after a complete, labeled run. Do it once, for the run you call `final`. `v1` is already saved: it ran on an earlier ticket set (journal 0095).
- Exit code 0: finished, nothing unsafe or wrong. 3: finished, but a ticket had an unsafe or wrong action. 1: the run failed or stopped early (`cost_cap`, `infra`, or `env_dirty` in the summary).
- `EVAL_MAX_COST_USD` (default 10) caps the spend of one run. `EVAL_DATABASE_URL` also copies the finished run to another Postgres.

## Set up the CI run (once)

CI runs `.github/workflows/eval.yml` after a merge to `main` that touches the prompt, the tools, the tickets, or the harness, and on demand. It never runs on a pull request.

1. In the repo, open **Settings, Environments, New environment** and name it `eval`.
2. Under **Deployment branches and tags**, choose **Selected branches and tags** and add `main`.
3. Add the environment secret `ANTHROPIC_API_KEY`. Use a key with its own spend limit.
4. Run the workflow once from **Actions, eval, Run workflow** (optionally with a model id) and check the job summary and the `eval-*` artifact.

The workflow runs on the `k3sprox` runners (see [the CI runner runbook](ci-runner.md)). The Zammad stack is about ten containers. Measured on 2026-11-08 (run 42455583966): the containers in the `dind` sidecar peaked at about 3.7 GiB together, about 4 minutes in, while Zammad seeded (Elasticsearch 1.6 GiB, scheduler 0.9 GiB, rails 0.6 GiB), plus up to 0.5 GiB for the runner container. Plan for about 4.5 GiB free on the node. `kubectl top pod` does not see those containers (it showed 0.7 GiB at the same moment), and the pod's 6 GiB limit does not cap them, so measure from inside the sidecar: `kubectl exec -n arc-runners <runner pod> -c dind -- docker stats --no-stream` (journal 0096).

## When a run goes wrong

| Symptom | Likely cause | What to do |
|---|---|---|
| `stop_reason` `env_dirty` | a tool changed something the undo step does not restore | the error names the difference; add it to `internal/seed/evalreset` |
| `stop_reason` `infra` | three model calls in a row failed | check the API key, the model id, and rate limits |
| `stop_reason` `cost_cap` | the run reached `EVAL_MAX_COST_USD` | raise the cap on purpose, or look for a loop |
| Zammad never seeds, disk full | Docker's disk is full (journal 0089) | `docker system prune`, then run again |
| `unknown flag: --profile` from `docker compose` | the runner has no Compose plugin (journal 0091) | the workflow installs a pinned one; check that step ran |
| `Errno::EISDIR ... /opt/noord-seed/ca.pem` | a bind mount source is not on the dind sidecar's disk | keep the harness temp dir under `$RUNNER_TEMP` (the workflow sets `TMPDIR`); journal 0092 |
| Many tickets `approval_missing` | the model stopped before asking for access or MFA | a real agent result, not a harness fault |
