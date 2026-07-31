#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT"

files=(
  internal/channel/adapter_host.go
  internal/channel/channel.go
  internal/channel/discord/discord.go
  internal/channel/discord/discord_test.go
  internal/channel/dispatch_test.go
  internal/channel/feishu/feishu.go
  internal/channel/feishu/feishu_test.go
  internal/channel/hub.go
  internal/channel/hub_command_test.go
  internal/channel/slack/slack.go
  internal/channel/slack/slack_test.go
  internal/channel/telegram/telegram.go
  internal/channel/telegram/telegram_test.go
  internal/session/channeladapter/adapter.go
  internal/session/channeladapter/adapter_test.go
  internal/session/channeladapter/binding_store.go
  internal/session/channeladapter/handler.go
  internal/session/channeladapter/input_queue.go
  internal/session/channeladapter/notifier_test.go
)

formatted="$(gofmt -d "${files[@]}")"
if [[ -n "$formatted" ]]; then
  printf '%s\n' "$formatted" >&2
  echo 'FAIL: gofmt produced a diff' >&2
  exit 1
fi
echo 'PASS: repair Go files are formatted'

bash -n \
  scripts/oneshot/check-channel-transport-compat.sh \
  scripts/oneshot/check-session-channel-adapter.sh \
  scripts/oneshot/check-channel-dispatch.sh \
  scripts/oneshot/check-known-issue-fixes.sh
echo 'PASS: repair shell scripts parse'

scripts/oneshot/check-task-state.py
scripts/oneshot/check-session-channel-adapter-compat.sh
scripts/oneshot/check-channel-transport-compat.sh
scripts/oneshot/check-channel-dispatch.sh
scripts/oneshot/check-boundaries.sh
scripts/oneshot/check-pty-regression.sh

scripts/oneshot/check-git-diff.sh
echo 'Known OD-OS-04/05 issue repair gate: PASS'
