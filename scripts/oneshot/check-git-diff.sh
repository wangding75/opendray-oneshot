#!/usr/bin/env bash
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT"
if git rev-parse --is-inside-work-tree >/dev/null 2>&1; then
  git diff --check "$@"
else
  printf 'SKIP: git diff --check unavailable in source-only archive; use the Git Bundle for history-aware validation\n'
fi
