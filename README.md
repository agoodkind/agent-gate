# agent-gate

A unified hook handler for [Claude Code](https://code.claude.com) and [Cursor](https://cursor.com). It registers with every lifecycle hook event in both tools, writes a structured audit trail for each one, and enforces configurable regex rules that can block unwanted shell commands before they execute.

## What it does

Both Claude Code and Cursor expose lifecycle hook systems that call external binaries via stdin/stdout JSON. `agent-gate` sits at that boundary as a single Go binary and does two things:

1. **Audit.** Every hook invocation, from every event type in both tools, produces a structured JSON log entry. The log captures the system, event name, session ID, working directory, tool name, command, which rules were checked, and the final decision.

2. **Enforce.** Rules defined in TOML are evaluated against each incoming payload. The first matching rule blocks the action and sends the violation message back to the LLM. The binary exits 2 on any unexpected error so that unknown failures block rather than allow.

The first built-in rule blocks shell output and error redirection (`2>&1`, `>/dev/null`, `&>`, and similar patterns), which LLMs commonly emit to suppress error output.

## Installation

Requires Go 1.21 or later (uses `log/slog` from the standard library).

```sh
git clone https://github.com/agoodkind/agent-gate
cd agent-gate
make deploy
```

`make deploy` runs `go install ./cmd/agent-gate` and places the binary at `$GOPATH/bin/agent-gate` (typically `~/go/bin/agent-gate`).

## Wiring up hooks

### Claude Code

Add the following `hooks` block to `~/.claude/settings.json`. The binary path below assumes the default `GOPATH`.

```json
{
  "hooks": {
    "PreToolUse":    [{"matcher": ".*", "hooks": [{"type": "command", "command": "/Users/you/go/bin/agent-gate"}]}],
    "PostToolUse":   [{"matcher": ".*", "hooks": [{"type": "command", "command": "/Users/you/go/bin/agent-gate"}]}],
    "SessionStart":  [{"hooks": [{"type": "command", "command": "/Users/you/go/bin/agent-gate"}]}],
    "SessionEnd":    [{"hooks": [{"type": "command", "command": "/Users/you/go/bin/agent-gate"}]}],
    "Stop":          [{"hooks": [{"type": "command", "command": "/Users/you/go/bin/agent-gate"}]}]
  }
}
```

The full list of 26 registered events is in the settings file produced by `make deploy`.

### Cursor

Add the following to `~/.cursor/hooks.json`:

```json
{
  "version": 1,
  "hooks": {
    "beforeShellExecution": [{"command": "/Users/you/go/bin/agent-gate", "failClosed": true}],
    "beforeMCPExecution":   [{"command": "/Users/you/go/bin/agent-gate", "failClosed": true}],
    "beforeSubmitPrompt":   [{"command": "/Users/you/go/bin/agent-gate", "failClosed": true}],
    "beforeReadFile":       [{"command": "/Users/you/go/bin/agent-gate", "failClosed": true}],
    "afterFileEdit":        [{"command": "/Users/you/go/bin/agent-gate", "failClosed": false}],
    "stop":                 [{"command": "/Users/you/go/bin/agent-gate", "failClosed": true}]
  }
}
```

`failClosed: true` means that if the binary crashes or times out, Cursor blocks the action rather than allowing it through.

## Configuration

On first run, `agent-gate` writes a default config file and then loads it. Config lives at the XDG config path:

```
$XDG_CONFIG_HOME/agent-gate/config.toml
~/.config/agent-gate/config.toml  (default)
```

### Log level

```toml
[log]
level = "debug"  # debug | info | warn | error
```

### Path overrides

By default all paths are resolved from XDG environment variables. Set a field below to override:

```toml
[paths]
audit_log = ""  # overrides $XDG_STATE_HOME/agent-gate/audit.jsonl
```

The resolution chain for each path is: TOML override > XDG env var > XDG spec default.

### Rules

Each `[[rules]]` block defines one enforcement rule. Rules are evaluated in order and the first match wins.

```toml
[[rules]]
name        = "no-shell-redirection"
description = "Block shell output/error redirection LLMs commonly emit"
events      = ["PreToolUse", "beforeShellExecution"]
field_paths = ["tool_input.command", "command"]
pattern     = '(\d+>&\d+|>&\d+|&>|\|&|>/dev/null|2>/dev/null|>>/dev/null|2>>/dev/null|&>/dev/null)'
action      = "block"
violation_message = "Shell redirection is not permitted."
```

| Field | Description |
|-------|-------------|
| `events` | Hook event names this rule applies to. Empty list means all events. |
| `field_paths` | Dot-paths into the JSON payload to inspect (`tool_input.command`, `command`, etc.). The first non-empty value found is tested. |
| `pattern` | RE2 regex matched against the extracted field value. |
| `action` | Only `"block"` is supported. |
| `violation_message` | Returned to the LLM and written to the audit log. |

## Audit log

The audit log is written as newline-delimited JSON to:

```
$XDG_STATE_HOME/agent-gate/audit.jsonl
~/.local/state/agent-gate/audit.jsonl  (default)
```

The XDG spec places logs in `$XDG_STATE_HOME` (not `$XDG_DATA_HOME`) because logs are application-generated state rather than portable user data.

Each entry looks like this:

```json
{
  "time": "2026-04-09T18:36:30Z",
  "level": "INFO",
  "msg": "hook.blocked",
  "system": "claude",
  "event": "PreToolUse",
  "session_id": "abc123",
  "cwd": "/Users/you/project",
  "tool_name": "Bash",
  "command": "ls 2>/dev/null",
  "rules_checked": ["no-shell-redirection"],
  "decision": "block",
  "blocking_rule": "no-shell-redirection",
  "violation_message": "Shell redirection is not permitted."
}
```

## Fail-closed behavior

`agent-gate` is designed to block on failure rather than allow through:

- A deferred `recover()` in `main` catches any panic and exits 2.
- All internal errors that cannot be recovered exit 2.
- Cursor hooks are configured with `failClosed: true` so Cursor-level timeouts and crashes also block.
- For Claude Code, exit code 2 causes the hook runner to surface the stderr message to the LLM instead of executing the tool.

## Project layout

```
cmd/agent-gate/main.go          entry point, panic recovery, stdin read, dispatch
internal/config/xdg.go          XDG path resolution (ConfigDir, StateDir, defaults)
internal/config/config.go       TOML structs, Load(), AuditLogPath() override chain
internal/config/defaults.go     default config written on first run
internal/audit/logger.go        slog JSON handler writing to audit.jsonl
internal/hook/types.go          RawPayload, HookSystem, Decision
internal/hook/detect.go         PascalCase = Claude, camelCase = Cursor
internal/hook/claude.go         26 enumerated Claude events, response helpers
internal/hook/cursor.go         6 enumerated Cursor events, response helpers
internal/hook/handler.go        orchestration: receive, log, evaluate, respond
internal/rules/engine.go        dot-path field extraction, regex rule evaluation
```

## Development

```sh
make build    # compile without installing
make deploy   # go install to $GOPATH/bin
make test     # go test -v -race ./...
make clean    # remove local build artifact
```
