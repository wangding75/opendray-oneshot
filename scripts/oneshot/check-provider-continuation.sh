#!/usr/bin/env bash
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT"

python3 scripts/oneshot/check-provider-adapters.py
python3 scripts/oneshot/check-runtime-context-continuation.py
scripts/oneshot/check-oneshot-executor.sh
python3 scripts/oneshot/check-task-state.py
scripts/oneshot/check-git-diff.sh
printf 'OD-OS-15/16/17 provider adapters and RuntimeContext continuation source gate: PASS\n'
