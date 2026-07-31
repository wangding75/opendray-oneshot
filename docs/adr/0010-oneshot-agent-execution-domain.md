# ADR 0010: Independent One-shot Agent execution domain

- Status: Accepted
- Date: 2026-07-27
- Decision owners: OpenDray maintainers
- Scope: backend runtime, channels, persistence, API and mobile clients

## Context

OpenDray's existing execution model is an interactive, long-lived PTY session.
The web terminal, Flutter terminal and channel replies operate on the same
session lifecycle: a CLI is started in a pseudo-terminal, input can be appended
repeatedly, terminal state can be resized or reattached, and output is retained
in a session ring buffer/transcript.

A message-dispatched One-shot Agent task has different reliability semantics:

- one request creates one durable Task;
- a Delivery is leased by a worker;
- one Run starts one ordinary child process without allocating a PTY;
- stdout and stderr are persisted independently;
- completion is determined by the process result and persisted state;
- retry and continue create later Runs instead of reusing a live process;
- cancellation and timeout terminate the One-shot process tree;
- the process exits after the Run.

Combining these models behind `Session.mode` would couple terminal concerns
(resize, reattach, ring buffer and interactive input) with task concerns
(delivery lease, idempotency, retry, completion, artifact persistence and
crash recovery).

## Decision

OpenDray has two independent bounded contexts.

### Interactive execution domain

Owned by `internal/session` and its channel adapter. It owns:

- InteractiveSession identity and state;
- PTY allocation and long-running CLI processes;
- terminal input, resize and reattach;
- ring buffer and interactive transcript;
- interactive channel bindings;
- interactive cancellation and termination semantics.

### One-shot execution domain

Owned by `internal/oneshot` subpackages. It owns:

- Task, Delivery, Run and RuntimeContext identities;
- durable queue lease, retry and dead-letter semantics;
- ordinary child process execution without PTY;
- ordered stdout/stderr records and artifacts;
- One-shot state machines, idempotency and crash recovery;
- One-shot channel bindings and notifications;
- One-shot cancellation, timeout and process-tree supervision.

`RuntimeContext` represents provider-owned resumable context metadata only. It
never represents a live process and never stores or references an Interactive
Session ID.

## Shared platform capabilities

Both domains may depend on neutral platform interfaces for:

- authentication, principals and authorization;
- project registration and controlled workspace resolution;
- provider metadata, CLI discovery and credential allocation;
- PostgreSQL pool and migration infrastructure;
- HTTP and WebSocket server infrastructure;
- Event Bus and audit sink;
- channel transport, normalized messages and outbound delivery;
- common logging, clock and ID generation.

Sharing platform capabilities does not permit sharing domain records or
lifecycle state.

## Dependency direction

The intended dependency graph is:

```text
internal/channel                     neutral channel core
        ^                                      ^
        |                                      |
internal/session/channeladapter      internal/oneshot/channeladapter
        |                                      |
internal/session                     internal/oneshot/application
                                               |
                         domain / queue / store / executor / provider adapters
```

Rules:

1. `internal/channel` must not import `internal/session` or `internal/oneshot`.
2. `internal/session` must not import `internal/oneshot`.
3. One-shot core packages must not import `internal/session`, PTY packages or
   interactive transcript/ring-buffer packages.
4. Channel-specific behavior lives in the two channel-adapter packages, not in
   Channel Core or either domain model.
5. Program composition occurs in the application bootstrap layer.

## Persistence boundary

Interactive records and One-shot records use separate tables and stores.

One-shot migrations must not:

- alter the `sessions` table to add an execution mode;
- reference interactive session tables with business foreign keys;
- write One-shot output into session transcript storage.

Infrastructure-level references such as the common user/project identity are
allowed through neutral platform tables.

## API and identifier boundary

Interactive APIs remain under their existing Session routes. One-shot APIs use
an independent namespace such as `/api/v1/oneshot`.

The following are prohibited:

- `POST /sessions?mode=oneshot`;
- a `Session.Mode = oneshot` branch;
- using a Session ID where a Task, Run or RuntimeContext ID is required;
- guessing a target domain from the most recently active object.

## Channel behavior

Channel transport, inbound normalization, authentication, attachment handling,
outbound sending, rate limits and delivery receipts are shared.

After normalization, routing is explicit:

- interactive commands or replies to interactive bindings enter the Session
  channel adapter;
- One-shot commands or replies to One-shot bindings enter the One-shot channel
  adapter.

Notification delivery retry never creates or retries an Agent Run.

## Feature flag behavior

The One-shot subsystem is opt-in. `oneshot.enabled` defaults to `false`.
When disabled:

- no One-shot worker is started;
- no One-shot channel dispatcher is registered;
- no One-shot notifier goroutine is started;
- One-shot APIs either remain unmounted or return the stable `disabled` error;
- existing PTY Session creation, channel replies and terminal clients continue
  unchanged.

Disabling One-shot must not require changes to Interactive Session data.

## Rejected alternatives

### Add `mode` to Session

Rejected because a single aggregate would acquire incompatible state machines,
process semantics and persistence requirements.

### Implement One-shot by creating a hidden PTY Session

Rejected because process completion, ordered stdout/stderr, cancellation,
retry, crash recovery and resource accounting would remain dependent on
terminal heuristics.

### Deploy a second CCCC service beside OpenDray

Rejected for this product phase because it duplicates identity, project,
provider, channel and mobile control planes. Relevant reliability concepts may
be implemented inside the independent One-shot domain instead.

### Duplicate Telegram and mobile backends

Rejected because transport and identity are neutral platform capabilities.
Only post-routing domain behavior must be separate.

## Consequences

Positive consequences:

- existing PTY behavior can be regression-tested and evolved independently;
- One-shot Runs can have durable retry, cancellation and audit semantics;
- Telegram and Flutter can share one Task/Run source of truth;
- disabling one execution mode does not invalidate the other mode's data.

Costs:

- additional domain objects, migrations, API resources and adapters;
- explicit application composition and more boundary tests;
- providers need separate non-interactive capability validation.

## Enforcement

`scripts/oneshot/check-boundaries.sh` statically checks the dependency,
process, route, persistence and documentation rules. Its fixture-based
self-test proves that representative violations are rejected.

Runtime and behavioral tests remain required in later tasks; this static gate
is necessary but not sufficient.
