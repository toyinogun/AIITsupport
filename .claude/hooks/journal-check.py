#!/usr/bin/env python3
"""Stop hook: ask Claude to log unlogged incidents in docs/journal/.

Reads the session transcript, counts failed tool calls since the last write
to docs/journal/, and blocks the stop once per batch of new failures so
Claude can decide whether any of them deserve a journal entry.
"""

import json
import os
import sys
import tempfile
from collections.abc import Iterator
from pathlib import Path

MIN_NEW_ERRORS = int(os.environ.get("JOURNAL_MIN_ERRORS", "2"))
JOURNAL_DIR = "/docs/journal/"
WRITE_TOOLS = {"Write", "Edit", "MultiEdit"}
IGNORED_ERROR_MARKERS = (
    "doesn't want to proceed",
    "user rejected",
    "permission",
    "was blocked",
)
STATE_DIR = Path(tempfile.gettempdir()) / "claude-journal-check"


def iter_content(transcript_path: str) -> Iterator[dict]:
    with open(transcript_path, encoding="utf-8") as handle:
        for line in handle:
            try:
                entry = json.loads(line)
            except json.JSONDecodeError:
                continue
            content = (entry.get("message") or {}).get("content")
            if isinstance(content, list):
                yield from (item for item in content if isinstance(item, dict))


def is_counted_error(item: dict) -> bool:
    if item.get("type") != "tool_result" or not item.get("is_error"):
        return False
    text = str(item.get("content", "")).lower()
    return not any(marker in text for marker in IGNORED_ERROR_MARKERS)


def is_journal_write(item: dict) -> bool:
    if item.get("type") != "tool_use" or item.get("name") not in WRITE_TOOLS:
        return False
    path = str((item.get("input") or {}).get("file_path", ""))
    return JOURNAL_DIR in path and not path.endswith("_template.md")


def errors_since_last_journal_write(transcript_path: str) -> tuple[int, int]:
    total_errors = 0
    errors_at_last_write = 0
    for item in iter_content(transcript_path):
        if is_counted_error(item):
            total_errors += 1
        elif is_journal_write(item):
            errors_at_last_write = total_errors
    return total_errors, total_errors - errors_at_last_write


def read_reviewed(session_id: str) -> int:
    try:
        return int((STATE_DIR / session_id).read_text())
    except (OSError, ValueError):
        return 0


def write_reviewed(session_id: str, count: int) -> None:
    STATE_DIR.mkdir(parents=True, exist_ok=True)
    (STATE_DIR / session_id).write_text(str(count))


def main() -> None:
    payload = json.load(sys.stdin)
    if payload.get("stop_hook_active"):
        return
    transcript_path = payload.get("transcript_path")
    session_id = payload.get("session_id") or "unknown"
    if not transcript_path or not os.path.exists(transcript_path):
        return

    total_errors, unlogged = errors_since_last_journal_write(transcript_path)
    new_since_review = total_errors - read_reviewed(session_id)
    if unlogged < MIN_NEW_ERRORS or new_since_review < MIN_NEW_ERRORS:
        return

    write_reviewed(session_id, total_errors)
    reason = (
        f"Build journal check: {unlogged} failed tool calls in this session "
        "are not covered by a journal entry yet. If any of them was a real "
        "incident (something broke and was fixed, worked around, or cost "
        "real time), use the journal skill now to log it in docs/journal/. "
        "If they were all trivial noise, say so in one line and stop."
    )
    print(json.dumps({"decision": "block", "reason": reason}))


if __name__ == "__main__":
    try:
        main()
    except Exception as error:  # never break the session over a journal nudge
        print(f"journal-check skipped: {error}", file=sys.stderr)
