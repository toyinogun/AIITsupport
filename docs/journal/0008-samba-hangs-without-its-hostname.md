# 0008. Samba starts but never listens unless the hostname is "samba"

**Date:** 2026-10-18 · **Week:** 0 (before week 1) · **Feature:** #6 Noord directory & seeded mess
**Time lost:** ~10 min (estimate) · **Story worthy:** post (suggestion)

## What I saw
`make directory-up` was healthy in 12 seconds, but the same image under testcontainers never became ready. The logs ended with Samba starting, then nothing:

```
entrypoint: provisioned AD.NOORD.EXAMPLE, skeleton and delegation in place
daemon 'samba' : Starting process...
TEST_SAMBA is set but Samba did not start (is Docker running?): run Samba container: generic container: start container: started hook: wait until ready: context deadline exceeded
```

Inside the container: `ldap_sasl_bind(SIMPLE): Can't contact LDAP server (-1)`.

## What I thought it was
First run: a slow first boot hitting a too short wait. That part was real (testcontainers' `WithWaitStrategy` wraps every wait in a 60 second default deadline, whatever the strategy's own timeout says), but a 4 minute deadline still timed out.

## What it actually was
The domain is provisioned with `--host-name=samba`. Compose sets `hostname: samba`, testcontainers gives the container a random one, and under any other hostname Samba starts but never opens its LDAP ports. No error, just silence.

## How I fixed it
The entrypoint now refuses to start when `hostname` is not `samba`, naming the fix. The `sambatest` helper sets the hostname with `WithConfigModifier` and waits with `WithWaitStrategyAndDeadline(4m)`. Live tests then passed in about 40 seconds. Not committed yet.

## What I would tell a team doing this for real
(draft) When a service works in Compose and hangs in tests, diff the container config first: hostname, env, and mounts, before touching timeouts.

## Evidence
- Error text or screenshot: above
- Commit or PR: not committed yet
- Eval run (if any): none
- Clip file and timestamp: TODO (you): clip file and timestamp

## Follow up (2026-10-21, #17 One command GitOps install)
It came back when moving Samba into the cluster. Spec 0015 runs Samba as a StatefulSet, whose pod is always named `samba-0`, and Kubernetes sets the host name to the pod name. I made the entrypoint take its host name from `SAMBA_HOST_NAME` and provision with it, then tested in Docker: as `samba` LDAPS came up in about a minute; as `samba-0`, and even as `sambazero` provisioned to match, Samba provisioned, started, and never answered in 2 minutes. So it is not a mismatch between the provisioned name and the host name: only `samba` works, still with no error. Root cause still unknown.

Consequence: a StatefulSet cannot run this image. A one replica Deployment can, since it honors `hostname: samba` in the pod spec. Spec 0015 was amended the same day (AC-10): Samba runs as that Deployment, with crash safe provisioning, and task 2 must confirm LDAPS answers inside the cluster. Time lost: ~25 min (estimate).
