# Handover package: the Noord IT support assistant

This folder is what you get when the engagement ends. Read it in this order. Every page links to the deeper runbook or spec when you need one. Noord Logistics and everyone in it are fictional.

| Page | What it answers |
|---|---|
| [architecture.md](architecture.md) | What runs where, who holds which credential, and how one Slack message becomes an audited action |
| [tools.md](tools.md) | Every tool, its risk tier, its guards, what the audit log keeps, and every refusal. Generated from the code, so it cannot drift |
| [eval-report.md](eval-report.md) | How the assistant scored on 50 test tickets, the first run against the final one, and why each failure happened |
| [runbook.md](runbook.md) | Your everyday jobs: deploy, monitor, tune the prompt, add a tool, rotate secrets, switch to read only or off |
| [data-processing.md](data-processing.md) | What personal data it touches, where it goes, how long it stays, and the one place it leaves the EU |
| [roi.md](roi.md) | What it saves and costs per month, with every assumption written down so you can replace it with your real numbers |

## The short version

- The assistant answers identity and access tickets in Slack: unlocks, password reset links, MFA re-registration (after you approve), access requests (after the group owner approves), and knowledge base questions. Everything else goes to you as a ticket with the context attached.
- It can never make an admin tier change. That is blocked in the tool server's code, not just in the prompt.
- Every tool call writes an audit row before it runs (the intent) and after (the outcome). If the intent row cannot be written, the action does not happen. If the outcome row fails after the action ran, only the intent row is left, so treat an intent with no outcome as "may have happened".
- You can stop all account changes in about a minute with one command, and the assistant keeps answering and filing tickets in read only mode ([runbook.md](runbook.md#switch-to-read-only-or-off)).

## Against the five success criteria

These are the criteria from the [discovery doc](../discovery.md). Two are not fully met, and the pages below say why.

| # | Criterion | Status | Where it is shown |
|---|---|---|---|
| 1 | At least 70% of routine tickets closed correctly by the agent alone | **Partly met.** All 60 routine attempts were handled correctly, but only 12 of the 20 routine tickets are ones the agent should resolve alone (60%, a conversation figure). The other 8 correctly go to you or get refused | [eval-report.md](eval-report.md#against-the-targets) |
| 2 | Every privileged action needs a human approval and is logged | **Met.** 0 unsafe actions in the final run; approvals name the approver in the audit log | [eval-report.md](eval-report.md), [tools.md](tools.md) |
| 3 | All data and model inference stay in the EU | **Not met.** What the assistant stores stays in your cluster and an EU bucket. Model calls go to Anthropic's global endpoint, because there was no EU region when this was built, and Slack stores the chat under its own residency terms | [data-processing.md](data-processing.md#where-model-inference-runs) |
| 4 | Quality and resolution time are measured, routine tickets under 60 seconds | **Met for conversations.** Published eval with failures included; the slowest routine conversation settled in 8.6 seconds. Ticket closure time for reset links (closed when the person uses the link) is not measured | [eval-report.md](eval-report.md) |
| 5 | The IT person can run, tune, and switch off the agent alone | **Met on paper.** One command install, a runbook, a kill switch. Worth a dry run with you before the engagement closes | [runbook.md](runbook.md) |

## Keeping this folder true

- `tools.md` is generated. A test (`TestCatalogMatchesHandoverDoc` in `internal/tools`) fails in CI if it no longer matches the tool registry. After you change a tool, regenerate it (see [runbook.md](runbook.md#add-a-tool)).
- The other pages are written by hand. When you change the prompt, rerun the eval and add the new run to [eval-report.md](eval-report.md). When the client gives real ticket volumes, replace the assumptions in [roi.md](roi.md).
