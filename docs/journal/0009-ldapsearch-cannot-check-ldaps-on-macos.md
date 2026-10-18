# 0009. ldapsearch on macOS cannot check our LDAPS cert

**Date:** 2026-10-18 · **Week:** 0 (before week 1) · **Feature:** #6 Noord directory & seeded mess
**Time lost:** ~8 min (estimate) · **Story worthy:** post (suggestion)

## What I saw
During `/check verify noord directory`, every `ldapsearch` against the freshly seeded directory failed, even though the Go seeder had just connected over the same LDAPS port.

With the `ldapsearch` that ships with macOS:

```
ldap_sasl_bind(SIMPLE): Can't contact LDAP server (-1)
```

With Homebrew OpenLDAP 2.6.13 and `LDAPTLS_CACERT=.dev/certs/ca.pem`:

```
TLS: hostname (<mac hostname>.local) does not match common name in certificate (samba).
TLS: can't connect: TLS: hostname does not match name in peer certificate.
```

## What I thought it was
That Samba was serving the wrong cert, or that our dev cert was missing the `localhost` SAN.

## What it actually was
Two client quirks, neither of them in our code. Apple's `ldapsearch` is built against Secure Transport and only trusts the keychain, so it ignores `LDAPTLS_CACERT` completely. Homebrew OpenLDAP swaps `ldaps://localhost` for the machine's own hostname before it checks the cert name, and no cert can list every laptop's hostname. The served cert was right: its SANs are `localhost`, `samba`, the cluster names, and `127.0.0.1`, the same as `.dev/certs/server.pem`.

## How I fixed it
No code change. Use Homebrew's `ldapsearch` (`/opt/homebrew/opt/openldap/bin/ldapsearch`) and `ldaps://127.0.0.1:55636`. Then it verifies against the dev CA, and it fails as it should when the CA is left out. The step in `docs/specs/0004-noord-directory/verify.md` that says `ldaps://localhost:55636` should switch to `127.0.0.1` and name the Homebrew client. Not committed yet.

## What I would tell a team doing this for real
(draft) When a CLI and your own client disagree about TLS, prove what cert the server actually serves (`openssl s_client`) before you touch the cert.

## Evidence
- Error text or screenshot: the two errors above, from the verify session on 2026-10-18
- Commit or PR: not committed yet
- Eval run (if any): none
- Clip file and timestamp: TODO (you): clip file and timestamp
