#!/usr/bin/env python3
"""Tests for format-go.py, the PostToolUse hook that formats edited Go files.

Run with: python3 -m unittest discover -s .claude/hooks
covers: DM-4 (spec 0002). The hook formats only Go files in the project,
skips the generated sqlc code, and never blocks Claude.
"""

import importlib.util
import io
import json
import os
import subprocess
import tempfile
import unittest
from pathlib import Path
from unittest import mock

HOOK_PATH = Path(__file__).with_name("format-go.py")
SPEC = importlib.util.spec_from_file_location("format_go", HOOK_PATH)
format_go = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(format_go)


def payload_for(path: Path | str | None) -> dict:
    return {"tool_input": {"file_path": str(path)}} if path is not None else {"tool_input": {}}


class ProjectTestCase(unittest.TestCase):
    """Gives each test a throwaway project directory with a few files."""

    def setUp(self) -> None:
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.project = Path(tmp.name).resolve()
        self.go_file = self.write("internal/server/server.go")
        self.generated_file = self.write("internal/db/queries/models.go")
        self.text_file = self.write("docs/notes.md")

    def write(self, relative: str) -> Path:
        path = self.project / relative
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text("package x\n", encoding="utf-8")
        return path


class TargetPathTests(ProjectTestCase):
    def test_returns_the_project_relative_path_for_a_go_file(self) -> None:
        got = format_go.target_path(payload_for(self.go_file), self.project)
        self.assertEqual(got, Path("internal/server/server.go"))

    def test_skips_a_file_that_is_not_go(self) -> None:
        self.assertIsNone(format_go.target_path(payload_for(self.text_file), self.project))

    def test_skips_the_generated_sqlc_code(self) -> None:
        self.assertIsNone(format_go.target_path(payload_for(self.generated_file), self.project))

    def test_skips_a_go_file_outside_the_project(self) -> None:
        with tempfile.TemporaryDirectory() as other:
            outside = Path(other) / "main.go"
            outside.write_text("package main\n", encoding="utf-8")
            self.assertIsNone(format_go.target_path(payload_for(outside), self.project))

    def test_skips_a_go_path_that_does_not_exist(self) -> None:
        missing = self.project / "internal" / "gone.go"
        self.assertIsNone(format_go.target_path(payload_for(missing), self.project))

    def test_skips_a_payload_without_a_file_path(self) -> None:
        self.assertIsNone(format_go.target_path(payload_for(None), self.project))
        self.assertIsNone(format_go.target_path({}, self.project))
        self.assertIsNone(format_go.target_path({"tool_input": None}, self.project))


class MainTests(ProjectTestCase):
    def run_hook(self, stdin: str, run_result=None, run_error: Exception | None = None):
        """Runs main() with the given stdin; returns (exit code, stderr, run mock)."""
        run = mock.Mock(return_value=run_result, side_effect=run_error)
        stderr = io.StringIO()
        with (
            mock.patch.dict(os.environ, {"CLAUDE_PROJECT_DIR": str(self.project)}),
            mock.patch.object(format_go.subprocess, "run", run),
            mock.patch.object(format_go.sys, "stdin", io.StringIO(stdin)),
            mock.patch.object(format_go.sys, "stderr", stderr),
        ):
            code = format_go.main()
        return code, stderr.getvalue(), run

    def test_formats_an_edited_go_file_with_the_pinned_tools(self) -> None:
        done = subprocess.CompletedProcess(args=[], returncode=0, stdout="", stderr="")
        code, stderr, run = self.run_hook(json.dumps(payload_for(self.go_file)), run_result=done)

        self.assertEqual(code, 0)
        self.assertEqual(stderr, "")
        run.assert_called_once()
        command = run.call_args.args[0]
        self.assertEqual(
            command,
            ["go", "tool", "-modfile=tools.mod", "golangci-lint", "fmt", "internal/server/server.go"],
        )
        self.assertEqual(run.call_args.kwargs["cwd"], self.project)

    def test_does_not_run_the_formatter_for_a_skipped_file(self) -> None:
        for path in (self.text_file, self.generated_file):
            with self.subTest(path=path.name):
                code, _, run = self.run_hook(json.dumps(payload_for(path)))
                self.assertEqual(code, 0)
                run.assert_not_called()

    def test_reports_bad_hook_input_without_blocking(self) -> None:
        code, stderr, run = self.run_hook("not json")

        self.assertEqual(code, 0)
        self.assertIn("format-go: read hook input", stderr)
        run.assert_not_called()

    def test_reports_a_formatter_failure_without_blocking(self) -> None:
        failed = subprocess.CompletedProcess(args=[], returncode=3, stdout="", stderr="syntax error\n")
        code, stderr, _ = self.run_hook(json.dumps(payload_for(self.go_file)), run_result=failed)

        self.assertEqual(code, 0)
        self.assertIn("format-go: format internal/server/server.go: syntax error", stderr)

    def test_reports_a_slow_tools_build_without_blocking(self) -> None:
        timeout = subprocess.TimeoutExpired(cmd="go", timeout=format_go.FORMAT_TIMEOUT_SECONDS)
        code, stderr, _ = self.run_hook(json.dumps(payload_for(self.go_file)), run_error=timeout)

        self.assertEqual(code, 0)
        self.assertIn("format-go: format internal/server/server.go", stderr)

    def test_reports_a_missing_go_binary_without_blocking(self) -> None:
        missing = FileNotFoundError("go: not found")
        code, stderr, _ = self.run_hook(json.dumps(payload_for(self.go_file)), run_error=missing)

        self.assertEqual(code, 0)
        self.assertIn("go: not found", stderr)

    def test_timeout_stays_under_the_hook_timeout(self) -> None:
        settings = json.loads((HOOK_PATH.parent.parent / "settings.json").read_text(encoding="utf-8"))
        hook = settings["hooks"]["PostToolUse"][0]["hooks"][0]
        self.assertIn("format-go.py", hook["command"])
        self.assertLess(format_go.FORMAT_TIMEOUT_SECONDS, hook["timeout"])


if __name__ == "__main__":
    unittest.main()
