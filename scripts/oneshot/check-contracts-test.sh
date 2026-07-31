#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
checker="$repo_root/scripts/oneshot/check-contracts.py"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

new_case() {
  local name="$1"
  local root="$tmp/$name"
  mkdir -p "$root/scripts/oneshot" "$root/docs/development/oneshot"
  cp "$checker" "$root/scripts/oneshot/check-contracts.py"
  cp -R "$repo_root/docs/development/oneshot/contracts" "$root/docs/development/oneshot/contracts"
  printf '%s\n' "$root"
}

expect_pass() {
  local name="$1" root="$2"
  if ! python3 "$checker" --root "$root" >"$tmp/$name.out" 2>"$tmp/$name.err"; then
    echo "FAIL: $name expected PASS" >&2
    cat "$tmp/$name.err" >&2
    exit 1
  fi
}

expect_fail() {
  local name="$1" root="$2" expected="$3"
  if python3 "$checker" --root "$root" >"$tmp/$name.out" 2>"$tmp/$name.err"; then
    echo "FAIL: $name expected failure" >&2
    exit 1
  fi
  if ! grep -q "$expected" "$tmp/$name.err"; then
    echo "FAIL: $name did not report: $expected" >&2
    cat "$tmp/$name.err" >&2
    exit 1
  fi
}

root="$(new_case clean)"
expect_pass clean "$root"

root="$(new_case schema-required)"
python3 - "$root" <<'PY'
import json, pathlib, sys
p = pathlib.Path(sys.argv[1])/'docs/development/oneshot/contracts/fixtures/oneshot-contract.json'
d = json.loads(p.read_text())
d.pop('status')
p.write_text(json.dumps(d, indent=2)+'\n')
PY
expect_fail schema-required "$root" "missing required property 'status'"

root="$(new_case unreachable-state)"
python3 - "$root" <<'PY'
import json, pathlib, sys
p = pathlib.Path(sys.argv[1])/'docs/development/oneshot/contracts/fixtures/oneshot-contract.json'
d = json.loads(p.read_text())
d['state_machines']['Run']['transitions'] = [t for t in d['state_machines']['Run']['transitions'] if not (t['from']=='running' and t['to']=='collecting_output')]
p.write_text(json.dumps(d, indent=2)+'\n')
PY
expect_fail unreachable-state "$root" "unreachable state"

root="$(new_case terminal-outgoing)"
python3 - "$root" <<'PY'
import json, pathlib, sys
p = pathlib.Path(sys.argv[1])/'docs/development/oneshot/contracts/fixtures/oneshot-contract.json'
d = json.loads(p.read_text())
d['state_machines']['Run']['transitions'].append({'from':'completed','to':'running','command':'illegal','guard':'none'})
p.write_text(json.dumps(d, indent=2)+'\n')
PY
expect_fail terminal-outgoing "$root" "terminal state 'completed' has outgoing transitions"

root="$(new_case sessions-route)"
python3 - "$root" <<'PY'
import json, pathlib, sys
p = pathlib.Path(sys.argv[1])/'docs/development/oneshot/contracts/fixtures/oneshot-contract.json'
d = json.loads(p.read_text())
d['api'][0]['path']='/api/v1/sessions?mode=oneshot'
p.write_text(json.dumps(d, indent=2)+'\n')
PY
expect_fail sessions-route "$root" "expected constant\|does not match\|outside One-shot base"

root="$(new_case duplicate-error)"
python3 - "$root" <<'PY'
import json, pathlib, sys
p = pathlib.Path(sys.argv[1])/'docs/development/oneshot/contracts/fixtures/oneshot-contract.json'
d = json.loads(p.read_text())
d['errors'].append(dict(d['errors'][0]))
p.write_text(json.dumps(d, indent=2)+'\n')
PY
expect_fail duplicate-error "$root" "duplicate error code"

root="$(new_case session-event)"
python3 - "$root" <<'PY'
import json, pathlib, sys
p = pathlib.Path(sys.argv[1])/'docs/development/oneshot/contracts/fixtures/oneshot-contract.json'
d = json.loads(p.read_text())
d['events'][0]['topic']='session.created'
p.write_text(json.dumps(d, indent=2)+'\n')
PY
expect_fail session-event "$root" "does not match\|outside One-shot namespace"

root="$(new_case missing-doc-coverage)"
sed -i 's/`oneshot.cancel_failed`/`oneshot.cancel_missing`/' "$root/docs/development/oneshot/contracts/errors.md"
expect_fail missing-doc-coverage "$root" "errors.md does not cover oneshot.cancel_failed"

printf 'One-shot contract checker self-test: PASS\n'
