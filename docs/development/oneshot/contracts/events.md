# One-shot event contract

- Contract status: Frozen by `OD-OS-03`
- Namespace: `oneshot.*`
- Machine source: `fixtures/oneshot-contract.json`

One-shot code must not publish `session.*`. Event Bus publication is a live
notification only; persisted StandardEvents/outbox records are the replay and
recovery source of truth.

## 1. Frame

```json
{
  "topic": "oneshot.run.output",
  "ts": "2026-07-27T06:00:01.123456Z",
  "cursor": "oseq_42",
  "data": {}
}
```

`ts` is UTC RFC3339Nano. Persisted streams include a cursor. Event payloads
must carry stable IDs and current status where applicable.

## 2. Task events

| Topic | Unique semantic |
|---|---|
| `oneshot.task.created` | Task and immutable request persisted. |
| `oneshot.task.queued` | Runnable Delivery durably available/reserved. |
| `oneshot.task.running` | Current Run reached start responsibility. |
| `oneshot.task.waiting_input` | Run exited and explicit input is needed. |
| `oneshot.task.completed` | Latest Run output/artifacts committed. |
| `oneshot.task.failed` | Dispatch or latest Run failed durably. |
| `oneshot.task.cancelled` | Queue or process-tree cancellation resolved. |
| `oneshot.task.timed_out` | Deadline outcome and cleanup recorded. |

Task payloads include `task_id`, `status`, owner tuple, `project_id`,
`provider_id`, `source` and `version`. Outcome events add Run/context/error IDs
as defined by the machine contract.

## 3. Run events

| Topic | Unique semantic |
|---|---|
| `oneshot.run.created` | One immutable Run exists for one Delivery. |
| `oneshot.run.started` | Ordinary child process started without PTY. |
| `oneshot.run.output` | One persisted StandardEvent is replayable. |
| `oneshot.run.waiting_input` | Terminal Run requests later continuation. |
| `oneshot.run.completed` | Terminal successful Run. |
| `oneshot.run.failed` | Terminal failed Run. |
| `oneshot.run.cancelled` | Terminal cancelled Run after cleanup. |
| `oneshot.run.timed_out` | Terminal timeout Run after cleanup. |

`oneshot.run.output` carries `sequence`, normalized `event_type` and `content`.
Raw bytes remain in StreamRecord/Artifact storage.

## 4. Delivery, context and Artifact events

| Topic | Unique semantic |
|---|---|
| `oneshot.delivery.dead_letter` | Delivery cannot be automatically reserved again. |
| `oneshot.context.created` | Provider context metadata persisted. |
| `oneshot.context.acquired` | Context exclusively held by one Run. |
| `oneshot.context.released` | Terminal Run returned a resumable context to active. |
| `oneshot.context.invalidated` | Provider proved context unusable; no replacement created. |
| `oneshot.context.revoked` | Owner/admin permanently revoked context use. |
| `oneshot.artifact.created` | Immutable content became owner-readable. |

## 5. Delivery and ordering guarantees

1. Persist state/event/outbox in the same transaction where required.
2. Event publication may be at-least-once; consumers deduplicate by persisted
   cursor/event ID.
3. Run output is ordered by Run-local sequence.
4. Task state events are ordered by aggregate version.
5. Notification delivery retry does not publish a new Run lifecycle.
6. A dropped in-process Event Bus message must be recoverable by replay.
