# 0014. The MFA clear said done while Graph still listed the phone

**Date:** 2026-10-18 · **Week:** 1 · **Feature:** #7 Entra tenant & sync (timeboxed)
**Time lost:** ~10 min (estimate) · **Story worthy:** video

## What I saw
`make entra-mfa NAME=eva.peters CLEAR=1` exited 0, but its final list still showed the phone:

```
after clearing:
  password
  phone mobile +* ********17
```

A plain `make entra-mfa NAME=eva.peters` a few seconds later showed only `password`.

## What I thought it was
That the delete had not worked at all.

## What it actually was
The delete worked. Graph's two lists settle at different speeds: `/authentication/phoneMethods` was empty right away, while `/authentication/methods` kept listing the phone for a few seconds. The clear waited on the fast list, then printed from the slow one.

## How I fixed it
The clear now waits until neither list shows a phone, and prints the final list from that same check. The fake Graph gained `StaleMethodReads` to model the lag, and `TestClearPhonesWaitsForBothLists` fails on the old logic. Not committed yet.

## What I would tell a team doing this for real
(draft) When an API has two views of the same data, wait on the one your caller will read, not the one that settles first.

## Evidence
- Error text or screenshot: the two outputs above
- Commit or PR: not committed yet
- Eval run (if any): none
- Clip file and timestamp: TODO (you): clip file and timestamp
