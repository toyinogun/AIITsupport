# Runbook: password reset links

This page is for you as Noord's IT person. It says what a password reset link is, what you see in the helpdesk and the audit log, and how to pause, void, or troubleshoot one (spec [0011](../specs/0011-password-reset-links/index.md)).

## What happens

1. An employee DMs the assistant that they forgot their password (or it expired, or they are locked out but the account is not locked).
2. The assistant opens a helpdesk ticket and calls `password_reset_link`. The tool server makes a single use link, valid for `RESET_LINK_TTL` (default 15 minutes), for that one account.
3. Agent code (never the model) posts the link in the DM, with link previews off. The conversation is marked `resolved`; the ticket stays **open**.
4. The employee opens the link and sets a new password on a small page the tool server serves on its own listener (`RESET_HTTP_ADDR`). The tool server writes it to AD as `svc-pwreset`, which also clears any lockout.
5. The tool server adds a note to the ticket and closes it.

An unused link just expires. The ticket stays open with its "link sent" note, so you see who never finished. A newer link for the same account voids the older one.

## What you see

**In the helpdesk**, on the conversation's ticket:

| Note | Means |
|---|---|
| `Password reset link sent to the requester. Single use, valid until YYYY-MM-DD HH:MM UTC.` | a link went out; the ticket waits for the reset |
| `The requester set a new password with the reset link.` (plus `The account lockout was cleared.` when it was locked) | the reset worked; the ticket is closed |

A ticket with only the first note after its expiry time means the link was never used. Follow up, or close it yourself.

**In the audit log** (`audit_log`), each link leaves two pairs of rows:

| `tool_name` | When | `result` keeps |
|---|---|---|
| `password_reset_link` | the link was issued | `link_id`, `expires_at`, `expires_in_minutes`, `voided_previous`, `ticket_noted` |
| `password_reset` | the employee submitted the page | `was_locked`, `ticket_closed`; `arguments` keeps `link_id` |

Neither holds the link, its token, or a password. `password_reset` is not a tool the model can call; only the reset page writes it.

```bash
docker compose exec -T postgres psql -U noord -d noord -c "SELECT occurred_at, tool_name, kind, outcome, error, requester_account, result FROM audit_log WHERE tool_name IN ('password_reset_link','password_reset') ORDER BY id DESC LIMIT 20;"
```

**In the links table** (`reset_links`): one row per link, holding only `sha256(token)`, the account, the thread, the ticket, and a status (`live`, `used`, `voided`). A `live` row past its `expires_at` is simply expired.

```bash
docker compose exec -T postgres psql -U noord -d noord -c "SELECT directory_account, status, created_at, expires_at, used_at FROM reset_links ORDER BY created_at DESC LIMIT 20;"
```

## Who never gets a link

The tool refuses, and the model tells the user or escalates, when:

| Refusal | Why | What the model does |
|---|---|---|
| `protected` | the account is a member, at any depth, of a built in admin group or a `RESET_DENIED_GROUPS` group (default `SG-IT-Admins`, `APP-Helpdesk-Agents`) | escalates as `privileged_request` |
| `account_disabled` | the account is disabled; a second layer only, since the identity check below already stops a leaver | escalates as `security_concern` |
| `rate_limited` | `RESET_LINK_LIMIT` links (default 3) for this account in `RESET_LINK_WINDOW` (default 24h), or `TOOL_WRITE_LIMIT` for the requester | says so, leaves the ticket open |
| `read_only` | account changes are paused (below) | says so, leaves the ticket open |
| `no_ticket`, `not_yours`, `ticket_closed`, `already_escalated`, `escalate_first` | the ticket or thread is not in a state to send one | follows the prompt for each |

Anyone the Slack identity check does not match to exactly one enabled Noord account never reaches the model at all. That includes a leaver whose account is disabled: they get the fixed `not_matched` reply, and no ticket or link.

`RESET_DENIED_GROUPS` is separate from `TOOL_DENIED_GROUPS`. That one fences which groups a tool may change and includes `SYNC-Entra-Users`, which nearly everyone is in, so it does not decide who gets a link. A name in `RESET_DENIED_GROUPS` that does not exist in AD refuses **every** reset and logs `denied_group_unresolved`; fix the name and restart the tool server.

## Pause resets

The kill switch pauses every account change, resets included. Write `read_only` to the file `KILL_SWITCH_FILE` points at (or start the tool server with `READ_ONLY=true` when no file is set). New links are refused, and a link already sent shows `Password changes are paused right now. Please contact IT.` and stays usable once you switch back to `read_write`.

To turn the feature off entirely, unset the four `RESET_PUBLIC_URL`, `RESET_HTTP_ADDR`, `RESET_LDAP_BIND_DN`, and `RESET_LDAP_BIND_PASSWORD` and restart the tool server. The tool disappears from the model's list and the reset listener does not start. Setting only some of them stops the tool server at start, naming each missing one.

## Void a link by hand

If a link went to the wrong place (a shared screen, a forwarded message), void it. Run as the database owner:

```bash
docker compose exec -T postgres psql -U noord -d noord -c "UPDATE reset_links SET status = 'voided', voided_at = now() WHERE directory_account = '<username>' AND status = 'live';"
```

The page then answers `This link is no longer valid.` The employee can DM the assistant again for a fresh one. A link already `used` cannot be used again; nothing to do.

## When the page says something went wrong

| The page says | Status | Likely cause | What to do |
|---|---|---|---|
| `This link is no longer valid.` | 410 | used, voided, expired, or mistyped | the employee asks for a new link |
| `The directory refused that password.` | 422 | AD's rules (length, complexity) | they choose another; the link still works |
| `Password changes are paused right now.` | 503 | the kill switch | switch back to `read_write` when ready |
| `This link can't be used for this account.` | 403 | the account was disabled, moved out of `OU=Users`, or added to a protected group after the link was sent | handle the reset yourself |
| `The password could not be changed.` | 502 | AD unreachable, or `svc-pwreset` lacks the rights | check the tool server log for `reset_submit` and its `cause` |
| `The service is busy.` | 503 | more than `RESET_MAX_INFLIGHT` submits at once, or the audit log was unreachable | try again in a minute |

Each submit logs one `reset_submit` line with `outcome` (`ok`, `invalid_link`, `paused`, `blocked`, `policy`, `failed`, `try_later`, `revert_failed`) and `cause` (an LDAP result code such as `ldap_50`, a Zammad status, `deadline`, or `network`). `revert_failed` means a failed submit could not put the link back; it stays used, and the employee needs a new one.

If the password was set but Zammad was down, the page still says the password is changed, the audit row shows `ticket_closed` false, and the ticket stays open with its "link sent" note. Close it yourself.

## The reset account, `svc-pwreset`

- It lives in `OU=Service Accounts` and holds exactly two rights on user objects under `OU=Users`: Reset Password, and write on `lockoutTime`. AD refuses it anything else, including any account in `OU=Admin Accounts`. `svc-itsupport` still cannot reset a password.
- Its password is `RESET_LDAP_BIND_PASSWORD`; Compose hands it to Samba as `SAMBA_PWRESET_PASSWORD` on first boot. The account is only created when that password is set.
- A Samba container made before you set the password has no `svc-pwreset`. Rebuild it (this reseeds the directory from `seed/directory/noord.yaml`; Postgres and Zammad are untouched):

  ```bash
  make directory-reset
  ```

- The Samba image sets `old password allowed period = 0`, so the replaced password stops working the moment the reset lands. Samba's default keeps it valid for 60 minutes, LDAP binds included (journal 0038). A Samba container made before this change needs `make directory-reset`.
- To rotate the password: change `RESET_LDAP_BIND_PASSWORD`, run `make directory-reset`, and restart the tool server. In the cluster it is a sealed secret (#17).

## Limits to know

- **Slack is the only identity factor.** Anyone holding an employee's Slack session can reset that employee's AD password, with no IT approval. Accepted for the demo; a second factor is a phase 2 follow up.
- **No password history on a reset.** Samba ignores the control that makes AD apply the history rule to a reset (journal 0036), so an employee can set a password they used before. The page does not claim otherwise.
- **The link is in the URL path**, so the browser history and any proxy that logs paths can hold it until it is used or expires. In the cluster the reset page is reached only through the Tailscale ingress, with path logging off (#17).
- **Entra is not touched.** The new password is set in AD only; the Entra password stays as it was (spec 0005).
- **Old links are deleted** `TICKET_RETENTION_DAYS` after they expired (the tool server logs `reset_links_cleaned`). The audit rows stay, as for every tool.

## Related

- [Spec 0011: password reset links](../specs/0011-password-reset-links/index.md), with [verify.md](../specs/0011-password-reset-links/verify.md)
- [Data retention runbook](data-retention.md)
