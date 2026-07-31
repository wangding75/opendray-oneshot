#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT"

if ! command -v go >/dev/null 2>&1; then
  if [[ "${SESSION_CHANNEL_ADAPTER_REQUIRE_GO:-0}" == "1" ]]; then
    echo 'FAIL: go is required for isolated Session channel adapter compatibility tests' >&2
    exit 1
  fi
  echo 'SKIP: go unavailable; isolated Session channel adapter compatibility tests not run' >&2
  exit 0
fi

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

write_module() {
  cat > "$1/go.mod" <<'MOD'
module github.com/opendray/opendray-v2

go 1.23.0
MOD
}

# Compile and race-test the real Session channel adapter against only the
# transport-neutral Channel/EventBus contracts. This deliberately avoids the
# repository's Go 1.25 toolchain download and all external modules.
ADAPTER="$TMP/adapter"
mkdir -p "$ADAPTER/internal/session/channeladapter" "$ADAPTER/internal/channel" "$ADAPTER/internal/eventbus"
cp internal/session/channeladapter/*.go "$ADAPTER/internal/session/channeladapter/"
cp -a internal/session/channeladapter/testdata "$ADAPTER/internal/session/channeladapter/"
cp internal/channel/channel.go internal/channel/card.go internal/channel/dispatch.go internal/channel/controls.go "$ADAPTER/internal/channel/"
cp internal/eventbus/bus.go "$ADAPTER/internal/eventbus/"
cat > "$ADAPTER/internal/channel/adapter_policy.go" <<'GO'
package channel

type AdapterChatPolicy struct {
	ChatEnabled     bool
	TypingEnabled   bool
	ReplyMaxChars   int
	IncludeSnippet  bool
	SnippetMaxChars int
}
GO
write_module "$ADAPTER"
(
  cd "$ADAPTER"
  GOTOOLCHAIN=local go test -race ./internal/session/channeladapter
)
echo 'PASS: isolated Session channel adapter tests'

# Compile and race-test the changed Channel Core without reaching PostgreSQL.
CHANNEL="$TMP/channel"
mkdir -p "$CHANNEL/internal/channel" "$CHANNEL/internal/eventbus" "$CHANNEL/stubs/pgx/pgxpool"
cp internal/channel/{channel.go,card.go,chatconfig.go,command.go,controls.go,dispatch.go,hub.go,adapter_host.go,delivery_contract.go,registry.go} "$CHANNEL/internal/channel/"
cp internal/channel/{card_test.go,channel_test.go,command_test.go,controls_test.go,dispatch_test.go,hub_command_test.go,hub_cooldown_test.go,typing_test.go} "$CHANNEL/internal/channel/"
cp internal/eventbus/bus.go "$CHANNEL/internal/eventbus/"
cat > "$CHANNEL/internal/channel/store_stub.go" <<'GO'
package channel

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5/pgxpool"
)

type FieldCipher interface {
	EncryptField(string) (string, error)
	DecryptField(string) (string, error)
}

type channelRow struct {
	ID      string
	Kind    string
	Config  json.RawMessage
	Enabled bool
}

type store struct{}

func newStore(*pgxpool.Pool) *store                                           { return &store{} }
func (*store) setCipher(FieldCipher)                                           {}
func (*store) List(context.Context) ([]channelRow, error)                      { return nil, nil }
func (*store) Get(context.Context, string) (channelRow, error)                 { return channelRow{}, ErrNotFound }
func (*store) Insert(context.Context, string, string, json.RawMessage, bool) error { return nil }
func (*store) Update(context.Context, string, json.RawMessage, *bool) error     { return nil }
func (*store) Delete(context.Context, string) error                            { return nil }
func (*store) InsertMessage(context.Context, ChannelMessage) (int64, error)     { return 1, nil }
GO
cat > "$CHANNEL/stubs/pgx/go.mod" <<'MOD'
module github.com/jackc/pgx/v5

go 1.23.0
MOD
cat > "$CHANNEL/stubs/pgx/pgxpool/pgxpool.go" <<'GO'
package pgxpool

type Pool struct{}
GO
cat > "$CHANNEL/go.mod" <<'MOD'
module github.com/opendray/opendray-v2

go 1.23.0

require github.com/jackc/pgx/v5 v5.0.0
replace github.com/jackc/pgx/v5 => ./stubs/pgx
MOD
(
  cd "$CHANNEL"
  GOTOOLCHAIN=local go test -race ./internal/channel
)
echo 'PASS: isolated Channel Core tests'

# Compile and race-test Telegram outbound binding metadata. The implementation
# only depends on the transport-neutral Channel contracts and the standard
# library, so it can run without downloading the full repository toolchain.
TELEGRAM="$TMP/telegram"
mkdir -p "$TELEGRAM/internal/channel/telegram" "$TELEGRAM/internal/channel"
cp internal/channel/telegram/*.go "$TELEGRAM/internal/channel/telegram/"
cp internal/channel/{channel.go,card.go,controls.go,registry.go} "$TELEGRAM/internal/channel/"
write_module "$TELEGRAM"
(
  cd "$TELEGRAM"
  GOTOOLCHAIN=local go test -race ./internal/channel/telegram
)
echo 'PASS: isolated Telegram transport tests'
