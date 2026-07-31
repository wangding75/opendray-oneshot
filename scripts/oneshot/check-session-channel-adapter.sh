#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT"

fail() {
  printf 'FAIL: %s\n' "$*" >&2
  exit 1
}

require_file() {
  [[ -f "$1" ]] || fail "missing $1"
  printf 'PASS: %s exists\n' "$1"
}

require_text() {
  local file="$1" pattern="$2" label="$3"
  grep -Eq "$pattern" "$file" || fail "$label"
  printf 'PASS: %s\n' "$label"
}

require_absent_tree() {
  local root="$1" pattern="$2" label="$3"
  if grep -R -E --include='*.go' "$pattern" "$root" >/dev/null 2>&1; then
    fail "$label"
  fi
  printf 'PASS: %s\n' "$label"
}

for file in \
  internal/session/channeladapter/adapter.go \
  internal/session/channeladapter/binding_store.go \
  internal/session/channeladapter/input_submitter.go \
  internal/session/channeladapter/input_queue.go \
  internal/session/channeladapter/handler.go \
  internal/session/channeladapter/notifier.go \
  internal/session/channeladapter/reply_tracker.go \
  internal/session/channeladapter/cards.go \
  internal/session/channeladapter/adapter_test.go \
  internal/session/channeladapter/binding_store_test.go \
  internal/session/channeladapter/input_submitter_test.go \
  internal/session/channeladapter/notifier_test.go; do
  require_file "$file"
done

require_text internal/session/channeladapter/handler.go '^type InteractiveHandler struct' 'InteractiveHandler exists'
require_text internal/session/channeladapter/binding_store.go '^type InteractiveBindingStore interface' 'InteractiveBindingStore exists'
require_text internal/session/channeladapter/notifier.go '^type SessionNotifier struct' 'SessionNotifier exists'
require_text internal/session/channeladapter/input_submitter.go '^type InputSubmitter struct' 'InputSubmitter exists'
require_text internal/session/channeladapter/input_queue.go '^type InputQueue struct' 'asynchronous per-session InputQueue exists'
require_text internal/session/channeladapter/adapter_test.go 'TestInteractiveHandlerFiveThousandRuneInputIsComplete' '5000-rune PTY input regression test'
require_text internal/session/channeladapter/adapter_test.go 'TestInputQueueSerializesConsecutiveLongMessages' 'consecutive long-message FIFO regression test'
require_text internal/session/channeladapter/notifier_test.go 'TestSessionNotifierUsesResolvedNonTelegramConversationBinding' 'non-Telegram conversation binding regression test'
require_text internal/app/app.go 'sessionchanneladapter\.New' 'application wires session channel adapter'
require_text internal/app/app.go 'SetInteractiveTargetController' 'application wires target controller'
require_text internal/app/app.go '"interactive"' 'application registers adapter as interactive dispatcher'

require_absent_tree internal/channel 'type SessionInputter interface' 'Channel Core no longer owns SessionInputter'
require_absent_tree internal/channel 'submitToSession' 'Channel Core no longer submits PTY input'
require_absent_tree internal/channel 'dispatchInteractiveInbound' 'Channel Core no longer implements interactive dispatcher'
require_absent_tree internal/channel 'lastSess[[:space:]]+map\[string\]string' 'Channel Core no longer owns last-session routing state'
require_absent_tree internal/channel 'activeSess[[:space:]]+map\[string\]string' 'Channel Core no longer owns active-session routing state'
require_absent_tree internal/channel 'outboundIndex[[:space:]]+map\[string\]map' 'Channel Core no longer owns reply binding index'
require_absent_tree internal/channel 'session\.turn_completed|session\.stopped|session\.interrupted|session\.input' 'Channel Core no longer subscribes interactive session events'
require_absent_tree internal/channel 'internal/session' 'Channel Core has no session import'

scripts/oneshot/check-session-channel-adapter-compat.sh
scripts/oneshot/check-channel-transport-compat.sh
scripts/oneshot/check-task-state.py
scripts/oneshot/check-boundaries.sh
scripts/oneshot/check-pty-regression.sh

scripts/oneshot/check-git-diff.sh
printf 'Session channel adapter source gate: PASS\n'
