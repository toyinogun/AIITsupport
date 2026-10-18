# Runbook: Noord's Entra tenant and the Graph sync

This sets up the cloud half of Noord (spec [0005](../specs/0005-entra-tenant-sync/index.md)): a dedicated Entra ID tenant, two app registrations, and the sync that fills it from Samba AD. Follow it once on evening one, then come back for secret rotation.

Never put a tenant id, client id, client secret, or a real phone number in this file or any tracked file. They go in `.env` (and your real number in `seed/entra/demo.local.yaml`), both gitignored.

## What you end up with

| Thing | Where it lives |
|---|---|
| A tenant on `<name>.onmicrosoft.com`, for Noord only | Entra admin center |
| The **seed app**, which may create, update, delete, and restore users and write phone methods | `SEED_GRAPH_*` in `.env`, read only by `cmd/seed` |
| The **tool server app**, which may read users and MFA methods and change only phone methods | `GRAPH_*` in `.env`, read by the tool server and `seed entra mfa` |

## 1. Create the tenant

1. Sign in to the Azure portal with a free Azure account (the free account route gives you a tenant you fully own).
2. In the Entra admin center, open **Manage tenants**, then **Create**, and pick **Microsoft Entra ID**.
3. Choose an organisation name such as "Noord Logistics (test)" and a domain prefix. Your domain is `<prefix>.onmicrosoft.com`. That value goes in `SEED_ENTRA_DOMAIN`.
4. Switch into the new tenant. Everything below happens there, never in another tenant you own.

## 2. Protect your admin account

1. Turn on two step verification for the Microsoft account you signed up with: **https://account.microsoft.com/security**, **Advanced security options**, **Two step verification** on. That account is the tenant's admin, and this is what protects it.
2. Turn security defaults **off**: **Overview**, **Properties**, **Manage security defaults**, **Disabled**. They only ask ordinary users for MFA when a sign in looks risky, and they cannot run alongside the per user MFA the demo users need (section 7). Spec 0005's Security model records the risk this accepts and why.

## 3. Register the two apps

Do this twice, once per row below: **App registrations**, then **New registration**, single tenant, no redirect URI.

| App | Suggested name | Application permissions (Microsoft Graph) |
|---|---|---|
| Seed app | `noord-seed` | `User.ReadWrite.All`, `User.DeleteRestore.All`, `UserAuthMethod-Phone.ReadWrite.All` |
| Tool server app | `noord-toolserver` | `User.Read.All`, `UserAuthenticationMethod.Read.All`, `UserAuthMethod-Phone.ReadWrite.All` |

For each app:

1. Open **API permissions**, remove the default delegated `User.Read`, then **Add a permission**, **Microsoft Graph**, **Application permissions**, and add exactly the three in its row. Nothing else.
2. Click **Grant admin consent** and check every row shows a green tick.
3. **Wait for consent to propagate.** Give it about 5 minutes. A token issued before consent does not carry the new roles, and each `make` run starts a new process, so it always asks for a fresh token. If a call still gets 403 right after consent, wait and run again before you change anything.
4. Open **Certificates & secrets**, add a **client secret** with a 6 month lifetime, and copy its **Value** (not the Secret ID) straight into `.env`. You cannot see it again later.

If a phone scoped permission (`UserAuthMethod-Phone.ReadWrite.All`) does not work on evening one, replace it on that app with `UserAuthenticationMethod.ReadWrite.All`, write a journal entry, and update spec 0005 (AC-1 allows this).

## 4. Fill in `.env`

```bash
SEED_GRAPH_TENANT_ID=<the tenant id, from Overview>
SEED_GRAPH_CLIENT_ID=<the seed app's Application (client) ID>
SEED_GRAPH_CLIENT_SECRET=<the seed app's secret value>
SEED_ENTRA_DOMAIN=<prefix>.onmicrosoft.com
GRAPH_TENANT_ID=<the same tenant id>
GRAPH_CLIENT_ID=<the tool server app's Application (client) ID>
GRAPH_CLIENT_SECRET=<the tool server app's secret value>
```

Set all three `SEED_GRAPH_*` or none: a partial set is a config error that names what is missing. With none set, `make entra-sync` says "Entra not configured, skipping" and exits 0.

## 5. The thin thread (evening one)

You might run these in order. Each one is safe to repeat.

```bash
make directory-reset
```

```bash
make entra-sync ONLY=eva.peters
```

```bash
make entra-mfa NAME=eva.peters
```

```bash
make entra-mfa NAME=eva.peters CLEAR=1
```

```bash
make entra-sync ONLY=eva.peters
```

What to look for:

- The first sync creates `eva.peters` with a `mobile` phone method (a fictional `+1 20255501NN` number from the 555-01xx range reserved for fiction) and logs `manager not in Entra yet`, since `--only` never creates the manager.
- `entra mfa` lists the methods with numbers masked to their last 2 digits. `CLEAR=1` deletes every phone method, waits until none are listed, and prints the list again.
- The second sync puts the `mobile` method back.
- Time the first sync of one user (create plus phone). Multiply by the number of synced users to check the full run fits AC-2's 3 minutes. Measured Oct 18: a full first run of 79 users took about 2 minutes, a rerun with nothing to change about 25 seconds.

**Licence go or no go.** A 403 on the auth method calls with the permissions above means the Free tier is not enough: start the Entra ID P1 trial and journal it. Result on Oct 18: the Free tier was enough. The tool server app listed and deleted phone methods with no 403.

## 6. The full sync

```bash
make noord-reset
```

That is a fresh Samba directory, then a full sync. The summary log line counts created, updated, restored, deleted, phones set, managers set, managers cleared, managers skipped, and writes. A second `make entra-sync` with nothing changed shows `writes: 0`.

The sync stops before any write, and says why, when:

- `SYNC-Entra-Users` holds a member outside `OU=Users,OU=Noord` (such as an admin account),
- it would delete more than `SEED_ENTRA_DELETE_LIMIT` (default 10) users,
- a user without the `employeeType` marker `noord-sync` holds an in scope `mailNickname` or UPN,
- two managed users share a `mailNickname`.

Users without the marker, your own admin account included, are never edited, restored, or deleted.

## 7. Demo users (for the video)

1. Copy `seed/entra/demo.example.yaml` to `seed/entra/demo.local.yaml` (gitignored) and put your own number on at most 2 synced users.
2. Set `SEED_ENTRA_DEMO_PASSWORD` in `.env`: at least 12 characters, 3 of upper, lower, digit, and symbol, and no part of any staff name. Use only letters, digits, `-`, `_`, and `!` (other symbols break how `make` loads `.env`), and never put 3 letters in a row: that is the easy way past the staff name rule, since 85 names hold many short parts like `van` or `bos`.
3. Run `make noord-reset`. New demo users get the password at create. A demo user that already exists keeps its old password, because the seed app has no right to reset passwords (spec 0005). Reset it yourself: in the Entra admin center, **Users**, the user, **Reset password**. Microsoft shows a temporary password; at the first sign in you are asked for a new one, and you choose the demo password.
4. Make each demo user ask for MFA on every sign in. These steps are manual, once per demo user, and they survive `make noord-reset` (only deleting the user from Entra loses them):
   - **Users**, **All users**, **Per-user multifactor authentication** (under **...** in the toolbar), tick the user, **Enable MFA**.
   - **Authentication methods**, **Policies**, **SMS**: enable it for all users and leave **Use for sign-in** unticked. Microsoft blocks texts on this tenant and retires its own SMS and voice delivery on Feb 1, 2027, so it is not worth scoping further.
5. Register an authenticator app on the demo user. Microsoft blocks texts on this tenant (error 399287, journal 0017), so a code app is the second factor that works: run `make entra-mfa NAME=<username> CLEAR=1`, sign in as the user in a private window, follow **More information required**, and add Microsoft Authenticator or another code app. Then `make entra-sync` puts the seeded mobile back.
6. Sign in as that user at `myaccount.microsoft.com` in a private window. You should get **Verify your identity**, and a code from the app signs you in. That is AC-8's pass condition.
7. When a user leaves the demo file, disable per user MFA for it.

**SMS is retiring.** Microsoft stops delivering SMS and voice MFA on Feb 1, 2027 and has nudged SMS users toward passkeys since Sep 1, 2026. The demo users may see a "register a passkey" prompt after MFA; you can skip it. Spec 0005's Follow-up records what this means for feature #14.

Every other user gets a random 32 character password at create that nobody ever sees.

## 8. Least privilege checks

The live tests check that the tool server app gets 403 on `POST /users` and on creating a Temporary Access Pass, and probe whether Graph lets it read or change your admin's phone methods (our code refuses either way, with "not managed").

```bash
DOCKER_AUTH_CONFIG='{"auths":{}}' TEST_SAMBA=1 TEST_ENTRA=1 go test -race -run Live ./internal/seed/entra/
```

They need Docker and the `.env` settings loaded into your shell (`set -a && . ./.env && set +a`). They never run in CI. The empty `DOCKER_AUTH_CONFIG` stops testcontainers from waiting forever on the Docker credential helper (journal 0011). `TestLiveReadScope` needs only `TEST_SAMBA=1`, no tenant.

**Admin probe result** (Oct 18): Graph lets the tool server app read an unmarked user's phone methods (status 200), as expected with tenant wide rights. Our code refuses it with "not managed" before any such call, and `make entra-mfa` on your admin proves that. The Entra side wall (an administrative unit) is a spec follow up. Both 403 checks passed: the tool server app cannot create users or issue a Temporary Access Pass.

**Expect lag.** Graph's reads trail its writes by tens of seconds. Right after you change or delete something in the portal, the next sync may not see it yet; the one after will. The live tests allow for this by syncing until a run makes no writes.

## 9. Rotate a client secret

Secrets last 6 months. Two weeks before one expires:

1. In the app's **Certificates & secrets**, add a new secret (6 months).
2. Put the new value in `.env` and run `make entra-sync` (seed app) or `make entra-mfa NAME=eva.peters` (tool server app) to prove it works.
3. Delete the old secret in the portal.

An expired or wrong secret fails with "client secret for `SEED_GRAPH_CLIENT_ID` (or `GRAPH_CLIENT_ID`) rejected or expired". `AADSTS7000222` in the detail means expired.

## Troubleshooting

| You see | What it usually means |
|---|---|
| 403 right after granting consent | Consent has not propagated yet; wait 5 minutes and run again |
| 403 on `authentication/methods` or `phoneMethods` | A licence gap: take the P1 trial (section 5) |
| A phone method delete refused | SMS sign in may be enabled for that number; write down the method type and journal it |
| `read the sync scope from SEED_LDAP_URL` | Samba is not running: `make directory-up` |
| `already exists` on create | Another object holds the UPN; the sync reads it again and continues, or stops if it is not managed |
