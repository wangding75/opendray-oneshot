#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT"

require_flutter="${MOBILE_AGENT_TASKS_REQUIRE_FLUTTER:-0}"

printf '==> Agent Tasks source contract\n'
python3 scripts/oneshot/check-mobile-agent-tasks.py

printf '==> i18n parity\n'
node scripts/check-i18n-parity.mjs

printf '==> One-shot page JSON contract\n'
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
mkdir -p "$TMP/internal/oneshot/store" "$TMP/internal/oneshot/domain" "$TMP/stubs/pgx/pgconn" "$TMP/stubs/pgx/pgxpool"
cp internal/oneshot/store/store.go "$TMP/internal/oneshot/store/"
cp -R internal/oneshot/domain/. "$TMP/internal/oneshot/domain/"
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
cat > "$TMP/stubs/pgx/pgx.go" <<'GO'
package pgx
import("context";"errors";"github.com/jackc/pgx/v5/pgconn")
var ErrNoRows=errors.New("no rows")
type Row interface{Scan(...any)error}
type Rows interface{Next()bool;Scan(...any)error;Close();Err()error}
type TxIsoLevel string
const(Serializable TxIsoLevel="serializable";ReadCommitted TxIsoLevel="read committed")
type TxOptions struct{IsoLevel TxIsoLevel}
type Tx interface{Exec(context.Context,string,...any)(pgconn.CommandTag,error);Query(context.Context,string,...any)(Rows,error);QueryRow(context.Context,string,...any)Row;Commit(context.Context)error;Rollback(context.Context)error}
GO
cat > "$TMP/stubs/pgx/pgconn/pgconn.go" <<'GO'
package pgconn
type PgError struct{Code,ConstraintName string}
func(e *PgError)Error()string{return e.Code}
type CommandTag struct{N int64}
func(c CommandTag)RowsAffected()int64{return c.N}
GO
cat > "$TMP/stubs/pgx/pgxpool/pgxpool.go" <<'GO'
package pgxpool
import("context";"github.com/jackc/pgx/v5";"github.com/jackc/pgx/v5/pgconn")
type Pool struct{}
func(*Pool)Begin(context.Context)(pgx.Tx,error){return nil,nil}
func(*Pool)BeginTx(context.Context,pgx.TxOptions)(pgx.Tx,error){return nil,nil}
func(*Pool)Query(context.Context,string,...any)(pgx.Rows,error){return nil,nil}
func(*Pool)QueryRow(context.Context,string,...any)pgx.Row{return nil}
func(*Pool)Exec(context.Context,string,...any)(pgconn.CommandTag,error){return pgconn.CommandTag{},nil}
GO
cat > "$TMP/internal/oneshot/store/page_json_test.go" <<'GO'
package store
import("encoding/json";"strings";"testing")
func TestPageJSONUsesFrozenMobileContract(t *testing.T){
 raw,err:=json.Marshal(Page[string]{Items:[]string{"x"},NextCursor:"opaque"})
 if err!=nil{t.Fatal(err)}
 got:=string(raw)
 if !strings.Contains(got,`"items":["x"]`)||!strings.Contains(got,`"next_cursor":"opaque"`){t.Fatalf("unexpected page JSON: %s",got)}
 if strings.Contains(got,"Items")||strings.Contains(got,"NextCursor"){t.Fatalf("Go field names leaked: %s",got)}
}
GO
(
  cd "$TMP"
  GOTOOLCHAIN=local GOPROXY=off go test ./internal/oneshot/store
)

printf '==> Provider One-shot capability extension contract\n'
CAP_TMP="$(mktemp -d)"
mkdir -p "$CAP_TMP/internal/catalog"
cp internal/catalog/oneshot_extension.go internal/catalog/handler_oneshot_test.go "$CAP_TMP/internal/catalog/"
cat > "$CAP_TMP/go.mod" <<'MOD'
module github.com/opendray/opendray-v2
go 1.23.0
MOD
cat > "$CAP_TMP/internal/catalog/stub.go" <<'GO'
package catalog
import "log/slog"
type Manifest struct{ID string}
type Provider struct{Manifest Manifest; Enabled bool; OneShot any}
type Handlers struct{log *slog.Logger; oneShotCapability OneShotCapabilityResolver}
GO
(
  cd "$CAP_TMP"
  GOTOOLCHAIN=local GOPROXY=off go test ./internal/catalog
)
rm -rf "$CAP_TMP"

if command -v flutter >/dev/null 2>&1; then
  printf '==> Flutter analyze and Agent Tasks tests\n'
  (
    cd app/mobile
    dart run slang
    flutter analyze
    flutter test test/features/agent_tasks
  )
elif [[ "$require_flutter" == "1" ]]; then
  printf 'flutter is required but was not found\n' >&2
  exit 1
else
  printf 'SKIP: flutter not found; set MOBILE_AGENT_TASKS_REQUIRE_FLUTTER=1 to make this fatal\n' >&2
fi

scripts/oneshot/check-git-diff.sh
printf 'OD-OS-21/22/23 mobile Agent Tasks source gate: PASS\n'
