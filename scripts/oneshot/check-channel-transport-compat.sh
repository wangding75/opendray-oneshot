#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT"

if ! command -v go >/dev/null 2>&1; then
  if [[ "${CHANNEL_TRANSPORT_REQUIRE_GO:-0}" == "1" ]]; then
    echo 'FAIL: go is required for isolated channel transport tests' >&2
    exit 1
  fi
  echo 'SKIP: go unavailable; isolated channel transport tests not run' >&2
  exit 0
fi

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

mkdir -p "$TMP/internal/channel"
cp internal/channel/channel.go internal/channel/card.go internal/channel/controls.go internal/channel/registry.go "$TMP/internal/channel/"
for package in telegram slack discord feishu; do
  mkdir -p "$TMP/internal/channel/$package"
  cp internal/channel/$package/*.go "$TMP/internal/channel/$package/"
done

mkdir -p "$TMP/stubs/websocket"
cat > "$TMP/stubs/websocket/go.mod" <<'MOD'
module github.com/gorilla/websocket

go 1.23.0
MOD
cat > "$TMP/stubs/websocket/websocket.go" <<'GO'
package websocket

import (
    "context"
    "errors"
    "net/http"
    "time"
)

const (
    TextMessage = 1
    PingMessage = 9
)

type Conn struct{}
func (*Conn) Close() error { return nil }
func (*Conn) SetReadLimit(int64) {}
func (*Conn) SetReadDeadline(time.Time) error { return nil }
func (*Conn) SetWriteDeadline(time.Time) error { return nil }
func (*Conn) SetPongHandler(func(string) error) {}
func (*Conn) ReadMessage() (int, []byte, error) { return 0, nil, errors.New("stub websocket") }
func (*Conn) WriteMessage(int, []byte) error { return nil }

type Dialer struct{}
func (*Dialer) DialContext(context.Context, string, http.Header) (*Conn, *http.Response, error) {
    return nil, nil, errors.New("stub websocket dial")
}
var DefaultDialer = &Dialer{}
GO
cat > "$TMP/go.mod" <<'MOD'
module github.com/opendray/opendray-v2

go 1.23.0

require github.com/gorilla/websocket v1.5.3
replace github.com/gorilla/websocket => ./stubs/websocket
MOD

(
  cd "$TMP"
  GOTOOLCHAIN=local GOPROXY=off go test -race \
    ./internal/channel/telegram \
    ./internal/channel/slack \
    ./internal/channel/discord \
    ./internal/channel/feishu
)
echo 'PASS: isolated Telegram, Slack, Discord, and Feishu transport tests'
