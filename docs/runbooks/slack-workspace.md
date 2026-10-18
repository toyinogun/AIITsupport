# Runbook: Noord's Slack workspace and the assistant app

This sets up the Slack half of Noord (spec [0006](../specs/0006-helpdesk-kb-slack/index.md)): a free workspace, the bot app built from `deploy/slack/manifest.yaml`, two channels, and at most two demo accounts. Follow it once; it takes about 20 minutes.

Never put a token, a workspace or channel id, or a real email address in this file or any tracked file. Tokens go in `.env`; demo emails go in `seed/directory/overlay.local.yaml`. Both are gitignored.

## What you end up with

| Thing | Where it lives |
|---|---|
| A free workspace called "Noord Logistics" | slack.com, owned by you |
| The app "Noord IT Assistant", with Socket Mode on | api.slack.com/apps |
| The bot token (`xoxb-...`) | `SLACK_BOT_TOKEN` in `.env` |
| The app level token (`xapp-...`) with `connections:write` | `SLACK_APP_TOKEN` in `.env` |
| `#it-help` (public) and `#it-escalations` (private), the bot in both | the workspace |

## 1. Create the workspace

1. Go to **https://slack.com/get-started** and create a new workspace with an email you control. The free plan is enough.
2. Name it **Noord Logistics**. Skip inviting people for now.
3. Create two channels: `#it-help` (public) and `#it-escalations` (private).

The free plan hides messages older than 90 days, and Slack hosts the data in the US. The data processing note (#22) already lists Slack as a processor.

## 2. Create the app from the manifest

1. Open **https://api.slack.com/apps**, choose **Create New App**, then **From a manifest**.
2. Pick the Noord Logistics workspace.
3. Paste the whole of `deploy/slack/manifest.yaml`, check the summary (Socket Mode on, interactivity on, the messages tab on, bot events `app_mention` and `message.im`, and the five bot scopes), and choose **Create**.
4. Choose **Install to Workspace** and allow it. Copy the **Bot User OAuth Token** (`xoxb-...`) into `SLACK_BOT_TOKEN` in `.env`.
5. Open **Basic Information**, **App-Level Tokens**, **Generate Token and Scopes**. Name it `socket`, add the scope `connections:write`, and generate. Copy the token (`xapp-...`) into `SLACK_APP_TOKEN` in `.env`.

Set both tokens or neither. With one set, the agent refuses to start and names the missing one.

## 3. Add the bot to the channels

In `#it-help` and in `#it-escalations`, type `/invite @Noord IT Assistant`. The bot only hears @mentions in channels it belongs to.

## 4. Demo accounts (optional, at most two)

The agent will match a Slack user to a Noord user by email (#10), so each demo account needs an email that `seed/directory/overlay.local.yaml` gives a Noord user.

1. Pick at most two Noord users, for example `eva.peters`, and give each a real inbox you control (a plus address such as `you+eva@...` works).
2. Put the pairs in `seed/directory/overlay.local.yaml` (copy `overlay.example.yaml`), then run `make noord-reset` so AD, Entra, and Zammad pick them up.
3. Invite each address to the workspace and accept the invite.

Zammad never mails these addresses: its email channels are off.

## 5. Check it works

```bash
make dev
```

1. The agent log shows `slack_connected`. With no tokens set it shows `slack_disabled` instead, and that is fine too.
2. DM the bot "hello". Within 5 seconds it answers in a thread with the holding reply, and the log shows one `slack_event_received` line (event id, channel id, event type; never your text).
3. Mention the bot in `#it-help`. It answers in that message's thread.
4. Edit or delete your message: no new reply.

Record the timing in the spec's `verify.md` (AC-12).

## Rotating or revoking a token

- Bot token: **OAuth & Permissions**, **Revoke Tokens**, then reinstall the app and update `SLACK_BOT_TOKEN`.
- App level token: **Basic Information**, **App-Level Tokens**, revoke `socket` and generate a new one, then update `SLACK_APP_TOKEN`.

A revoked or wrong token makes the agent exit with an error naming the setting (`invalid_auth`), never the value.

## Lockouts from typos

Not Slack, but worth knowing for demos: a wrong password at the Zammad login counts toward the AD lockout (5 tries in 15 minutes), exactly as at a real hybrid company. `make directory-login NAME=<user>` shows the lock state.
