# One-shot operations and integration guide

## Status

The One-shot source implementation is complete through `OD-OS-26`. It is an
opt-in execution domain that is independent from interactive PTY Sessions.
Source gates can run in a reduced offline toolchain; production acceptance
still requires Go 1.25, a real PostgreSQL instance, Flutter/Android tooling,
real provider CLIs and a Telegram Bot.

## Enablement

One-shot remains disabled by default. Configure the existing One-shot section
in `config.toml` or through the corresponding environment variables:

```toml
[oneshot]
enabled = true
worker_count = 1
attachment_max_bytes = 20971520
attachment_ttl = "24h"
codex_minimum_version = "0.132.0"
claude_minimum_version = "2.1.146"
```

Setting `enabled = false` disables the One-shot API and worker without changing
Session routes, PTY startup, terminal WebSockets or Session channel routing.
`worker_count` controls worker concurrency while One-shot is enabled. Minimum
provider versions can also be overridden with
`OPENDRAY_ONESHOT_CODEX_MINIMUM_VERSION` and
`OPENDRAY_ONESHOT_CLAUDE_MINIMUM_VERSION`; lowering them requires a local
create/resume/cancel/JSONL compatibility run.

## API and events

The control plane is mounted below `/api/v1/oneshot` and uses the existing
bearer middleware. The frozen contract is in
`docs/development/oneshot/contracts/http-api.md`.

Core resources:

- Task creation/list/read/continue/cancel/retry;
- Run read, event replay and Artifact download;
- Task and Run WebSocket replay with opaque cursors;
- staged attachment create/read/delete;
- owner/project-scoped notification preferences and transactional Outbox.

Clients must use the One-shot capability extension returned by
`/api/v1/providers`. PTY provider fields do not imply One-shot resume or
attachment support.

## Telegram

Explicit One-shot commands:

```text
/run --project <id> --provider <id> --workspace <absolute-path> -- <prompt>
/tasks [project_id]
/task <task_id>
/continue <task_id> <prompt>
/cancel <task_id> [project_id]
/retry <task_id> [project_id]
```

Plain Telegram text remains in the Session/PTy routing domain. A reply enters
One-shot only when it targets the exact outbound One-shot result message.
Telegram file identifiers are opened by the transport and staged through the
same attachment service used by mobile; Bot tokens and Telegram file URLs are
never stored as provider references.

## Mobile

The mobile application exposes `Agent Tasks` as a first-level feature. It uses
the One-shot REST/WebSocket APIs, supports Task creation/list/detail,
continuation, cancel/retry, event replay, Artifact integrity verification and
optional Telegram completion notification. Attachment controls are hidden
unless the selected One-shot adapter explicitly advertises support.

## Attachment policy

The server controls filenames, storage keys and expiry. It validates maximum
size, detects MIME from content, checks declared/detected compatibility,
calculates SHA-256, and binds references to a Delivery inside the same database
transaction. Cross-owner, cross-project, expired and deleted references are
rejected.

The current implementation provides deterministic content-policy validation;
it does not include an antivirus engine. Deployments that require malware
scanning must insert a scanner before changing a staged attachment to `ready`.

## Migration

Apply migrations in normal order. `0086_oneshot_attachments.sql` adds:

- `oneshot_staged_attachments`;
- `oneshot_delivery_attachments`;
- `oneshot_notification_preferences`.

It also adds a unique Task/Delivery key required by the attachment foreign key.
The migration does not alter or reference Session business tables.

Before production migration:

1. back up the development/production database;
2. run the migration against a representative clone;
3. verify indexes, foreign keys and lock duration;
4. run create/continue/retry with owned and cross-owner attachment fixtures;
5. run Outbox claim/retry/restart tests.

## Rollback

Application rollback is safe while no new One-shot attachment references need
to be read by the previous binary. Disable One-shot API/worker first, drain
leases and notification Outbox, then roll back the binary.

Database rollback is intentionally manual because dropping attachment and
preference tables is destructive. Preserve tables by default. Drop them only
after confirming there are no required staged bytes, Delivery bindings or
notification preferences. Never modify historical migration files.

## Reproducible source gates

```bash
scripts/oneshot/check-contracts.sh
scripts/oneshot/check-control-plane.sh
scripts/oneshot/check-final-hardening.sh
scripts/oneshot/check-pty-regression.sh
scripts/oneshot/check-known-issue-fixes.sh
```

Required final runtime gate on a provisioned host:

```bash
PTY_REGRESSION_REQUIRE_GO=1 \
PTY_REGRESSION_REQUIRE_MOBILE=1 \
scripts/oneshot/check-pty-regression.sh
```

Also run Go 1.25 full vet/race/lint/build, live PostgreSQL integration,
Flutter analyze/test/release APK, real Codex/Claude smoke, Telegram Bot E2E and
mobile weak-network/reconnect tests.
