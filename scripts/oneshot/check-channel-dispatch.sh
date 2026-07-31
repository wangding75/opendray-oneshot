#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT"

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

require_text() {
  local file="$1"
  local pattern="$2"
  local label="$3"
  grep -Eq "$pattern" "$file" || fail "$label"
  echo "PASS: $label"
}

require_absent() {
  local file="$1"
  local pattern="$2"
  local label="$3"
  if grep -Eq "$pattern" "$file"; then
    fail "$label"
  fi
  echo "PASS: $label"
}

formatted="$(gofmt -d \
  internal/channel/channel.go \
  internal/channel/dispatch.go \
  internal/channel/hub.go \
  internal/channel/dispatch_test.go \
  internal/channel/telegram/telegram.go \
  internal/channel/telegram/telegram_test.go \
  internal/channel/adapter_host.go \
  internal/session/channeladapter/adapter.go \
  internal/session/channeladapter/binding_store.go \
  internal/session/channeladapter/input_submitter.go \
  internal/session/channeladapter/handler.go \
  internal/session/channeladapter/notifier.go \
  internal/session/channeladapter/reply_tracker.go \
  internal/session/channeladapter/cards.go \
  internal/app/app.go)"
[[ -z "$formatted" ]] || fail "gofmt produced a diff"
echo "PASS: gofmt"

require_text internal/channel/channel.go '^type ReplyAddress struct' 'neutral ReplyAddress type'
require_text internal/channel/channel.go '^type Attachment struct' 'neutral Attachment type'
require_text internal/channel/dispatch.go '^type InboundDispatcher interface' 'InboundDispatcher interface'
require_text internal/channel/dispatch.go '^type InboundDispatcherChain struct' 'deterministic dispatcher chain'
require_text internal/channel/hub.go 'func \(h \*Hub\) SetInboundDispatcher' 'Hub dispatcher seam'
require_text internal/channel/hub.go 'func \(h \*Hub\) processInboundAfterPersist' 'post-persistence dispatcher entry'
require_text internal/channel/hub.go 'channel\.inbound_dispatch\.handled' 'handled audit event'
require_text internal/channel/hub.go 'channel\.inbound_dispatch\.not_handled' 'not-handled audit event'
require_text internal/channel/hub.go 'channel\.inbound_dispatch\.timed_out' 'timeout audit event'
require_text internal/channel/hub.go 'channel\.inbound_dispatch\.panicked' 'panic audit event'
require_text internal/app/app.go 'NewInboundDispatcherChain' 'application dispatcher chain wiring'
require_text internal/app/app.go 'sessionchanneladapter\.InboundPriority' 'interactive adapter registration'
require_text internal/channel/dispatch_test.go 'TestProcessInboundHandledDoesNotPublishUnroutedMessage' 'handled stops fallback test'
require_text internal/channel/dispatch_test.go 'TestProcessInboundNotHandledPublishesNeutralMessageEvent' 'not-handled neutral event test'
require_text internal/channel/dispatch_test.go 'TestProcessInboundDispatcherErrorIsTerminal' 'error duplicate-delivery test'
require_text internal/channel/dispatch_test.go 'TestDispatchInboundPanicAndTimeoutAreAuditedAndTerminal' 'panic and timeout test'
require_text internal/channel/dispatch_test.go 'TestUnknownSlashCommandCanBeClaimedByExecutionDomain' 'execution-domain slash command routing test'
require_text internal/channel/dispatch_test.go 'TestRegisteredChannelCommandKeepsPrecedenceOverDomains' 'registered Channel command precedence test'
require_text internal/channel/telegram/telegram_test.go 'next getUpdates offset' 'Telegram update offset regression test'
require_absent internal/channel/dispatch.go 'internal/(session|oneshot)' 'neutral dispatcher has no execution-domain import'
require_absent internal/channel/channel.go 'internal/(session|oneshot)' 'neutral channel types have no execution-domain import'

handle_inbound_body="$(awk '
  /func \(h \*Hub\) handleInbound/ {flag=1}
  flag {print}
  flag && /^}/ {exit}
' internal/channel/hub.go)"
if grep -q 'submitToSession' <<<"$handle_inbound_body"; then
  fail 'handleInbound still directly writes PTY'
fi
echo 'PASS: handleInbound delegates instead of writing PTY directly'

if GOTOOLCHAIN=local go version 2>/dev/null | grep -Eq 'go1\.(2[5-9]|[3-9][0-9])'; then
  GOTOOLCHAIN=local go test ./internal/channel/... 
  echo 'PASS: Go channel tests'
else
  if [[ "${CHANNEL_DISPATCH_REQUIRE_GO:-0}" == "1" ]]; then
    fail 'Go >= 1.25 is required for channel tests'
  fi
  echo 'SKIP: Go >= 1.25 unavailable; source and sandbox compatibility gates only'
fi

scripts/oneshot/check-boundaries.sh
scripts/oneshot/check-pty-regression.sh

scripts/oneshot/check-git-diff.sh

echo 'Channel inbound dispatcher source gate: PASS'
