# Discovery: Noord Logistics B.V.

**Engagement:** AI IT support agent, first line identity and access · **Written:** Oct 17, 2026 · **Length:** three weeks to handover

_Written as if sales just handed over the account. Every later design choice, test, and number is measured against this page. Noord Logistics is fictional._

## The client

Noord Logistics is an 80 person freight forwarder in Lelystad. Its IT is one overloaded generalist and a backlog of tickets that look the same week after week.

- **People:** office staff, warehouse staff, and drivers who share devices
- **Identity:** on premises Active Directory synced to Entra ID, partway through a migration, so not every user exists in both places yet
- **Tools:** Microsoft 365, Slack for chat, a self hosted ticketing system
- **Support model:** one IT generalist, with an outsourced MSP for anything they cannot handle
- **Constraint:** they hold EU customer data, so GDPR and EU data residency are not up for negotiation

## What hurts (from the discovery call)

1. **Password resets and lockouts** are a large share of all tickets, and they pile up on Monday mornings.
2. **MFA re-registration** after someone changes phones costs hours every week.
3. **Access requests stall.** "Can I get access to X" sits unanswered because nobody knows who owns which group.
4. **The knowledge base is out of date,** so the same questions come back again and again.
5. **There is no audit trail** of who granted which access, and the auditor has noticed.

## What success means (the client signs off on these)

| # | Success criterion | How we will show it |
|---|---|---|
| 1 | Routine tickets are resolved end to end, with no human | At least 70% of routine eval tickets closed correctly by the agent alone |
| 2 | Every privileged action needs a human approval and is logged | 0 unsafe actions in the eval; an audit row for every tool call, naming the approver |
| 3 | All data and model inference stay in the EU | EU hosted cluster, EU region model endpoint, a data processing note that says so |
| 4 | Ticket quality and resolution time are measured, not guessed | A 50 ticket eval with published results, failures included; routine tickets closed in under 60 seconds |
| 5 | The IT person can run, tune, and switch off the agent alone | A runbook, a one command install, and a kill switch that drops it to read only |

## In scope

First line IT support for identity and access, delivered in Slack, with a ticket for every conversation.

- Account unlocks and password reset links
- MFA re-registration, with IT approval
- Group membership lookups and access requests, approved by the group owner
- Creating, updating, and closing tickets with a summary
- Answers from the knowledge base, with links to the article
- Escalation to a human, with the full context attached

## Out of scope

- **Hardware, printers, and on site fixes.** The agent can open a ticket for these, but it will not try to fix them.
- **Admin tier changes,** such as Domain Admins or Global Admin. The agent refuses, explains why, and escalates. This is blocked in code, not just in the prompt.
- **Mailbox and SharePoint permissions.** A possible stretch later, not part of this engagement.

## Why the scope is this narrow

The goal is something that works at Noord on a Monday morning, not a demo that does everything. Identity and access tickets are the highest volume, the most repetitive, and the easiest to check for right or wrong, so they are the best place to earn trust before the agent does anything riskier.

## Open questions for the client

- How many tickets a month, and roughly how many minutes each one takes? (We need this for the ROI estimate.)
- Who should approve MFA resets when the IT person is away?
- How long should ticket bodies be kept? (This sets the retention value.)
