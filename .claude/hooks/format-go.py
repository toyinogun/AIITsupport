#!/usr/bin/env python3
"""PostToolUse hook: format a Go file right after Claude edits it.

Runs the pinned golangci-lint formatters (gofumpt plus goimports) on the
edited file, so the pre commit format check never surprises you. It skips
anything that isn't a .go file inside the project, and the generated sqlc
code. It never blocks Claude: any failure goes to stderr and it exits 0.
"""

import json
import os
import subprocess
import sys
from pathlib import Path

GENERATED_DIR = Path("internal/db/queries")
FORMAT_TIMEOUT_SECONDS = 290  # under the hook's 300s, a cold tools build is slow


def target_path(payload: dict, project_dir: Path) -> Path | None:
    raw = (payload.get("tool_input") or {}).get("file_path")
    if not raw:
        return None
    path = Path(raw).resolve()
    if path.suffix != ".go" or not path.is_file():
        return None
    try:
        relative = path.relative_to(project_dir)
    except ValueError:
        return None
    if relative.is_relative_to(GENERATED_DIR):
        return None
    return relative


def main() -> int:
    project_dir = Path(os.environ.get("CLAUDE_PROJECT_DIR", ".")).resolve()
    try:
        payload = json.load(sys.stdin)
    except json.JSONDecodeError as err:
        print(f"format-go: read hook input: {err}", file=sys.stderr)
        return 0

    relative = target_path(payload, project_dir)
    if relative is None:
        return 0

    command = ["go", "tool", "-modfile=tools.mod", "golangci-lint", "fmt", str(relative)]
    try:
        result = subprocess.run(
            command,
            cwd=project_dir,
            capture_output=True,
            text=True,
            timeout=FORMAT_TIMEOUT_SECONDS,
            check=False,
        )
    except (OSError, subprocess.TimeoutExpired) as err:
        print(f"format-go: format {relative}: {err}", file=sys.stderr)
        return 0
    if result.returncode != 0:
        print(f"format-go: format {relative}: {result.stderr.strip()}", file=sys.stderr)
    return 0


if __name__ == "__main__":
    sys.exit(main())
