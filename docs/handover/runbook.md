# Runbook: running the assistant on your own

This page is for you as Noord's IT person, after the engagement ends. It covers the six jobs you will actually do: deploy, monitor, tune the prompt, add a tool, rotate secrets, and switch to read only or off. Each section gives you the short path and links to the detailed runbook. Every step is a recommendation; skip what does not fit your setup.

Everything runs from a clone of this repo on your own machine, against the cluster named by `KUBE_CONTEXT` in your `deploy/clusters/<name>/secrets.env`.

## Deploy and release

**First install** into a fresh k3s cluster:

```bash
make cluster-init CLUSTER=<name>
```

Fill every `CHANGE_ME` in `deploy/clusters/<name>/secrets.env`, keep a copy of that file somewhere safe, then:

```bash
make install CLUSTER=<name>
```

It checks first and changes nothing if a check fails (exit 2). It ends with a smoke check and prints the helpdesk and reset page addresses. The kill switch starts at `read_only`, so nothing changes in AD or Entra until you flip it (below).

**Release a new version** after CI has built the images for a commit:

```bash
make release CLUSTER=<name> SHA=<full sha>
make install CLUSTER=<name>
```

`release` pins the new image tag in the cluster's `values.yaml`; run `install` after it so the cluster really moves (journal 0086).

**Check it is healthy** at any time:

```bash
make smoke CLUSTER=<name>
```

The full detail (prerequisites, exit codes, backups, restore, the sealing key) is in [deploy.md](../runbooks/deploy.md). One habit worth keeping: after the first install, run `make seal-key-backup CLUSTER=<name>` and store the result offline. Without it, a lost cluster means resealing every secret.

## Monitor

Open the Grafana dashboard "Noord assistant, last 24 hours" (the address is in [observability.md](../runbooks/observability.md#where-it-is)). Five things are worth a glance each morning:

| Look at | Healthy | Act when |
|---|---|---|
| Account changes mode | what you set (`read_write` or `read_only`) | **unknown**: the tool server is down or cannot reach the database |
| Approvals waiting | empty, or younger than 30 minutes | anything older: a card was missed or Slack is down |
| Errors by stage | zero or near zero | any `llm` (model outage or key problem), a growing `slack`, any `sweep` (a restart cut a conversation off) |
| Escalated and Refused | steady | a sudden rise in Refused means someone is probing for admin rights |
| Cost per conversation | near the eval's $0.01 to $0.03 | well above it: a prompt change made it loop, or a long conversation |

Also check, once a week, the Zammad tickets tagged `kb-review`: the assistant tags a ticket when it used a stale knowledge base article or set one aside as conflicting. Fixing those articles is the cheapest way to make it better.

To follow one conversation, click **open trace** on its row. The traces and dashboard never show a name, an email, or message text. Detail: [observability.md](../runbooks/observability.md).

## Tune the prompt

The assistant's instructions are one Markdown file, [internal/agent/prompt/system.md](../../internal/agent/prompt/system.md), with a section per kind of ticket (locked out, forgotten password, MFA, access, knowledge base, privileged requests, escalation). It is built into the agent binary, so a change ships like any other code change.

1. Edit the section that covers the behavior you want to change. Keep one change per round, so you can tell what moved the numbers.
2. Run the eval on the tickets that cover it, then on all of them:

   ```bash
   make eval ARGS="--only ev-041,ev-042,ev-044"
   make eval ARGS="--label prompt-<date> --repeat 3"
   go run ./cmd/eval compare final prompt-<date>
   ```

3. Ship it only if unsafe is still 0 and nothing that passed in `final` now fails. The report shows a prompt hash, so you can always tell which prompt a run used.
4. Merge, wait for CI to build the images, then release that commit (see [Deploy and release](#deploy-and-release)).

Never put a rule that matters for safety only in the prompt. If the assistant must never do something, it belongs in the tool server's code (a refusal or a fence), and the prompt only explains it.

**Knobs that are settings, not prompt.** In the cluster, set them by name under `agent.env` or `toolserver.env` in `deploy/clusters/<name>/values.yaml` (on a laptop, in `.env`), then run `make install CLUSTER=<name>`. Each is documented in [`.env.example`](../../.env.example), and both programs refuse to start on a value out of range:

| Setting | What it changes |
|---|---|
| `LLM_MODEL` | the model; it must have a price row in `eval/pricing.yaml`, and you should rerun the eval after a change |
| `AGENT_MAX_TURNS`, `AGENT_MAX_TOKENS_PER_TICKET` | how long the assistant may work on one ticket before it hands over to you |
| `AGENT_CONVERSATIONS_PER_HOUR` | a cap per person, against floods |
| `TOOL_WRITE_LIMIT`, `TOOL_WRITE_WINDOW`, `TICKET_CREATE_LIMIT` | how many account changes and tickets one person can cause in a window |
| `APPROVAL_TIMEOUT`, `ACCESS_APPROVAL_TIMEOUT` | how long an MFA or access approval waits before it expires |
| `TOOL_DENIED_GROUPS`, `RESET_DENIED_GROUPS`, `MFA_DENIED_GROUPS` | which groups no tool may change, and whose members never get a reset link or an MFA clear |
| `KB_STALE_AFTER_DAYS` | when a knowledge base article counts as stale |
| `TICKET_RETENTION_DAYS` | how long chat text and assistant tickets are kept after they end |

## Add a tool

A new tool is a code change in `internal/tools`, reviewed like any other. The conventions are in [internal/tools/AGENTS.md](../../internal/tools/AGENTS.md); the short path:

1. **Pick the tier first.** Read only: `read`. A small change to the requester's own account or ticket: `low`. Anything a human should approve: `medium`. Anything the assistant must never do: `blocked`. When unsure, go one tier higher.
2. **Pick the handler kind**, because it decides the guards. A handler that changes a directory account takes `AccountDeps`, and that alone puts it behind the kill switch and `TOOL_WRITE_LIMIT`. Anything else takes `Deps`, which holds no writer at all.
3. **Add one registry entry** in `registry.go` (or its own file): name, a description the model reads, the fields with their limits, and the argument and result keys the audit log may keep. Free text is never kept; the registry refuses to build if you try.
4. **Refuse with the refusal table**, never with free text. Add a row to `refusal.go` and to `allRefusals` in `refusalcode.go`.
5. **If it reaches a new client system**, add that package to both deny lists (`.golangci.yml` rule `agent-credential-boundary` and `cmd/agent/boundary_test.go`), so the agent can never import it.
6. **Write the tests next to the code**, then regenerate the tool spec:

   ```bash
   go test ./internal/tools/ -run TestCatalogMatchesHandoverDoc -update
   ```

   Commit the changed [tools.md](tools.md) with the code. CI fails if you forget.
7. **Teach the prompt** when to use it, add eval tickets that check it (including one where it must not run), and run the eval.

## Rotate secrets

Every secret of the install sits in `deploy/clusters/<name>/secrets.env` and, sealed, in git. The general path for one value:

1. Change the value in `secrets.env`.
2. Run `make install CLUSTER=<name>`. It reseals only what changed, pushes, and restarts only the workloads that read it.

Some secrets also need a step at the system that issued them:

| Secret | Where to make the new one | Detail |
|---|---|---|
| `ANTHROPIC_API_KEY` | the Anthropic console; revoke the old key after the release | general path |
| `SLACK_BOT_TOKEN`, `SLACK_APP_TOKEN` | the Slack app settings | [slack-workspace.md](../runbooks/slack-workspace.md#rotating-or-revoking-a-token) |
| Entra app secret or certificate (`GRAPH_*`) | the app's **Certificates & secrets** in Entra, two weeks before expiry | [entra-tenant.md](../runbooks/entra-tenant.md#9-rotate-a-client-secret) |
| `ZAMMAD_TOKEN` | generated by the installer in the demo; at a real client, a new token for the assistant's Zammad user, then delete the old one | general path |
| `TOOLSERVER_TOKEN` | any long random value; both programs read it, and both restart | general path |
| `GRAFANA_ADMIN_PASSWORD` | general path, then restart Grafana | [observability.md](../runbooks/observability.md#the-grafana-admin-password) |
| Samba account passwords (`LDAP_BIND_PASSWORD`, `RESET_LDAP_BIND_PASSWORD`) | only by resetting the demo directory | [deploy.md](../runbooks/deploy.md#reset-the-demo-directory) |
| Backup bucket keys (`BACKUP_S3_*`) | Cloudflare R2, a token scoped to the one bucket | general path |

If you think a secret leaked, rotate it first and read the audit log second. The tool server never logs a secret, and the agent never holds a client system one.

## Switch to read only, or off

**Read only** stops every account change (unlock, reset link, MFA clear, group add) while the assistant keeps answering questions, searching the knowledge base, opening tickets, and escalating to you:

```bash
kubectl -n noord-agent patch configmap noord-kill-switch --type merge -p '{"data":{"mode":"read_only"}}'
```

It takes effect in about a minute with no restart. Check that the dashboard's Account changes tile says `read_only`. Use `read_write` in the same command to switch back. Only the exact text `read_write` allows changes; anything else, including an unreadable file, means read only. ArgoCD ignores this value on purpose, so it never flips it back for you.

Do this first whenever something looks wrong: an unexpected change in AD, a model outage that makes replies strange, or a security concern.

**Off** stops the assistant entirely. Slack messages get no reply until you resume, and the reset page stays up so links already sent still work:

```bash
make pause CLUSTER=<name>
```

`make resume CLUSTER=<name>` brings it back. To remove everything, including the database volumes, use `make uninstall CLUSTER=<name>` ([deploy.md](../runbooks/deploy.md#uninstall)); bucket backups stay.

## Other runbooks you may need

- [mfa-approvals.md](../runbooks/mfa-approvals.md): what an MFA approval card shows and what to check before you click Approve
- [password-reset.md](../runbooks/password-reset.md): who never gets a link, voiding a link by hand
- [data-retention.md](../runbooks/data-retention.md): what is purged, when, and how to check it ran
- [eval.md](../runbooks/eval.md): running and reading the eval
- [ci-runner.md](../runbooks/ci-runner.md): the CI runners
