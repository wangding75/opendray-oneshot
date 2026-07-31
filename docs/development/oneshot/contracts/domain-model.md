# One-shot domain model contract

- Contract status: Frozen by `OD-OS-03`
- Contract version: `1.0.0`
- Machine source: `fixtures/oneshot-contract.json`
- Machine schema: `schema/oneshot-contract.schema.json`
- Architecture boundary: `architecture.md`

## 1. Scope

This contract defines the One-shot Agent execution domain. It does not change
or extend the Interactive PTY `Session` aggregate. The canonical resources are:

```text
Task -> Delivery -> Run
  |                  |
  +-> RuntimeContext +-> StreamRecord -> StandardEvent
  +-------------------------------------> Artifact
```

All IDs are opaque strings. Clients must not infer ordering or ownership from
an ID. Server timestamps are UTC RFC3339Nano values.

## 2. Principal and ownership

Every Task is owned by an authenticated principal:

```text
principal_kind: admin | integration
principal_id:   authenticated principal identifier
project_id:     registered project/workspace
provider_id:    selected One-shot adapter
```

The owner tuple is immutable. A RuntimeContext is usable only when its
`principal_kind`, `principal_id`, `project_id` and `provider_id` exactly match
the requesting Task/Run. Run, event and Artifact authorization is always
derived through Task ownership; a client-supplied owner is never trusted.

## 3. Task

ID prefix: `otk_`

| Field | Type | Required | Immutable | Meaning |
|---|---|---:|---:|---|
| `id` | string | yes | yes | Stable Task ID. |
| `principal_kind` | enum | yes | yes | `admin` or `integration`. |
| `principal_id` | string | yes | yes | Authenticated owner ID. |
| `project_id` | string | yes | yes | Controlled workspace/project. |
| `provider_id` | string | yes | yes | One-shot provider adapter. |
| `source` | object | yes | yes | `api`, `telegram`, `mobile` or `web` origin snapshot and reply address metadata. |
| `prompt` | string | yes | yes | Original requested work. |
| `status` | enum | yes | no | `pending`, `queued`, `running`, `waiting_input`, `completed`, `failed`, `cancelled`, `timed_out`. |
| `current_run_id` | nullable string | no | no | Latest/current Run. |
| `runtime_context_id` | nullable string | no | no | Provider continuity metadata, never a Session ID. |
| `version` | integer | yes | no | Optimistic concurrency version starting at 1. |
| `created_at` | timestamp | yes | yes | Creation time. |
| `updated_at` | timestamp | yes | no | Last aggregate mutation. |

Invariants:

1. Owner, project, provider, source and original prompt never change.
2. A Task has at most one active Run.
3. `current_run_id` belongs to that Task.
4. `runtime_context_id` belongs to the same owner/project/provider tuple.
5. Continue and retry never rewrite an earlier Run; they create a new Delivery
   and Run.
6. `cancelled` is terminal for the Task. A new Task is required to execute
   again.
7. `completed`, `failed`, `timed_out` and `waiting_input` are quiescent outcome
   states. Only an explicit owner-authorized `continue` or `retry` command may
   open a new execution cycle.

## 4. Delivery

ID prefix: `odl_`

Delivery is execution dispatch, not channel notification delivery.

| Field | Type | Required | Immutable | Meaning |
|---|---|---:|---:|---|
| `id` | string | yes | yes | Delivery ID. |
| `task_id` | string | yes | yes | Owning Task. |
| `operation` | enum | yes | yes | `new`, `continue`, `retry`. |
| `requested_by_kind` | enum | yes | yes | Principal kind. |
| `requested_by_id` | string | yes | yes | Principal ID. |
| `input` | object | yes | yes | Prompt delta, attachment refs and options. |
| `idempotency_key` | string | yes | yes | Request key. |
| `payload_sha256` | string | yes | yes | Canonical request digest. |
| `status` | enum | yes | no | `pending`, `reserved`, `retry_wait`, `acknowledged`, `dead_letter`, `cancelled`. |
| `attempt` | integer | yes | no | Pre-execution reservation attempt. |
| `max_attempts` | integer | yes | yes | Automatic pre-execution limit. |
| `available_at` | timestamp | yes | no | Earliest reservation time. |
| `lease_owner` | nullable string | no | no | Worker lease owner. |
| `lease_until` | nullable timestamp | no | no | Lease expiry. |
| `run_id` | nullable string | no | no | At most one Run, immutable once assigned. |
| `last_error_code` | nullable string | no | no | Last dispatch error. |
| `created_at` | timestamp | yes | yes | Creation time. |
| `updated_at` | timestamp | yes | no | Last mutation. |

A reserved Delivery must have a lease owner and future lease expiry. Automatic
Delivery retry is allowed only for retryable failures before a new provider
child process starts. A model/process failure resolves the Delivery and leaves
a failed Run; `/retry` creates another Delivery and Run.

## 5. Run

ID prefix: `orn_`

| Field | Type | Required | Immutable | Meaning |
|---|---|---:|---:|---|
| `id` | string | yes | yes | Run ID. |
| `task_id` | string | yes | yes | Owning Task. |
| `delivery_id` | string | yes | yes | Unique originating Delivery. |
| `provider_id` | string | yes | yes | Adapter used by this Run. |
| `runtime_context_id` | nullable string | no | yes | Context used for resume, if any. |
| `status` | enum | yes | no | `created`, `starting`, `running`, `collecting_output`, `waiting_input`, `completed`, `failed`, `cancelled`, `timed_out`. |
| `pid` | nullable integer | no | no | Ordinary child PID while known. |
| `exit_code` | nullable integer | no | no | Provider process exit code. |
| `error_code` | nullable string | no | no | Stable One-shot error code. |
| `error_message` | nullable string | no | no | Sanitized diagnostic. |
| `started_at` | nullable timestamp | no | no | Process start time. |
| `finished_at` | nullable timestamp | no | no | Required in every terminal Run state. |
| `created_at` | timestamp | yes | yes | Run row creation time. |

Each Run starts at most one ordinary child process. It must not call
`pty.Start`, allocate a terminal, write a Session ring buffer or publish
`session.*` events. Run terminal states are immutable.

## 6. RuntimeContext

ID prefix: `orc_`

| Field | Type | Required | Immutable | Meaning |
|---|---|---:|---:|---|
| `id` | string | yes | yes | OpenDray context ID. |
| `principal_kind` | enum | yes | yes | Owner kind. |
| `principal_id` | string | yes | yes | Owner ID. |
| `project_id` | string | yes | yes | Workspace owner. |
| `provider_id` | string | yes | yes | Provider adapter. |
| `provider_context_id` | string | yes | yes | Provider-native resume identifier. |
| `workspace_path` | string | yes | yes | Server-resolved controlled workspace. |
| `status` | enum | yes | no | `active`, `busy`, `invalid`, `revoked`. |
| `version` | integer | yes | no | Optimistic concurrency version. |
| `created_at` | timestamp | yes | yes | Creation time. |
| `updated_at` | timestamp | yes | no | Last mutation. |

RuntimeContext contains no live process, `SessionID`, PID or PTY. `busy`
prevents concurrent resume. `invalid` and `revoked` are terminal. A resume
failure never silently creates a replacement context.

## 7. StreamRecord

ID prefix: `osr_`

`StreamRecord` is append-only raw stdout/stderr evidence. `sequence` is unique
and strictly increasing within a Run across both streams. `raw_artifact_id`
retains the original bytes even when UTF-8 decoding fails. `decode_status` is
`valid_utf8`, `lossy_utf8` or `binary`.

Required fields:

```text
id, run_id, sequence, stream, byte_offset, byte_length,
raw_artifact_id, decode_status, sha256, received_at
```

`text` is nullable. Raw bytes are authoritative.

## 8. StandardEvent

ID prefix: `ose_`

`StandardEvent` is an append-only adapter-normalized event. Required fields:

```text
id, run_id, sequence, type, adapter_id, adapter_version,
content, occurred_at
```

`source_stream_record_id` is nullable. An adapter event never replaces or
mutates raw output.

## 9. Artifact

ID prefix: `oar_`

Artifact kinds:

```text
raw_stdout, raw_stderr, structured_output, final_result,
file, log, attachment
```

Artifacts are immutable and content-addressed by SHA-256. `storage_key` is
server-controlled and must not accept an arbitrary client absolute path.
Authorization follows the owning Task.

## 10. Idempotency and source identity

`Idempotency-Key` is required for create, continue and retry. Identity scope:

```text
principal_kind + principal_id + method + canonical path + key
```

The stored key is bound to a SHA-256 digest of canonical JSON and immutable
attachment references. Same key/same payload returns the original response and
creates no new Task, Delivery or Run. Same key/different payload returns
`oneshot.idempotency_conflict`.

Telegram intake additionally has a unique `(channel_id, source_message_id)`
before Task creation. Channel notification retry is independent and must never
re-run the Agent.
