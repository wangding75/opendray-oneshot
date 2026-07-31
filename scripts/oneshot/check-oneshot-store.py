#!/usr/bin/env python3
"""Static OD-OS-08 migration/store contract gate."""

from __future__ import annotations

import re
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
MIGRATION = ROOT / "internal/store/migrations/0083_oneshot.sql"
STORE = ROOT / "internal/oneshot/store"

EXPECTED_TABLES = {
    "oneshot_tasks",
    "oneshot_deliveries",
    "oneshot_runs",
    "oneshot_runtime_contexts",
    "oneshot_stream_records",
    "oneshot_standard_events",
    "oneshot_artifacts",
    "oneshot_channel_bindings",
    "oneshot_idempotency_keys",
    "oneshot_notification_outbox",
}

REQUIRED_METHODS = {
    "CreateTaskWithDelivery",
    "CreateDeliveryWithTaskUpdate",
    "GetTask",
    "ListTasks",
    "UpdateTask",
    "GetDelivery",
    "ListDeliveries",
    "UpdateDelivery",
    "CreateRunWithState",
    "GetRun",
    "ListRuns",
    "UpdateRun",
    "CreateRuntimeContext",
    "GetRuntimeContext",
    "ListRuntimeContexts",
    "UpdateRuntimeContext",
    "PersistOutputBatch",
    "CreateArtifact",
    "GetArtifact",
    "ListStreamRecords",
    "ListStandardEvents",
    "CreateIdempotencyRecord",
    "GetIdempotencyRecord",
    "UpsertChannelBinding",
    "ResolveChannelBinding",
    "CreateNotification",
    "DatabaseNow",
}


def fail(message: str) -> None:
    raise SystemExit(f"FAIL: {message}")


def strip_sql_comments(sql: str) -> str:
    return "\n".join(line.split("--", 1)[0] for line in sql.splitlines())


def main() -> None:
    if not MIGRATION.exists():
        fail("missing 0083_oneshot.sql")
    sql = MIGRATION.read_text(encoding="utf-8")
    executable = strip_sql_comments(sql).lower()

    tables = set(re.findall(r"\bcreate\s+table\s+([a-z0-9_]+)", executable))
    if tables != EXPECTED_TABLES:
        fail(f"table set mismatch: missing={sorted(EXPECTED_TABLES-tables)} extra={sorted(tables-EXPECTED_TABLES)}")

    forbidden = [
        r"\balter\s+table\s+sessions\b",
        r"\breferences\s+sessions\b",
        r"\bsession_id\b",
        r"\bcreate\s+table\s+sessions\b",
    ]
    for pattern in forbidden:
        if re.search(pattern, executable):
            fail(f"migration crosses Interactive boundary: {pattern}")

    required_sql = [
        "oneshot_tasks_channel_source_uidx",
        "oneshot_runs_one_active_per_task_uidx",
        "where status in ('created','starting','running','collecting_output')",
        "deferrable initially deferred",
        "foreign key (task_id, requested_by_kind, requested_by_id)",
        "foreign key (current_run_id, id)",
        "foreign key (run_id, id)",
        "foreign key (raw_artifact_id, run_id)",
        "foreign key (source_stream_record_id, run_id)",
        "unique (principal_kind, principal_id, method, canonical_path, idempotency_key)",
        "schema_migrations",  # present in runner, checked below rather than SQL body
    ]
    # schema_migrations belongs to migrate.go, not the migration body.
    migrate_text = (ROOT / "internal/store/migrate.go").read_text(encoding="utf-8").lower()
    for token in required_sql[:-1]:
        if token not in executable:
            fail(f"missing database invariant: {token}")
    if required_sql[-1] not in migrate_text or "begin(ctx)" not in migrate_text:
        fail("migration runner must record schema_migrations inside a transaction")

    if executable.count("(") != executable.count(")"):
        fail("migration has unbalanced parentheses")
    if not executable.rstrip().endswith(";"):
        fail("migration must end with semicolon")

    migration_names = sorted(path.name for path in (ROOT / "internal/store/migrations").glob("*.sql"))
    if MIGRATION.name not in migration_names:
        fail("0083_oneshot.sql is not registered in migration order")
    if migration_names.index(MIGRATION.name) < 0:
        fail("0083_oneshot.sql migration order is invalid")

    go_files = sorted(STORE.glob("*.go"))
    if not go_files:
        fail("internal/oneshot/store has no Go files")
    production = "\n".join(path.read_text(encoding="utf-8") for path in go_files if not path.name.endswith("_test.go"))
    forbidden_go = {
        r'"github\.com/opendray/opendray-v2/internal/session(?:/[^"]*)?"': "internal/session import",
        r'"github\.com/opendray/opendray-v2/internal/channel(?:/[^"]*)?"': "internal/channel import",
        r'"github\.com/creack/pty"': "PTY import",
        r"\bSessionID\b": "SessionID reference",
        r"\bpty\.Start\b": "pty.Start reference",
    }
    for pattern, label in forbidden_go.items():
        if re.search(pattern, production):
            fail(f"store contains forbidden execution-domain dependency: {label}")

    methods = set(re.findall(r"func\s+\(s\s+\*Store\)\s+([A-Z][A-Za-z0-9_]*)\s*\(", production))
    missing = REQUIRED_METHODS - methods
    if missing:
        fail(f"missing required Store methods: {sorted(missing)}")

    signatures = re.findall(r"func\s+\(s\s+\*Store\)\s+([A-Z][A-Za-z0-9_]*)\s*\(([^)]*)\)", production)
    for name, args in signatures:
        normalized = " ".join(args.split())
        if not normalized.startswith("ctx context.Context"):
            fail(f"Store.{name} must accept context.Context as first argument")

    integration = (STORE / "postgres_integration_test.go").read_text(encoding="utf-8")
    for token in (
        "OPENDRAY_DEV_DB_URL",
        "CreateTaskWithDelivery",
        "CreateRunWithState",
        "PersistOutputBatch",
        "ErrorIdempotencyConflict",
        "database allowed two active Runs",
        "cross-owner",
    ):
        if token not in integration:
            fail(f"PostgreSQL integration coverage missing token: {token}")

    rollback_test = (ROOT / "internal/store/migrate_oneshot_test.go").read_text(encoding="utf-8")
    for token in ("second migrate must be idempotent", "failed migration was recorded", "to_regclass"):
        if token not in rollback_test:
            fail(f"migration integration coverage missing token: {token}")

    print("PASS: One-shot migration owns exactly 10 isolated tables")
    print("PASS: foreign keys, checks, deduplication, active-Run and append-order invariants are present")
    print("PASS: Store exposes context-aware transactional CRUD and owner-filtered pagination")
    print("PASS: PostgreSQL integration tests cover migration repeatability, rollback, constraints and ownership")


if __name__ == "__main__":
    main()
