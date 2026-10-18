# Verify: Entra tenant and Graph sync · spec 0005 · updated 2026-10-18
_Steps derived from spec 0005 acceptance criteria and its Value sourcing table. `/check verify` runs these; `/test` locks the durable ones. Steps marked (tenant) need the real tenant from `docs/runbooks/entra-tenant.md` and `.env` filled in._

## Offline (no tenant, no network)
- [ ] `make check` → lint, gen, tidy, vuln, secrets, and every test pass → AC-16
- [ ] `go test -race ./internal/graph/... ./internal/seed/entra/ ./cmd/seed/ ./internal/config/` → the fake Graph tests pass: plan, number generator, demo validation, marker fence, retry handling → AC-16
- [ ] With `SEED_GRAPH_*` empty in `.env`: `make entra-sync` → logs "Entra not configured, skipping", exits 0 → AC-13
- [ ] Set only `SEED_GRAPH_CLIENT_ID`, then `make entra-sync` → config error naming `SEED_GRAPH_TENANT_ID`, `SEED_GRAPH_CLIENT_SECRET`, `SEED_ENTRA_DOMAIN` → AC-13
- [ ] Stop Samba (`docker compose stop samba`) with `SEED_GRAPH_*` set: `make entra-sync` → fails naming `SEED_LDAP_URL` and the URL, no Graph call → AC-12
- [ ] `DOCKER_AUTH_CONFIG='{"auths":{}}' TEST_SAMBA=1 go test -race -run TestLiveReadScope ./internal/seed/entra/` → the scope read from real Samba is exactly the synced users with their AD managers; an admin account added to `SYNC-Entra-Users` trips the scope guard → AC-3
- [ ] Put 3 users, or a `synced: false` user, or a shared number in `seed/entra/demo.local.yaml`, then `go run ./cmd/seed validate` → fails naming the rule, never echoing the number → AC-8
- [ ] `git ls-files | xargs grep -nE '\+[0-9]{2} ?[0-9]{8,}' | grep -v '20255501'` → every phone shaped value in tracked files is in the 555-01xx fictional range; no tenant id, client id, or secret is tracked → AC-15

## Tenant setup (tenant)
- [ ] Both app registrations show exactly the AC-1 permissions with admin consent; `docs/runbooks/entra-tenant.md` holds no ids or secrets → AC-1
- [ ] Security defaults off; your admin account signs in with your Microsoft account's two step verification; per user MFA shows Enabled on each demo user, and SMS in the authentication methods policy targets only the demo users group → AC-1, AC-8

## Thin thread (tenant)
- [ ] `make directory-reset && make entra-sync ONLY=eva.peters` → `eva.peters` exists in Entra with `employeeType` `noord-sync`, UPN `eva.peters@<SEED_ENTRA_DOMAIN>`, and one `mobile` method; the log says `manager not in Entra yet` → AC-5, AC-4
- [ ] `make entra-mfa NAME=eva.peters` → lists `password` and `phone mobile` with only the last 2 digits visible → AC-9, AC-15
- [ ] `make entra-mfa NAME=eva.peters CLEAR=1` → every phone method deleted, the final list shows none; then `make entra-sync` → the mobile is back → AC-9
- [ ] `make entra-mfa NAME=<your admin's mailNickname>` → fails with "not managed"; `make entra-mfa NAME=made.up` → "not found in Entra" → AC-10

## Full sync (tenant)
- [ ] `make noord-reset` → exits 0 in under 3 minutes; the summary line shows created, updated, restored, deleted, phones_set, managers_set, managers_cleared, managers_skipped, and duration_ms → AC-2, AC-13
- [ ] In Entra: every `SYNC-Entra-Users` member is present; `piotr.nowak`, `olga.petrenko`, `andrzej.zielinski`, `bogdan.ionescu`, `ricardo.silva`, `alex.dobrev`, and `adm.ruben.mulder` are absent → AC-3
- [ ] Spot check `eva.peters`: displayName, givenName, surname, department `Sales`, jobTitle `Account Manager`, companyName `Noord Logistics`, usageLocation `NL`, accountEnabled true, manager `daan.bos` → AC-2, AC-4 (Value sourcing: mapped fields, manager id)
- [ ] A leaver such as `thomas.meijer` is present and still enabled, with stale hints → AC-2 (Value sourcing: accountEnabled from the AD disabled bit)
- [ ] The first user in bytewise sorted `noord.yaml` usernames has `+1 2025550101`; a user further down has its 2 digit position number → AC-5 (Value sourcing: generated number)
- [ ] Run `make entra-sync` again → `writes` is 0 → AC-6
- [ ] In the portal: change one department, delete one user's phone method, delete one user; `make entra-sync` → all three put back, the deleted user restored with the same object id → AC-6
- [ ] Remove a user from `SYNC-Entra-Users` in Samba; `make entra-sync` → that user is in the recycle bin; with 11 removed (or `SEED_ENTRA_DELETE_LIMIT=0` and 1 removed) → exits 1 with no writes → AC-7
- [ ] Create an unmanaged user in the portal with an in scope `mailNickname`; `make entra-sync` → exits 1 naming it, no writes; delete it afterwards → AC-7
- [ ] Every managed user's UPN ends in `@<SEED_ENTRA_DOMAIN>`, never `@noord.example` → AC-2 (Value sourcing: UPN suffix)

## Demo users (tenant)
- [ ] Copy `demo.example.yaml` to `demo.local.yaml` with your number on one user, set `SEED_ENTRA_DEMO_PASSWORD`, run `make noord-reset` → that user's mobile is your number; no log line shows it → AC-8, AC-15
- [ ] A weak demo password, or one holding a staff name → the sync fails before any network call → AC-8
- [ ] Sign in as the demo user at `myaccount.microsoft.com` in a private window → "Verify your identity" appears, and a code from the registered authenticator app signs you in (SMS is blocked by Microsoft, error 399287) → AC-8
- [ ] For a demo user that already existed, reset its password in the portal (Users, the user, Reset password), sign in, and choose the demo password when asked → signing in with it works → AC-8

## Least privilege (tenant)
- [ ] `DOCKER_AUTH_CONFIG='{"auths":{}}' TEST_SAMBA=1 TEST_ENTRA=1 go test -race -run TestLiveSync ./internal/seed/entra/` → passes: `POST /users` and a Temporary Access Pass POST with `GRAPH_*` both get 403; the admin probe result is logged; write it into the runbook and the journal → AC-11

## Timebox
- [ ] By end of Wed Oct 21: the steps above green (manual sign in excepted), or the fallback taken: a journal entry, scope #7 marked fallback, #14 escalates MFA tickets → AC-14

## Acceptance-criteria coverage
- AC-1 tenant setup · AC-2 full sync, spot checks · AC-3 offline live scope test, full sync gap check · AC-4 thin thread, spot check · AC-5 thin thread, number positions · AC-6 rerun, drift · AC-7 deletes, limit, collision · AC-8 demo users, validate · AC-9 thin thread MFA · AC-10 thin thread fence · AC-11 least privilege · AC-12 offline Samba down, unit retries · AC-13 offline unconfigured, `make noord-reset` · AC-14 timebox · AC-15 offline grep, masking, demo logs · AC-16 offline tests
