# Agent Instructions

## Development

- Use `make test`, `make lint`, and `make check` instead of direct broad Go commands.
- Targeted package tests are acceptable for narrow debugging, for example `go test ./internal/rules -run TestName`.
- Use `make deploy` for local deployment. It builds and signs the current tree to the active per-user binary path, then restarts the supervised user service.
- If the binary is already deployed and only the daemon needs refreshing, use `make daemon-restart`; it keeps launchd/systemd as the process owner.
- Edits to `~/.config/agent-gate/config.toml` auto-reload via fsnotify with a 200ms debounce, so config-only changes do not need a restart. Verify reload by tailing `~/.local/state/agent-gate/agent-gate.jsonl` for `config reloaded`.
- Check the running daemon with `make daemon-status` after deployment-sensitive changes.
- On macOS, signing uses `config.mk`/`CERT_ID` when present or the first Developer ID Application identity. The hardened runtime signature includes `packaging/macos/agent-gate.entitlements` so Homebrew PCRE2 can load.
- Hooks must stay transport-only. Enforcement, provider detection, payload enrichment, response formatting, and audit logging belong in the daemon.
- Use `make proto` after editing protobuf definitions; generated files are managed by Buf.

## Hook Inventory

`HOOKS.md` is the hook inventory and install-template reference. Keep it focused on registered hook events and template behavior.
