# Agent Instructions

## Configuration architecture

- Make enforcement policy 100% configuration driven. Configuration defines rule conditions, command descriptions, argument roles, protected targets, exemptions, and enforcement actions.
- Use Go for generic parsing, fact extraction, configuration validation, and rule evaluation. Do not encode policy in command-specific branches, fixed executable or extension lists, implicit exemptions, or hidden defaults.
- When configuration cannot express a required rule, expose, extend, or fix a reusable primitive. Express the rule through that primitive in configuration. Do not add a special case for the command or incident.
- Store shipped policy defaults as explicit configuration data under the same schema as user rules. After the required primitives exist, add and change rules only through configuration.
- Fix false positives in the responsible primitive or configuration rule. Preserve independent facts and checks, and reject invalid or unsupported configuration with a clear error instead of guessing policy.

## Development

- Use `make test`, `make lint`, and `make check` instead of direct broad Go commands.
- Targeted package tests are acceptable for narrow debugging, for example `go test ./internal/rules -run TestName`.
- Use `make deploy` for local deployment. It builds and signs the current tree to the active per-user binary path, then restarts the supervised user service.
- If the binary is already deployed and only the daemon needs refreshing, use `make daemon-restart`; it keeps launchd/systemd as the process owner.
- Edits to `~/.config/agent-gate/config.toml` auto-reload via fsnotify with a 200ms debounce, so config-only changes do not need a restart. Verify reload by tailing `~/.local/state/agent-gate/agent-gate.jsonl` for `config reloaded`.
- Check the running daemon with `make daemon-status` after deployment-sensitive changes.
- Architecture cutover boundary: commit `b2ca7c7` and local daemon `daemon listening` at `2026-05-09T19:26:29.009711-07:00` in `~/.local/state/agent-gate/agent-gate.jsonl` mark the durable-intake/sync-deferred cutover. Query history before that timestamp as legacy pre-cutover audit behavior, and query history at or after that timestamp as post-cutover durable-intake-backed behavior.
- Data queries across the cutover: before `2026-05-09T19:26:29.009711-07:00`, legacy audit outputs are the only queryable source; at or after that timestamp, intake records show event receipt/replay state and audit outputs show derived outcomes.
- On macOS, signing uses `config.mk`/`CERT_ID` when present or the first Developer ID Application identity. The hardened runtime signature includes `packaging/macos/agent-gate.entitlements` so Homebrew PCRE2 can load.
- Hooks must stay transport-only. Enforcement, provider detection, payload enrichment, response formatting, and audit logging belong in the daemon.
- Use `make proto` after editing protobuf definitions; generated files are managed by Buf.

## Hook Inventory

`HOOKS.md` is the hook inventory and install-template reference. Keep it focused on registered hook events and template behavior.
