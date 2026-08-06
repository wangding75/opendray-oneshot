#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT"

fail() { printf 'FAIL: %s\n' "$*" >&2; exit 1; }

mapfile -t files < <(find internal/oneshot/adapter internal/oneshot/application internal/oneshot/executor internal/oneshot/queue internal/oneshot/recovery internal/oneshot/saga internal/oneshot/store -type f -name '*.go' -print | sort)
[[ ${#files[@]} -gt 0 ]] || fail 'OD-OS-10 Go files are missing'
formatted="$(gofmt -d "${files[@]}")"
[[ -z "$formatted" ]] || { printf '%s\n' "$formatted" >&2; fail 'OD-OS-10 Go files are not formatted'; }
echo 'PASS: OD-OS-10 Go files are formatted'

python3 scripts/oneshot/check-oneshot-executor.py

if command -v go >/dev/null 2>&1; then
  TMP="$(mktemp -d)"
  trap 'rm -rf "$TMP"' EXIT
  mkdir -p "$TMP/internal/oneshot" "$TMP/internal/store/migrations" "$TMP/stubs/pgx/pgconn" "$TMP/stubs/pgx/pgxpool"
  cp -R internal/oneshot/domain "$TMP/internal/oneshot/domain"
  cp -R internal/oneshot/queue "$TMP/internal/oneshot/queue"
  cp -R internal/oneshot/adapter "$TMP/internal/oneshot/adapter"
  cp -R internal/oneshot/application "$TMP/internal/oneshot/application"
  cp -R internal/oneshot/executor "$TMP/internal/oneshot/executor"
  cp -R internal/oneshot/saga "$TMP/internal/oneshot/saga"
  cp -R internal/oneshot/recovery "$TMP/internal/oneshot/recovery"
  cp -R internal/oneshot/testdata "$TMP/internal/oneshot/testdata"
  cp -R internal/oneshot/store "$TMP/internal/oneshot/store"
  cp -R internal/oneshot/workspacepolicy "$TMP/internal/oneshot/workspacepolicy"
  cp internal/store/migrations/0083_oneshot.sql "$TMP/internal/store/migrations/0083_oneshot.sql"
  cp internal/store/migrations/0084_oneshot_run_saga.sql "$TMP/internal/store/migrations/0084_oneshot_run_saga.sql"
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
type CommandTag struct { N int64 }
func (c CommandTag) RowsAffected() int64 { return c.N }
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
const (
  Serializable TxIsoLevel = "serializable"
  ReadCommitted TxIsoLevel = "read committed"
)
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
func Open(context.Context,string,int)(*Store,error){ return &Store{pool:&pgxpool.Pool{}},nil }
func (s *Store) Migrate(context.Context,*slog.Logger) error { return nil }
func (s *Store) Close() {}
func (s *Store) Pool() *pgxpool.Pool { return s.pool }
GO
  (
    cd "$TMP"
    GOTOOLCHAIN=local GOPROXY=off go vet ./internal/oneshot/adapter ./internal/oneshot/application ./internal/oneshot/executor ./internal/oneshot/queue ./internal/oneshot/recovery ./internal/oneshot/saga ./internal/oneshot/store
    GOTOOLCHAIN=local GOPROXY=off go test -race -coverprofile=executor.cover ./internal/oneshot/adapter ./internal/oneshot/application ./internal/oneshot/executor ./internal/oneshot/queue ./internal/oneshot/recovery ./internal/oneshot/saga
    GOTOOLCHAIN=local GOPROXY=off go test -race ./internal/oneshot/store
    GOTOOLCHAIN=local GOPROXY=off go test -tags postgres -run '^$' ./internal/oneshot/store
    CGO_ENABLED=0 GOOS=windows GOARCH=amd64 GOTOOLCHAIN=local GOPROXY=off go test -c -o "$TMP/oneshot-executor-windows.test.exe" ./internal/oneshot/executor
    CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 GOTOOLCHAIN=local GOPROXY=off go test -c -o "$TMP/oneshot-executor-darwin.test" ./internal/oneshot/executor
  )
  echo 'PASS: isolated adapter/executor/store vet, race tests, PostgreSQL-tag and Windows/macOS compilation'
else
  [[ "${ONESHOT_EXECUTOR_REQUIRE_GO:-0}" != "1" ]] || fail 'go is required for isolated executor validation'
  echo 'SKIP: go unavailable; isolated executor tests not run'
fi

scripts/oneshot/check-boundaries.sh
python3 scripts/oneshot/check-task-state.py
scripts/oneshot/check-git-diff.sh
echo 'OD-OS-10 Shell One-shot Adapter and Executor source gate: PASS'
