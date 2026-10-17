# AI IT Support Engineer (Noord Logistics)

Mock FDE engagement. The plan lives in `docs/scope/scope.md`; start there.

## Build journal

- When something in this project breaks and gets fixed, worked around, or costs real time, use the `journal` skill to log it in `docs/journal/` before you finish the turn. This includes eval failures and timeboxes that were hit.
- A Stop hook (`.claude/hooks/journal-check.py`) asks for this automatically when a turn had tool failures that are not logged yet. If none of them were real incidents, say so in one line and stop.

## Public repo

- This repo is public. Never write a real employer or client name, real personal data, or secrets into any file. Use the fictional Noord Logistics data only.
- `prd.md` is private and gitignored. Do not link to it or copy it into tracked files.
