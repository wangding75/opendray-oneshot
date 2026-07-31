#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT"

scripts/oneshot/check-oneshot-executor.sh
python3 scripts/oneshot/check-oneshot-output.py
scripts/oneshot/check-known-issue-fixes.sh
scripts/oneshot/check-pty-regression.sh
python3 scripts/oneshot/check-task-state.py
scripts/oneshot/check-git-diff.sh

echo 'OD-OS-11 ordered output, Artifact and StandardEvent source gate: PASS'
