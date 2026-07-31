# One-shot error-code contract

- Contract status: Frozen by `OD-OS-03`
- Namespace: `oneshot.*`
- Machine source: `fixtures/oneshot-contract.json`

Codes are stable API and event identifiers. Text may improve, but code meaning
and HTTP mapping require a contract change.

| Code | HTTP | Retryable | Meaning |
|---|---:|:---:|---|
| `oneshot.disabled` | 503 | no | One-shot disabled; PTY remains available. |
| `oneshot.unauthorized` | 401 | no | No valid principal. |
| `oneshot.forbidden` | 403 | no | Required One-shot scope missing. |
| `oneshot.invalid_request` | 400 | no | Invalid body, query or path input. |
| `oneshot.idempotency_required` | 400 | no | Required Idempotency-Key missing. |
| `oneshot.idempotency_conflict` | 409 | no | Same key reused with different payload. |
| `oneshot.task_not_found` | 404 | no | Task absent or not visible. |
| `oneshot.run_not_found` | 404 | no | Run absent or not visible. |
| `oneshot.artifact_not_found` | 404 | no | Artifact absent or not visible. |
| `oneshot.context_not_found` | 404 | no | RuntimeContext absent. |
| `oneshot.context_owner_mismatch` | 403 | no | Context owner/project/provider mismatch. |
| `oneshot.unsupported_provider` | 422 | no | No enabled non-interactive adapter. |
| `oneshot.provider_unavailable` | 503 | yes | CLI, credential or runtime dependency unavailable. |
| `oneshot.resume_unsupported` | 422 | no | Adapter cannot resume. |
| `oneshot.resume_failed` | 409 | no | Specified context could not be restored; no replacement was created. |
| `oneshot.invalid_transition` | 409 | no | Command illegal from current state. |
| `oneshot.run_conflict` | 409 | yes | Task/context already has an active Run. |
| `oneshot.queue_unavailable` | 503 | yes | Delivery could not be durably queued/reserved. |
| `oneshot.delivery_exhausted` | 503 | no | Pre-execution attempts exhausted; dead-lettered. |
| `oneshot.execution_failed` | 502 | no | Provider process produced failed outcome. |
| `oneshot.output_persist_failed` | 500 | yes | Raw/normalized output persistence failed. |
| `oneshot.artifact_unavailable` | 503 | yes | Artifact storage temporarily unavailable. |
| `oneshot.cancel_failed` | 500 | yes | Process-tree cancellation/reconciliation failed. |
| `oneshot.timeout` | 504 | no | Deadline exceeded and process terminated. |
| `oneshot.rate_limited` | 429 | yes | Principal/provider/channel limit reached. |
| `oneshot.internal` | 500 | yes | Unexpected internal failure; diagnose by request ID. |

## Mapping rules

- Authentication/authorization errors do not reveal whether an inaccessible
  resource exists.
- `retryable=true` is advisory; clients use bounded backoff and must preserve
  the same Idempotency-Key for the same operation.
- Provider authentication failure may map to `oneshot.provider_unavailable`
  with sanitized details; secrets never appear.
- A provider non-zero exit maps to `oneshot.execution_failed` unless a more
  specific stable adapter error applies.
- `oneshot.timeout` and `oneshot.cancel_failed` are distinct: timeout describes
  the deadline outcome; cancel failure means cleanup could not be confirmed.
- Every error response includes `request_id`. Write failures are audited with
  error code and result, excluding prompt/secret bodies.
