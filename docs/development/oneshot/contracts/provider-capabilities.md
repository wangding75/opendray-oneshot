# One-shot Provider Capability Freeze

## Scope

This document freezes the source-level non-interactive contract used by the
first One-shot providers. Interactive PTY manifests remain separate.

## Codex

Provider ID: `codex`

New execution shape:

```text
codex exec --json --color never [provider options] -C <absolute-workspace> -
```

Resume shape:

```text
codex exec --json --color never [provider options] -C <absolute-workspace> resume <thread-id> -
```

The prompt is supplied on stdin, never as shell text. The adapter parses JSONL,
preserves unknown lines as raw/unparsed events, and captures `thread_id` as the
provider context identity.

Declared capabilities:

```text
non_interactive: true
resume: true
structured_output: true
attachments: false
cancellation: true
```

## Claude Code

Provider ID: `claude-code`

New execution shape:

```text
claude -p --output-format stream-json --verbose [provider options]
```

Resume shape:

```text
claude -p --output-format stream-json --verbose [provider options] --resume <session-id>
```

The prompt is supplied on stdin. The adapter parses stream JSON, preserves raw
output, and captures `session_id` as the provider context identity.

Declared capabilities:

```text
non_interactive: true
resume: true
structured_output: true
attachments: false
cancellation: true
```

## Shared safety rules

- The executable path comes from the shared provider catalog.
- The child process uses an absolute workspace and an explicit environment.
- Secret environment values are redacted from diagnostics.
- Cancellation is performed by the One-shot ProcessSupervisor, not PTY input.
- Resume never falls back to a new provider context.
- RuntimeContext never references an Interactive Session.

## Validation status

Deterministic fake-CLI, golden-argument, JSONL/stream-JSON, cancellation, error
mapping, initial-context, and continuation tests run in the source gate. A live
provider smoke test remains a release gate when the corresponding CLI and
credentials are available.
