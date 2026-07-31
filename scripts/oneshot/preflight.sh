#!/usr/bin/env bash
set -u

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$repo_root"

run() {
  local name="$1"
  shift
  printf '\n## %s\n' "$name"
  printf '$'
  printf ' %q' "$@"
  printf '\n'
  "$@"
  local code=$?
  printf '[exit=%d]\n' "$code"
  return 0
}

printf '# OpenDray One-shot preflight\n'
printf 'utc=%s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
printf 'local=%s\n' "$(TZ=Asia/Taipei date +%Y-%m-%dT%H:%M:%S%z 2>/dev/null || date +%Y-%m-%dT%H:%M:%S%z)"
printf 'repo=%s\n' "$repo_root"
printf 'branch=%s\n' "$(git branch --show-current 2>/dev/null || true)"
printf 'head=%s\n' "$(git rev-parse HEAD 2>/dev/null || true)"
printf 'upstream=%s\n' "$(git rev-parse 'HEAD@{upstream}' 2>/dev/null || true)"
printf 'os=%s\n' "$(uname -a)"

run "Git" git --version
run "Go local" env GOTOOLCHAIN=local go version
run "Node" node --version
run "Corepack" corepack --version
run "pnpm" pnpm --version
run "Flutter" flutter --version
run "Dart" dart --version
run "PostgreSQL client" psql --version

printf '\n## Repository counts\n'
printf 'files=%s\n' "$(find . -type f -not -path './.git/*' | wc -l | tr -d ' ')"
printf 'go_files=%s\n' "$(find . -type f -name '*.go' | wc -l | tr -d ' ')"
printf 'go_test_files=%s\n' "$(find . -type f -name '*_test.go' | wc -l | tr -d ' ')"
printf 'dart_files=%s\n' "$(find app/mobile -type f -name '*.dart' 2>/dev/null | wc -l | tr -d ' ')"
printf 'ts_tsx_files=%s\n' "$(find app -type f \( -name '*.ts' -o -name '*.tsx' \) 2>/dev/null | wc -l | tr -d ' ')"
printf 'internal_go_packages=%s\n' "$(find internal -type f -name '*.go' -printf '%h\n' 2>/dev/null | sort -u | wc -l | tr -d ' ')"
printf 'migration_files=%s\n' "$(find internal/store/migrations -maxdepth 1 -type f 2>/dev/null | wc -l | tr -d ' ')"

run "i18n parity" node scripts/check-i18n-parity.mjs
run "Go test baseline" env GOTOOLCHAIN=local go test ./...
run "Web dependency check" corepack pnpm --version
run "Flutter availability" sh -c 'command -v flutter >/dev/null && flutter --version'
run "PostgreSQL availability" sh -c 'command -v psql >/dev/null && psql --version'
