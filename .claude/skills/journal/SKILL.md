---
name: journal
description: Write or update a build journal entry in docs/journal/ for this project. Use it whenever something broke and was fixed, worked around, or cost real time; for example a failing build or test that got fixed, a /debug fix, an eval failure, a service or tool that refused to cooperate, a timebox that was hit (such as the Entra fallback), or a design that had to change after it met reality. Also use it when the user says "log this", "journal this", "add this to the journal", or runs /journal, and when the build journal Stop hook asks for it.
---

# Build journal

The journal in `docs/journal/` is the raw material for the LinkedIn post and the "what broke" part of the video. Your job is to capture incidents while the details are still in this session, so the user only has to add their own judgment later.

## What counts as an incident

Log it when something went wrong and it mattered: it broke and you fixed it, you had to work around it, it changed the plan, or it cost more than about 15 minutes. Skip trivial noise, such as a typo you corrected right away, a file you were only probing for, or a command you retried with a fixed flag.

If you are unsure, log it. A short entry is cheap to drop later; a lost one cannot be rebuilt.

## Steps

1. **Read** `docs/journal/README.md` (the index) and `docs/journal/_template.md`. List the existing `NNNN-*.md` entries.
2. **Find the incidents** in this session that are not logged yet. One distinct problem is one entry. If the problem already has an entry (same root cause), update that entry instead of creating a new one.
3. **Write each entry** as `docs/journal/NNNN-short-name.md`, using the next free number (four digits) and the template's sections:
   - **What I saw:** the exact error text or wrong behavior, copied from the session. Trim long logs to the lines that matter.
   - **What I thought it was:** the first hypothesis from the session, even if it was wrong. If the session does not show one, write `TODO (you): what did you first think it was?`
   - **What it actually was:** the root cause, in one or two sentences. If it was never confirmed, say so.
   - **How I fixed it:** what changed. Add the commit hash from `git log` if there is one, else `not committed yet`.
   - **What I would tell a team doing this for real:** a one line lesson, marked `(draft)` so the user can rewrite it in their voice.
   - **Evidence:** error text, commit or PR, eval run id, and `TODO (you): clip file and timestamp` unless the session names one.
   - **Header fields:** today's date, the build week from the timeline in `docs/scope/scope.md`, the feature number and name from the scope, **Time lost** estimated from the session timestamps and marked `(estimate)`, and **Story worthy** as your suggestion (`post`, `video`, `both`, or `no`).
4. **Add or update the row** in the index table in `docs/journal/README.md`. Remove the empty placeholder row once the first real row exists.
5. **Tell the user** in one or two lines which entries you wrote and which `TODO (you)` fields they should fill in.

## Rules

- **The repo is public.** Never write a real employer or client name, real user data, credentials, tokens, or internal hostnames. Redact them as `[redacted]`. Fictional Noord Logistics data is fine.
- **Do not invent details.** Anything not in the session or the repo is a `TODO (you)` field, never a guess presented as fact.
- **Do not commit or push.** Leave the entries for the user to review; they can ask you to commit.
- Keep entries short. The post needs the story, not the full log.
