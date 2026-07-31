#!/usr/bin/env bash
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT"

printf '==> OD-OS-24 attachment and cross-device source contract\n'
python3 - <<'PY'
from pathlib import Path
required = {
 'internal/oneshot/attachment/service.go': ['safeFilename','DetectContentType','sanitizeSourceRef','CleanupExpired'],
 'internal/oneshot/api/attachments.go': ['ParseMultipartForm','attachmentMaxBytes'],
 'internal/oneshot/store/attachment.go': ['BindDeliveryAttachments','principal_kind','project_id','expires_at'],
 'internal/store/migrations/0086_oneshot_attachments.sql': ['oneshot_staged_attachments','oneshot_delivery_attachments','oneshot_notification_preferences'],
 'internal/channel/telegram/telegram.go': ['OpenAttachment','getFile','telegramAttachments'],
 'app/mobile/lib/features/agent_tasks/data/agent_tasks_api.dart': ['stageAttachment','FormData','/api/v1/oneshot/attachments'],
}
for name,tokens in required.items():
    text=Path(name).read_text()
    missing=[x for x in tokens if x not in text]
    if missing: raise SystemExit(f'{name}: missing {missing}')
api=Path('internal/oneshot/api/handler.go').read_text()+Path('internal/oneshot/api/attachments.go').read_text()
for route in ['r.Post("/attachments"','r.Get("/attachments/{attachment_id}"','r.Delete("/attachments/{attachment_id}"']:
    if route not in api: raise SystemExit(f'missing route {route}')
if 'artifact://requirements' in Path('internal/oneshot/channeladapter/adapter.go').read_text():
    raise SystemExit('raw transport attachment reference leaked into One-shot adapter')
print('attachment/cross-device static contract: PASS')
PY

printf '==> security static checks\n'
if rg -n 'exec\.Command(Context)?\([^\n]*("sh"|"bash"|"cmd"|"powershell")' internal/oneshot; then
  echo 'FAIL: shell trampoline found in One-shot source' >&2; exit 1
fi
if rg -n -g'!*_test.go' 'bot[A-Za-z0-9_-]{20,}|api\.telegram\.org/file/bot[^"+ ]+' internal/oneshot app/mobile/lib/features/agent_tasks; then
  echo 'FAIL: token-bearing Telegram file URL found in execution/mobile domain' >&2; exit 1
fi
if rg -n 'internal/session|creack/pty|xterm' internal/oneshot; then
  echo 'FAIL: PTY/Session import leaked into One-shot domain' >&2; exit 1
fi
printf 'security static checks: PASS\n'

printf '==> attachment unit, race, coverage, and fuzz gates\n'
TMP="$(mktemp -d)"; trap 'rm -rf "$TMP"' EXIT
mkdir -p "$TMP/internal/oneshot"
cp -R internal/oneshot/domain internal/oneshot/attachment "$TMP/internal/oneshot/"
cat > "$TMP/go.mod" <<'MOD'
module github.com/opendray/opendray-v2
go 1.23.0
MOD
(
 cd "$TMP"
 GOTOOLCHAIN=local GOPROXY=off go vet ./internal/oneshot/attachment
 GOTOOLCHAIN=local GOPROXY=off go test -race -cover ./internal/oneshot/attachment
 GOTOOLCHAIN=local GOPROXY=off go test -run='^$' -fuzz=FuzzStageFilenameAndMIME -fuzztime=2s ./internal/oneshot/attachment
)

printf '==> provider output parser race and fuzz gates\n'
ADAPTER_TMP="$(mktemp -d)"
mkdir -p "$ADAPTER_TMP/internal/oneshot"
cp -R internal/oneshot/domain internal/oneshot/adapter internal/oneshot/testdata "$ADAPTER_TMP/internal/oneshot/"
cat > "$ADAPTER_TMP/go.mod" <<'MOD'
module github.com/opendray/opendray-v2
go 1.23.0
MOD
(
 cd "$ADAPTER_TMP"
 GOTOOLCHAIN=local GOPROXY=off go vet ./internal/oneshot/adapter
 GOTOOLCHAIN=local GOPROXY=off go test -race -cover ./internal/oneshot/adapter
 GOTOOLCHAIN=local GOPROXY=off go test -run='^$' -fuzz=FuzzDecodeJSONLinesNeverPanicsOrCrossesRunState -fuzztime=2s ./internal/oneshot/adapter
)
rm -rf "$ADAPTER_TMP"

printf '==> control plane, channel transport, and mobile source gates\n'
scripts/oneshot/check-control-plane-compile.sh
scripts/oneshot/check-channel-transport-compat.sh
scripts/oneshot/check-mobile-agent-tasks.sh

scripts/oneshot/check-git-diff.sh
printf 'OD-OS-24/25 final hardening source gate: PASS\n'
