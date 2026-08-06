#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT"

fail() { printf 'FAIL: %s\n' "$*" >&2; exit 1; }

mapfile -t files < <(find internal/oneshot/queue internal/oneshot/application -type f -name '*.go' -print | sort)
[[ ${#files[@]} -gt 0 ]] || fail 'OD-OS-09 Go files are missing'
formatted="$(gofmt -d "${files[@]}")"
[[ -z "$formatted" ]] || { printf '%s\n' "$formatted" >&2; fail 'OD-OS-09 Go files are not formatted'; }
echo 'PASS: OD-OS-09 Go files are formatted'

python3 scripts/oneshot/check-oneshot-queue.py

if command -v go >/dev/null 2>&1; then
  TMP="$(mktemp -d)"
  trap 'rm -rf "$TMP"' EXIT
  mkdir -p "$TMP/internal/oneshot" "$TMP/internal/store" "$TMP/stubs/pgx/pgconn" "$TMP/stubs/pgx/pgxpool"
  cp -R internal/oneshot/domain "$TMP/internal/oneshot/domain"
  cp -R internal/oneshot/queue "$TMP/internal/oneshot/queue"
  cp -R internal/oneshot/workspacepolicy "$TMP/internal/oneshot/workspacepolicy"
  mkdir -p "$TMP/internal/oneshot/application" "$TMP/internal/oneshot/store"
  cp internal/oneshot/application/dispatch_service.go internal/oneshot/application/dispatch_service_test.go "$TMP/internal/oneshot/application/"
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
  cat > "$TMP/internal/oneshot/store/attachment_stub.go" <<'GO'
package store

import (
  "context"

  "github.com/jackc/pgx/v5/pgconn"
  "github.com/opendray/opendray-v2/internal/oneshot/domain"
)

// BindDeliveryAttachments is intentionally stubbed in the historical OD-OS-09
// isolation module. Attachment ownership is validated by the later store gate;
// this gate verifies queue semantics without importing the complete store graph.
func BindDeliveryAttachments(context.Context, interface {
  Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}, domain.Owner, string, string, string, []string) error {
  return nil
}
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
    GOTOOLCHAIN=local GOPROXY=off go vet ./internal/oneshot/domain ./internal/oneshot/queue ./internal/oneshot/application
    GOTOOLCHAIN=local GOPROXY=off go test -race ./internal/oneshot/domain ./internal/oneshot/queue ./internal/oneshot/application
    GOTOOLCHAIN=local GOPROXY=off go test -tags postgres -run '^$' ./internal/oneshot/queue
  )
  echo 'PASS: isolated One-shot queue/application vet, race tests, and PostgreSQL-tag compilation'
else
  [[ "${ONESHOT_QUEUE_REQUIRE_GO:-0}" != "1" ]] || fail 'go is required for isolated queue validation'
  echo 'SKIP: go unavailable; isolated queue tests not run'
fi

if [[ -n "${OPENDRAY_DEV_DB_URL:-}" ]]; then
  GOTOOLCHAIN=auto go test -tags postgres ./internal/oneshot/queue
  echo 'PASS: live PostgreSQL queue competition and recovery tests'
else
  echo 'SKIP: OPENDRAY_DEV_DB_URL not set; live PostgreSQL queue tests deferred to provisioned/local environment'
fi

ONESHOT_STORE_ISOLATED_ONLY=1 scripts/oneshot/check-oneshot-store.sh

scripts/oneshot/check-git-diff.sh
echo 'OD-OS-09 reliable PostgreSQL Delivery Queue source gate: PASS'
