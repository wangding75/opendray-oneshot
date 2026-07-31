# OD-OS-02 Summary — 双执行域架构与依赖边界冻结

## Status

NEEDS CHANGES — architecture implementation is complete; runtime feature-flag
and full PTY validation are deferred to the user's local toolchain.

## Timing

- Started: `2026-07-27T14:04:53+08:00`
- Implementation committed: `2026-07-27T14:05:10+08:00`
- Report completed: `2026-07-27T14:05:30+08:00`

## Baseline and commits

- Baseline Commit: `188ea1d9b39f99ad93be1d33769a7708bc4e5fbc`
- Implementation Commit: `7a3edf359deafe7b06e2cb22628d412beb4708b5`
- Push target: sandbox-local repository only
- GitHub Push: not performed

## Design conclusion

OpenDray will keep two independent bounded contexts:

1. Interactive PTY owns Session, PTY process, terminal input/resize/reattach,
   ring buffer, transcript, interactive binding and Session termination.
2. One-shot owns Task, Delivery, Run, RuntimeContext, ordinary child process,
   ordered stdout/stderr, artifacts, retry, timeout, cancellation and One-shot
   binding.

The domains may share neutral authentication, project, provider metadata,
credentials, PostgreSQL infrastructure, HTTP/WS infrastructure, Event Bus,
audit and Channel Transport. They may not share process instances, state
machines, output stores, bindings, API resources or cancellation semantics.

The following designs are now explicitly rejected:

- `Session.Mode = pty | oneshot`;
- `/sessions?mode=oneshot`;
- One-shot implemented through a hidden PTY Session;
- RuntimeContext containing a Session ID;
- Channel Core importing either execution domain;
- unsupported One-shot providers silently falling back to PTY.

## Changed files

- `docs/adr/0010-oneshot-agent-execution-domain.md`
- `docs/development/oneshot/contracts/architecture.md`
- `scripts/oneshot/check-boundaries.sh`
- `scripts/oneshot/check-boundaries-test.sh`
- `docs/development/oneshot/evidence/OD-OS-02-boundary-check.txt`
- `docs/development/oneshot/reports/OD-OS-02-summary.md`
- `docs/development/oneshot/task-state.yaml`
- `docs/development/oneshot/OPENDRAY_ONESHOT_DEVELOPMENT_TASKBOOK.md`

## Tests added

The boundary checker self-test builds an isolated clean fixture and then injects
representative violations. It proves that the checker rejects:

- One-shot importing Session;
- Session importing One-shot;
- Channel Core importing an execution domain;
- One-shot importing or calling PTY;
- RuntimeContext exposing SessionID;
- mixed Session execution mode;
- One-shot exposed through Session routes;
- One-shot migrations referencing Session tables;
- missing frozen architecture documents.

A classic production-code RED phase does not apply because this task freezes an
architecture contract and creates its first static checker. Negative fixtures
provide executable failure evidence.

## Validation performed

- `scripts/oneshot/check-boundaries.sh`: PASS.
- `scripts/oneshot/check-boundaries-test.sh`: PASS.
- Bash syntax check: PASS.
- `git diff --check`: PASS.
- ADR status/alternatives/consequences/enforcement checks: PASS.
- Architecture contract frozen/default-disabled/RuntimeContext/Channel Core
  checks: PASS.

Evidence:

- `docs/development/oneshot/evidence/OD-OS-02-boundary-check.txt`

## Deferred validation

The following cannot be executed in the current sandbox:

1. Gateway startup with `oneshot.enabled=false` because the repository requires
   Go 1.25 and the sandbox has Go 1.23.2 with no outbound toolchain download.
2. Full PTY regression gate because its Go tests require the same toolchain.
3. Flutter Session regression because Flutter and Dart are unavailable.

These are deferred, not treated as passed.

## Impact

- Production Go behavior changed: no.
- API behavior changed: no.
- Database schema changed: no.
- Telegram behavior changed: no.
- Flutter behavior changed: no.
- Architecture constraints for subsequent tasks: frozen.

## Compatibility

The task adds documentation and static checks only. Existing PTY runtime code is
unchanged. The contract requires One-shot to default disabled and requires all
later implementation to preserve existing Session APIs and channel behavior.

## Next task

`OD-OS-03 — One-shot 契约、状态机、API 与错误码冻结`

OD-OS-01 and OD-OS-02 remain in the validation-deferred list until the user's
local Go/Flutter environment runs their runtime gates.
