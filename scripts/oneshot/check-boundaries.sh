#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'USAGE'
Usage: check-boundaries.sh [--root PATH]

Checks the OpenDray Interactive/One-shot architecture boundary.
The default root is the repository containing this script.
USAGE
}

root="${ONESHOT_BOUNDARY_ROOT:-}"
while [[ $# -gt 0 ]]; do
  case "$1" in
    --root)
      [[ $# -ge 2 ]] || { echo "ERROR: --root requires a path" >&2; exit 2; }
      root="$2"
      shift 2
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "ERROR: unknown argument: $1" >&2
      usage >&2
      exit 2
      ;;
  esac
done

if [[ -z "$root" ]]; then
  root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
else
  root="$(cd "$root" && pwd)"
fi
cd "$root"

fail=0
report() {
  printf 'ERROR: %s\n' "$*" >&2
  fail=1
}

require_file() {
  [[ -f "$1" ]] || report "required architecture artifact is missing: $1"
}

contains_go_pattern() {
  local path="$1"
  local pattern="$2"
  [[ -e "$path" ]] || return 1
  grep -R --include='*.go' -nE "$pattern" "$path" >/dev/null 2>&1
}

contains_text_pattern() {
  local path="$1"
  local pattern="$2"
  [[ -e "$path" ]] || return 1
  grep -R -nE "$pattern" "$path" >/dev/null 2>&1
}

adr='docs/adr/0010-oneshot-agent-execution-domain.md'
contract='docs/development/oneshot/contracts/architecture.md'
require_file "$adr"
require_file "$contract"

if [[ -f "$adr" ]]; then
  grep -qE '^- Status: Accepted$' "$adr" || report 'One-shot ADR is not Accepted'
  grep -q '^## Rejected alternatives$' "$adr" || report 'One-shot ADR does not record rejected alternatives'
  grep -q '^## Consequences$' "$adr" || report 'One-shot ADR does not record consequences'
  grep -q '^## Enforcement$' "$adr" || report 'One-shot ADR does not record enforcement'
fi

if [[ -f "$contract" ]]; then
  grep -q 'Contract status: Frozen' "$contract" || report 'architecture contract is not Frozen'
  grep -q 'oneshot.enabled.*false' "$contract" || report 'architecture contract does not freeze disabled-by-default behavior'
  grep -q 'RuntimeContext' "$contract" || report 'architecture contract does not define RuntimeContext boundary'
  grep -q 'Channel Core' "$contract" || report 'architecture contract does not define Channel Core boundary'
fi

module_path='github.com/opendray/opendray-v2/internal'

if contains_go_pattern internal/oneshot "\"${module_path}/session([/\"]|$)"; then
  report 'internal/oneshot imports internal/session'
fi
if contains_go_pattern internal/session "\"${module_path}/oneshot([/\"]|$)"; then
  report 'internal/session imports internal/oneshot'
fi
if contains_go_pattern internal/channel "\"${module_path}/(session|oneshot)([/\"]|$)"; then
  report 'neutral internal/channel imports an execution domain'
fi
if contains_go_pattern internal/oneshot '(github.com/creack/pty|pty\.Start[[:space:]]*\()'; then
  report 'One-shot implementation references PTY runtime primitives'
fi
if contains_go_pattern internal/oneshot 'SessionID[[:space:]]+(string|\*string)|SessionId[[:space:]]+(string|\*string)'; then
  report 'One-shot RuntimeContext/domain model exposes an Interactive Session ID'
fi
if contains_go_pattern internal/session '(Mode|ExecutionMode)[^\n]*(oneshot|one-shot)|oneshot[^\n]*(Mode|ExecutionMode)'; then
  report 'mixed Session mode implementation detected'
fi
if contains_go_pattern internal '(sessions[^\n]*(mode=oneshot|oneshot)|mode=oneshot[^\n]*sessions)'; then
  report 'One-shot is exposed through Interactive Session routes'
fi

if [[ -d internal/store/migrations ]]; then
  while IFS= read -r migration; do
    [[ -n "$migration" ]] || continue
    if grep -vE '^[[:space:]]*--' "$migration" \
      | grep -qiE 'ALTER[[:space:]]+TABLE[[:space:]]+(IF[[:space:]]+EXISTS[[:space:]]+)?sessions\b|REFERENCES[[:space:]]+sessions\b'; then
      report "One-shot migration modifies or references interactive sessions: $migration"
    fi
  done < <(find internal/store/migrations -maxdepth 1 -type f -iname '*oneshot*' -print | sort)
fi

if [[ "$fail" -ne 0 ]]; then
  exit 1
fi
printf 'One-shot architecture boundaries: PASS\n'
