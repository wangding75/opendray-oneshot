#!/usr/bin/env python3
from __future__ import annotations

import json
import re
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
FEATURE = ROOT / "app/mobile/lib/features/agent_tasks"
TESTS = ROOT / "app/mobile/test/features/agent_tasks"


def fail(message: str) -> None:
    raise AssertionError(message)


def require_text(path: Path, *needles: str) -> str:
    if not path.is_file():
        fail(f"missing required file: {path.relative_to(ROOT)}")
    text = path.read_text(encoding="utf-8")
    for needle in needles:
        if needle not in text:
            fail(f"{path.relative_to(ROOT)} missing: {needle}")
    return text


def check_balanced_dart(path: Path) -> None:
    text = path.read_text(encoding="utf-8")
    stack: list[tuple[str, int]] = []
    pairs = {")": "(", "]": "[", "}": "{"}
    openers = set(pairs.values())
    i = 0
    quote: str | None = None
    triple = False
    line_comment = False
    block_comment = 0
    while i < len(text):
        c = text[i]
        n = text[i + 1] if i + 1 < len(text) else ""
        if line_comment:
            if c == "\n":
                line_comment = False
            i += 1
            continue
        if block_comment:
            if c == "/" and n == "*":
                block_comment += 1
                i += 2
                continue
            if c == "*" and n == "/":
                block_comment -= 1
                i += 2
                continue
            i += 1
            continue
        if quote:
            if c == "\\":
                i += 2
                continue
            if triple and text.startswith(quote * 3, i):
                quote = None
                triple = False
                i += 3
                continue
            if not triple and c == quote:
                quote = None
            i += 1
            continue
        if c == "/" and n == "/":
            line_comment = True
            i += 2
            continue
        if c == "/" and n == "*":
            block_comment = 1
            i += 2
            continue
        if c in ("'", '"'):
            triple = text.startswith(c * 3, i)
            quote = c
            i += 3 if triple else 1
            continue
        if c in openers:
            stack.append((c, i))
        elif c in pairs:
            if not stack or stack[-1][0] != pairs[c]:
                fail(f"unbalanced {c} in {path.relative_to(ROOT)} at offset {i}")
            stack.pop()
        i += 1
    if quote or block_comment or stack:
        fail(f"unterminated token in {path.relative_to(ROOT)}")


def main() -> int:
    required_files = [
        FEATURE / "domain/agent_task_models.dart",
        FEATURE / "data/agent_tasks_api.dart",
        FEATURE / "data/agent_tasks_repository.dart",
        FEATURE / "data/agent_tasks_stream.dart",
        FEATURE / "presentation/agent_task_controllers.dart",
        FEATURE / "presentation/agent_tasks_screen.dart",
        FEATURE / "presentation/create_agent_task_screen.dart",
        FEATURE / "presentation/agent_task_detail_screen.dart",
        FEATURE / "presentation/agent_tasks_strings.dart",
        FEATURE / "presentation/widgets/agent_task_status_badge.dart",
    ]
    for path in required_files:
        if not path.is_file():
            fail(f"missing feature file: {path.relative_to(ROOT)}")

    for path in list(FEATURE.rglob("*.dart")) + list(TESTS.rglob("*.dart")):
        check_balanced_dart(path)

    all_feature = "\n".join(path.read_text(encoding="utf-8") for path in FEATURE.rglob("*.dart"))
    forbidden = [
        r"package:opendray/features/sessions/",
        r"package:xterm/",
        r"\bTerminalController\b",
        r"\bTerminalView\b",
        r"\bSessionDetail\b",
    ]
    for pattern in forbidden:
        if re.search(pattern, all_feature, re.IGNORECASE):
            fail(f"Agent Tasks feature crosses PTY/Session boundary: {pattern}")

    models = require_text(
        FEATURE / "domain/agent_task_models.dart",
        "enum AgentTaskStatus",
        "waiting_input",
        "timed_out",
        "class AgentTask",
        "class AgentRun",
        "class AgentEvent",
        "class AgentArtifact",
        "class AgentProviderCapability",
        "class AgentEventTimeline",
        "reply_address",
        "source_message_id",
        "supports_non_interactive",
        "PTY manifest supportsImages is not a One-shot attachment contract",
    )
    for status in (
        "pending", "queued", "running", "waiting_input", "completed",
        "failed", "cancelled", "timed_out",
    ):
        if status not in models:
            fail(f"missing Task status: {status}")

    api = require_text(
        FEATURE / "data/agent_tasks_api.dart",
        "/api/v1/oneshot/tasks",
        "/continue",
        "/cancel",
        "/retry",
        "/events",
        "/artifacts",
        "Idempotency-Key",
        "oneshot.artifact_integrity_failed",
        "sha256.convert",
        "sha-256=",
        "next_cursor",
        "NextCursor",
        "metadataLengthMatches",
    )
    if api.count("Idempotency-Key") < 3:
        fail("create/continue/retry do not all send Idempotency-Key")

    stream = require_text(
        FEATURE / "data/agent_tasks_stream.dart",
        "Authorization",
        "Bearer $token",
        "cursor",
        "Timer(backoff",
        "maxBackoff",
        "AgentStreamCursorTracker",
        "AgentStreamErrorMapper",
        "error.isUnauthorized",
        "IOWebSocketChannel.connect",
        "pingInterval",
    )
    if "backoff.inMilliseconds * 2" not in stream:
        fail("WebSocket reconnect is missing exponential backoff")

    controllers = require_text(
        FEATURE / "presentation/agent_task_controllers.dart",
        "loadMore",
        "setStatus",
        "_loadAllEvents",
        "_loadAllArtifacts",
        "clearStreamCursor",
        "_subscribedRunId",
        "_stopRunStream",
        "state.selectedRun?.id != runId",
        "AgentEventTimeline.merge",
        "continueTask",
        "cancelTask",
        "retryTask",
    )
    if "maxHistoricalEvents = 10000" not in controllers:
        fail("historical output replay is not bounded for mobile")

    create = require_text(
        FEATURE / "presentation/create_agent_task_screen.dart",
        "_continueContext",
        "_resumeTaskId",
        "supportsResume",
        "attachments",
        "timeoutSeconds",
        "telegramNotify",
        "newIdempotencyKey",
        "repository.continueTask",
        "_loadResumableTasks",
        "maxCandidates = 500",
        "maxScannedTasks = 2000",
        "repository.createTask",
        "constraints.maxWidth < 620",
    )
    if "if (!_continueContext)" not in create:
        fail("new-task-only timeout/notification fields leak into Continue flow")

    detail = require_text(
        FEATURE / "presentation/agent_task_detail_screen.dart",
        "waitingInput",
        "continueTask",
        "controller.cancel",
        "controller.retry",
        "ListView.builder",
        "stdout",
        "stderr",
        "raw",
        "saveFile",
        "downloadArtifact",
        "_StatusTimeline",
        "launchUrl",
    )
    if "itemCount: visible.length" not in detail:
        fail("large output view is not virtualized")

    router = require_text(
        ROOT / "app/mobile/lib/core/routing/app_router.dart",
        "/agent-tasks/new",
        "/agent-tasks/:id",
        "CreateAgentTaskScreen",
        "AgentTaskDetailScreen",
    )
    home = require_text(
        ROOT / "app/mobile/lib/features/home/home_shell.dart",
        "AgentTasksScreen",
        "Icons.task_alt_outlined",
        "AgentTasksScreen",
    )
    list_screen = require_text(
        FEATURE / "presentation/agent_tasks_screen.dart",
        "allProjects",
        "setProject",
        "PopupMenuButton<String>",
    )
    _ = list_screen
    if home.index("SessionsScreen()") > home.index("AgentTasksScreen()"):
        fail("Agent Tasks is not a first-level peer immediately after Sessions")

    if "this == AgentTaskStatus.completed" in models.split("bool get canRetry", 1)[1].split(";", 1)[0]:
        fail("completed Task must not expose Retry")

    catalog_handler = require_text(
        ROOT / "internal/catalog/oneshot_extension.go",
        "WithOneShotCapabilityResolver",
        "attachOneShotCapability",
    )
    appwire = require_text(
        ROOT / "internal/oneshot/appwire/catalog.go",
        "DescribeCatalogProvider",
        "adapter.ClaudeProviderID",
    )
    _ = catalog_handler, appwire

    page = require_text(
        ROOT / "internal/oneshot/store/store.go",
        '`json:"items"`',
        '`json:"next_cursor,omitempty"`',
    )
    _ = page, router

    language_maps = []
    for locale in ("en", "es", "zh"):
        path = ROOT / f"app/i18n/{locale}.json"
        data = json.loads(path.read_text(encoding="utf-8"))
        if not isinstance(data.get("agentTasks"), dict):
            fail(f"{locale}.json missing agentTasks dictionary")
        language_maps.append(set(data["agentTasks"]))
    if not all(keys == language_maps[0] for keys in language_maps[1:]):
        fail("Agent Tasks i18n keys are not parity across en/es/zh")

    required_tests = {
        "agent_task_models_test.dart": ["AgentEventTimeline", "waiting_input"],
        "agent_tasks_api_contract_test.dart": ["Idempotency-Key", "artifact_integrity_failed"],
        "agent_tasks_stream_test.dart": ["AgentStreamCursorTracker", "opaque-cursor"],
        "agent_tasks_widget_test.dart": ["320", "Duplicate submit taps", "AgentTaskStatus.values"],
    }
    for name, needles in required_tests.items():
        require_text(TESTS / name, *needles)

    print("OD-OS-21/22/23 Flutter Agent Tasks source contract: PASS")
    print(f"feature Dart files: {len(list(FEATURE.rglob('*.dart')))}")
    print(f"specialized tests: {len(required_tests)}")
    print(f"agentTasks i18n keys: {len(language_maps[0])} across en/es/zh")
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except AssertionError as exc:
        print(f"FAIL: {exc}", file=sys.stderr)
        raise SystemExit(1)
