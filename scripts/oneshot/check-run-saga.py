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


saga = read("internal/oneshot/saga/types.go")
service = read("internal/oneshot/executor/run_service.go")
recovery = read("internal/oneshot/recovery/reconciler.go")
store = read("internal/oneshot/store/saga.go")
run_store = read("internal/oneshot/store/run.go")
store_tests = read("internal/oneshot/store/postgres_integration_test.go")
migration = read("internal/store/migrations/0084_oneshot_run_saga.sql")
service_tests = read("internal/oneshot/executor/run_service_saga_test.go")
recovery_tests = read("internal/oneshot/recovery/reconciler_test.go")
memory = read("internal/oneshot/queue/memory.go")
postgres = read("internal/oneshot/queue/postgres.go")

stages = (
    "run_created", "credential_acquired", "command_built", "process_started",
    "running_persisted", "process_exited", "output_committed",
    "terminal_persisted", "credential_released", "acknowledged",
)
for stage in stages:
    if stage not in saga or stage not in migration:
        fail(f"Saga stage {stage} is not frozen in code and migration")
for field in ("FailureStage", "PrimaryErrorCode", "PrimaryErrorMessage", "CompensationError"):
    if field not in saga or field not in service:
        fail(f"Saga failure audit field {field} is missing")
for step in ("CreateRunWithSaga", "AcquireCredential", "BuildCommand", "StartWithOutput", "FinalizeRunWithTask", "ReleaseCredential"):
    if step not in service:
        fail(f"execution Saga step {step} is missing")
if "insertInitialSagaState" not in run_store or "CreateRunWithSaga" not in run_store:
    fail("Run and run_created Saga checkpoint are not persisted in one transaction")
if "CreateRunWithSaga" not in store_tests or "GetSagaState" not in store_tests:
    fail("live PostgreSQL test does not cover atomic initial Saga creation")
for token in ("ListRecoverableRuns", "AcknowledgeRecovered", "TerminateExistingTree", "StageAcknowledged"):
    if token not in recovery and token not in store:
        fail(f"crash recovery token {token} is missing")
if "if err := r.RunOnce(ctx); err != nil" not in recovery:
    fail("startup recovery errors are ignored")
if "d.run_id IS NULL" not in postgres:
    fail("PostgreSQL dispatch queue does not exclude Deliveries that already own a Run")
if "d.lease_until IS NULL OR d.lease_until<=clock_timestamp()" not in store:
    fail("recovery query does not wait for the active Delivery lease to expire")
if "if delivery.RunID != nil" not in memory:
    fail("memory dispatch queue does not exclude Saga-owned Deliveries")
if "silently acknowledge" not in memory:
    fail("queue/Saga acknowledgement boundary is not documented")
for test_name in (
    "TestRunSagaRunCreationFailureDoesNotStartProcess",
    "TestRunSagaStartFailureAndOutputFailuresAreAuditable",
    "TestRunSagaCrashCheckpointsRemainRecoverable",
    "TestACKFailureDoesNotRerunCompletedProviderProcess",
    "TestCredentialReleaseFailureIsRecordedForRecovery",
):
    if test_name not in service_tests:
        fail(f"missing execution Saga test {test_name}")
for test_name in (
    "TestReconcilerAcknowledgesTerminalRunWithoutRerun",
    "TestReconcilerFinalizesCommittedOutputAndReleasesCredential",
    "TestReconcilerMarksInterruptedRunningRunFailed",
    "TestReconcilerPersistsACKAndCompensationFailuresThenRetries",
    "TestReconcilerRunFailsClosedWhenStartupRecoveryFails",
):
    if test_name not in recovery_tests:
        fail(f"missing recovery test {test_name}")
combined = saga + service + recovery + store + run_store
for forbidden in ("internal/session", "session_transcripts", "github.com/creack/pty"):
    if forbidden in combined:
        fail(f"Saga/recovery contains forbidden dependency: {forbidden}")
print("PASS: OD-OS-13 durable execution Saga, compensation audit, ACK recovery and restart reconciliation")
