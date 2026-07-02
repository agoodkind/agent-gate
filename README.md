# agent-gate

A unified hook daemon for [Claude Code](https://code.claude.com), [Cursor](https://cursor.com), Codex, and Gemini CLI. Hook binaries are dumb gRPC clients; the daemon writes a structured audit trail and enforces configurable rules that can block unwanted actions across providers.

## What it does

Claude Code, Cursor, Codex, and Gemini CLI all expose lifecycle hook systems that call external binaries via stdin/stdout JSON. `agent-gate` installs a small hook entrypoint for each tool, but all parsing, enrichment, rule evaluation, response formatting, and audit logging happen in the background daemon.

1. **Audit.** Every hook invocation produces a structured JSON log entry with full payload details: tool inputs (command, file_path, old_string, new_string, content), prompts, agent messages, session metadata, and build provenance (commit, version, buildHash, dirty).

2. **Enforce.** Rules defined in TOML are evaluated against each incoming payload. The first matching rule blocks the action and sends the violation message back to the LLM. Rules support per-system event filtering, multi-condition AND logic, and audit-only mode (log without blocking).

## Installation

### One-liner (recommended)

Pulls the latest rolling release tarball for your platform, installs the binary
to `${XDG_BIN_HOME:-$HOME/.local/bin}`, installs and starts the user daemon
service, writes or merges the canonical `agent-gate` config with daemon-owned
auto-update enabled, and merges hook templates into your Claude, Codex,
Cursor, Gemini, and Copilot config files. Existing user settings in those
files are
preserved.

```sh
curl -fsSL https://raw.githubusercontent.com/agoodkind/agent-gate/main/install.sh | bash
```

Flags:

```sh
./install.sh --bin-only          # binary only, skip hook config updates
./install.sh --hooks-only        # update hook configs, skip download
./install.sh --service-only      # install/start only the user daemon service
./install.sh --no-service        # skip launchd/systemd user service setup
./install.sh --no-config         # skip agent-gate config creation / merge
./install.sh --no-auto-update    # write or merge config with auto-update off
./install.sh --auto-update check # write or merge config with check-only mode
./install.sh --no-claude         # opt out of Claude (additive: combine flags)
./install.sh --no-codex
./install.sh --no-cursor
./install.sh --no-gemini
./install.sh --no-copilot
./install.sh --bin-dir /opt/bin  # override $XDG_BIN_HOME
./install.sh --version v1.2.3    # pin to a specific release tag
./install.sh --channel stable    # use GitHub's latest stable release
./install.sh --repo owner/name   # override the GitHub release repo
./install.sh --require-attestation
```

`make install-release`, `make install-release-bin`, `make install-release-hooks`,
and `make install-release-service` are thin wrappers around the script. The script
downloads or locates the release binary first, then delegates hook and service
setup to `agent-gate install`.

### From source

Requires the Go version declared in `go.mod`. Go 1.21 and newer can select a
newer toolchain automatically when `GOTOOLCHAIN=auto`, but older source builds
are not supported. `agent-gate` uses PCRE2 via cgo, so build hosts must have
PCRE2 and a working C toolchain available.

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

## Daemon and hooks

The daemon is the source of truth for enforcement. Hook invocations only
read stdin, forward raw bytes and a small provider hint over gRPC, mirror
the daemon response, and exit. If stdin, daemon availability, RPC, or hook
entrypoint failures prevent daemon evaluation, hooks fail open with the
hinted provider's allow response and exit code 0. When no provider hint is
available, hooks use a generic empty success response.

The installer manages a per-user service:

- macOS: `~/Library/LaunchAgents/io.goodkind.agent-gate.plist`
- Linux: `${XDG_CONFIG_HOME:-$HOME/.config}/systemd/user/agent-gate.service`

Useful local commands:

```sh
make install-service
make daemon-status
make daemon-restart
```

`make deploy` builds a signed binary to the stable per-user path and
restarts the user service under launchd or systemd so the supervisor owns
the active daemon process.

## Wiring up hooks

The one-liner above wires hooks for all supported tools automatically.
Templates live in [`hooks/`](hooks/) and the canonical inventory is in
[HOOKS.md](HOOKS.md). To re-apply after a binary move or template
change:

```sh
make install-hooks
```

To opt out of any tool, pass `--no-claude`, `--no-codex`, `--no-cursor`,
`--no-gemini`, or `--no-copilot` (combinable). Hook updates preserve unrelated
settings. Codex updates a marked TOML block and ensures `[features] hooks = true`;
JSON-based tools update only their hook key.

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

Fresh installs write a canonical config file when none exists. Re-running
`install.sh` preserves existing config and adds the `[update]` table when it
is missing.

### Auto-Update

Direct installs use the daemon-owned updater by default.

```toml
[update]
enabled = true
mode = "apply"
interval = "24h"
repo = "agoodkind/agent-gate"
allow_prerelease = true
```

Useful local commands:

```sh
agent-gate update check
agent-gate update apply --dry-run
agent-gate update status
```

### Rules

Each `[[rules]]` block defines one enforcement rule. Agent-gate evaluates every applicable rule and reports every concrete match in the block message.

```toml
[[rules]]
name        = "no-shell-redirection"
description = "Block direct shell output/error redirections that suppress, buffer, or truncate command output"
events      = ["PreToolUse", "beforeShellExecution"]
field_paths = ["cmd_redirections"]
pattern     = '.+'
action      = "block"
violation_message = "Direct shell output redirection is not permitted here."
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
| `action` | `"block"` (default) stops the tool call where the protocol allows it. `"audit"` records the violation without blocking. The legacy `class` field is no longer accepted. |
| `violation_message` | Returned to the LLM and written to the audit log. |
| `diagnostic_format` | `"detailed"` by default. `"message_only"` returns only `violation_message`. |
| `conditions` | Multi-condition AND rules. Each condition has a `kind`; missing `kind` means `regex` for compatibility. |

### Virtual fields

The rule engine supports virtual field paths for advanced matching:

- `effective_cwd`: Computes the actual operation directory from the best available source. It prefers normalized operation-level cwd values such as `tool_input.workdir`, transcript function-call arguments, or an existing `effective_cwd`, then falls back to top-level `cwd` and simulates `cd` chains in the command.
- `cmd_segments`: Splits shell commands on `&&`, `||`, `;`, and newlines for per-segment matching.
- `cmd_comments`: Extracts unquoted shell comments from command fields so prose rules can scan comments without treating command flags or option separators as prose.
- `cmd_double_hyphen_prose`: Extracts command tokens where ASCII double hyphen is not acting as a flag or option separator, so prose rules can still catch command arguments like `this--is` without blocking normal shell syntax.
- `cmd_redirections`: Extracts direct shell output redirections that are unsafe for interactive agent work, while ignoring quoted script content, heredoc bodies, and standard-stream-only duplication.

### Condition Kinds

Rules with `[[rules.conditions]]` use AND semantics: every condition must pass.

- `kind = "regex"`: matches `pattern` and optional `not_pattern` against the first non-empty value from `field_paths`. This is the default for existing configs.
- `kind = "command"`: matches shell command segments by `argv0` and optional `subcommands`. `strip_env`, `strip_args`, and `cwd_flags` can normalize common wrappers and command-specific cwd flags.
- `kind = "project"`: walks upward from the effective cwd, finds the nearest directory containing any `root_markers`, then applies `require_any`, `require_all`, and `forbid_any` relative to that root. When a prior `command` condition matched, project checks use the cwd active at each matched command segment, so `cd project && go test` is evaluated against `project`.

Example:

```toml
[[rules]]
name = "go-build-through-make"
description = "Require make build for Go build in Go modules that provide a Makefile"
claude_events = ["PreToolUse"]
cursor_events = ["preToolUse", "beforeShellExecution"]
codex_events = ["PreToolUse"]
gemini_events = ["BeforeTool"]
action = "block"
violation_message = "Run make build instead of go build in Go modules that provide a Makefile."

[[rules.conditions]]
kind = "command"
argv0 = "go"
subcommands = ["build"]
strip_env = true
strip_args = ["env", "time", "command"]
cwd_flags = ["-C"]
pattern = '^build(?:\s|$)'

[[rules.conditions]]
kind = "project"
root_markers = ["go.mod"]
require_any = ["Makefile", "makefile", "GNUmakefile"]
```

## Audit log

Each hook event is normalized into an audit event and written to the
configured audit outputs. The default JSONL output writes:

```
$XDG_STATE_HOME/agent-gate/events/YYYY/MM/DD/events.jsonl
$XDG_STATE_HOME/agent-gate/payloads/sha256/ab/cd/<payload-hash>.json
~/.local/state/agent-gate/events/...     (default)
```

SQLite output is optional and writes to
`$XDG_STATE_HOME/agent-gate/sqlite/audit.db` when enabled.

The `<system>` folder is chosen by a priority chain that inspects env
vars set by each tool, payload fingerprints, and finally the CLI
subcommand hint. Tools that share another tool's hook config (Cursor
copying Claude, VS Code extensions copying Claude, anyone copying
Codex) are still classified by who actually invoked the binary, not by
the config they came from.

| Folder      | Picked when                                                                                                       |
| ----------- | ----------------------------------------------------------------------------------------------------------------- |
| `claude/`   | `CLAUDE_CODE_ENTRYPOINT` env set or `AI_AGENT=claude-code/...`, or payload has `transcript_path`, `permission_mode`, `agent_id`, or `agent_type` |
| `codex/`    | `CODEX_THREAD_ID` or `CODEX_CI` env set (direct invoker wins even when claude env is also inherited)              |
| `copilot/`  | Any `COPILOT_OTEL_*` env set. Copilot Chat uses Claude-style event names and VS Code tool argument shapes, so a Copilot adapter normalizes payload fields before rules run |
| `cursor/`   | `CURSOR_VERSION`, `CURSOR_WORKSPACE_NAME`, or `CURSOR_MODE` env, or payload has `cursor_version`, `conversation_id`, `generation_id`, `workspace_roots`, or `user_email`, or camel-case event name |
| `gemini/`   | `GEMINI_CLI` env, or event name in {`BeforeTool`, `AfterTool`, `BeforeAgent`, `AfterAgent`, `BeforeModel`, `AfterModel`, `BeforeToolSelection`, `PreCompress`} |
| `vscode/`   | `VSCODE_PID` or `VSCODE_IPC_HOOK` env set and none of the above matched. Catches generic VS Code extensions that are not Copilot |
| `unknown/`  | No fingerprint matched. Anything landing here is a detection gap to file as a follow-up                           |

Mature layout on disk:

```
~/.local/state/agent-gate/
├── events/YYYY/MM/DD/events.jsonl
├── payloads/sha256/ab/cd/<hash>.json
└── sqlite/audit.db
```

`CLAUDECODE=1` is intentionally not a primary Claude signal because it
is inherited by every subprocess of a claude shell.
`CLAUDE_CODE_ENTRYPOINT` is set fresh by the claude binary on each
invocation and is robust against inherited env. The full priority chain
lives in [`internal/hook/detect.go`](internal/hook/detect.go).

The daemon owns the writer. It normalizes hook log entries, deduplicates
duplicate hook fires, and fans out to independently configured outputs on
a background goroutine. JSONL and SQLite are peer outputs; neither is used
as recovery for the other.

Each event includes normalized operation, decision, and violation details:

```json
{
  "event_id": "evt_...",
  "schema_version": 1,
  "time": "2026-04-15T17:57:22Z",
  "level": "info",
  "message": "hook.blocked",
  "system": "claude",
  "event_name": "PreToolUse",
  "session_id": "abc123",
  "tool_use_id": "toolu_...",
  "tool_name": "Edit",
  "operation": {
    "cwd": "/Users/you/project",
    "effective_cwd": "/Users/you/project",
    "command": "go build ./..."
  },
  "decision": {
    "kind": "block",
    "rules_checked": ["use-make-not-go-direct"],
    "rules_matched": ["use-make-not-go-direct"]
  },
  "violations": [
    {"rule": "use-make-not-go-direct", "mode": "blocking"}
  ],
  "raw_payload_hash": "sha256:..."
}
```

Query recent audit state with separate durable intake visibility and derived
audit decisions:

```
agent-gate query seen --today
agent-gate query seen --system claude --session abc123 --event PreToolUse --tool Bash
agent-gate query seen --state pending --event-id intake_... --limit 20
agent-gate query seen --since 24h --json --include-normalized --include-env
agent-gate query decisions --today
agent-gate query decisions --system claude --decision block
agent-gate query decisions --rule use-make-not-go-direct --since 24h --json
agent-gate query decisions --system claude --decision block --rule use-make-not-go-direct
```

`query seen` reads durable intake records and omits raw payload bodies. It
prints normalized hook JSON and environment fingerprints only when
`--include-normalized` or `--include-env` is set. When a requested time range
starts before the first intake record, the command reports the first available
record timestamp and clamps the intake-side range dynamically. Use
`query decisions` for the existing audit decision view, including legacy ranges
that predate durable intake records.

## Fail-open behavior

`agent-gate` fails open for its own availability, transport, and internal
hook-entrypoint failures:

- Stdin read failures emit the hinted provider allow shape and exit 0.
- Daemon unavailable and daemon RPC failures emit the hinted provider allow shape and exit 0.
- Hook invocation panics are recovered and emit the hinted provider allow shape and exit 0.
- Unknown providers emit empty stdout and exit 0 because no provider-specific allow schema is known.
- Provider templates that support the field use `failClosed: false`.

This fail-open behavior only covers agent-gate failures. If the daemon
successfully evaluates a hook and returns a policy block, the hook process
mirrors the daemon stdout, stderr, and exit code exactly.

## Project layout

```
cmd/agent-gate/main.go          entry point, hook fail-open recovery, stdin read, dispatch
internal/version/version.go     build metadata (commit, version, buildHash, dirty via ldflags)
internal/config/config.go       TOML structs, Load(), audit path resolution
internal/config/xdg.go          XDG path resolution
internal/config/defaults.go     default config written on first run
internal/audit/logger.go        audit Sink interface
internal/audit/session.go       event logger and JSONL/SQLite audit outputs
internal/audit/query.go         logs query backend
internal/hook/types.go          RawPayload, HookSystem, Decision
internal/hook/detect.go         Claude/Cursor autodetection plus provider override support
internal/hook/claude.go         26 Claude events, payload parsing, response helpers
internal/hook/cursor.go         20 Cursor events, payload parsing, response helpers
internal/hook/codex.go          Codex events and response helpers
internal/hook/gemini.go         Gemini CLI events and response helpers
internal/hook/vscode.go         VS Code payload adapter
internal/hook/copilot.go        Copilot payload and transcript adapter
internal/hook/provider.go       provider-aware orchestration and dispatch
internal/hook/response_boundary.go provider-neutral response boundary
internal/hook/handler.go        provider-specific audit attribute extraction
internal/runtime/*.go           provider runtime adapter scaffolding (Claude active, others stubbed)
internal/rules/engine.go        dot-path field extraction, regex rule evaluation
docs/hook-schemas.md            complete JSON schemas for supported providers
config.toml.example             annotated example configuration
```

## Development

```sh
make build    # compile/sign with ldflags (version, commit, buildHash)
make deploy   # build/sign to ~/.local/bin and restart the supervised daemon
make proto    # regenerate protobuf/gRPC files with Buf
make test     # go test -race ./...
make lint     # golangci-lint
make check    # full: vet + lint + test + govulncheck
make clean    # remove local build artifact
make spawn-smoke INPUT=/path/to/large.txt
make spawn-smoke ARGS='-generate-bytes 1048576'
```

`make spawn-smoke` runs a real subprocess harness against `agent-gate` and
probes when launch-time argv or env padding starts failing with `E2BIG`. By
default it targets `~/.local/bin/agent-gate`; override with
`ARGS='-target ./dist/agent-gate -payload-kind preToolUse'`. If you do not
want to provide an input file, use `-generate-bytes` and the harness will
write lorem-style text to a temporary file and use that as the payload source.
