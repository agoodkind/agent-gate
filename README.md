# agent-gate

A unified hook handler for [Claude Code](https://code.claude.com), [Cursor](https://cursor.com), Codex, and Gemini CLI. It writes a structured audit trail with build metadata on every invocation and enforces configurable regex rules that can block unwanted actions across providers.

## What it does

Claude Code, Cursor, Codex, and Gemini CLI all expose lifecycle hook systems that call external binaries via stdin/stdout JSON. `agent-gate` sits at that boundary as a single Go binary and does two things:

1. **Audit.** Every hook invocation produces a structured JSON log entry with full payload details: tool inputs (command, file_path, old_string, new_string, content), prompts, agent messages, session metadata, and build provenance (commit, version, buildHash, dirty).

2. **Enforce.** Rules defined in TOML are evaluated against each incoming payload. The first matching rule blocks the action and sends the violation message back to the LLM. Rules support per-system event filtering, multi-condition AND logic, and audit-only mode (log without blocking).

## Installation

### One-liner (recommended)

Pulls the latest release tarball for your platform, installs the binary
to `${XDG_BIN_HOME:-$HOME/.local/bin}`, and merges hook templates into
your Claude, Codex, and Gemini config files. Existing user settings in
those files are preserved.

```sh
curl -fsSL https://raw.githubusercontent.com/agoodkind/agent-gate/main/install.sh | bash
```

Flags:

```sh
./install.sh --bin-only          # binary only, skip hook config updates
./install.sh --hooks-only        # update hook configs, skip download
./install.sh --no-claude         # opt out of Claude (additive: combine flags)
./install.sh --no-codex
./install.sh --no-gemini
./install.sh --bin-dir /opt/bin  # override $XDG_BIN_HOME
./install.sh --version v1.2.3    # pin to a specific release tag
```

`make install`, `make install-bin`, and `make install-hooks` are thin
wrappers around the script.

### From source

Requires Go 1.21 or later. `agent-gate` uses PCRE2 via cgo, so build
hosts must have PCRE2 and a working C toolchain available.

Install prerequisites:
- macOS: `brew install pcre2`
- Debian/Ubuntu: `sudo apt-get update && sudo apt-get install -y libpcre2-dev`
- Fedora/RHEL: `sudo dnf install pcre2-devel`
- Alpine: `apk add pcre2-dev build-base`

When building, set `CGO_ENABLED=1`. Rule patterns compile against system
`libpcre2-8` (PCRE2 10.x) via cgo. There is no bundled third-party Go
regex binding.

```sh
git clone https://github.com/agoodkind/agent-gate
cd agent-gate
make build               # writes dist/agent-gate
```

CI and release builds export `CGO_ENABLED=1` so PCRE2 JIT and match
limits are available at runtime.

## Wiring up hooks

The one-liner above wires hooks for all three tools automatically.
Templates live in [`hooks/`](hooks/) and the canonical inventory is in
[`.agent.md`](.agent.md). To re-apply after a binary move or template
change:

```sh
make install-hooks
```

To opt out of any tool, pass `--no-claude`, `--no-codex`, or
`--no-gemini` (combinable). Hook merges only touch the `.hooks` key,
so any other settings in the target config files are preserved.

## Hook event reference

Full JSON payload schemas for Claude, Cursor, Codex, and Gemini CLI are documented in [docs/hook-schemas.md](docs/hook-schemas.md).

**Sources:**
- Claude Code hooks: https://code.claude.com/docs/en/hooks
- Cursor hooks: https://cursor.com/docs/hooks
- Codex hooks: see repository docs / local integration contract
- Gemini CLI hooks: see repository docs / local integration contract

## Configuration

Config lives at the XDG config path:

```
$XDG_CONFIG_HOME/agent-gate/config.toml
~/.config/agent-gate/config.toml  (default)
```

See [config.toml.example](config.toml.example) for a complete annotated example.

### Rules

Each `[[rules]]` block defines one enforcement rule. Agent-gate evaluates every applicable rule and reports every concrete match in the block message.

```toml
[[rules]]
name        = "no-shell-redirection"
description = "Block shell output/error redirection"
events      = ["PreToolUse", "beforeShellExecution"]
field_paths = ["tool_input.command", "command"]
pattern     = '(\d+>&\d+|>&\d+|&>|\|&|>/dev/null)'
action      = "block"
violation_message = "Shell redirection is not permitted."
```

| Field | Description |
|-------|-------------|
| `name` | Unique identifier for audit log and error messages. |
| `events` | Hook events this rule applies to. Empty = all events. |
| `claude_events` | Claude-only event filter (takes precedence over `events` for Claude). |
| `cursor_events` | Cursor-only event filter (takes precedence over `events` for Cursor). |
| `codex_events` | Codex-only event filter (takes precedence over `events` for Codex). |
| `gemini_events` | Gemini-only event filter (takes precedence over `events` for Gemini). |
| `field_paths` | Dot-paths into the JSON payload. First non-empty value is tested. |
| `pattern` | PCRE2 regex matched against the extracted field value. |
| `action` | `"block"` is the only supported action. |
| `audit_only` | If `true`, log the violation but do not block. |
| `violation_message` | Returned to the LLM and written to the audit log. |
| `conditions` | Multi-condition AND rules. Each condition has `field_paths`, `pattern`, and optional `not_pattern`. |

### Virtual fields

The rule engine supports virtual field paths for advanced matching:

- `effective_cwd`: Simulates `cd` chains in a command to compute the actual working directory.
- `cmd_segments`: Splits shell commands on `&&`, `||`, `;`, and newlines for per-segment matching.

## Audit log

Each hook event is written as a newline-delimited JSON entry to a
per-conversation file under:

```
$XDG_STATE_HOME/agent-gate/conversations/<system>/<session_id>/<event>.jsonl
~/.local/state/agent-gate/conversations/...  (default)
```

The path is keyed on the `system` (`claude`, `cursor`, `codex`,
`gemini`), the session or conversation id from the payload, and the hook
event name. The daemon owns the writer: it keeps an LRU of open file
handles and drains an unbounded queue on a background goroutine, so hook
processes never block on disk I/O and no entry is dropped.

Each entry includes build provenance (`commit`, `version`, `buildHash`,
`dirty`) and full payload details:

```json
{
  "time": "2026-04-15T17:57:22Z",
  "level": "INFO",
  "msg": "hook.received",
  "commit": "b887996",
  "version": "v1.2.0",
  "buildHash": "88668c50e75b",
  "dirty": "false",
  "system": "claude",
  "event": "PreToolUse",
  "session_id": "abc123",
  "cwd": "/Users/you/project",
  "tool_name": "Edit",
  "ti_file_path": "main.go",
  "ti_old_string_snippet": "func main() {",
  "ti_new_string_snippet": "func main() {\n\tlog.Info(\"starting\")",
  "rules_checked": ["no-emdashes"],
  "decision": "allow"
}
```

## Fail-closed behavior

`agent-gate` blocks on failure rather than allowing through:

- A deferred `recover()` in `main` catches any panic and exits 2.
- All internal errors exit 2.
- Cursor hooks use `failClosed: true`.
- For Claude Code, exit code 2 surfaces the stderr message to the LLM.

## Project layout

```
cmd/agent-gate/main.go          entry point, panic recovery, stdin read, dispatch
internal/version/version.go     build metadata (commit, version, buildHash, dirty via ldflags)
internal/config/config.go       TOML structs, Load(), AuditLogPath()
internal/config/xdg.go          XDG path resolution
internal/config/defaults.go     default config written on first run
internal/audit/logger.go        slog JSON handler with build metadata attrs
internal/hook/types.go          RawPayload, HookSystem, Decision
internal/hook/detect.go         Claude/Cursor autodetection plus provider override support
internal/hook/claude.go         26 Claude events, payload parsing, response helpers
internal/hook/cursor.go         20 Cursor events, payload parsing, response helpers
internal/hook/codex.go          Codex events and response helpers
internal/hook/gemini.go         Gemini CLI events and response helpers
internal/hook/provider.go       provider-aware orchestration and dispatch
internal/hook/handler.go        provider-specific audit attribute extraction
internal/runtime/*.go           provider runtime adapter scaffolding (Claude active, others stubbed)
internal/rules/engine.go        dot-path field extraction, regex rule evaluation
docs/hook-schemas.md            complete JSON schemas for supported providers
config.toml.example             annotated example configuration
```

## Development

```sh
make build    # compile with ldflags (version, commit, buildHash)
make deploy   # go install to $GOPATH/bin
make test     # go test -race ./...
make lint     # golangci-lint
make check    # full: vet + lint + test + govulncheck
make clean    # remove local build artifact
```
