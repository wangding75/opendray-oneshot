# OD-OS-25 Summary — Security, reliability, performance and fault injection

## Result

**PASS — source hardening gate.** Provisioned-environment stress and external
service fault injection remain pending.

- Baseline: `c218ab335728a91b3cbe9e90bdcfcf649341a9a5`
- Branch: `feat/oneshot-agent`
- Final commit: recorded in the delivery receipt generated after report freeze
- Source period: 2026-07-28

## Security and reliability controls

- Owner/project checks for staged attachment create/read/delete/bind.
- Cross-owner, cross-project, deleted and expired references rejected.
- Basename-only filename policy; traversal, control characters and oversized
  names rejected.
- Maximum upload enforced at HTTP and service layers.
- MIME detected from content and compared to declared type; executable and
  spoofed inputs rejected.
- SHA-256 and byte length recorded before ready status.
- Server-generated storage keys; storage paths not returned to clients.
- Telegram Bot token/file URL never persisted as a provider reference.
- Byte cleanup on metadata transaction failure and duplicate-source races.
- Notification failures retry only Outbox delivery and never create a Run.
- Output parsers fuzzed for malformed and ambiguous JSONL without panic or
  cross-run state contamination.
- Static checks reject shell trampolines and Session/PTY imports in One-shot.

## Fault and negative tests

Covered by executable source fixtures:

- traversal, oversize, MIME spoof and unsupported executable attachment;
- storage success followed by metadata failure;
- duplicate source reference and idempotent notification delivery;
- transport failure schedules Outbox retry only;
- exact outbound notification reply binding;
- Telegram attachment descriptor does not expose Bot token;
- provider attachment capability rejection;
- malformed provider output fuzz;
- PTY baseline mutations for launch, input, conversation scope, mobile stream
  and test matrix removal.

## Results

| Gate | Result |
|---|---:|
| Attachment race | PASS |
| Attachment coverage | 47.1% |
| Attachment fuzz | PASS |
| Provider parser race | PASS |
| Provider adapter coverage | 77.4% |
| Provider parser fuzz | PASS |
| API race | PASS, 14.2% |
| Channel adapter race | PASS, 20.9% |
| Notification race | PASS, 46.8% |
| AppWire race | PASS, 18.8% |
| Architecture boundary | PASS |
| PTY baseline / mutations | 16/16 and 5/5 PASS |

## Limitations of the evidence

The sandbox does not provide Go 1.25, a live PostgreSQL database, Flutter,
Android build tooling, real provider CLIs or Telegram credentials. Therefore
full-repository race/lint/build, real DB lock/failure tests, high-concurrency
worker/process pressure, mobile weak-network tests and external rate-limit
fault injection are not reported as passed.
