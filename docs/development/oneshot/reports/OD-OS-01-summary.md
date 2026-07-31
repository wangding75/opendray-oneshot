# OD-OS-01 Summary — 现有 PTY Session 行为特征化与回归门禁

## Status

PASS — task implementation and executable source-level regression gate complete.
The Go 1.25 and Flutter runtime suites are explicitly deferred to the user's
local final acceptance environment; they are not represented as passed.

## Timing

- Started: `2026-07-27T11:56:01+08:00`
- Source gate completed: `2026-07-27T14:21:48+08:00`
- Elapsed across interrupted work sessions: recorded in Git/task history

## Baseline and commits

- Task baseline Commit: `5574ad7`
- Initial characterization Commit: `e2eedb3`
- Initial blocker record: `188ea1d`
- Completion Commit: `d8392ec95b4c04c3018f129995366b3b49554adc`
- Push target: sandbox-local repository only
- GitHub Push: not performed

## Design conclusion

The existing interactive mode is conclusively PTY-backed and remains the
compatibility baseline:

- `internal/session.Manager.spawn` launches provider CLIs through `pty.Start`.
- A live `runningSession` owns the PTY file descriptor, ring buffer, virtual
  terminal, subscribers and provider process.
- The Session HTTP API exposes PTY input, resize, replay and stream routes.
- Channel routing priority remains reply-to outbound message → active Session →
  last notified Session, scoped by channel ID.
- Channel text is submitted rune-by-rune and followed by an independent CR.
- Flutter `SessionTerminalView` remains a live PTY client using the Session
  WebSocket and resize/input endpoints.

## Completed changes

- Frozen the human-readable PTY compatibility contract.
- Added a machine-readable PTY coverage matrix.
- Added Go characterization tests for channel-scoped target resolution and
  partial PTY write failure behavior.
- Added Flutter API contract tests for `/input` and `/resize`.
- Added an executable Python source compatibility checker with 15 checks.
- Added a negative self-test proving representative regressions are rejected.
- Upgraded the regression gate to two explicit layers:
  - source gate, runnable in the sandbox;
  - strict Go/Flutter runtime gate, mandatory at final local acceptance.
- Preserved all production Go, Flutter, API, database and Telegram behavior.

## Changed files

- `docs/development/oneshot/contracts/pty-baseline.md`
- `docs/development/oneshot/contracts/pty-test-matrix.yaml`
- `scripts/oneshot/check-pty-source-baseline.py`
- `scripts/oneshot/check-pty-source-baseline-test.sh`
- `scripts/oneshot/check-pty-regression.sh`
- `internal/channel/hub_pty_baseline_test.go`
- `app/mobile/test/core/api/sessions_api_contract_test.dart`
- `docs/development/oneshot/evidence/OD-OS-01-regression-final.txt`
- `docs/development/oneshot/OPENDRAY_ONESHOT_DEVELOPMENT_TASKBOOK.md`
- `docs/development/oneshot/task-state.yaml`

## Red / Lock / Green evidence

The source checker self-test mutates an isolated fixture and proves the gate
fails when any of these protected capabilities is removed:

1. `pty.Start` launch path;
2. Session `/input` route;
3. channel-scoped outbound binding map;
4. Flutter Session WebSocket route;
5. the machine-readable PTY test matrix.

The unmodified source then passes all 15 source checks. This is executable
negative evidence rather than a documentation-only assertion.

## Validation performed

Passed in the current sandbox:

- `python3 -m py_compile scripts/oneshot/check-pty-source-baseline.py`
- Bash syntax checks for all OD-OS-01 scripts
- `scripts/oneshot/check-pty-source-baseline.py`
- `scripts/oneshot/check-pty-source-baseline-test.sh`
- `scripts/oneshot/check-pty-regression.sh`
- i18n parity
- `git diff --check`

Evidence:

- `docs/development/oneshot/evidence/OD-OS-01-regression-final.txt`

## Local runtime gate still mandatory

The strict command is:

```bash
PTY_REGRESSION_REQUIRE_GO=1 \
PTY_REGRESSION_REQUIRE_MOBILE=1 \
scripts/oneshot/check-pty-regression.sh
```

Current sandbox result is an expected non-zero toolchain availability failure:

- repository requires Go `1.25.0`; sandbox has Go `1.23.2`;
- Flutter/Dart are not installed;
- outbound toolchain/dependency download is blocked.

This is recorded as `deferred_toolchain_unavailable`, not as a test pass. The
user explicitly selected the workflow “complete source development first,
perform full runtime debugging locally”.

## Impact

- Production Go behavior changed: no.
- Production Flutter behavior changed: no.
- API behavior changed: no.
- Database changed: no.
- Telegram behavior changed: no.
- PTY compatibility protection: increased.

## Acceptance result

OD-OS-01 is complete for source development and may serve as the dependency for
subsequent implementation tasks. Final release acceptance remains blocked until
the strict local Go/Flutter runtime gate passes.

## Next task

`OD-OS-03 — One-shot 契约、状态机、API 与错误码冻结`

OD-OS-02 implementation already exists and remains subject to the same final
local runtime gate before release acceptance.
