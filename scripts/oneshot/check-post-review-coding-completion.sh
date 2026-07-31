#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT"

printf '==> post-review deterministic source assertions\n'
python3 scripts/oneshot/check-code-review-fixes.py

printf '==> provider, executor, recovery, and Saga gates\n'
scripts/oneshot/check-provider-continuation.sh

printf '==> Queue and isolated Store gates\n'
scripts/oneshot/check-oneshot-queue.sh

printf '==> security, REST, channel transport, attachment, and mobile source gates\n'
scripts/oneshot/check-final-hardening.sh

printf '==> PTY and channel regression gates\n'
scripts/oneshot/check-known-issue-fixes.sh

printf '==> state and diff gates\n'
python3 scripts/oneshot/check-task-state.py
scripts/oneshot/check-git-diff.sh

printf 'Post-review One-shot coding completion gate: PASS\n'
