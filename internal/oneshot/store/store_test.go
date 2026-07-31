package store

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestCursorRoundTripAndBounds(t *testing.T) {
	now := time.Date(2026, 7, 27, 12, 30, 45, 123, time.UTC)
	encoded := encodeCursor(now, "otk_cursor")
	limit, cursor, err := normalizePage(PageRequest{Cursor: encoded, Limit: 200})
	if err != nil {
		t.Fatal(err)
	}
	if limit != 200 || cursor == nil || !cursor.CreatedAt.Equal(now) || cursor.ID != "otk_cursor" {
		t.Fatalf("round trip mismatch: limit=%d cursor=%+v", limit, cursor)
	}
	if _, _, err := normalizePage(PageRequest{Limit: 201}); err == nil {
		t.Fatal("limit above contract maximum must fail")
	}
	if _, _, err := normalizePage(PageRequest{Cursor: base64.RawURLEncoding.EncodeToString([]byte(`{"id":"x"}`))}); err == nil {
		t.Fatal("cursor without timestamp must fail")
	}
}

func TestMigrationContractContainsIsolatedOneShotSchema(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	migrationPath := filepath.Join(filepath.Dir(file), "..", "..", "store", "migrations", "0083_oneshot.sql")
	raw, err := os.ReadFile(migrationPath)
	if err != nil {
		t.Fatal(err)
	}
	sql := strings.ToLower(string(raw))
	tables := []string{
		"oneshot_tasks", "oneshot_deliveries", "oneshot_runs",
		"oneshot_runtime_contexts", "oneshot_stream_records",
		"oneshot_standard_events", "oneshot_artifacts",
		"oneshot_channel_bindings", "oneshot_idempotency_keys",
		"oneshot_notification_outbox",
	}
	for _, table := range tables {
		if !strings.Contains(sql, "create table "+table) {
			t.Errorf("migration missing %s", table)
		}
	}
	forbidden := []string{"alter table sessions", "references sessions", "session_id"}
	for _, token := range forbidden {
		if strings.Contains(sql, token) {
			t.Errorf("migration crosses Interactive boundary with %q", token)
		}
	}
	required := []string{
		"oneshot_runs_one_active_per_task_uidx",
		"oneshot_tasks_channel_source_uidx",
		"deferrable initially deferred",
		"check (status in",
		"unique (run_id, sequence)",
		"unique (principal_kind, principal_id, method, canonical_path, idempotency_key)",
	}
	for _, token := range required {
		if !strings.Contains(sql, token) {
			t.Errorf("migration missing required invariant %q", token)
		}
	}
	for _, status := range []string{"'active'", "'busy'", "'invalid'", "'revoked'", "'valid_utf8'", "'lossy_utf8'", "'binary'"} {
		if !strings.Contains(sql, status) {
			t.Errorf("migration missing frozen status %s", status)
		}
	}
}
