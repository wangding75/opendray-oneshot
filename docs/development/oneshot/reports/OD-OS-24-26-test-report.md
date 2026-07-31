# OD-OS-24/25/26 Test Report

## Verdict

**Source gate: PASS**

**Provisioned runtime gate: PENDING**

## Environment

- Repository branch: `feat/oneshot-agent`
- Baseline: `c218ab335728a91b3cbe9e90bdcfcf649341a9a5`
- Sandbox Go: 1.23.2 isolated compatibility modules
- Required repository Go: 1.25
- Flutter/Dart: unavailable
- Live PostgreSQL: unavailable
- External Provider/Telegram credentials: unavailable

## Machine-readable/source checks

| Check | Result |
|---|---:|
| Contract schema/docs/self-test | PASS — 16 routes |
| Architecture boundaries/self-test | PASS |
| REST/WS/Telegram control source gate | PASS |
| Attachment static/security gate | PASS |
| Attachment vet/race | PASS |
| Attachment coverage | 47.1% |
| Attachment fuzz | PASS |
| Provider parser vet/race | PASS |
| Provider adapter coverage | 77.4% |
| Provider parser fuzz | PASS |
| API isolated race | PASS — 14.2% |
| Channel adapter isolated race | PASS — 20.9% |
| Notification isolated race | PASS — 46.8% |
| AppWire isolated race | PASS — 18.8% |
| Telegram/Slack/Discord/Feishu | PASS |
| Mobile Agent Tasks source contract | PASS |
| i18n parity | PASS |
| PTY source baseline | 16/16 PASS |
| PTY negative mutation tests | 5/5 PASS |
| task-state schema | PASS |
| git diff whitespace | PASS |

## Security fixtures

- cross-owner/project attachment binding rejection;
- traversal, control characters, oversize and MIME spoof rejection;
- no Telegram token URL or storage path exposure;
- storage cleanup after persistence failure;
- provider capability rejection;
- notification idempotency and retry-only behavior;
- malformed JSONL parser fuzz;
- exact outbound-message reply binding.

## Skipped / pending

No PASS is claimed for:

- full Go 1.25 repository vet/race/lint/build;
- real PostgreSQL migration/lease/failure/restart/rollback;
- Flutter analyze/test/APK and device weak-network tests;
- web locked-dependency lint/build;
- real Codex/Claude and Telegram/mobile E2E;
- antivirus malware scanning.

## Evidence

- `docs/development/oneshot/evidence/OD-OS-24-26-validation.txt`
- `docs/development/oneshot/evidence/OD-OS-24-26-pty-regression.txt`
- `docs/development/oneshot/evidence/OD-OS-25-hardening.txt`
