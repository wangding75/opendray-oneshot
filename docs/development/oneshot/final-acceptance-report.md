# OpenDray One-shot final acceptance report

## Acceptance state

| Dimension | State |
|---|---|
| OD-OS-00 through OD-OS-26 source implementation | PASS |
| Deterministic source/contract/architecture gates | PASS |
| PTY source regression and mutation gate | PASS |
| Complete source archive and Git history delivery | PASS after final commit packaging |
| Provisioned-host runtime acceptance | PENDING |
| Production release approval | NOT GRANTED by this report |

## Delivered platform

The branch implements an independent One-shot execution platform beside the
existing interactive PTY Session platform:

1. Task/Delivery/Run/RuntimeContext domain and PostgreSQL persistence;
2. lease queue, ordinary process execution, output/event/artifact collection,
   cancellation, timeout, Saga and recovery;
3. Provider Registry with Codex and Claude Code non-interactive adapters;
4. secure REST/WS control plane with ownership, project scope, audit and
   idempotency;
5. Telegram commands and exact reply binding;
6. Flutter Agent Tasks list/create/detail/control/replay surface;
7. cross-device attachment staging and durable notification Outbox;
8. source security/race/fuzz and PTY regression gates.

## Isolation conclusion

The stronger evidence supports the conclusion that the two execution domains
are source-isolated: package boundaries forbid cross-imports, One-shot does not
spawn PTYs or use Session IDs/tables, Session routing retains priority for
ordinary messages, and the PTY baseline remains 16/16 with five mutation checks.
This does not replace real host/provider/device testing.

## Known limitations

- Current adapters advertise `attachments=false`; staging infrastructure is
  ready, but clients correctly hide attachment controls until an adapter
  implements provider-specific attachment arguments.
- Attachment validation is deterministic content policy, not antivirus.
- Runtime behavior under a real PostgreSQL server, Go 1.25, mobile background
  suspension and external Provider/Bot failures remains to be proven.
- Source coverage is uneven; API and mobile orchestration need additional
  runtime/integration coverage.

## Required production acceptance sequence

1. provision the repository-specified Go 1.25 toolchain and locked web/mobile
   dependencies;
2. run full vet/race/lint/build and Flutter release build;
3. apply migrations to a representative PostgreSQL clone and run concurrent
   queue/attachment/Outbox/restart/rollback tests;
4. run real Codex and Claude Task create/continue/cancel/failure flows;
5. run Telegram and mobile cross-device attachment/notification flows under
   duplicate updates, rate limits, disconnects and process restarts;
6. rerun PTY gates with `PTY_REGRESSION_REQUIRE_GO=1` and
   `PTY_REGRESSION_REQUIRE_MOBILE=1`;
7. record the resulting production acceptance separately.
