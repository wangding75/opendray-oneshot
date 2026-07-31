# OD-OS-00 Repository inventory

## Counts at imported baseline

- Files: 1,297 before One-shot planning files were added.
- Go files: 476.
- Go test files: 185.
- Dart files: 142.
- TypeScript/TSX files: 185.
- Internal Go package directories: 55.
- Existing migration files: 81.

## Major surfaces

- `cmd/opendray`: Gateway executable.
- `internal/session`: interactive PTY Session domain.
- `internal/channel`: channel transport, hub, commands, Telegram adapter and related routing.
- `internal/catalog` and `internal/providers`: provider metadata and runtime integrations.
- `internal/store`: SQL stores and migrations.
- `app/web`: React/Vite web client.
- `app/mobile`: Flutter mobile client.
- `third_party/xterm`: in-repository mobile terminal fork.

## Baseline CI requirements

- Node 22 and pnpm from the root `packageManager` field.
- Web build before Go tests/build because `internal/web/dist` is embedded.
- Go 1.25.
- Go vet, race tests, build and golangci-lint.
- Zero-dependency i18n parity check.
