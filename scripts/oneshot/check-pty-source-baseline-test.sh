#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
checker="$repo_root/scripts/oneshot/check-pty-source-baseline.py"

python3 "$checker" --repo "$repo_root" >/dev/null

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
fixture="$tmp/fixture"
case_dir="$tmp/case"
mkdir -p "$fixture"

# Copy only the files consumed by the checker. Symlinks are not used so each
# mutation proves the checker observes the temporary fixture, not the source.
while IFS= read -r file; do
  mkdir -p "$fixture/$(dirname "$file")"
  cp "$repo_root/$file" "$fixture/$file"
done <<'FILES'
go.mod
internal/session/manager.go
internal/session/handler.go
internal/session/channeladapter/binding_store.go
internal/session/channeladapter/input_submitter.go
internal/session/channeladapter/binding_store_test.go
internal/session/channeladapter/input_submitter_test.go
app/mobile/lib/core/api/sessions_api.dart
app/mobile/lib/features/sessions/session_terminal_view.dart
app/mobile/test/core/api/sessions_api_contract_test.dart
docs/development/oneshot/contracts/pty-baseline.md
docs/development/oneshot/contracts/pty-test-matrix.yaml
FILES

expect_failure() {
  local name="$1"
  local mutation="$2"
  rm -rf "$case_dir"
  cp -a "$fixture" "$case_dir"
  bash -c "$mutation"
  if python3 "$checker" --repo "$case_dir" >/dev/null 2>&1; then
    printf 'FAIL: checker accepted mutation: %s\n' "$name" >&2
    exit 1
  fi
  printf 'PASS: checker rejected mutation: %s\n' "$name"
}

expect_failure "PTY launch removed" \
  "sed -i 's/pty.Start(/pty_REMOVED(/' '$case_dir/internal/session/manager.go'"
expect_failure "Session input route removed" \
  "sed -i 's#r.Post(\"/input\", h.input)#r.Post(\"/input-removed\", h.input)#' '$case_dir/internal/session/handler.go'"
expect_failure "conversation scope collapsed" \
  "sed -i 's/scopeKey(channelID, conversationID string)/scopeKey(channelID string)/' '$case_dir/internal/session/channeladapter/binding_store.go'"
expect_failure "mobile stream route removed" \
  "sed -i 's#/api/v1/sessions/\$sessionId/stream#/api/v1/sessions/\$sessionId/removed#' '$case_dir/app/mobile/lib/features/sessions/session_terminal_view.dart'"
expect_failure "test matrix removed" \
  "rm '$case_dir/docs/development/oneshot/contracts/pty-test-matrix.yaml'"

printf 'PTY source baseline checker self-test passed\n'
