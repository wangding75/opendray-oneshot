# OD-OS-00 Summary — 开发分支、环境预检与基线登记

## Status

PASS — baseline and environment limitations recorded

## Timing

- Started: `2026-07-27T11:49:15+0800`
- Ended: `2026-07-27T11:50:30+0800`

## Source and Git

- Source archive SHA-256: `ee13137c9c99ae653379ac36d1156b0f3813895e0582f915d505cf14ee769bbf`
- Uploaded archive contained no `.git` history.
- Local imported baseline Commit: `649c7cb0a03b156a2ba4690be319c0086751b6a0`
- Development branch: `feat/oneshot-agent`
- Task implementation Commit: `f0535fcb1f7247741d4ab6a12a80d9302cff7fe1`
- Branch remote: sandbox-local bare repository at `/mnt/data/opendray-origin.git`.
- GitHub Push: not performed because no user Fork remote or credentials were supplied.

## Completed

- Initialized a local Git repository from the uploaded source snapshot.
- Created and pushed `main` and `feat/oneshot-agent` to a sandbox-local bare remote.
- Copied the complete taskbook and master execution instruction into the repository.
- Added a repeatable One-shot preflight script.
- Recorded source provenance, repository inventory, tool versions and baseline command results.
- Marked OD-OS-00 complete and advanced task state to OD-OS-01.

## Baseline results

| Gate | Result | Evidence |
|---|---|---|
| i18n parity | PASS | English/Spanish/Chinese token parity passed |
| Go tests | ENVIRONMENT BLOCKED | Repository requires Go 1.25; installed local Go is 1.23.2 and outbound toolchain download is unavailable |
| Web install/lint/build | ENVIRONMENT BLOCKED | pnpm is absent and Corepack cannot download it without network access |
| Flutter analyze/test/build | ENVIRONMENT BLOCKED | Flutter and Dart are not installed |
| PostgreSQL checks | ENVIRONMENT BLOCKED | `psql` is not installed |

These are recorded environment constraints rather than product-code failures. They remain mandatory gates for later provisioned CI and final OD-OS-26 acceptance.

## Evidence

- `docs/development/oneshot/evidence/OD-OS-00-preflight.txt`
- `docs/development/oneshot/evidence/OD-OS-00-source-provenance.md`
- `docs/development/oneshot/evidence/OD-OS-00-repository-inventory.md`

## Compatibility and product impact

- Product runtime code changed: no.
- PTY behavior changed: no.
- Database schema changed: no.
- Telegram behavior changed: no.
- Flutter behavior changed: no.

## Remaining limitations

- Imported baseline cannot be mapped to an upstream GitHub Commit because the uploaded ZIP contained no Git metadata.
- GitHub branch Push must be performed later against the user's Fork.
- Full Go/Web/Mobile/PostgreSQL baseline must run in a provisioned environment or CI.

## Next task

`OD-OS-01 — 现有 PTY Session 行为特征化与回归门禁`
