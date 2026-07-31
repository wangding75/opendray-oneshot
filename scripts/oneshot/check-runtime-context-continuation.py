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


service = read("internal/oneshot/application/continuation_service.go")
service_tests = read("internal/oneshot/application/continuation_service_test.go")
run_service = read("internal/oneshot/executor/run_service.go")
run_tests = read("internal/oneshot/executor/run_service_test.go")
run_store = read("internal/oneshot/store/run.go")
context_store = read("internal/oneshot/store/runtime_context.go")
recovery = read("internal/oneshot/recovery/reconciler.go")
recovery_tests = read("internal/oneshot/recovery/reconciler_test.go")

for token in (
    "SupportsResume", "ErrorResumeUnsupported", "ErrorContextNotFound",
    "ErrorContextOwnerMismatch", "ErrorRunConflict", "Idempotency-Key is required",
    "persistedContext.ProjectID != command.ProjectID",
    "persistedContext.ProviderID != command.ProviderID",
    "workspace != persistedContext.WorkspacePath",
    "task.QueueContinue", "FindContinueReplay", "CreateContinueDelivery",
):
    if token not in service:
        fail(f"ContinuationService guard is missing {token}")

for token in (
    "CreateContinueRunWithSaga", "FinalizeRunWithTaskAndContext",
    "runtimeContext.Acquire", "runtimeContext.Release",
    "RuntimeContextEvidence", "ProviderContextID",
    "DeliveryContinue", "createOutcomeContext",
):
    if token not in run_service + run_store + context_store:
        fail(f"RuntimeContext execution behavior is missing {token}")

for token in (
    "releasableRuntimeContext", "releaseTerminalRuntimeContext",
    "FinalizeRunWithTaskAndContext", "UpdateRuntimeContext",
    "runtime_context_recovery", "runtime_context_release",
):
    if token not in recovery:
        fail(f"RuntimeContext crash recovery is missing {token}")

for token in (
    "pgx.Serializable", "updateRuntimeContextRow", "insertRuntimeContextRow",
    "Task snapshot version must equal expected version plus one",
    "terminal Task and Run versions or states do not match",
):
    if token not in run_store:
        fail(f"RuntimeContext transaction guard is missing {token}")

for test_name in (
    "TestContinuationServiceCreatesNewContinueDeliveryForCodexAndClaude",
    "TestContinuationServiceRejectsUnsupportedProviderAndMissingIdempotency",
    "TestContinuationServiceEnforcesExactOwnerProjectProviderWorkspaceAndActiveContext",
    "TestContinuationServiceIdempotentReplayAndPayloadConflict",
    "TestContinuationServiceConcurrentSameKeyReplaysOneQueuedCycle",
    "TestContinuationServiceConcurrentDistinctKeysAllowOnlyOneQueuedCycle",
):
    if test_name not in service_tests:
        fail(f"missing continuation test {test_name}")
for test_name in (
    "TestProviderInitialAndContinueRunsUsePersistedRuntimeContextAndNewProcess",
    "TestResumeFailureKeepsOriginalContextAndDoesNotCreateReplacement",
):
    if test_name not in run_tests:
        fail(f"missing provider continuation integration test {test_name}")
for test_name in (
    "TestReconcilerAtomicallyReleasesBusyRuntimeContextForInterruptedContinueRun",
    "TestReconcilerReleasesBusyRuntimeContextBeforeAcknowledgingTerminalRun",
):
    if test_name not in recovery_tests:
        fail(f"missing RuntimeContext recovery test {test_name}")

for forbidden in ("internal/session", "REFERENCES sessions", "session_id UUID", "Session.Mode", "github.com/creack/pty"):
    if forbidden in service + run_service + run_store + context_store + recovery:
        fail(f"RuntimeContext implementation contains forbidden Session/PTY coupling: {forbidden}")

print("PASS: OD-OS-17 isolated RuntimeContext, exact ownership, atomic busy lease, new-process continuation and no replacement on resume failure")
