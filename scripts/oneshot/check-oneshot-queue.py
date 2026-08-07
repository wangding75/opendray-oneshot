#!/usr/bin/env python3
from pathlib import Path
import re
import sys

ROOT = Path(__file__).resolve().parents[2]


def fail(message: str) -> None:
    print(f"FAIL: {message}", file=sys.stderr)
    raise SystemExit(1)

required = [
    ROOT / "internal/oneshot/queue/postgres.go",
    ROOT / "internal/oneshot/queue/memory.go",
    ROOT / "internal/oneshot/queue/worker.go",
    ROOT / "internal/oneshot/application/dispatch_service.go",
    ROOT / "internal/oneshot/queue/memory_test.go",
    ROOT / "internal/oneshot/queue/postgres_integration_test.go",
    ROOT / "internal/oneshot/application/dispatch_service_test.go",
]
for path in required:
    if not path.is_file():
        fail(f"missing required OD-OS-09 file: {path.relative_to(ROOT)}")

queue_text = "\n".join(path.read_text(encoding="utf-8") for path in (ROOT / "internal/oneshot/queue").glob("*.go"))
postgres = (ROOT / "internal/oneshot/queue/postgres.go").read_text(encoding="utf-8")
application = (ROOT / "internal/oneshot/application/dispatch_service.go").read_text(encoding="utf-8")
tests = "\n".join(path.read_text(encoding="utf-8") for path in (ROOT / "internal/oneshot").rglob("*_test.go"))
migration = (ROOT / "internal/store/migrations/0083_oneshot.sql").read_text(encoding="utf-8")

for forbidden in (
    'github.com/opendray/opendray-v2/internal/session',
    'github.com/opendray/opendray-v2/internal/channel',
    'pty.Start(',
    'SessionID',
):
    if forbidden in queue_text + application:
        fail(f"execution queue crossed frozen boundary: {forbidden}")

required_sql_fragments = [
    "FOR UPDATE OF d SKIP LOCKED",
    "clock_timestamp()",
    "d.run_id IS NULL",
    "d.attempt < d.max_attempts",
    "lease_owner=$2",
    "lease_until",
    "status='retry_wait'",
    "status='acknowledged'",
    "status='dead_letter'",
    "status='cancelled'",
    "oneshot.delivery_exhausted",
    "pgx.Serializable",
    "ON CONFLICT (principal_kind,principal_id,method,canonical_path,idempotency_key) DO NOTHING",
    "Idempotency-Key was reused with a different payload",
]
for fragment in required_sql_fragments:
    if fragment not in postgres:
        fail(f"PostgreSQL queue implementation missing: {fragment}")

# Catch malformed static INSERT statements even when a live PostgreSQL instance
# is not available in the source-validation environment.
for table, expected_count in (("oneshot_tasks", 17), ("oneshot_deliveries", 18)):
    match = re.search(
        rf"INSERT INTO {table} \(\s*(.*?)\s*\) VALUES \((.*?)\)",
        postgres,
        flags=re.DOTALL,
    )
    if match is None:
        fail(f"cannot parse static INSERT for {table}")
    columns = [item.strip() for item in match.group(1).replace("\n", "").split(",") if item.strip()]
    placeholders = re.findall(r"\$([0-9]+)", match.group(2))
    if len(columns) != expected_count or len(set(columns)) != expected_count:
        fail(f"{table} INSERT columns are missing or duplicated: {columns}")
    if placeholders != [str(index) for index in range(1, expected_count + 1)]:
        fail(f"{table} INSERT placeholders are not contiguous: {placeholders}")

if "w.repository.RenewLease" not in (ROOT / "internal/oneshot/queue/worker.go").read_text(encoding="utf-8"):
    fail("queue worker does not renew active Delivery leases")

for field in ("attempt", "max_attempts", "available_at", "lease_owner", "lease_until", "idempotency_key", "payload_sha256"):
    if field not in migration:
        fail(f"0083 One-shot migration missing queue field: {field}")

for fragment in (
    "telegram:",
    "SourceMessageID",
    "CanonicalCreatePayloadSHA256",
    "domain.DeliveryInput",
    "ErrorIdempotencyRequired",
):
    if fragment not in application:
        fail(f"dispatch idempotency implementation missing: {fragment}")

required_tests = [
    "TestMemoryQueueConcurrentClaimHasSingleWinner",
    "TestMemoryQueueLeaseExpiryRecovery",
    "TestMemoryQueueNackBackoffAndExhaustion",
    "TestMemoryQueueAckPreventsRestartDuplicate",
    "TestMemoryQueueExpiredLeaseWithTerminalRunWaitsForSagaReconciler",
    "TestWorkerRenewsLeaseWhileProcessorIsRunning",
    "TestWorkerCancelsProcessorWhenLeaseRenewalFails",
    "TestDispatchServiceIdempotentReplayAndConflict",
    "TestDispatchServiceTelegramDerivesStableKey",
    "TestPostgresQueueCompetitionLeaseRecoveryIdempotencyAndRestart",
]
for name in required_tests:
    if name not in tests:
        fail(f"missing OD-OS-09 regression test: {name}")

print("PASS: OD-OS-09 PostgreSQL queue source contract")
print("PASS: SKIP LOCKED lease claim, expiry recovery, ack/nack/dead-letter/cancel")
print("PASS: API and Telegram idempotency payload binding")
print("PASS: restart protection excludes Deliveries that already own a Run")
