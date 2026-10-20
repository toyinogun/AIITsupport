# 0048. The CI fork check failed open when a fork is deleted

**Date:** 2026-10-20 · **Week:** 1 · **Feature:** #4 Coding standards & tooling
**Time lost:** ~5 min (estimate) · **Story worthy:** post

## What I saw
The fresh model review of spec 0012 noticed that every job picked its runner with `${{ github.event.pull_request.head.repo.fork && 'ubuntu-latest' || 'rpi01' }}`. When the fork behind a pull request is deleted, `head.repo` is null, so the expression is falsy and picks `rpi01`: outside code would run on the self hosted Raspberry Pi on the tailnet, the one thing the runbook promises never happens.

## What I thought it was
TODO (you): what did you first think it was?

## What it actually was
A deny list that defaults to allow. The expression asked "is this a fork?" and treated "don't know" as "no".

## How I fixed it
Flipped it to an allow list in all eight jobs and the runbook: `${{ (github.event_name == 'push' || github.event.pull_request.head.repo.full_name == github.repository) && 'rpi01' || 'ubuntu-latest' }}`. Only a push to `main` or a pull request whose head is this repo reaches the Pi; anything else, a missing head repo included, runs on GitHub's runners. The runbook also now says to keep fork workflows on "Require approval for all outside collaborators". Not committed yet.

## What I would tell a team doing this for real
(draft) A gate in front of your own hardware should name who is allowed in, never who is kept out.

## Evidence
- Error text or screenshot: the review finding "CI fork check fails open when the head repository is gone, `.github/workflows/ci.yml:26`"
- Commit or PR: not committed yet
- Eval run (if any): none
- Clip file and timestamp: TODO (you): clip file and timestamp
