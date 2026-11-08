# ROI estimate

This page estimates what the assistant saves and costs Noord per month. The client has not yet answered the discovery question about ticket volume and minutes per ticket (see the [discovery doc](../discovery.md#open-questions-for-the-client)), so **every input below is an assumption**, numbered so you can replace it. Only the model cost and the share of tickets the assistant can finish come from measurement (the [eval report](eval-report.md)). Swap in your real numbers and the arithmetic still holds.

## The assumptions

| # | Assumption | Value | Why this value |
|---|---|---|---|
| A1 | Staff | 80 people | the client profile |
| A2 | Tickets per person per month | 1.5, so 120 tickets a month | a common figure for an office plus warehouse workforce; replace with Zammad's real count |
| A3 | Share of tickets in scope (identity, access, knowledge base questions) | 40%, so **48 tickets a month** | discovery says resets and lockouts are "a large share"; 40% is a cautious reading |
| A4 | IT minutes per in scope ticket today | 12 minutes | includes reading, checking the account, acting, replying, and the context switch |
| A5 | Loaded cost of IT time | €45 an hour | salary plus overhead for one IT generalist in the Netherlands, rounded |
| A6 | What happens to in scope tickets | 55% closed by the assistant alone, 20% closed after a one click approval, 25% handed to IT | the real mix is more routine than the eval set; the eval set's own mix is used as the pessimistic case below |
| A7 | IT minutes saved per ticket | closed alone: 12; after approval: 10 (about 2 minutes to read the card and click); handed to IT: 3 (identity checked, ticket written, context attached) | from A4 |
| A8 | Model cost per conversation | $0.035 (three times the eval's measured $0.0115 per single message ticket) | real conversations run longer than one message |
| A9 | Exchange rate | 1 USD = 0.90 EUR | rounded |
| A10 | Hosting | €40 a month | the assistant's share of the cluster (two small pods, its Postgres, monitoring) plus backup storage, on hardware Noord already runs |
| A11 | Upkeep by IT | 2 hours a month (€90) | the morning dashboard glance, `kb-review` tickets, one release a month, an occasional eval run |

## The arithmetic (base case)

| Line | Calculation | Per month |
|---|---|---|
| Average minutes saved per ticket | 0.55 × 12 + 0.20 × 10 + 0.25 × 3 | 9.35 minutes |
| Hours saved | 48 tickets × 9.35 minutes | 7.5 hours |
| Value of time saved | 7.5 hours × €45 | **€337** |
| Model cost | 48 × $0.035 × 0.90 | €1.50 |
| Hosting | A10 | €40 |
| Upkeep | A11 | €90 |
| **Net saving** | | **about €205 a month, €2,450 a year** |

**Break even** is about **19 in scope tickets a month**: each ticket saves about €7.00 of IT time and costs about €0.03 in model calls, against about €130 of fixed monthly cost.

The model is the smallest line by far. Even at ten times A8 the base case barely moves. The numbers that matter are A3 (volume) and A4 (minutes per ticket).

## How sensitive it is

| Case | In scope tickets a month | Mix | Hours saved | Net per month |
|---|---|---|---|---|
| Low volume | 25 | base (A6) | 3.9 | about €45 |
| **Base** | **48** | **base** | **7.5** | **about €205** |
| Pessimistic mix | 48 | the eval set: 28% alone, 20% approval, 52% to IT (6.9 minutes saved) | 5.5 | about €120 |
| Double volume | 100 | base | 15.6 | about €570 |
| Four times volume | 200 | base | 31.2 | about €1,265 |

One cross check: discovery says MFA re-registration alone "costs hours every week". If that is two hours a week, it is about 8.7 hours a month on its own, more than the whole base case saves. So the base case is probably low, and the real volume is the first number worth getting from the client.

## What this leaves out

**Not counted as savings, but real:**

- **Waiting time for staff.** An unlock takes about 7 seconds instead of waiting for IT, which matters most on Monday mornings when lockouts pile up. Warehouse staff and drivers locked out of a shared device stop working until it is fixed.
- **The audit trail.** Every access change now names who asked, who approved, and when. The auditor raised its absence; fixing it by hand would cost IT time this estimate does not count.
- **Access requests that stalled** because nobody knew the group owner. The assistant finds the owner and asks them.

**Not counted as costs:**

- **The build itself.** The engagement fee is a one time cost. Payback in months is that fee divided by the net monthly saving above.
- **A wrong action.** The final eval had 0 unsafe actions and 1 wrong one in 150 attempts (a missing ticket, no account change). A wrong action in production would cost IT time to find and undo; the audit log and the kill switch keep that cost small but not zero.
- **Price changes.** Model prices are in `eval/pricing.yaml` (dated 2026-10-06). Moving to EU bound inference (see [data-processing.md](data-processing.md#where-model-inference-runs)) may cost more per call; at these volumes it would still be a few euros a month.

## Replacing the assumptions with real numbers

1. **A2 and A3:** count last quarter's Zammad tickets, and how many were lockouts, resets, MFA, access, or "how do I" questions.
2. **A4:** ask the IT person to time ten typical tickets.
3. **A6:** after a month in production, read the real mix from the dashboard (Resolved, Escalated, Refused) and the approvals count.
4. **A8:** read cost per conversation from the dashboard.

Then redo the base case table with those values.
