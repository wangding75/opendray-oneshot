# OD-OS-24 Summary — Cross-device synchronization, attachments and notifications

## Result

**PASS — source implementation and executable source gates.** Runtime/device
validation remains pending as listed below.

- Baseline: `c218ab335728a91b3cbe9e90bdcfcf649341a9a5`
- Branch: `feat/oneshot-agent`
- Final commit: recorded in the delivery receipt generated after report freeze
- Source period: 2026-07-28

## Design conclusion

Telegram, mobile and API now operate on the same durable Task/Delivery/Run
records. Attachments use one owner/project-scoped staging service and are bound
to a Delivery in the same transaction as task control. Notification routing is
an explicit owner/project preference and is independent from inbound Session or
PTY routing.

## Delivered

- `oat_*` staged attachment IDs, domain snapshot and lifecycle.
- Server-controlled filename/storage key, size limit, detected MIME policy,
  SHA-256, expiry and cleanup worker.
- PostgreSQL staged attachment, Delivery binding and notification-preference
  tables in migration `0086`.
- REST attachment create/read/delete under `/api/v1/oneshot`.
- Mobile multipart staging and optional Telegram completion notification.
- Telegram document/photo opening through Bot API without persisting token URLs.
- Transactional attachment binding for create/continue/retry.
- Explicit cross-device notification preference used before original Task reply
  address, allowing a Telegram continuation to receive the final result.
- Transactional terminal notification Outbox for Run terminal states and queued
  Task cancellation; stable notification idempotency keys.
- Principal, project, source and reply destination audit/log evidence.

## Defects found and fixed

1. Raw Telegram file IDs could otherwise become provider-facing references.
2. Mobile upload and Telegram attachment paths could diverge into separate
   lifecycle models.
3. Telegram continuation of a mobile-origin Task could still notify the
   original source instead of the latest explicit destination.
4. Task-only cancellation notification rendered a blank `Run:` line.
5. Attachment REST routes existed in code but were absent from the governed
   frozen contract. The OD-OS-24 extension now raises the route count to 16.

## Tests and evidence

- Attachment `go vet` and `go test -race`: PASS.
- Attachment coverage: 47.1%.
- Attachment filename/MIME fuzz: PASS, tens of thousands of inputs.
- Telegram/Slack/Discord/Feishu isolated transport tests: PASS.
- API, channel adapter, notification and appwire isolated race tests: PASS.
- Mobile source contract and three-language parity: PASS.
- PTY source baseline: 16/16 PASS; negative mutations: 5/5 PASS.

Evidence:

- `evidence/OD-OS-24-26-validation.txt`
- `evidence/OD-OS-24-26-pty-regression.txt`
- `evidence/OD-OS-25-hardening.txt`

## Impact

- API: three governed attachment routes added; existing 13 control/stream routes
  unchanged.
- Database: three One-shot-only tables and Delivery binding FK/index; no Session
  table reference.
- Telegram: explicit commands and exact reply binding preserved; attachments
  now use secure staging.
- Mobile: file picker/upload is capability-gated.
- PTY: no code path or schema change in the Session execution domain.

## Pending runtime gates

- Live PostgreSQL migration, contention, rollback and cleanup-worker restart.
- Flutter analyze/test/release build and device file picker.
- Real Telegram Bot download, duplicate Update and notification retry E2E.
- No antivirus engine is integrated; current scanning is deterministic
  filename/size/content-MIME policy.
