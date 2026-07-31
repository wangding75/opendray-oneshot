#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT"

fail() { printf 'FAIL: %s\n' "$*" >&2; exit 1; }

files=(
  internal/channel/delivery_contract.go
  internal/channel/delivery/*.go
  internal/channel/adapter_host.go
  internal/channel/hub.go
  internal/channel/telegram/telegram.go
  internal/channel/slack/slack.go
  internal/channel/discord/discord.go
  internal/channel/feishu/feishu.go
  internal/app/app.go
)
formatted="$(gofmt -d "${files[@]}")"
[[ -z "$formatted" ]] || { printf '%s\n' "$formatted" >&2; fail 'delivery Go files are not formatted'; }
echo 'PASS: delivery Go files are formatted'

for required in \
  internal/channel/delivery/model.go \
  internal/channel/delivery/service.go \
  internal/channel/delivery/outbox.go \
  internal/channel/delivery/postgres_store.go \
  internal/channel/delivery/memory_store.go \
  internal/store/migrations/0082_channel_delivery_outbox.sql; do
  [[ -f "$required" ]] || fail "missing $required"
done
echo 'PASS: delivery implementation and migration exist'

if grep -R -E --include='*.go' 'internal/(session|oneshot)' internal/channel/delivery >/dev/null; then
  fail 'channel delivery imports an execution domain'
fi
echo 'PASS: delivery is independent from Session and One-shot domains'

grep -q 'channel_delivery_outbox' internal/store/migrations/0082_channel_delivery_outbox.sql || fail 'outbox table missing'
grep -q 'channel_delivery_attempts' internal/store/migrations/0082_channel_delivery_outbox.sql || fail 'attempt table missing'
grep -q 'idempotency_key' internal/store/migrations/0082_channel_delivery_outbox.sql || fail 'idempotency key missing'
echo 'PASS: durable outbox schema covers attempts and idempotency'

if ! command -v go >/dev/null 2>&1; then
  if [[ "${CHANNEL_DELIVERY_REQUIRE_GO:-0}" == "1" ]]; then
    fail 'go is required for isolated channel delivery tests'
  fi
  echo 'SKIP: go unavailable; isolated channel delivery tests not run'
  exit 0
fi

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
mkdir -p "$TMP/internal/channel/delivery" "$TMP/internal/channel" \
  "$TMP/stubs/uuid" "$TMP/stubs/pgx/pgxpool"
cp internal/channel/delivery/*.go "$TMP/internal/channel/delivery/"
cp internal/channel/channel.go internal/channel/card.go internal/channel/delivery_contract.go "$TMP/internal/channel/"

cat > "$TMP/stubs/uuid/go.mod" <<'MOD'
module github.com/google/uuid

go 1.23.0
MOD
cat > "$TMP/stubs/uuid/uuid.go" <<'GO'
package uuid

import (
  "fmt"
  "sync/atomic"
)

var next uint64
func NewString() string { return fmt.Sprintf("uuid-%d", atomic.AddUint64(&next, 1)) }
GO

cat > "$TMP/stubs/pgx/go.mod" <<'MOD'
module github.com/jackc/pgx/v5

go 1.23.0
MOD
cat > "$TMP/stubs/pgx/pgx.go" <<'GO'
package pgx

import "errors"
var ErrNoRows = errors.New("no rows")
GO
cat > "$TMP/stubs/pgx/pgxpool/pgxpool.go" <<'GO'
package pgxpool

import "context"

type Pool struct{}
type Row struct{}
func (*Row) Scan(...any) error { return nil }
type Rows struct{}
func (*Rows) Next() bool { return false }
func (*Rows) Scan(...any) error { return nil }
func (*Rows) Close() {}
func (*Rows) Err() error { return nil }
type CommandTag struct{ N int64 }
func (c CommandTag) RowsAffected() int64 { return c.N }
func (*Pool) QueryRow(context.Context, string, ...any) *Row { return &Row{} }
func (*Pool) Query(context.Context, string, ...any) (*Rows, error) { return &Rows{}, nil }
func (*Pool) Exec(context.Context, string, ...any) (CommandTag, error) { return CommandTag{N:1}, nil }
GO

cat > "$TMP/go.mod" <<'MOD'
module github.com/opendray/opendray-v2

go 1.23.0

require (
  github.com/google/uuid v1.6.0
  github.com/jackc/pgx/v5 v5.9.2
)
replace github.com/google/uuid => ./stubs/uuid
replace github.com/jackc/pgx/v5 => ./stubs/pgx
MOD

(
  cd "$TMP"
  GOTOOLCHAIN=local GOPROXY=off go vet ./internal/channel/delivery
  GOTOOLCHAIN=local GOPROXY=off go test -race ./internal/channel/delivery
)
echo 'PASS: isolated shared channel delivery vet and tests'

scripts/oneshot/check-session-channel-adapter-compat.sh
scripts/oneshot/check-channel-transport-compat.sh
scripts/oneshot/check-boundaries.sh
scripts/oneshot/check-pty-regression.sh
scripts/oneshot/check-task-state.py

scripts/oneshot/check-git-diff.sh
echo 'OD-OS-06 shared channel delivery source gate: PASS'
