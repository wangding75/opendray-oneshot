#!/usr/bin/env bash
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT"
python3 scripts/oneshot/check-control-api.py
python3 scripts/oneshot/check-replay-outbox.py
python3 scripts/oneshot/check-telegram-oneshot.py
python3 scripts/oneshot/check-task-state.py
scripts/oneshot/check-git-diff.sh
printf 'OD-OS-18/19/20 control-plane source gate: PASS\n'
