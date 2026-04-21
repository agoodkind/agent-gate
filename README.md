# agent-gate

A unified hook handler for [Claude Code](https://code.claude.com) and [Cursor](https://cursor.com). It registers with every lifecycle hook event in both tools (26 Claude + 20 Cursor), writes a structured audit trail with build metadata on every invocation, and enforces configurable regex rules that can block unwanted actions.

## What it does

Both Claude Code and Cursor expose lifecycle hook systems that call external binaries via stdin/stdout JSON. `agent-gate` sits at that boundary as a single Go binary and does two things:

1. **Audit.** Every hook invocation produces a structured JSON log entry with full payload details: tool inputs (command, file_path, old_string, new_string, content), prompts, agent messages, session metadata, and build provenance (commit, version, buildHash, dirty).

2. **Enforce.** Rules defined in TOML are evaluated against each incoming payload. The first matching rule blocks the action and sends the violation message back to the LLM. Rules support per-system event filtering, multi-condition AND logic, and audit-only mode (log without blocking).

## Installation

Requires Go 1.21 or later.

`agent-gate` now uses PCRE2 via cgo, so build hosts must have PCRE2 and a working C toolchain available.

Install prerequisites:
- macOS: `brew install pcre2`
- Debian/Ubuntu: `sudo apt-get update && sudo apt-get install -y libpcre2-dev`
- Fedora/RHEL: `sudo dnf install pcre2-devel`
- Alpine: `apk add pcre2-dev build-base`

When building, set CGO_ENABLED=1. Rule patterns compile against system `libpcre2-8` (PCRE2 10.x) via cgo; there is no bundled third-party Go regex binding.

```sh
git clone https://github.com/agoodkind/agent-gate
cd agent-gate
make deploy
```

`make deploy` runs `go install` with ldflags that inject git commit, version, and build hash into the binary.
CI and release builds should export CGO_ENABLED=1 so PCRE2 JIT and match limits are available at runtime.

## Wiring up hooks

### Claude Code

Add to `~/.claude/settings.json`. All 26 events should be registered:

```json
{
  "hooks": {
    "SessionStart":       [{"hooks": [{"type": "command", "command": "/path/to/agent-gate"}]}],
    "SessionEnd":         [{"hooks": [{"type": "command", "command": "/path/to/agent-gate"}]}],
    "UserPromptSubmit":   [{"hooks": [{"type": "command", "command": "/path/to/agent-gate"}]}],
    "PreToolUse":         [{"matcher": ".*", "hooks": [{"type": "command", "command": "/path/to/agent-gate"}]}],
    "PostToolUse":        [{"matcher": ".*", "hooks": [{"type": "command", "command": "/path/to/agent-gate"}]}],
    "PostToolUseFailure": [{"matcher": ".*", "hooks": [{"type": "command", "command": "/path/to/agent-gate"}]}],
    "PermissionRequest":  [{"matcher": ".*", "hooks": [{"type": "command", "command": "/path/to/agent-gate"}]}],
    "PermissionDenied":   [{"matcher": ".*", "hooks": [{"type": "command", "command": "/path/to/agent-gate"}]}],
    "Notification":       [{"hooks": [{"type": "command", "command": "/path/to/agent-gate"}]}],
    "SubagentStart":      [{"hooks": [{"type": "command", "command": "/path/to/agent-gate"}]}],
    "SubagentStop":       [{"hooks": [{"type": "command", "command": "/path/to/agent-gate"}]}],
    "TaskCreated":        [{"hooks": [{"type": "command", "command": "/path/to/agent-gate"}]}],
    "TaskCompleted":      [{"hooks": [{"type": "command", "command": "/path/to/agent-gate"}]}],
    "Stop":               [{"hooks": [{"type": "command", "command": "/path/to/agent-gate"}]}],
    "StopFailure":        [{"hooks": [{"type": "command", "command": "/path/to/agent-gate"}]}],
    "TeammateIdle":       [{"hooks": [{"type": "command", "command": "/path/to/agent-gate"}]}],
    "InstructionsLoaded": [{"hooks": [{"type": "command", "command": "/path/to/agent-gate"}]}],
    "ConfigChange":       [{"hooks": [{"type": "command", "command": "/path/to/agent-gate"}]}],
    "CwdChanged":         [{"hooks": [{"type": "command", "command": "/path/to/agent-gate"}]}],
    "FileChanged":        [{"hooks": [{"type": "command", "command": "/path/to/agent-gate"}]}],
    "WorktreeCreate":     [{"hooks": [{"type": "command", "command": "/path/to/agent-gate"}]}],
    "WorktreeRemove":     [{"hooks": [{"type": "command", "command": "/path/to/agent-gate"}]}],
    "PreCompact":         [{"hooks": [{"type": "command", "command": "/path/to/agent-gate"}]}],
    "PostCompact":        [{"hooks": [{"type": "command", "command": "/path/to/agent-gate"}]}],
    "Elicitation":        [{"hooks": [{"type": "command", "command": "/path/to/agent-gate"}]}],
    "ElicitationResult":  [{"hooks": [{"type": "command", "command": "/path/to/agent-gate"}]}]
  }
}
```

### Cursor

Add to `~/.cursor/hooks.json`. All 20 events should be registered:

```json
{
  "version": 1,
  "hooks": {
    "sessionStart":         [{"command": "/path/to/agent-gate", "failClosed": true}],
    "sessionEnd":           [{"command": "/path/to/agent-gate", "failClosed": true}],
    "preToolUse":           [{"command": "/path/to/agent-gate", "failClosed": true}],
    "postToolUse":          [{"command": "/path/to/agent-gate", "failClosed": true}],
    "postToolUseFailure":   [{"command": "/path/to/agent-gate", "failClosed": true}],
    "subagentStart":        [{"command": "/path/to/agent-gate", "failClosed": true}],
    "subagentStop":         [{"command": "/path/to/agent-gate", "failClosed": true}],
    "beforeShellExecution": [{"command": "/path/to/agent-gate", "failClosed": true}],
    "afterShellExecution":  [{"command": "/path/to/agent-gate", "failClosed": true}],
    "beforeMCPExecution":   [{"command": "/path/to/agent-gate", "failClosed": true}],
    "afterMCPExecution":    [{"command": "/path/to/agent-gate", "failClosed": true}],
    "beforeReadFile":       [{"command": "/path/to/agent-gate", "failClosed": true}],
    "afterFileEdit":        [{"command": "/path/to/agent-gate", "failClosed": true}],
    "beforeSubmitPrompt":   [{"command": "/path/to/agent-gate", "failClosed": true}],
    "preCompact":           [{"command": "/path/to/agent-gate", "failClosed": true}],
    "stop":                 [{"command": "/path/to/agent-gate", "failClosed": true}],
    "afterAgentResponse":   [{"command": "/path/to/agent-gate", "failClosed": true}],
    "afterAgentThought":    [{"command": "/path/to/agent-gate", "failClosed": true}],
    "beforeTabFileRead":    [{"command": "/path/to/agent-gate", "failClosed": true}],
    "afterTabFileEdit":     [{"command": "/path/to/agent-gate", "failClosed": true}]
  }
}
```

`failClosed: true` means that if the binary crashes or times out, Cursor blocks the action.

## Hook event reference

Full JSON payload schemas for all 46 events are documented in [docs/hook-schemas.md](docs/hook-schemas.md).

**Sources:**
- Claude Code hooks: https://code.claude.com/docs/en/hooks
- Cursor hooks: https://cursor.com/docs/hooks

## Configuration

Config lives at the XDG config path:

```
$XDG_CONFIG_HOME/agent-gate/config.toml
~/.config/agent-gate/config.toml  (default)
```

See [config.toml.example](config.toml.example) for a complete annotated example.

### Rules

Each `[[rules]]` block defines one enforcement rule. Rules are evaluated in order; the first match wins.

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

Written as newline-delimited JSON to:

```
$XDG_STATE_HOME/agent-gate/audit.jsonl
~/.local/state/agent-gate/audit.jsonl  (default)
```

Every log entry includes build provenance (`commit`, `version`, `buildHash`, `dirty`) and full payload details:

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
internal/hook/detect.go         PascalCase = Claude, camelCase = Cursor
internal/hook/claude.go         26 Claude events, payload parsing, response helpers
internal/hook/cursor.go         20 Cursor events, payload parsing, response helpers
internal/hook/handler.go        orchestration: receive, log full payload, evaluate, respond
internal/rules/engine.go        dot-path field extraction, regex rule evaluation
docs/hook-schemas.md            complete JSON schemas for all 46 hook events
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
