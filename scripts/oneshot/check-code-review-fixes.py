#!/usr/bin/env python3
from pathlib import Path
import sys

ROOT = Path(__file__).resolve().parents[2]


def text(path: str) -> str:
    return (ROOT / path).read_text(encoding="utf-8")


def require(path: str, *tokens: str) -> None:
    content = text(path)
    missing = [token for token in tokens if token not in content]
    if missing:
        raise AssertionError(f"{path}: missing {missing}")


def forbid(path: str, *tokens: str) -> None:
    content = text(path)
    found = [token for token in tokens if token in content]
    if found:
        raise AssertionError(f"{path}: forbidden {found}")


def main() -> int:
    require(
        "internal/oneshot/executor/run_service.go",
        "task, err = domain.RestoreTask(persistedTask)",
        "delivery, err = domain.RestoreDelivery(persistedDelivery)",
        "run, err = domain.RestoreRun(persistedRun)",
        "s.recordFailure(context.WithoutCancel(ctx), owner, &state, saga.StageRunCreated, err, nil)",
    )
    forbid(
        "internal/oneshot/executor/run_service.go",
        "task, _ = domain.RestoreTask(persistedTask)",
        "delivery, _ = domain.RestoreDelivery(persistedDelivery)",
        "run, _ = domain.RestoreRun(persistedRun)",
        "runtimeContext, _ = domain.RestoreRuntimeContext(persistedContext)",
    )
    require(
        "internal/oneshot/api/handler.go",
        "func sourceFromRESTRequest",
        "REST callers cannot create Telegram source identities",
        "REST source routing fields are server-controlled",
        "if err := dec.Decode(&trailing); !errors.Is(err, io.EOF)",
        "func newRequestIDFrom",
    )
    require(
        "internal/oneshot/store/lifecycle.go",
        "WHERE id=$4 AND task_id=$1",
        "FOR UPDATE",
        "ON CONFLICT (aggregate_id,topic) WHERE aggregate_kind='run' DO UPDATE",
        "SET data=oneshot_lifecycle_events.data",
        'domain.NewDomainError(domain.ErrorRunNotFound, "Run not found", nil)',
    )
    require(
        "scripts/oneshot/check-oneshot-queue.sh",
        "internal/oneshot/store/attachment_stub.go",
        "BindDeliveryAttachments",
    )
    require(
        "internal/config/config.go",
        "func validateProviderMinimumVersion",
        "providerMinimumVersionPattern",
        'validateProviderMinimumVersion("codex_minimum_version"',
        'validateProviderMinimumVersion("claude_minimum_version"',
    )
    require(
        "internal/oneshot/api/handler_test.go",
        "TestTaskAndRunReadRoutesReturnOwnerScopedResources",
        "TestContinueCancelAndRetryRoutesCallApplicationServices",
        "TestRunEventsArtifactsAndDownloadRoutes",
        "TestAttachmentStageReadAndDeleteRoutes",
    )
    require(
        "internal/oneshot/adapter/codex.go",
        'CodexMinimumProviderVersion = "0.132.0"',
    )
    require(
        "internal/oneshot/adapter/claude.go",
        'ClaudeMinimumProviderVersion = "2.1.146"',
    )
    forbid(
        "internal/oneshot/adapter/codex.go",
        'MinimumProviderVersion() string { return "0.0.0" }',
    )
    forbid(
        "internal/oneshot/adapter/claude.go",
        'MinimumProviderVersion() string { return "0.0.0" }',
    )
    require(
        ".github/workflows/ci.yml",
        "Backend (PostgreSQL integration)",
        "Mobile (Flutter)",
        "-tags=postgres",
        "flutter analyze",
        "flutter test",
        "flutter build apk --debug",
    )
    require(
        "app/mobile/lib/features/agent_tasks/data/agent_tasks_stream.dart",
        "static AgentStreamFrame decodeFrame",
        "reconnectTimer?.cancel()",
        "unawaited(controller.close())",
    )
    require(
        "internal/oneshot/executor/output_collector.go",
        "CleanupTimeout time.Duration",
        "context.WithTimeout(base, c.cleanupTimeout)",
    )
    require(
        "internal/oneshot/channeladapter/adapter.go",
        "send One-shot continuation acknowledgement",
    )
    require(
        "scripts/oneshot/check-git-diff.sh",
        "source-only archive",
    )
    require(
        "docs/development/oneshot/task-state.yaml",
        "current_phase: post_review_coding_completed",
        "known_p0_p1_remaining: 0",
        "runtime_only_remaining:",
    )
    forbid(
        "docs/development/oneshot/task-state.yaml",
        "last_commit: HEAD",
    )
    forbid(
        "docs/development/oneshot/OPERATIONS.md",
        "worker_enabled = true",
    )
    print("One-shot post-review coding fixes: PASS")
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except AssertionError as exc:
        print(f"FAIL: {exc}", file=sys.stderr)
        raise SystemExit(1)
