# OD-OS-03 Summary — One-shot contract freeze

## Status

`PASS`

This status applies to the OD-OS-03 contract/document/schema/validator scope.
No Go, database, Telegram or Flutter production implementation was authorized
or added by this task.

## Timing

- Started: `2026-07-27T14:31:00+0800`
- Finished: `2026-07-27T14:46:18+0800`
- Elapsed: approximately 15 minutes

## Git

- Branch: `feat/oneshot-agent`
- Baseline commit: `5520d16d901883d03d73dc2523621e7ab1b311c7`
- Implementation commit: `c0d9dd3ee5a61db1dac7b4e4517843e96028a161`
- Push target: sandbox-local origin only; GitHub credentials are not available
  in this environment.

## Design conclusion

The One-shot contract is now frozen independently from Interactive PTY Session.
The canonical machine-readable manifest defines:

- 7 resources: Task, Delivery, Run, RuntimeContext, StreamRecord,
  StandardEvent and Artifact;
- 4 state machines with explicit legal transitions and guards;
- 13 HTTP/WebSocket operations under `/api/v1/oneshot/**`;
- 23 unique `oneshot.*` events;
- 26 stable structured error codes;
- ownership, idempotency, pagination, UTC timestamp, error-envelope and audit
  rules.

One-shot API paths do not reuse `/sessions` or `/custom-tasks`. RuntimeContext
contains no Session/process identity. Run terminal states are immutable.
Task quiescent outcomes may open a new cycle only through an explicit Continue
or Retry that creates a new Delivery and Run; earlier Runs remain immutable.

## Files added

- `docs/development/oneshot/contracts/domain-model.md`
- `docs/development/oneshot/contracts/state-machines.md`
- `docs/development/oneshot/contracts/http-api.md`
- `docs/development/oneshot/contracts/events.md`
- `docs/development/oneshot/contracts/errors.md`
- `docs/development/oneshot/contracts/schema/oneshot-contract.schema.json`
- `docs/development/oneshot/contracts/fixtures/oneshot-contract.json`
- `docs/development/oneshot/contracts/fixtures/api-examples.json`
- `scripts/oneshot/check-contracts.py`
- `scripts/oneshot/check-contracts.sh`
- `scripts/oneshot/check-contracts-test.sh`
- `docs/development/oneshot/evidence/OD-OS-03-contract-validation.txt`

## Red / negative proof

The checker self-test mutates isolated copies and proves the gate rejects:

- missing schema-required fields;
- unreachable states;
- outgoing transitions from terminal states;
- One-shot routes placed under Session paths;
- duplicate error codes;
- `session.*` event topics;
- error codes missing from the human-readable contract.

The initial validation also failed when `Delivery.retry_wait` was absent from
the state-machine document, proving documentation coverage is enforced. The
missing coverage was corrected before PASS.

## Validation

Passed:

```text
scripts/oneshot/check-contracts.sh
  7 resources
  4 state machines
  13 routes
  23 events
  26 errors

scripts/oneshot/check-contracts-test.sh
scripts/oneshot/check-boundaries.sh
python3 -m py_compile scripts/oneshot/check-contracts.py
python3 -m json.tool <schema and fixtures>
bash -n <contract scripts>
scripts/oneshot/check-pty-regression.sh (source gate)
git diff --check
```

Evidence:

- `docs/development/oneshot/evidence/OD-OS-03-contract-validation.txt`

The PTY source gate remained 15/15 PASS and its five negative mutations PASS.
Go 1.25 and Flutter runtime suites remain deferred to the final local runtime
gate because those toolchains are unavailable in the sandbox; they were not
reported as passed.

## Impact

### API

Freezes future One-shot paths, scopes, idempotency and error response format.
It does not mount routes in this task.

### Database

Defines persistence invariants but adds no migration in this task.

### Telegram

Defines source identity, deduplication and deterministic One-shot ownership.
No Telegram behavior changed.

### Mobile

Defines the API/event contract future Flutter work will consume. No mobile code
changed.

### PTY compatibility

No Session or Channel production code changed. Existing PTY source regression
and architecture boundary gates pass.

## Remaining work

- OD-OS-04: add neutral Channel Core inbound dispatcher.
- Runtime Go/Flutter PTY verification remains a final local acceptance gate.
- Contract changes after this task require an explicit contract-change record
  and corresponding fixture/schema/version update.
