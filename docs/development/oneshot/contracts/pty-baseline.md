# Interactive PTY Baseline Contract

Status: Frozen by `OD-OS-01`

## Purpose

This contract characterizes the existing interactive execution mode before
Channel Core extraction and One-shot Agent development. It is a compatibility
gate, not a redesign proposal.

## Runtime identity

The existing session domain is explicitly PTY-backed:

- `internal/session.Manager` owns live `runningSession` values.
- `Manager.spawn` launches provider CLIs through `pty.Start`.
- Each live session owns a PTY file descriptor, ring buffer, virtual terminal,
  subscriber set and provider process.
- PTY output is pumped into the ring buffer, event bus and subscribers.
- Mobile/Web terminal clients write keystrokes to the same live PTY.

One-shot development must not change this identity.

## Session state baseline

Persisted states:

```text
pending
running
idle
stopped
ended
interrupted
```

Key semantics:

- `stopped`: explicit user stop; row remains restartable.
- `ended`: provider process exited on its own; row remains restartable.
- `interrupted`: Gateway shutdown/crash interrupted a previously live session;
  startup reconciliation may resume it.
- `stopped`, `ended`, and `interrupted` are terminal but restartable.
- Concurrent starts for the same session are excluded by the `starting`
  reservation.

## Existing Session API

Mounted under `/api/v1/sessions`:

```text
GET    /
POST   /
GET    /{id}
DELETE /{id}
POST   /{id}/start
POST   /{id}/stop
POST   /{id}/input
POST   /{id}/resize
GET    /{id}/buffer
GET    /{id}/stream
GET    /{id}/history
PATCH  /{id}/claude-account
PATCH  /{id}/antigravity-account
POST   /{id}/uploads
```

Compatibility requirements:

- `/input` continues to write raw data into PTY stdin.
- `/resize` continues to resize the PTY.
- `/stream` continues to expose the live PTY byte stream.
- `/buffer` continues to replay ring-buffer output.
- Restart preserves the logical session row and ID while spawning a new PTY.

## Channel-to-PTY call chain

Current inbound plain-text path:

```text
Telegram/other Channel
  → channel implementation receives platform update
  → Channel Hub authorizes sender
  → inbound message is persisted
  → slash/control commands are handled first
  → neutral InboundDispatcher chain
  → session/channeladapter.InteractiveHandler
  → MemoryBindingStore.Resolve
       1. reply-to outbound message
       2. active session pin
       3. most recently notified session
  → InputSubmitter.Submit
  → one PTY write per Unicode rune
  → settle delay
  → final carriage return (Enter)
  → session.Manager.Input
  → live PTY stdin
```

Routing hints are scoped by `channel_id + conversation_id`. The same platform message ID in two channels or conversations must resolve independently.

## Telegram/session reply baseline

The following behavior must remain observable after Channel Core extraction:

- Explicit reply to a Session notification targets that exact Session.
- Active-session selection has priority over last-notified fallback.
- Unknown reply IDs fall through to active/last Session according to current
  priority.
- Plain text with no target publishes `channel.message_received`; it is not
  silently injected into an unrelated Session.
- Unauthorized senders are denied before persistence and PTY injection.
- Commands and control-keyboard labels are not typed into the PTY.
- A PTY write failure stops the remaining runes and Enter submission.
- Session turn/idle events continue to drive typing and reply notifications.

## Mobile baseline

The Flutter client currently treats a Session as a live terminal:

- REST `/input` sends raw PTY bytes.
- REST `/resize` sends terminal dimensions.
- WebSocket `/api/v1/sessions/{id}/stream` carries PTY output/input.
- `SessionTerminalView` owns xterm state, reconnect behavior, input forwarding,
  resize forwarding and finished→live restart handling.
- Session and Terminal navigation remain separate from future Agent Task pages.

## Allowed future changes

- Move Session-specific channel behavior into `session/channeladapter`.
- Add a neutral inbound dispatcher in Channel Core.
- Extract channel transport/delivery interfaces.
- Add tests and diagnostics that preserve this contract.

## Prohibited regressions

- Replacing PTY Sessions with One-shot processes.
- Adding `Session.Mode` for One-shot.
- Routing `/run` through Session Manager.
- Writing One-shot output into Session ring buffers/transcripts.
- Breaking reply-to, active-session or last-session routing priority.
- Removing mobile/Web terminal attach, input, resize, replay or reconnect.
- Allowing routing hints to leak across channel IDs.

## Regression gate

The gate has two layers:

1. An executable source-level compatibility gate that runs without Go/Flutter
   and protects the frozen PTY call chain, routes, conversation-scoped bindings,
   mobile endpoint wiring and required regression tests.
2. The real Go and Flutter runtime suites, which remain mandatory at the final
   local acceptance gate.

Development/source gate:

```bash
scripts/oneshot/check-pty-regression.sh
```

Strict local runtime/final gate:

```bash
PTY_REGRESSION_REQUIRE_GO=1 \
PTY_REGRESSION_REQUIRE_MOBILE=1 \
scripts/oneshot/check-pty-regression.sh
```

A missing runtime toolchain is never reported as a runtime pass. The normal
source gate prints an explicit SKIP, while the strict command exits non-zero.
