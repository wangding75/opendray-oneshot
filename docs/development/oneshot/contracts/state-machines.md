# One-shot state-machine contract

- Contract status: Frozen by `OD-OS-03`
- Machine source: `fixtures/oneshot-contract.json`

State changes must use compare-and-swap or equivalent transactional guards.
Direct arbitrary status assignment is prohibited.

## 1. Task

Initial state: `pending`

```text
pending -> queued -> running -> completed
   |          |          |----> waiting_input -> queued
   |          |          |----> failed --------> queued
   |          |          |----> timed_out -----> queued
   |          |          `----> cancelled
   |          |----> failed
   |          `----> cancelled
   |----> failed
   `----> cancelled

completed --continue--> queued
```

Allowed transitions:

| From | To | Command | Required guard |
|---|---|---|---|
| `pending` | `queued` | `dispatch` | Request validated; feature/provider enabled. |
| `pending` | `cancelled` | `cancel` | No Run started. |
| `pending` | `failed` | `reject` | Durable preparation failed. |
| `queued` | `running` | `start_run` | Delivery reserved; Run starts. |
| `queued` | `cancelled` | `cancel` | Cancel before child start. |
| `queued` | `failed` | `dispatch_failed` | Non-retryable/exhausted dispatch. |
| `running` | `waiting_input` | `run_waiting_input` | Process exited; output committed. |
| `running` | `completed` | `run_completed` | Output/artifacts committed. |
| `running` | `failed` | `run_failed` | Failure/compensation persisted. |
| `running` | `cancelled` | `run_cancelled` | Process tree terminated or absent. |
| `running` | `timed_out` | `run_timed_out` | Deadline cleanup resolved/recorded. |
| `waiting_input` | `queued` | `continue` | Owned active RuntimeContext; new Delivery. |
| `waiting_input` | `cancelled` | `cancel` | Owner-authorized. |
| `completed` | `queued` | `continue` | Owned active RuntimeContext; new Delivery. |
| `failed` | `queued` | `retry_or_continue` | New Delivery; continue requires context. |
| `timed_out` | `queued` | `retry_or_continue` | New Delivery; continue requires context. |

`cancelled` is terminal. Quiescent outcomes do not change automatically; only
an explicit new command can create a new Delivery/Run cycle. Earlier Run
outcomes remain immutable.

## 2. Delivery

Initial state: `pending`

```text
pending ----reserve----> reserved ----ack----> acknowledged
   |                       |  |----nack----> retry_wait --reserve--+
   `----cancel---------->  |  |----dead----> dead_letter           |
retry_wait --cancel-----> cancelled <----cancel--------------------+
```

Terminal states: `acknowledged`, `dead_letter`, `cancelled`.

A `reserved` row must carry `lease_owner` and `lease_until`. A lease expiry is
reconciled transactionally; it does not permit two workers to create two Runs.
Once `run_id` is assigned, the same Delivery cannot create another Run.

## 3. Run

Initial state: `created`

```text
created -> starting -> running -> collecting_output -> completed
   |          |           |              |-----------> waiting_input
   |          |           |              |-----------> failed
   |          |           |              |-----------> cancelled
   |          |           |              `-----------> timed_out
   |          |           |----> failed/cancelled/timed_out
   |          `---------------> failed/cancelled/timed_out
   `--------------------------> cancelled
```

Terminal states:

```text
waiting_input, completed, failed, cancelled, timed_out
```

Terminal Run states have no outgoing transitions. `finished_at` is required.
Cancel/timeout status is written only after the process tree is terminated or
already absent, with cleanup failure recorded when necessary.

## 4. RuntimeContext

Initial state: `active`

```text
active --acquire--> busy --release--> active
  |                   |----invalidate--> invalid
  |                   `----revoke------> revoked
  |----invalidate----------------------> invalid
  `----revoke--------------------------> revoked
```

`invalid` and `revoked` are terminal. Context acquisition requires exact owner,
project and provider match plus an optimistic lock. No concurrent Run may use
one context.

## 5. Cross-aggregate transaction rules

1. Create Task + initial Delivery in one transaction.
2. Reserve Delivery + create/attach Run with uniqueness guards.
3. Task `running` and Run `starting/running` must agree.
4. Persist output and terminal Run outcome before the matching Task outcome.
5. ACK Delivery only after the durable outcome is known.
6. Continue/retry first creates a new Delivery; it never mutates an old Run.
7. RuntimeContext `busy` acquisition and new active Run uniqueness are atomic.
8. Notification outbox failure cannot revert or re-execute a completed Run.

## 6. Canonical machine state sets

These sets are machine-checked and are the only valid persisted values:

- Task: `pending`, `queued`, `running`, `waiting_input`, `completed`, `failed`, `cancelled`, `timed_out`.
- Delivery: `pending`, `reserved`, `retry_wait`, `acknowledged`, `dead_letter`, `cancelled`.
- Run: `created`, `starting`, `running`, `collecting_output`, `waiting_input`, `completed`, `failed`, `cancelled`, `timed_out`.
- RuntimeContext: `active`, `busy`, `invalid`, `revoked`.
