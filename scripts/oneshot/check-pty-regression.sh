#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$repo_root"

require_go="${PTY_REGRESSION_REQUIRE_GO:-0}"
require_mobile="${PTY_REGRESSION_REQUIRE_MOBILE:-0}"

printf '==> executable PTY source baseline\n'
python3 scripts/oneshot/check-pty-source-baseline.py
scripts/oneshot/check-pty-source-baseline-test.sh

printf '==> i18n parity\n'
node scripts/check-i18n-parity.mjs

have_go_125=0
if command -v go >/dev/null 2>&1; then
  go_version="$(GOTOOLCHAIN=local go env GOVERSION 2>/dev/null || true)"
  if [[ "$go_version" =~ ^go1\.(2[5-9]|[3-9][0-9])([.]|$) ]]; then
    have_go_125=1
  fi
fi

if [[ "$have_go_125" == "1" ]]; then
  printf '==> Go PTY Session and Channel regression\n'
  GOTOOLCHAIN=local go test -count=1 ./internal/session ./internal/session/channeladapter ./internal/channel ./internal/channel/telegram
elif [[ "$require_go" == "1" ]]; then
  printf 'Go >= 1.25 is required but was not found\n' >&2
  exit 1
else
  printf 'SKIP: Go >= 1.25 not found; set PTY_REGRESSION_REQUIRE_GO=1 to make this fatal\n' >&2
fi

if command -v flutter >/dev/null 2>&1; then
  printf '==> Flutter PTY API and shell regression\n'
  (
    cd app/mobile
    flutter test \
      test/core/api/sessions_api_contract_test.dart \
      test/widget_test.dart \
      test/features/channels/channel_config_test.dart
  )
elif [[ "$require_mobile" == "1" ]]; then
  printf 'flutter is required but was not found\n' >&2
  exit 1
else
  printf 'SKIP: flutter not found; set PTY_REGRESSION_REQUIRE_MOBILE=1 to make this fatal\n' >&2
fi
