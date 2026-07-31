# One-shot HTTP and WebSocket contract

- Contract status: Frozen by `OD-OS-03`; attachment extension approved by `OD-OS-24`
- Base path: `/api/v1/oneshot`
- Authentication: existing combined admin/integration bearer middleware
- Machine source: `fixtures/oneshot-contract.json`

The new API is separate from `/api/v1/sessions` and `/api/v1/custom-tasks`.
No One-shot operation accepts a Session ID or creates a PTY.

## 1. Routes

| Protocol | Method | Path | Scope | Idempotency | Success |
|---|---|---|---|---|---|
| HTTP | POST | `/api/v1/oneshot/tasks` | `oneshot:task:create` | required | 202 |
| HTTP | GET | `/api/v1/oneshot/tasks` | `oneshot:task:read` | none | 200 |
| HTTP | GET | `/api/v1/oneshot/tasks/{task_id}` | `oneshot:task:read` | none | 200 |
| HTTP | POST | `/api/v1/oneshot/tasks/{task_id}/continue` | `oneshot:task:continue` | required | 202 |
| HTTP | POST | `/api/v1/oneshot/tasks/{task_id}/cancel` | `oneshot:task:cancel` | optional | 200/202 |
| HTTP | POST | `/api/v1/oneshot/tasks/{task_id}/retry` | `oneshot:task:retry` | required | 202 |
| HTTP | GET | `/api/v1/oneshot/tasks/{task_id}/runs` | `oneshot:run:read` | none | 200 |
| HTTP | GET | `/api/v1/oneshot/runs/{run_id}` | `oneshot:run:read` | none | 200 |
| HTTP | GET | `/api/v1/oneshot/runs/{run_id}/events` | `oneshot:run:read` | none | 200 |
| HTTP | GET | `/api/v1/oneshot/runs/{run_id}/artifacts` | `oneshot:artifact:read` | none | 200 |
| HTTP | GET | `/api/v1/oneshot/artifacts/{artifact_id}` | `oneshot:artifact:read` | none | 200 |
| HTTP | POST | `/api/v1/oneshot/attachments` | `oneshot:task:create` | none | 201 |
| HTTP | GET | `/api/v1/oneshot/attachments/{attachment_id}` | `oneshot:artifact:read` | none | 200 |
| HTTP | DELETE | `/api/v1/oneshot/attachments/{attachment_id}` | `oneshot:task:create` | none | 204 |
| WS | GET | `/api/v1/oneshot/tasks/stream` | `event:subscribe:oneshot.task.*` | none | 101 |
| WS | GET | `/api/v1/oneshot/runs/{run_id}/stream` | `event:subscribe:oneshot.run.*` | none | 101 |

The static `/tasks/stream` route must be mounted before `/{task_id}`.
Every route performs owner/project authorization after authentication. Admin
bypass semantics may follow existing platform policy, but bypass actions remain
audited.

## 2. Create Task

```http
POST /api/v1/oneshot/tasks
Idempotency-Key: <opaque key>
Content-Type: application/json
```

```json
{
  "project_id": "prj_demo",
  "provider_id": "codex",
  "prompt": "Run the focused unit tests and fix the failure.",
  "source": {"kind": "mobile", "client_request_id": "mob_01"},
  "attachments": [],
  "timeout_seconds": 3600
}
```

The server derives principal fields from authentication. Project path and
credentials are server-resolved. Response 202 contains the Task and initial
Delivery ID.

## 3. Continue and retry

`continue` requires an owned `RuntimeContext` and follow-up input. It creates a
new Delivery and later a new Run. It never writes to a live terminal.

`retry` creates a new Delivery/Run for a failed or timed-out execution. It does
not reuse the old Run and does not mean “resend the Telegram result”.

## 4. Cancel

Cancel is idempotent. A queued Task returns 200 once queue responsibility is
cancelled. An active child may return 202 while termination is in progress and
200 once the process tree is terminated or already absent. The API must not
report cancellation success based only on a database status update.

## 5. List, sort and pagination

All list endpoints use:

```text
cursor: optional opaque value
limit:  default 50, minimum 1, maximum 200
sort:   endpoint allow-list, default -created_at
```

Response:

```json
{"items": [], "next_cursor": null}
```

A cursor encodes stable sort values plus resource ID. Clients must not construct
or modify it. Ownership filters are applied before pagination.

## 6. WebSocket and replay

WebSocket frames use the existing event shape with an added replay cursor:

```json
{
  "topic": "oneshot.run.output",
  "ts": "2026-07-27T06:00:01.123456Z",
  "cursor": "oseq_42",
  "data": {}
}
```

Clients may reconnect with `?cursor=<last-confirmed-cursor>`. Replay comes from
persisted events, not the in-process Event Bus. Slow consumers are disconnected
with a documented close code after their bounded queue fills; publishers are
not allowed to lose persisted events.

## 7. Idempotency

`Idempotency-Key` is required for create, continue and retry. Scope:

```text
principal_kind + principal_id + HTTP method + canonical route + key
```

Same payload returns the original response. Different payload returns HTTP 409
with `oneshot.idempotency_conflict`. Cancel is semantically idempotent; a key is
accepted but not required.

## 8. Error envelope

All One-shot errors use:

```json
{
  "error": {
    "code": "oneshot.run_conflict",
    "message": "task already has an active run",
    "request_id": "req_01HXYZ",
    "retryable": true,
    "details": {"task_id": "otk_01HXYZ"}
  }
}
```

Messages and details must not expose secrets, provider tokens, raw credentials,
unregistered absolute paths or unsanitized command lines.

## 9. Artifact response

Artifact download verifies Task ownership and returns immutable bytes with:

```text
Content-Type
Content-Length
Digest: sha-256=<base64 digest>
Content-Disposition
ETag
```

Range support is optional in v1; incorrect digest or missing backing content
returns a structured error rather than partial success.

## 10. Audit

Every write and denied owner/scope check records:

```text
actor_kind, actor_id, action, subject_kind, subject_id,
request_id, source, project_id, provider_id, result
```

Conditional metadata includes Task/Run/RuntimeContext IDs, a hash of the
idempotency key and error code. Raw prompt bodies and secrets are excluded.

## 10. Staged attachments (`OD-OS-24` extension)

`POST /api/v1/oneshot/attachments` accepts a multipart `file` plus
`project_id`, optional `source_kind`, and optional opaque `source_ref`.
The server enforces owner/project isolation, filename safety, configured size
limits, detected MIME policy, SHA-256 calculation, expiry and server-controlled
storage keys. The response is a staged attachment reference (`oat_*`); it never
returns a local path, Telegram bot token URL or provider credential.

`GET` returns metadata only. `DELETE` removes an unbound staged attachment.
Task create/continue/retry bind staged references transactionally and reject
references owned by another principal/project, expired references, deleted
references and unsupported provider attachment capabilities.

This extension is independent from Session and PTY routes. Current provider
adapters advertise attachment support through their One-shot capability
descriptor; clients must hide attachment controls when the capability is false.
