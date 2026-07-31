# OD-OS-26 Summary — Final PTY regression, dual-mode acceptance and delivery

## Result

**PASS — final source delivery gate.** Final runtime/release acceptance remains
`pending_runtime_validation` until the provisioned-host gates run.

- Baseline: `c218ab335728a91b3cbe9e90bdcfcf649341a9a5`
- Branch: `feat/oneshot-agent`
- Final commit: recorded in the delivery receipt generated after report freeze
- Source period: 2026-07-28

## Dual-mode acceptance

Source and architecture evidence proves:

- One-shot packages do not import Session or PTY runtime packages.
- Session packages do not import the One-shot execution domain.
- neutral Channel Core dispatches to adapters without writing PTY directly.
- plain Telegram text remains a Session fallback; only explicit commands or an
  exact One-shot result reply enter One-shot.
- One-shot migrations do not reference Session business tables.
- One-shot API/worker are opt-in and independently disabled.
- mobile Agent Tasks and existing terminal features use separate models and
  routes.

## Regression results

- PTY executable source baseline: 16/16 PASS.
- PTY checker negative mutation suite: 5/5 PASS.
- Session Channel Adapter: PASS.
- Channel Core: PASS.
- Telegram, Slack, Discord and Feishu transports: PASS.
- shared Channel Delivery: PASS.
- i18n parity: English/Spanish/Chinese 100%.
- One-shot contracts: 7 resources, 4 state machines, 16 routes, 23 events and
  26 errors — PASS.
- attachment/security hardening: PASS.
- `git diff --check`: PASS.

## Documentation and operations

Updated:

- `README.md` and `README.zh.md`;
- `CHANGELOG.md`;
- `config.example.toml`;
- governed HTTP contract and machine fixture;
- `docs/development/oneshot/OPERATIONS.md`;
- task book and machine task state;
- final acceptance, task summaries, test report and evidence.

Operations documentation includes enablement, API/WS, Telegram commands,
mobile behavior, attachment policy, migration, rollback and reproducible gates.

## Delivery scope

The source archive contains the complete repository at final HEAD, not an
incremental patch. The Git Bundle contains the complete
`feat/oneshot-agent` history. Archive integrity, tracked-file count, Bundle
verification and clone-to-HEAD equality are verified after the single delivery
commit.

## Runtime gates still required

- Go 1.25: full `go vet`, `go test -race`, `golangci-lint`, binary build.
- Web lint/build with locked Node dependencies.
- live PostgreSQL migrations, leases, Outbox, restart and rollback.
- Flutter analyze/test/release APK and real device tests.
- real Codex and Claude create/continue/cancel/failure smoke.
- real Telegram and mobile cross-device E2E.

These are not source defects proven by current evidence, but they prevent a
claim of production-release acceptance.
