//go:build postgres

package store

import (
	"context"
	"io"
	"log/slog"
	"os"
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
