#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT"

fail() { printf 'FAIL: %s\n' "$*" >&2; exit 1; }

mapfile -t files < <(find internal/oneshot/domain -maxdepth 1 -type f -name '*.go' -print | sort)
[[ ${#files[@]} -gt 0 ]] || fail 'internal/oneshot/domain has no Go files'
formatted="$(gofmt -d "${files[@]}")"
[[ -z "$formatted" ]] || { printf '%s\n' "$formatted" >&2; fail 'One-shot domain Go files are not formatted'; }
echo 'PASS: One-shot domain Go files are formatted'

python3 scripts/oneshot/check-domain-contract.py

if ! command -v go >/dev/null 2>&1; then
  if [[ "${ONESHOT_DOMAIN_REQUIRE_GO:-0}" == "1" ]]; then
    fail 'go is required for isolated One-shot domain tests'
  fi
  echo 'SKIP: go unavailable; isolated One-shot domain tests not run'
else
  TMP="$(mktemp -d)"
  trap 'rm -rf "$TMP"' EXIT
  mkdir -p "$TMP/internal/oneshot/domain"
  cp internal/oneshot/domain/*.go "$TMP/internal/oneshot/domain/"
  cat > "$TMP/go.mod" <<'MOD'
module github.com/opendray/opendray-v2

go 1.23.0
MOD
  (
    cd "$TMP"
    GOTOOLCHAIN=local GOPROXY=off go vet ./internal/oneshot/domain
    GOTOOLCHAIN=local GOPROXY=off go test -race -coverprofile=coverage.out ./internal/oneshot/domain
    coverage="$(GOTOOLCHAIN=local go tool cover -func=coverage.out | awk '/^total:/{gsub("%", "", $3); print $3}')"
    awk -v value="$coverage" 'BEGIN { if ((value + 0) < 65) exit 1 }' \
      || fail "One-shot domain coverage ${coverage}% is below 65%"
    printf 'PASS: isolated One-shot domain vet, race tests, and coverage (%s%%)\n' "$coverage"
  )
fi

scripts/oneshot/check-contracts.sh
scripts/oneshot/check-known-issue-fixes.sh
scripts/oneshot/check-task-state.py

scripts/oneshot/check-git-diff.sh
echo 'OD-OS-07 One-shot domain source gate: PASS'
