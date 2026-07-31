#!/usr/bin/env python3
from pathlib import Path
import sys

ROOT = Path(__file__).resolve().parents[2]


def fail(message: str) -> None:
    print(f"FAIL: {message}", file=sys.stderr)
    raise SystemExit(1)


def read(path: str) -> str:
    target = ROOT / path
    if not target.is_file():
        fail(f"missing {path}")
    return target.read_text(encoding="utf-8")


types = read("internal/oneshot/adapter/types.go")
common = read("internal/oneshot/adapter/provider_common.go")
codex = read("internal/oneshot/adapter/codex.go")
claude = read("internal/oneshot/adapter/claude.go")
codex_tests = read("internal/oneshot/adapter/codex_test.go")
claude_tests = read("internal/oneshot/adapter/claude_test.go")
process = read("internal/oneshot/executor/process_supervisor.go")

for token in ("Stdin []byte", "RuntimeContextExtractor", "ResultInterpreter", "RuntimeContextEvidence"):
    if token not in types:
        fail(f"provider adapter contract is missing {token}")
if "bytes.NewReader(spec.Stdin)" not in process or "os.Stdin" in process:
    fail("provider prompt is not isolated from gateway stdin")

for token in (
    '[]string{"exec", "--json", "--color", "never"}',
    '"--skip-git-repo-check"', '"--model"', '"model_reasoning_effort=',
    '"--sandbox"', '"-C"', '"resume"', 'ProviderContextID',
):
    if token not in codex:
        fail(f"Codex adapter is missing {token}")
for token in (
    '[]string{"-p", "--output-format", "stream-json", "--verbose"}',
    '"--model"', '"--permission-mode"', '"--max-turns"',
    '"--dangerously-skip-permissions"', '"--resume"', 'ProviderContextID',
):
    if token not in claude:
        fail(f"Claude adapter is missing {token}")

for token in ("OPENAI_API_KEY", "CODEX_API_KEY", "CODEX_HOME"):
    if token not in codex:
        fail(f"Codex secret environment handling is missing {token}")
for token in ("ANTHROPIC_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN", "CLAUDE_CONFIG_DIR"):
    if token not in claude:
        fail(f"Claude secret environment handling is missing {token}")
for token in ("ErrorRateLimited", "ErrorUnauthorized", "ErrorForbidden", "ErrorResumeFailed"):
    if token not in common:
        fail(f"provider failure mapping is missing {token}")
if "state.resume" not in common or "states.prepare" not in codex + claude:
    fail("new and resume Runs are not explicitly distinguished")

for fixture in (
    "internal/oneshot/testdata/codex/success.jsonl",
    "internal/oneshot/testdata/codex/auth-error.jsonl",
    "internal/oneshot/testdata/codex/rate-limit.jsonl",
    "internal/oneshot/testdata/codex/unknown.jsonl",
    "internal/oneshot/testdata/claude/success.jsonl",
    "internal/oneshot/testdata/claude/auth-error.jsonl",
    "internal/oneshot/testdata/claude/rate-limit.jsonl",
    "internal/oneshot/testdata/claude/permission-error.jsonl",
):
    read(fixture)

for test_name in (
    "TestCodexBuildCommandGolden",
    "TestCodexResumeGoldenAndWorkspaceGuard",
    "TestCodexJSONLContextAndUnknownOutput",
    "TestCodexFixtureAndResumeFailureMapping",
    "TestCodexFailureFixtures",
):
    if test_name not in codex_tests:
        fail(f"missing Codex test {test_name}")
for test_name in (
    "TestClaudeBuildCommandGolden",
    "TestClaudeResumeGolden",
    "TestClaudeStreamJSONContextAndErrors",
    "TestClaudeFixtureAndResumeFailureMapping",
    "TestClaudeFailureFixtures",
):
    if test_name not in claude_tests:
        fail(f"missing Claude test {test_name}")

for forbidden in ("internal/session", "github.com/creack/pty", "pty.Start", "Session.Mode"):
    if forbidden in types + common + codex + claude:
        fail(f"provider adapter contains forbidden interactive dependency: {forbidden}")

print("PASS: OD-OS-15/16 Codex and Claude non-interactive adapters, JSONL, context IDs, resume args, redaction and failure mapping")
