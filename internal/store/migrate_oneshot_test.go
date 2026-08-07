//go:build postgres

package store

import (
	"context"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"
)

func postgresMigrationStore(t *testing.T) *Store {
	t.Helper()
	dsn := os.Getenv("OPENDRAY_DEV_DB_URL")
	if dsn == "" {
		t.Skip("OPENDRAY_DEV_DB_URL not set; use a disposable PostgreSQL database")
	}
	st, err := Open(context.Background(), dsn, 2)
	if err != nil {
		t.Skipf("PostgreSQL unavailable: %v", err)
	}
	return st
}

// applyMigrationsUpTo applies all migrations lexically up to and including
// targetVersion (e.g. "0087_oneshot_model_field"). It uses the embedded
// migration filesystem (loadMigrations from migrate.go) and is only available
// when the real migrate.go is compiled — not in the isolated stub environment
// used by check-oneshot-store.sh.
func applyMigrationsUpTo(t *testing.T, st *Store, targetVersion string) {
	t.Helper()
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	files, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	applied := false
	for _, f := range files {
		if err := st.applyOne(ctx, f); err != nil {
			t.Fatalf("apply %s: %v", f.version, err)
		}
		if f.version == targetVersion {
			applied = true
			break
		}
	}
	if !applied {
		t.Fatalf("target migration %s not found or not applied", targetVersion)
	}
	_ = log
}

// insertSeedProvider inserts a minimal provider row for FK satisfaction.
func insertSeedProvider(t *testing.T, st *Store, id string) {
	t.Helper()
	ctx := context.Background()
	_, err := st.pool.Exec(ctx,
		`INSERT INTO providers (id, manifest_hash, config, enabled) VALUES ($1, 'seed', '{}'::jsonb, TRUE) ON CONFLICT (id) DO NOTHING`,
		id,
	)
	if err != nil {
		t.Fatal(err)
	}
}

// insertTask inserts a oneshot_task row with the given model.
func insertTask(t *testing.T, st *Store, taskID, providerID string, model *string) {
	t.Helper()
	ctx := context.Background()
	_, err := st.pool.Exec(ctx, `
		INSERT INTO oneshot_tasks (
			id, principal_kind, principal_id, project_id, provider_id,
			source, source_kind, prompt, status, model
		) VALUES (
			$1, 'admin', 'test', 'test', $2,
			'{}'::jsonb, 'api', 'test prompt', 'pending', $3
		)`, taskID, providerID, model,
	)
	if err != nil {
		t.Fatal(err)
	}
}

// insertRun inserts a oneshot_run row with the given model.
func insertRun(t *testing.T, st *Store, runID, taskID, providerID, deliveryID string, model *string) {
	t.Helper()
	ctx := context.Background()
	_, err := st.pool.Exec(ctx, `
		INSERT INTO oneshot_runs (
			id, task_id, delivery_id, provider_id, status, model
		) VALUES (
			$1, $2, $3, $4, 'created', $5
		)`, runID, taskID, deliveryID, providerID, model,
	)
	if err != nil {
		t.Fatal(err)
	}
}

// insertDelivery inserts a minimal oneshot_delivery row for FK satisfaction.
func insertDelivery(t *testing.T, st *Store, deliveryID, taskID string) {
	t.Helper()
	ctx := context.Background()
	_, err := st.pool.Exec(ctx, `
		INSERT INTO oneshot_deliveries (
			id, task_id, operation, requested_by_kind, requested_by_id,
			input, idempotency_key, payload_sha256, status, max_attempts
		) VALUES (
			$1, $2, 'new', 'admin', 'test',
			'{}'::jsonb, $1 || '-ik', 'e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855', 'pending', 3
		)`, deliveryID, taskID,
	)
	if err != nil {
		t.Fatal(err)
	}
}

func TestOneShotMigrationIsRecordedAndRepeatable(t *testing.T) {
	st := postgresMigrationStore(t)
	defer st.Close()
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := st.Migrate(ctx, log); err != nil {
		t.Fatal(err)
	}
	if err := st.Migrate(ctx, log); err != nil {
		t.Fatalf("second migrate must be idempotent: %v", err)
	}
	var count int
	if err := st.pool.QueryRow(ctx, `SELECT COUNT(*) FROM schema_migrations WHERE version='0083_oneshot'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("schema_migrations count=%d; want 1", count)
	}
}

func TestMigrationFailureRollsBackBodyAndVersion(t *testing.T) {
	st := postgresMigrationStore(t)
	defer st.Close()
	ctx := context.Background()
	if err := st.ensureMigrationsTable(ctx); err != nil {
		t.Fatal(err)
	}
	suffix := time.Now().UTC().Format("20060102150405")
	version := "zz_od08_rollback_" + suffix
	table := "od08_rollback_" + suffix
	body := `CREATE TABLE ` + table + ` (id INT PRIMARY KEY); SELECT definitely_missing_od08_function();`
	if err := st.applyOne(ctx, migrationFile{version: version, body: body}); err == nil {
		t.Fatal("malformed migration unexpectedly succeeded")
	}
	var relation *string
	if err := st.pool.QueryRow(ctx, `SELECT to_regclass($1)`, table).Scan(&relation); err != nil {
		t.Fatal(err)
	}
	if relation != nil {
		t.Fatalf("failed migration left table %s behind", table)
	}
	var count int
	if err := st.pool.QueryRow(ctx, `SELECT COUNT(*) FROM schema_migrations WHERE version=$1`, version).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("failed migration was recorded")
	}
}

// ---------------------------------------------------------------------------
// 0088 model snapshot constraint migration tests
// ---------------------------------------------------------------------------

const migration0088Version = "0088_oneshot_model_snapshot_constraints"

// load0088Body reads the 0088 migration SQL from the embedded filesystem.
func load0088Body(t *testing.T) string {
	t.Helper()
	files, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if f.version == migration0088Version {
			return f.body
		}
	}
	t.Fatalf("migration %s not found in embedded filesystem", migration0088Version)
	return ""
}

// freshMigratedStore returns a Store with all migrations up to 0087 applied.
// It resets the public schema first so each test starts from a clean slate.
func freshMigratedStore(t *testing.T) *Store {
	t.Helper()
	st := postgresMigrationStore(t)
	ctx := context.Background()

	// Reset schema for clean test isolation
	if _, err := st.pool.Exec(ctx, `DROP SCHEMA IF EXISTS public CASCADE; CREATE SCHEMA public;`); err != nil {
		t.Fatal(err)
	}
	// Re-create pgvector extension (dropped with the schema cascade)
	if _, err := st.pool.Exec(ctx, `CREATE EXTENSION IF NOT EXISTS vector;`); err != nil {
		t.Fatal(err)
	}
	// Re-create schema_migrations table (dropped with the schema)
	if err := st.ensureMigrationsTable(ctx); err != nil {
		t.Fatal(err)
	}

	applyMigrationsUpTo(t, st, "0087_oneshot_model_field")
	return st
}

// Case 1: Valid existing data — both task and run have non-empty model.
func TestModelMigration_ValidExistingData(t *testing.T) {
	st := freshMigratedStore(t)
	defer st.Close()
	ctx := context.Background()

	insertSeedProvider(t, st, "valid-prov")
	insertTask(t, st, "otk_valid01", "valid-prov", ptr("model-a"))
	insertDelivery(t, st, "odl_valid01", "otk_valid01")
	insertRun(t, st, "orn_valid01", "otk_valid01", "valid-prov", "odl_valid01", ptr("model-a"))

	body := load0088Body(t)
	if err := st.applyOne(ctx, migrationFile{version: migration0088Version, body: body}); err != nil {
		t.Fatalf("migration 0088 failed on valid data: %v", err)
	}

	// Verify data preserved
	var taskModel string
	if err := st.pool.QueryRow(ctx, `SELECT model FROM oneshot_tasks WHERE id='otk_valid01'`).Scan(&taskModel); err != nil {
		t.Fatal(err)
	}
	if taskModel != "model-a" {
		t.Errorf("task model = %q, want %q", taskModel, "model-a")
	}

	var runModel string
	if err := st.pool.QueryRow(ctx, `SELECT model FROM oneshot_runs WHERE id='orn_valid01'`).Scan(&runModel); err != nil {
		t.Fatal(err)
	}
	if runModel != "model-a" {
		t.Errorf("run model = %q, want %q", runModel, "model-a")
	}
}

// Case 2: Run NULL, task known — run should be backfilled from task.
func TestModelMigration_RunNullTaskKnown(t *testing.T) {
	st := freshMigratedStore(t)
	defer st.Close()
	ctx := context.Background()

	insertSeedProvider(t, st, "backfill-prov")
	insertTask(t, st, "otk_bf01", "backfill-prov", ptr("model-b"))
	insertDelivery(t, st, "odl_bf01", "otk_bf01")
	insertRun(t, st, "orn_bf01", "otk_bf01", "backfill-prov", "odl_bf01", nil)

	body := load0088Body(t)
	if err := st.applyOne(ctx, migrationFile{version: migration0088Version, body: body}); err != nil {
		t.Fatalf("migration 0088 failed: %v", err)
	}

	var runModel string
	if err := st.pool.QueryRow(ctx, `SELECT model FROM oneshot_runs WHERE id='orn_bf01'`).Scan(&runModel); err != nil {
		t.Fatal(err)
	}
	if runModel != "model-b" {
		t.Errorf("run model = %q, want %q (backfilled from task)", runModel, "model-b")
	}
}

// Case 3: Run blank, task known — run should be backfilled from task.
func TestModelMigration_RunBlankTaskKnown(t *testing.T) {
	st := freshMigratedStore(t)
	defer st.Close()
	ctx := context.Background()

	insertSeedProvider(t, st, "blankrf-prov")
	insertTask(t, st, "otk_bf02", "blankrf-prov", ptr("model-c"))
	insertDelivery(t, st, "odl_bf02", "otk_bf02")
	// Insert a blank run model — use raw SQL since insertRun doesn't support blank strings well
	_, err := st.pool.Exec(ctx, `
		INSERT INTO oneshot_runs (id, task_id, delivery_id, provider_id, status, model)
		VALUES ('orn_bf02', 'otk_bf02', 'odl_bf02', 'blankrf-prov', 'created', '   ')
	`)
	if err != nil {
		t.Fatal(err)
	}

	body := load0088Body(t)
	if err := st.applyOne(ctx, migrationFile{version: migration0088Version, body: body}); err != nil {
		t.Fatalf("migration 0088 failed: %v", err)
	}

	var runModel string
	if err := st.pool.QueryRow(ctx, `SELECT model FROM oneshot_runs WHERE id='orn_bf02'`).Scan(&runModel); err != nil {
		t.Fatal(err)
	}
	if runModel != "model-c" {
		t.Errorf("run model = %q, want %q (backfilled from task)", runModel, "model-c")
	}
}

// Case 4: Task model is NULL — migration must FAIL.
// This is the core anti-drift test: we must NOT re-read the current provider config.
func TestModelMigration_TaskNull(t *testing.T) {
	st := freshMigratedStore(t)
	defer st.Close()
	ctx := context.Background()

	insertSeedProvider(t, st, "nulltask-prov")
	// Set the provider's default model in config, simulating a current
	// provider config with a model field — the migration must NOT use it.
	_, err := st.pool.Exec(ctx, `UPDATE providers SET config = '{"model":"model-new"}'::jsonb WHERE id='nulltask-prov'`)
	if err != nil {
		t.Fatal(err)
	}

	insertTask(t, st, "otk_null01", "nulltask-prov", nil)

	body := load0088Body(t)
	err = st.applyOne(ctx, migrationFile{version: migration0088Version, body: body})
	if err == nil {
		t.Fatal("migration 0088 should have FAILED on NULL task model")
	}
	if !strings.Contains(err.Error(), "unresolved") {
		t.Errorf("error should mention unresolved, got: %v", err)
	}

	// Verify task was NOT written with "model-new" (anti-drift)
	var taskModel *string
	if err := st.pool.QueryRow(ctx, `SELECT model FROM oneshot_tasks WHERE id='otk_null01'`).Scan(&taskModel); err != nil {
		t.Fatal(err)
	}
	if taskModel != nil {
		t.Errorf("task model = %v, want nil (must not backfill from current provider config)", taskModel)
	}
}

// Case 5: Task model is blank — migration must FAIL.
func TestModelMigration_TaskBlank(t *testing.T) {
	st := freshMigratedStore(t)
	defer st.Close()
	ctx := context.Background()

	insertSeedProvider(t, st, "blanktask-prov")
	_, err := st.pool.Exec(ctx, `
		INSERT INTO oneshot_tasks (
			id, principal_kind, principal_id, project_id, provider_id,
			source, source_kind, prompt, status, model
		) VALUES (
			'otk_blank01', 'admin', 'test', 'test', 'blanktask-prov',
			'{}'::jsonb, 'api', 'test prompt', 'pending', '   '
		)
	`)
	if err != nil {
		t.Fatal(err)
	}

	body := load0088Body(t)
	err = st.applyOne(ctx, migrationFile{version: migration0088Version, body: body})
	if err == nil {
		t.Fatal("migration 0088 should have FAILED on blank task model")
	}
}

// Case 6: Run unresolved (task also NULL) — migration must FAIL.
func TestModelMigration_UnresolvedRun(t *testing.T) {
	st := freshMigratedStore(t)
	defer st.Close()
	ctx := context.Background()

	insertSeedProvider(t, st, "unresrun-prov")
	insertTask(t, st, "otk_ur01", "unresrun-prov", nil)
	insertDelivery(t, st, "odl_ur01", "otk_ur01")
	insertRun(t, st, "orn_ur01", "otk_ur01", "unresrun-prov", "odl_ur01", nil)

	body := load0088Body(t)
	err := st.applyOne(ctx, migrationFile{version: migration0088Version, body: body})
	if err == nil {
		t.Fatal("migration 0088 should have FAILED on unresolved run")
	}
	if !strings.Contains(err.Error(), "unresolved") {
		t.Errorf("error should mention unresolved, got: %v", err)
	}
}

// Case 7: New constraints — NULL, empty, blank INSERT/UPDATE must fail; normal must succeed.
func TestModelMigration_Constraints(t *testing.T) {
	st := freshMigratedStore(t)
	defer st.Close()
	ctx := context.Background()

	insertSeedProvider(t, st, "constraint-prov")
	insertTask(t, st, "otk_c01", "constraint-prov", ptr("model-d"))
	insertDelivery(t, st, "odl_c01", "otk_c01")
	insertRun(t, st, "orn_c01", "otk_c01", "constraint-prov", "odl_c01", ptr("model-d"))

	body := load0088Body(t)
	if err := st.applyOne(ctx, migrationFile{version: migration0088Version, body: body}); err != nil {
		t.Fatalf("migration 0088 failed: %v", err)
	}

	// Task NULL INSERT must fail
	_, err := st.pool.Exec(ctx, `
		INSERT INTO oneshot_tasks (
			id, principal_kind, principal_id, project_id, provider_id,
			source, source_kind, prompt, status, model
		) VALUES (
			'otk_c02', 'admin', 'test', 'test', 'constraint-prov',
			'{}'::jsonb, 'api', 'test prompt', 'pending', NULL
		)
	`)
	if err == nil {
		t.Fatal("INSERT with NULL task model should fail")
	}

	// Task empty INSERT must fail
	_, err = st.pool.Exec(ctx, `
		INSERT INTO oneshot_tasks (
			id, principal_kind, principal_id, project_id, provider_id,
			source, source_kind, prompt, status, model
		) VALUES (
			'otk_c03', 'admin', 'test', 'test', 'constraint-prov',
			'{}'::jsonb, 'api', 'test prompt', 'pending', ''
		)
	`)
	if err == nil {
		t.Fatal("INSERT with empty task model should fail")
	}

	// Task blank INSERT must fail
	_, err = st.pool.Exec(ctx, `
		INSERT INTO oneshot_tasks (
			id, principal_kind, principal_id, project_id, provider_id,
			source, source_kind, prompt, status, model
		) VALUES (
			'otk_c04', 'admin', 'test', 'test', 'constraint-prov',
			'{}'::jsonb, 'api', 'test prompt', 'pending', '   '
		)
	`)
	if err == nil {
		t.Fatal("INSERT with blank task model should fail")
	}

	// Run NULL INSERT must fail
	_, err = st.pool.Exec(ctx, `
		INSERT INTO oneshot_runs (
			id, task_id, delivery_id, provider_id, status, model
		) VALUES (
			'orn_c02', 'otk_c01', 'odl_c01', 'constraint-prov', 'created', NULL
		)
	`)
	if err == nil {
		t.Fatal("INSERT with NULL run model should fail")
	}

	// Run empty INSERT must fail
	_, err = st.pool.Exec(ctx, `
		INSERT INTO oneshot_runs (
			id, task_id, delivery_id, provider_id, status, model
		) VALUES (
			'orn_c03', 'otk_c01', 'odl_c01', 'constraint-prov', 'created', ''
		)
	`)
	if err == nil {
		t.Fatal("INSERT with empty run model should fail")
	}

	// Run blank INSERT must fail
	_, err = st.pool.Exec(ctx, `
		INSERT INTO oneshot_runs (
			id, task_id, delivery_id, provider_id, status, model
		) VALUES (
			'orn_c04', 'otk_c01', 'odl_c01', 'constraint-prov', 'created', '   '
		)
	`)
	if err == nil {
		t.Fatal("INSERT with blank run model should fail")
	}

	// Normal INSERT must succeed
	_, err = st.pool.Exec(ctx, `
		INSERT INTO oneshot_tasks (
			id, principal_kind, principal_id, project_id, provider_id,
			source, source_kind, prompt, status, model
		) VALUES (
			'otk_c05', 'admin', 'test', 'test', 'constraint-prov',
			'{}'::jsonb, 'api', 'test prompt', 'pending', 'model-e'
		)
	`)
	if err != nil {
		t.Fatalf("INSERT with valid task model should succeed: %v", err)
	}

	// Normal run INSERT must succeed
	insertDelivery(t, st, "odl_c05", "otk_c05")
	_, err = st.pool.Exec(ctx, `
		INSERT INTO oneshot_runs (
			id, task_id, delivery_id, provider_id, status, model
		) VALUES (
			'orn_c05', 'otk_c05', 'odl_c05', 'constraint-prov', 'created', 'model-e'
		)
	`)
	if err != nil {
		t.Fatalf("INSERT with valid run model should succeed: %v", err)
	}

	// UPDATE to NULL must fail
	_, err = st.pool.Exec(ctx, `UPDATE oneshot_tasks SET model = NULL WHERE id = 'otk_c01'`)
	if err == nil {
		t.Fatal("UPDATE to NULL task model should fail")
	}

	// UPDATE to empty must fail
	_, err = st.pool.Exec(ctx, `UPDATE oneshot_tasks SET model = '' WHERE id = 'otk_c01'`)
	if err == nil {
		t.Fatal("UPDATE to empty task model should fail")
	}
}

// Case 8: Failed migration atomicity — unresolved data causes rollback,
// no partial constraints, no version recorded, database remains clean.
func TestModelMigration_FailedMigrationAtomicity(t *testing.T) {
	st := freshMigratedStore(t)
	defer st.Close()
	ctx := context.Background()

	insertSeedProvider(t, st, "atomic-prov")
	insertTask(t, st, "otk_atomic01", "atomic-prov", ptr("model-f"))
	insertDelivery(t, st, "odl_atomic01", "otk_atomic01")
	insertRun(t, st, "orn_atomic01", "otk_atomic01", "atomic-prov", "odl_atomic01", ptr("model-f"))

	// Insert an unresolved task
	insertTask(t, st, "otk_atomic02", "atomic-prov", nil)

	body := load0088Body(t)
	err := st.applyOne(ctx, migrationFile{version: migration0088Version, body: body})
	if err == nil {
		t.Fatal("migration 0088 should have FAILED on unresolved data")
	}

	// 1. Version NOT recorded
	var count int
	if err := st.pool.QueryRow(ctx, `SELECT COUNT(*) FROM schema_migrations WHERE version=$1`, migration0088Version).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("failed migration 0088 was recorded in schema_migrations")
	}

	// 2. No NOT NULL constraint left behind — verify by inserting NULL model
	//    Use a separate task so the one-active-per-task index doesn't conflict.
	insertTask(t, st, "otk_atomic03", "atomic-prov", ptr("model-f"))
	insertDelivery(t, st, "odl_atomic03", "otk_atomic03")
	_, err = st.pool.Exec(ctx, `
		INSERT INTO oneshot_runs (id, task_id, delivery_id, provider_id, status, model)
		VALUES ('orn_atomic03', 'otk_atomic03', 'odl_atomic03', 'atomic-prov', 'created', NULL)
	`)
	if err != nil {
		t.Fatalf("NULL model INSERT should succeed after rollback (no constraint left): %v", err)
	}

	// 3. No blank CHECK constraint left behind
	insertTask(t, st, "otk_atomic04", "atomic-prov", ptr("model-f"))
	insertDelivery(t, st, "odl_atomic04", "otk_atomic04")
	_, err = st.pool.Exec(ctx, `
		INSERT INTO oneshot_runs (id, task_id, delivery_id, provider_id, status, model)
		VALUES ('orn_atomic04', 'otk_atomic04', 'odl_atomic04', 'atomic-prov', 'created', '')
	`)
	if err != nil {
		t.Fatalf("blank model INSERT should succeed after rollback (no CHECK left): %v", err)
	}

	// 4. Existing data unmodified (the valid task/run still have their model)
	var taskModel string
	if err := st.pool.QueryRow(ctx, `SELECT model FROM oneshot_tasks WHERE id='otk_atomic01'`).Scan(&taskModel); err != nil {
		t.Fatal(err)
	}
	if taskModel != "model-f" {
		t.Errorf("task model = %q, want %q after rollback", taskModel, "model-f")
	}

	// 5. Cleanup: the unresolved task is still there
	var unresolvedCount int
	if err := st.pool.QueryRow(ctx, `SELECT COUNT(*) FROM oneshot_tasks WHERE id='otk_atomic02'`).Scan(&unresolvedCount); err != nil {
		t.Fatal(err)
	}
	if unresolvedCount != 1 {
		t.Errorf("unresolved task count = %d, want 1 (not deleted)", unresolvedCount)
	}
}

func ptr(s string) *string { return &s }
