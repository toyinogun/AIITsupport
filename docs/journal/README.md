# Build journal

Everything that goes wrong while building the Noord Logistics agent, written down the moment it happens. This is the raw material for the LinkedIn post and the "what broke" part of the video, so capture it while the details are fresh, not from memory in week 4.

## How to use it

- **One file per incident:** copy [`_template.md`](_template.md) to `NNNN-short-name.md` (for example `0003-entra-sync-stuck.md`) and fill it in the same evening.
- **Log small things too.** A 20 minute detour is still a data point. You can drop it later; you cannot recover it later.
- **Grab evidence while you can:** the error text, a screenshot, the commit that fixed it, the eval run id. Start a screen recording whenever something looks like it is about to go sideways.
- **Eval failures count.** Each failure category from eval v1 gets an entry, fixed or not.
- **Add a row to the index** below so week 4 is a sorting job, not a writing job.

## Predictions (fill in before week 1)

Write down what you expect to break before you start. Comparing these with the real index makes a strong beat in the post ("I thought X would be the hard part. It was Y.").

1.
2.
3.

## Index

| # | Date | Title | Feature (scope #) | Time lost | Story worthy |
|---|------|-------|-------------------|-----------|--------------|
| [0001](0001-sqlc-refuses-empty-scaffold.md) | 2026-10-17 | sqlc refuses to generate on an empty scaffold | #3 Stack & architecture | ~5 min | no |
| [0002](0002-dev-ports-already-taken.md) | 2026-10-17 | Local dev ports already taken on the laptop | #3 Stack & architecture | ~3 min | no |
| [0003](0003-go-tool-missing-go-sum.md) | 2026-10-17 | go.sum entries missing after adding goose as a tool | #3 Stack & architecture | ~2 min | no |

**Story worthy** is `post`, `video`, `both`, or `no`. In week 4, pick the top three for the launch post and the video.
