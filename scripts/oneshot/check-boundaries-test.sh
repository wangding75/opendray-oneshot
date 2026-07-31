#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
checker="$script_dir/check-boundaries.sh"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

new_fixture() {
  local name="$1"
  local root="$tmp/$name"
  mkdir -p \
    "$root/docs/adr" \
    "$root/docs/development/oneshot/contracts" \
    "$root/internal/channel" \
    "$root/internal/session" \
    "$root/internal/oneshot" \
    "$root/internal/store/migrations"

  cat > "$root/docs/adr/0010-oneshot-agent-execution-domain.md" <<'DOC'
# ADR 0010
- Status: Accepted
## Rejected alternatives
## Consequences
## Enforcement
DOC
  cat > "$root/docs/development/oneshot/contracts/architecture.md" <<'DOC'
# Contract
- Contract status: Frozen
oneshot.enabled defaults to false.
RuntimeContext is separate.
Channel Core is neutral.
DOC
  printf '%s\n' 'package channel' > "$root/internal/channel/channel.go"
  printf '%s\n' 'package session' > "$root/internal/session/session.go"
  printf '%s\n' 'package oneshot' > "$root/internal/oneshot/task.go"
  printf '%s\n' '-- oneshot tables only' > "$root/internal/store/migrations/0082_oneshot.sql"
  printf '%s\n' "$root"
}

expect_pass() {
  local name="$1"
  local root="$2"
  if ! "$checker" --root "$root" >"$tmp/$name.out" 2>"$tmp/$name.err"; then
    echo "FAIL: $name expected PASS" >&2
    cat "$tmp/$name.err" >&2
    exit 1
  fi
}

expect_fail() {
  local name="$1"
  local root="$2"
  local expected="$3"
  if "$checker" --root "$root" >"$tmp/$name.out" 2>"$tmp/$name.err"; then
    echo "FAIL: $name expected failure" >&2
    exit 1
  fi
  if ! grep -q "$expected" "$tmp/$name.err"; then
    echo "FAIL: $name did not report expected message: $expected" >&2
    cat "$tmp/$name.err" >&2
    exit 1
  fi
}

root="$(new_fixture clean)"
expect_pass clean "$root"

root="$(new_fixture oneshot-imports-session)"
cat > "$root/internal/oneshot/task.go" <<'GO'
package oneshot
import "github.com/opendray/opendray-v2/internal/session"
var _ = session.Manager{}
GO
expect_fail oneshot-imports-session "$root" 'internal/oneshot imports internal/session'

root="$(new_fixture session-imports-oneshot)"
cat > "$root/internal/session/session.go" <<'GO'
package session
import "github.com/opendray/opendray-v2/internal/oneshot"
var _ = oneshot.Task{}
GO
expect_fail session-imports-oneshot "$root" 'internal/session imports internal/oneshot'

root="$(new_fixture channel-imports-domain)"
cat > "$root/internal/channel/channel.go" <<'GO'
package channel
import "github.com/opendray/opendray-v2/internal/session"
var _ = session.Manager{}
GO
expect_fail channel-imports-domain "$root" 'neutral internal/channel imports an execution domain'

root="$(new_fixture oneshot-uses-pty)"
cat > "$root/internal/oneshot/task.go" <<'GO'
package oneshot
import "github.com/creack/pty"
var _ = pty.Start
GO
expect_fail oneshot-uses-pty "$root" 'One-shot implementation references PTY runtime primitives'

root="$(new_fixture runtime-context-session-id)"
cat > "$root/internal/oneshot/task.go" <<'GO'
package oneshot
type RuntimeContext struct { SessionID string }
GO
expect_fail runtime-context-session-id "$root" 'One-shot RuntimeContext/domain model exposes an Interactive Session ID'

root="$(new_fixture mixed-session-mode)"
cat > "$root/internal/session/session.go" <<'GO'
package session
const ExecutionMode = "oneshot"
GO
expect_fail mixed-session-mode "$root" 'mixed Session mode implementation detected'

root="$(new_fixture sessions-route)"
cat > "$root/internal/oneshot/task.go" <<'GO'
package oneshot
const route = "/sessions?mode=oneshot"
GO
expect_fail sessions-route "$root" 'One-shot is exposed through Interactive Session routes'

root="$(new_fixture migration-cross-reference)"
cat > "$root/internal/store/migrations/0082_oneshot.sql" <<'SQL'
CREATE TABLE oneshot_runs (
  id TEXT PRIMARY KEY,
  session_id TEXT REFERENCES sessions(id)
);
SQL
expect_fail migration-cross-reference "$root" 'One-shot migration modifies or references interactive sessions'

root="$(new_fixture missing-doc)"
rm "$root/docs/development/oneshot/contracts/architecture.md"
expect_fail missing-doc "$root" 'required architecture artifact is missing'

printf 'One-shot boundary checker self-test: PASS\n'
