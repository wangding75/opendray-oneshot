#!/usr/bin/env python3
"""Executable source-level compatibility gate for the interactive PTY domain.

This checker is intentionally independent from the Go and Flutter toolchains.
It protects the frozen OD-OS-01 contract in environments where those toolchains
are not available. Runtime tests remain mandatory when the release gate sets
PTY_REGRESSION_REQUIRE_GO=1 and PTY_REGRESSION_REQUIRE_MOBILE=1.
"""

from __future__ import annotations

import argparse
import re
import sys
from dataclasses import dataclass
from pathlib import Path


@dataclass(frozen=True)
class Check:
    name: str
    path: str
    patterns: tuple[str, ...]
    mode: str = "all"


def _read(root: Path, relative: str) -> str:
    path = root / relative
    if not path.is_file():
        raise AssertionError(f"missing required file: {relative}")
    return path.read_text(encoding="utf-8")


def _assert_patterns(root: Path, check: Check) -> None:
    text = _read(root, check.path)
    matches = [re.search(pattern, text, flags=re.MULTILINE | re.DOTALL) is not None for pattern in check.patterns]
    ok = all(matches) if check.mode == "all" else any(matches)
    if not ok:
        missing = [pattern for pattern, matched in zip(check.patterns, matches) if not matched]
        raise AssertionError(
            f"{check.name}: {check.path} no longer satisfies the frozen PTY contract; "
            f"missing pattern(s): {missing}"
        )


def _checks() -> tuple[Check, ...]:
    return (
        Check(
            "repository Go baseline",
            "go.mod",
            (r"^go\s+1\.25(?:\.0)?\s*$",),
        ),
        Check(
            "PTY process launch",
            "internal/session/manager.go",
            (
                r'"github\.com/creack/pty"',
                r"\bpty\.Start\s*\(",
                r"type\s+runningSession\s+struct\s*\{.*?pty\s+\*os\.File",
            ),
        ),
        Check(
            "live Session runtime state",
            "internal/session/manager.go",
            (
                r"type\s+runningSession\s+struct\s*\{.*?ring\s+\*RingBuffer",
                r"type\s+runningSession\s+struct\s*\{.*?vt\s+vt10x\.Terminal",
                r"type\s+runningSession\s+struct\s*\{.*?subs\s+map\[chan\s+\[\]byte\]struct\{\}",
            ),
        ),
        Check(
            "Session service PTY operations",
            "internal/session/handler.go",
            (
                r"Input\(ctx\s+context\.Context,\s+id\s+string,\s+data\s+\[\]byte\)\s+error",
                r"Resize\(ctx\s+context\.Context,\s+id\s+string,\s+cols,\s+rows\s+uint16\)\s+error",
                r"Subscribe\(ctx\s+context\.Context,\s+id\s+string\)",
                r"Buffer\(ctx\s+context\.Context,\s+id\s+string,\s+since\s+int64\)",
            ),
        ),
        Check(
            "Session HTTP PTY routes",
            "internal/session/handler.go",
            (
                r'r\.Post\("/input",\s*h\.input\)',
                r'r\.Post\("/resize",\s*h\.resize\)',
                r'r\.Get\("/buffer",\s*h\.buffer\)',
                r'r\.Get\("/stream",\s*h\.stream\)',
                r'r\.Post\("/start",\s*h\.start\)',
                r'r\.Post\("/stop",\s*h\.stop\)',
            ),
        ),
        Check(
            "session adapter target resolution priority",
            "internal/session/channeladapter/binding_store.go",
            (
                r"func\s+\(s\s+\*MemoryBindingStore\)\s+Resolve\s*\(",
                r"reply_to_outbound_msg_id",
                r"if\s+sessionID\s*:=\s*s\.active\[scope\].*?if\s+sessionID\s*:=\s*s\.last\[scope\]",
            ),
        ),
        Check(
            "conversation-scoped interactive routing state",
            "internal/session/channeladapter/binding_store.go",
            (
                r"last\s+map\[string\]string",
                r"active\s+map\[string\]string",
                r"outbound\s+map\[string\]map\[string\]outboundBinding",
                r"scopeKey\(channelID,\s*conversationID\s+string\)",
            ),
        ),
        Check(
            "rune-by-rune PTY submission",
            "internal/session/channeladapter/input_submitter.go",
            (
                r"func\s+\(s\s+\*InputSubmitter\)\s+Submit\s*\(",
                r"for\s+_,\s*r\s*:=\s*range\s+text",
                r"\[\]byte\(string\(r\)\)",
                r"\[\]byte\{'\\r'\}",
            ),
        ),
        Check(
            "mobile raw PTY input API",
            "app/mobile/lib/core/api/sessions_api.dart",
            (
                r"Future<void>\s+input\(String\s+id,\s+String\s+data\)",
                r"'/api/v1/sessions/\$id/input'",
                r"data:\s*\{'data':\s*data\}",
            ),
        ),
        Check(
            "mobile terminal resize API",
            "app/mobile/lib/core/api/sessions_api.dart",
            (
                r"Future<void>\s+resize\(String\s+id,",
                r"'/api/v1/sessions/\$id/resize'",
                r"data:\s*\{'cols':\s*cols,\s*'rows':\s*rows\}",
            ),
        ),
        Check(
            "mobile PTY websocket and resize forwarding",
            "app/mobile/lib/features/sessions/session_terminal_view.dart",
            (
                r"class\s+SessionTerminalView",
                r"/api/v1/sessions/\$sessionId/stream",
                r"\.resize\(widget\.sessionId,\s*cols:\s*width,\s*rows:\s*height\)",
            ),
        ),
        Check(
            "session adapter binding regression tests",
            "internal/session/channeladapter/binding_store_test.go",
            (
                r"TestMemoryBindingStoreResolutionPriority",
                r"TestMemoryBindingStoreScopesByChannelAndConversation",
                r"TestMemoryBindingStoreExpiresReplyBinding",
            ),
        ),
        Check(
            "session adapter input regression tests",
            "internal/session/channeladapter/input_submitter_test.go",
            (
                r"TestInputSubmitterRuneByRuneThenEnter",
                r"TestInputSubmitterStopsAfterMidStreamFailure",
            ),
        ),
        Check(
            "mobile PTY API contract tests",
            "app/mobile/test/core/api/sessions_api_contract_test.dart",
            (
                r"input keeps the existing PTY endpoint and raw data payload",
                r"resize keeps the existing PTY endpoint and dimensions",
            ),
        ),
        Check(
            "PTY contract document",
            "docs/development/oneshot/contracts/pty-baseline.md",
            (
                r"Status:\s*Frozen by `OD-OS-01`",
                r"Channel-to-PTY call chain",
                r"Prohibited regressions",
            ),
        ),
        Check(
            "PTY test coverage matrix",
            "docs/development/oneshot/contracts/pty-test-matrix.yaml",
            (
                r"contract:\s*OD-OS-01",
                r"runtimeRequiredAtFinalGate:\s*true",
                r"sourceGate:",
            ),
        ),
    )


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--repo", default=str(Path(__file__).resolve().parents[2]))
    args = parser.parse_args()
    root = Path(args.repo).resolve()

    failures: list[str] = []
    for check in _checks():
        try:
            _assert_patterns(root, check)
            print(f"PASS: {check.name}")
        except AssertionError as exc:
            failures.append(str(exc))
            print(f"FAIL: {exc}", file=sys.stderr)

    if failures:
        print(f"PTY source baseline failed: {len(failures)} check(s)", file=sys.stderr)
        return 1

    print(f"PTY source baseline passed: {len(_checks())} checks")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
