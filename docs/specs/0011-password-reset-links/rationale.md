# 0011. Password reset links: decision record

The build spec is [index.md](index.md). This file holds the why.

## Context

The Monday morning pile at Noord is lockouts and forgotten passwords (at least 14 of the 20 seeded tickets). Spec 0008 handles the locked half; when the account is not locked, the assistant today only opens a ticket for Ruben. The scope's bar for #13: a verified user gets a working reset link from Slack, the ticket closes with a summary, and an unverified user gets none.

The forces:

- **Nothing can reset an AD password today, on purpose.** `svc-itsupport` is denied password resets (spec 0004 AC-11, "granting a password reset is a spec change"), and the Entra tool server app cannot reset or issue a Temporary Access Pass (spec 0005). Samba is the source of truth; Entra is a one way sync with no password writeback, so Microsoft's own reset portal cannot fix the password people actually sign in with.
- **The credential rule.** Only the tool server holds client system credentials; the agent never does (root `AGENTS.md`, checked by depguard and a boundary test). Whatever writes the password must live behind that line.
- **A reset needs a browser.** The new password must never pass through Slack or the model (the prompt already says "never ask for or accept a password"), so the employee needs a page outside Slack, which means something the employee's browser can reach, served by a process that can write AD.
- **The identity bar is Slack.** Spec 0008's code checked requester (Slack gate plus a unique email match) is the only identity proof the system has. Noord has no working second channel: Entra SMS is blocked by Microsoft's fraud protection (journal 0017) and Zammad mail is off.
- **Secrets and redaction.** Spec 0010 keeps personal data out of logs and spans and purges message content; a link token is a bearer secret, worse than personal data if it leaks.
- **Slice 3 is second in the cut order**, so the design must stay small enough to build in a few evenings.

## Options considered

### Option 1: Tool server link and page, with a dedicated reset account (chosen)

A `password_reset_link` tool stores a hashed single use token in Postgres; agent code posts the URL; the tool server serves the page on a second listener and writes the password as a new `svc-pwreset` account limited to Reset Password and `lockoutTime` on staff.

**Pros**:
- Keeps one credential holder; reuses the dispatcher, audit recorder, kill switch, fence, and Zammad client as they are.
- The power to reset passwords is isolated in one narrow AD account and one code path.

**Cons**:
- The tool server, which holds every client credential, now answers browser requests.
- A second service account and secret to provision, rotate, and seal.

### Option 2: A separate reset binary

A third binary (`cmd/resetweb`) holds only the reset account and serves the page; the tool server issues tokens into a shared table.

**Pros**:
- The process a browser reaches holds no AD read account, Zammad token, or Graph secret; a compromise of the page leaks only the reset right.

**Cons**:
- A second credential holder breaks the project's core rule and its two boundary checks, which then need an exception.
- It needs its own audit writes, Zammad access for the close, kill switch, and deployment: most of the tool server again, for one page.

### Option 3: A temporary password in the DM

The tool server sets a random temporary password with "must change at next sign in" and the agent DMs it. No web page at all.

**Pros**:
- Smallest build: no listener, no page, no token table.

**Cons**:
- A working password sits in Slack history and would pass through agent code and possibly the model, against the prompt's own rule.
- Not what the scope promises (a link), and it leaves the password usable by anyone who reads the DM until the user changes it.

### Option 4: Point users at Microsoft's self service reset portal

The assistant sends a link to Entra's reset page and lets Microsoft handle identity and the new password.

**Pros**:
- No reset code of our own; Microsoft's identity checks and MFA.

**Cons**:
- It changes only the Entra password; Samba, which users and Zammad sign in against, keeps the old one (no writeback in spec 0005).
- Depends on the timeboxed Entra tenant and on SMS, which Microsoft blocks on this tenant.

## Rationale

Option 1 is the only one that changes the password people actually use (Samba) without moving a credential outside the tool server. Option 4 fails on the first force (Entra is not the source of truth), and Option 3 fails on the secrecy force (a live password in Slack). Option 2 buys real isolation, but at the cost of the project's central safety claim and a second copy of the audit, kill switch, and Zammad plumbing; at Noord's size, a separate listener with no MCP routes, a Tailscale only ingress, no script on the page, and a dedicated `svc-pwreset` account give most of that isolation for a fraction of the code. The narrow account also means a leaked `svc-itsupport` password still cannot reset anyone, so spec 0004's original guarantee stands.

Identity stays at the Slack check (the engineer's choice), made tolerable by the link's shape: DM only, single use, 15 minutes, one account, voided by the next link, three per day, and a ticket IT can see. That risk is written down rather than hidden, with a second factor as the phase 2 follow up.

Calls made while writing, each with its runner up:

- **Who posts the link**: agent code strips `reset_url` and posts it (the engineer's pick), so the token never reaches the model provider or the messages table. Runner up: the model writes the URL, simpler but it leaks the token into history and can mangle it.
- **Password rules text**: read from the domain object (`minPwdLength`, complexity, history) by the existing reader, so AD stays the one source of truth and there is no length setting to keep in step. Runner up: a `RESET_MIN_PASSWORD_LENGTH` setting kept equal to the domain by hand.
- **Double submit and crashes**: a guarded claim (`live` → `used`) committed before AD is touched, reverted on a failed write while unexpired. Two tabs cannot both write, a crash can never leave a reusable link, and no Postgres connection is held across the LDAP call. Runner up (the first draft): `SELECT ... FOR UPDATE NOWAIT` for the whole submit, which rolled back to `live` on a crash after the write and held a connection per submit.
- **No attempt counter** (changed after the cross check, with the engineer): the first draft voided a link after 5 bad submissions. It guarded nothing, since a stolen link works on the first try and AD policy refusals are typos, not guesses; the TTL, single use, and the daily limit already bound a link. Dropping it removed a column, a void reason, a setting, and a page.
- **Escalated threads** (moved after the cross check): the tool refuses at issue time (`already_escalated`, `escalate_first`) instead of the page skipping the close, so the page always closes an open ticket. One rule, in one place, reusing spec 0009's codes.
- **Locked and forgot** (changed after the cross check): "forgot" wins over the lock state, because the reset clears the lockout in the same write; an unlock alone would leave the user still unable to sign in.
- **Flood protection**: a cap on concurrent POSTs (`RESET_MAX_INFLIGHT`) with no new dependency; the 256 bit token already makes guessing hopeless, so the cap only protects AD and Postgres. Runner up: a per client token bucket (`golang.org/x/time/rate`), more precise but a new module for little gain behind Tailscale.
- **CSRF**: none beyond the token. The page sets no cookie and the token in the path is the only authority, so a cross site form has nothing to ride on. Runner up: a double submit cookie, which adds state without adding a secret.
- **Preview safety**: GET never changes state, and the Slack post turns unfurling off, so link previews and scanners cannot burn a link.
- **The issue note failing**: non fatal (`ticket_noted` false), because the link works and the audit row records it. Runner up: fail the call and void the link, which turns a Zammad blip into no link at all.
- **Account changed since issue**: an audited refusal with the `blocked` page; the link stays `live` and simply expires, so no extra void reason is needed.
- **Token hygiene beyond the database**: https outside loopback, form values read only from the body, no HTTP tracing middleware on the reset listener, and spans named by route pattern, so the token stays out of query logs and span names.
