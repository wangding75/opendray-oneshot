#!/usr/bin/env bash
set -euo pipefail
repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$repo_root"
python3 scripts/oneshot/check-contracts.py
scripts/oneshot/check-contracts-test.sh
scripts/oneshot/check-boundaries.sh
