# One-shot architecture contract

- Contract status: Frozen
- ADR: `docs/adr/0010-oneshot-agent-execution-domain.md`
- Applies from: `feat/oneshot-agent`

## 1. Purpose

This contract freezes the boundary between OpenDray's existing Interactive PTY
execution domain and the new One-shot Agent execution domain. Later tasks may
fill in implementation details, but they may not merge the two lifecycles
without an explicit replacement ADR.

## 2. Domain ownership

| Concern | Interactive PTY | One-shot Agent |
|---|---|---|
| Primary aggregate | InteractiveSession | Task |
| Execution attempt | live Session process | Run |
| Dispatch reliability | channel/session input | Delivery lease |
| Provider continuity | live PTY + provider resume | RuntimeContext metadata |
| Process | long-lived PTY child | ordinary per-Run child process |
| Input | repeated terminal input | immutable Run request |
| Output | terminal stream/ring buffer | ordered stdout/stderr records |
| Completion | idle/stopped/terminated | completed/failed/cancelled/timed_out |
| Retry | not a task retry concept | new Delivery and new Run |
| Continue | write to live PTY or Session resume | new Run using RuntimeContext |
| Cancellation | Session termination | One-shot process-tree supervision |
| Channel binding | interactive binding | One-shot task/run binding |
| API resource | Session | Task/Run/RuntimeContext/Artifact |

## 3. Package boundaries

### 3.1 Neutral platform packages

Neutral packages may be consumed by both domains:

```text
internal/auth
internal/audit
internal/channel
internal/config
internal/eventbus
internal/project*
internal/catalog
internal/cliacct
internal/store infrastructure
```

Neutral packages must not make business decisions for Session or One-shot.

### 3.2 Interactive packages

```text
internal/session/**
internal/session/channeladapter/**
```

Interactive packages own PTY behavior. They must not import One-shot packages.

### 3.3 One-shot packages

Target structure:

```text
internal/oneshot/domain/**
internal/oneshot/application/**
internal/oneshot/queue/**
internal/oneshot/store/**
internal/oneshot/executor/**
internal/oneshot/provideradapter/**
internal/oneshot/channeladapter/**
internal/oneshot/api/**
```

One-shot core packages must not import Interactive Session or PTY packages.
Only `internal/oneshot/channeladapter` may depend on the neutral Channel Core.

### 3.4 Application composition

The application bootstrap may depend on both domains to wire interfaces. It
must not translate one domain object into the other domain's lifecycle object.

## 4. Shared versus isolated capabilities

### 4.1 Shared

- principal/authentication/authorization;
- project and controlled workspace resolution;
- provider metadata and executable discovery;
- credential allocation interfaces;
- PostgreSQL connection pool and migration runner;
- HTTP router and WebSocket infrastructure;
- Event Bus, logging, clock, ID generation and audit;
- channel transport, message normalization, attachment intake and outbound
  delivery.

### 4.2 Isolated

- aggregate IDs and state machines;
- process handles and supervisors;
- output buffers and persisted output records;
- channel binding stores;
- retry and cancellation semantics;
- API resources and WebSocket event namespaces;
- tables and business foreign keys;
- notifier decision logic.

## 5. Lifecycle invariants

1. One Task may have multiple Runs, but at most one active Run.
2. One Delivery may create at most one Run.
3. Retry creates a new Delivery and Run; it never re-sends a channel message as
   a substitute for execution.
4. Continue creates a new Run and may reference one RuntimeContext.
5. RuntimeContext stores provider context metadata only and has no process.
6. A Run starts one ordinary process and exits after completion.
7. A Run never allocates a PTY.
8. Waiting for user input is a persisted Task state; it is not a live terminal
   waiting state.
9. Cancel success requires the One-shot process tree to be terminated or
   already absent.
10. Notification delivery retry never creates a Run.

## 6. Channel routing contract

```text
Channel Transport
  -> normalized ChannelMessage
  -> deterministic ingress router
       -> InteractiveChannelAdapter
       -> OneShotChannelAdapter
```

Routing rules:

- explicit One-shot command routes only to One-shot;
- explicit Session command routes only to Interactive;
- a reply binding resolves only within its owning binding store;
- normal text may preserve the existing Interactive fallback;
- no route may select a domain by "most recently active Task or Session";
- one channel update may create at most one Task.

Channel Core owns transport retry. One-shot owns task retry.

## 7. Persistence contract

One-shot tables use a dedicated prefix or subsystem-owned schema. They must not
alter or reference interactive session tables for business relationships.

Required separation:

```text
sessions / session transcript / interactive bindings
                     X
oneshot_tasks / deliveries / runs / runtime_contexts / stream_records /
standard_events / artifacts / oneshot bindings
```

Common user, project and provider identities may be referenced through neutral
platform tables.

## 8. API and event contract

- Interactive routes remain unchanged.
- One-shot routes use `/api/v1/oneshot/**`.
- Interactive events retain `session.*` names.
- One-shot events use `oneshot.*` names.
- A One-shot handler must not publish `session.output` or `session.ended`.
- A Session handler must not update Task or Run state.

## 9. Feature flag contract

`oneshot.enabled` defaults to `false`.

With the flag disabled:

- One-shot workers, channel handlers and notifier loops are inactive;
- no One-shot process is started;
- Interactive PTY startup and channel routing are unchanged;
- existing Session API, Web Terminal and Flutter Terminal remain available.

The flag controls activation, not data migration. Existing One-shot records are
retained when the feature is disabled.

## 10. Forbidden implementations

The following fail the architecture gate:

```text
Session.Mode = "oneshot"
POST /sessions?mode=oneshot
One-shot calling pty.Start
One-shot importing internal/session
Session importing internal/oneshot
Channel Core importing either execution domain
RuntimeContext.SessionID
One-shot migration ALTER TABLE sessions
One-shot migration REFERENCES sessions
silent fallback from unsupported One-shot provider to PTY
```

## 11. Verification

Run:

```bash
scripts/oneshot/check-boundaries.sh
scripts/oneshot/check-boundaries-test.sh
```

The first command checks the repository. The second creates clean and violating
fixtures to verify the checker itself.

This architecture gate does not replace runtime tests. Gateway startup with
One-shot disabled and the full PTY regression suite must be executed when the
required Go, Flutter and database toolchains are available.
