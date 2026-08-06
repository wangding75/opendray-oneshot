#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT"

fail() { printf 'FAIL: %s\n' "$*" >&2; exit 1; }

mapfile -t files < <(find internal/oneshot/store -maxdepth 1 -type f -name '*.go' -print | sort)
[[ ${#files[@]} -gt 0 ]] || fail 'internal/oneshot/store has no Go files'
formatted="$(gofmt -d "${files[@]}" internal/store/migrate_oneshot_test.go)"
[[ -z "$formatted" ]] || { printf '%s\n' "$formatted" >&2; fail 'OD-OS-08 Go files are not formatted'; }
echo 'PASS: OD-OS-08 Go files are formatted'

python3 scripts/oneshot/check-oneshot-store.py

if command -v go >/dev/null 2>&1; then
  TMP="$(mktemp -d)"
  trap 'rm -rf "$TMP"' EXIT
  mkdir -p "$TMP/internal/oneshot" "$TMP/internal/store/migrations" "$TMP/stubs/pgx/pgconn" "$TMP/stubs/pgx/pgxpool"
  cp -R internal/oneshot/domain "$TMP/internal/oneshot/domain"
  cp -R internal/oneshot/saga "$TMP/internal/oneshot/saga"
  cp -R internal/oneshot/store "$TMP/internal/oneshot/store"
  cp internal/store/migrations/0083_oneshot.sql "$TMP/internal/store/migrations/"
  cp internal/store/migrations/0084_oneshot_run_saga.sql "$TMP/internal/store/migrations/"
  cp internal/store/migrations/0085_oneshot_control_plane.sql "$TMP/internal/store/migrations/"
  cp internal/store/migrations/0086_oneshot_attachments.sql "$TMP/internal/store/migrations/"
  cp internal/store/migrate_oneshot_test.go "$TMP/internal/store/"
  cat > "$TMP/go.mod" <<'MOD'
module github.com/opendray/opendray-v2

go 1.23.0

require github.com/jackc/pgx/v5 v5.0.0
replace github.com/jackc/pgx/v5 => ./stubs/pgx
MOD
  cat > "$TMP/stubs/pgx/go.mod" <<'MOD'
module github.com/jackc/pgx/v5

go 1.23.0
MOD
  cat > "$TMP/stubs/pgx/pgconn/pgconn.go" <<'GO'
package pgconn

type PgError struct { Code string; ConstraintName string }
func (e *PgError) Error() string { return e.Code }
type CommandTag struct{}
func (CommandTag) RowsAffected() int64 { return 1 }
GO
  cat > "$TMP/stubs/pgx/pgx.go" <<'GO'
package pgx
import (
  "context"
  "errors"
  "github.com/jackc/pgx/v5/pgconn"
)
var ErrNoRows = errors.New("no rows")
type Row interface { Scan(...any) error }
type Rows interface { Next() bool; Scan(...any) error; Close(); Err() error }
type TxIsoLevel string
const Serializable TxIsoLevel = "serializable"
type TxOptions struct { IsoLevel TxIsoLevel }
type Tx interface {
  Exec(context.Context,string,...any)(pgconn.CommandTag,error)
  Query(context.Context,string,...any)(Rows,error)
  QueryRow(context.Context,string,...any) Row
  Commit(context.Context) error
  Rollback(context.Context) error
}
GO
  cat > "$TMP/stubs/pgx/pgxpool/pgxpool.go" <<'GO'
package pgxpool
import (
  "context"
  "github.com/jackc/pgx/v5"
  "github.com/jackc/pgx/v5/pgconn"
)
type Pool struct{}
func (*Pool) Begin(context.Context)(pgx.Tx,error){ return nil,nil }
func (*Pool) BeginTx(context.Context,pgx.TxOptions)(pgx.Tx,error){ return nil,nil }
func (*Pool) Query(context.Context,string,...any)(pgx.Rows,error){ return nil,nil }
func (*Pool) QueryRow(context.Context,string,...any) pgx.Row { return nil }
func (*Pool) Exec(context.Context,string,...any)(pgconn.CommandTag,error){ return pgconn.CommandTag{},nil }
func (*Pool) Close() {}
GO
  cat > "$TMP/internal/store/store_stub.go" <<'GO'
package store
import (
  "context"
  "log/slog"
  "github.com/jackc/pgx/v5/pgxpool"
)
type Store struct{ pool *pgxpool.Pool }
type migrationFile struct{ version, body string }
func Open(context.Context,string,int)(*Store,error){ return &Store{pool:&pgxpool.Pool{}},nil }
func (s *Store) Migrate(context.Context,*slog.Logger) error { return nil }
func (s *Store) Close() {}
func (s *Store) Pool() *pgxpool.Pool { return s.pool }
func (s *Store) ensureMigrationsTable(context.Context) error { return nil }
func (s *Store) applyOne(context.Context,migrationFile) error { return nil }
GO
  (
    cd "$TMP"
    GOTOOLCHAIN=local GOPROXY=off go vet ./internal/oneshot/domain ./internal/oneshot/saga ./internal/oneshot/store
    GOTOOLCHAIN=local GOPROXY=off go test -race ./internal/oneshot/domain ./internal/oneshot/saga ./internal/oneshot/store
    GOTOOLCHAIN=local GOPROXY=off go test -tags postgres -run '^$' ./internal/store ./internal/oneshot/store
  )
  echo 'PASS: isolated One-shot domain/store vet and race tests'
else
  [[ "${ONESHOT_STORE_REQUIRE_GO:-0}" != "1" ]] || fail 'go is required for isolated store validation'
  echo 'SKIP: go unavailable; isolated store tests not run'
fi

if [[ -n "${OPENDRAY_DEV_DB_URL:-}" ]]; then
  GOTOOLCHAIN=auto go test -tags postgres ./internal/store ./internal/oneshot/store
  echo 'PASS: live PostgreSQL migration and Store integration tests'
else
  echo 'SKIP: OPENDRAY_DEV_DB_URL not set; live PostgreSQL tests deferred to provisioned/local environment'
fi

if [[ "${ONESHOT_STORE_ISOLATED_ONLY:-0}" == "1" ]]; then
  scripts/oneshot/check-git-diff.sh
  echo 'OD-OS-08/24 isolated One-shot PostgreSQL Store source gate: PASS'
  exit 0
fi

# The domain gate already runs frozen contracts, architecture boundaries,
# OD-OS-04/05 regression checks, PTY source checks, i18n, and task-state.
scripts/oneshot/check-oneshot-domain.sh

scripts/oneshot/check-git-diff.sh
echo 'OD-OS-08 One-shot PostgreSQL Store source gate: PASS'
