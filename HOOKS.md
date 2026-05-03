# agent-gate hook reference

This file is the canonical inventory of every hook `agent-gate` registers
across Claude Code, Codex, Gemini, Copilot, and Cursor. The JSON templates
that `install.sh` merges into each tool's config live under `hooks/` and stay
in sync with this list.

## Install paths

| Tool    | Config file                             | Top-level key |
| ------- | --------------------------------------- | ------------- |
| Claude  | `$HOME/.claude/settings.json`           | `hooks`       |
| Codex   | `$HOME/.codex/config.toml`              | `hooks.*`     |
| Gemini  | `$HOME/.gemini/settings.json`           | `hooks`       |
| Copilot | `$HOME/.copilot/hooks/agent-gate.json`  | `hooks`       |
| Cursor  | `$HOME/.cursor/hooks.json`              | `hooks`       |

Observed local state on this machine:

- Claude hooks are active in `$HOME/.claude/settings.json`, and each event
  object currently includes `failClosed: false` plus a nested `hooks` array.
- Codex hooks are active in `$HOME/.codex/config.toml` as TOML tables such as
  `[[hooks.SessionStart]]` and `[[hooks.SessionStart.hooks]]`.
- Gemini hooks are active in `$HOME/.gemini/settings.json`, and each event
  object currently includes `failClosed: false` plus a nested `hooks` array.
- Copilot hooks are active in `$HOME/.copilot/hooks/agent-gate.json`, and each
  event object currently includes `failClosed: false` plus a nested `hooks`
  array.
- Cursor hooks are active in `$HOME/.cursor/hooks.json`, and each event object
  currently includes `command` plus `failClosed: false`.

Each registered hook executes the installed binary
(`$XDG_BIN_HOME/agent-gate`, falling back to `$HOME/.local/bin/agent-gate`)
with the appropriate subcommand:

- Claude: bare `agent-gate` (auto-detects)
- Codex: `agent-gate codex-hook`
- Gemini: `agent-gate gemini-hook`
- Copilot: bare `agent-gate` (auto-detects via `COPILOT_OTEL_*` env)
- Cursor: bare `agent-gate`

The hook binary reads the hook payload on stdin and forwards it to the
`agent-gate` daemon over gRPC. The daemon evaluates rules from
`$XDG_CONFIG_HOME/agent-gate/config.toml`, writes normalized audit events
under `$XDG_STATE_HOME/agent-gate/events/YYYY/MM/DD/events.jsonl`, and
returns the provider-specific allow or block response for the hook process
to mirror.

## Claude Code hooks

Source: `hooks/claude.json`. All events route to `agent-gate`.

Installed object shape in `$HOME/.claude/settings.json`:

```json
{
  "failClosed": false,
  "matcher": ".*",
  "hooks": [
    {
      "type": "command",
      "command": "/Users/agoodkind/.local/bin/agent-gate"
    }
  ]
}
```

| Event                | Matcher | Notes                              |
| -------------------- | ------- | ---------------------------------- |
| `ConfigChange`       |         | Detect mid-session config edits    |
| `CwdChanged`         |         | Working directory transitions      |
| `Elicitation`        |         | Pre-elicitation step               |
| `ElicitationResult`  |         | Post-elicitation                   |
| `FileChanged`        |         | External file modification         |
| `InstructionsLoaded` |         | Project / system instructions read |
| `Notification`       |         | Outbound notification              |
| `PermissionDenied`   | `.*`    | Tool permission denial             |
| `PermissionRequest`  | `.*`    | Tool permission prompt             |
| `PostCompact`        |         | After context compaction           |
| `PostToolUse`        | `.*`    | Tool result observed               |
| `PostToolUseFailure` | `.*`    | Tool failure observed              |
| `PreCompact`         |         | Before context compaction          |
| `PreToolUse`         | `.*`    | Tool invocation about to run       |
| `SessionEnd`         |         | Session shutdown                   |
| `SessionStart`       |         | Session boot                       |
| `Stop`               |         | Assistant turn end                 |
| `StopFailure`        |         | Assistant turn errored out         |
| `SubagentStart`      |         | Subagent dispatched                |
| `SubagentStop`       |         | Subagent finished                  |
| `TaskCompleted`      |         | Task tracker completion            |
| `TaskCreated`        |         | Task tracker creation              |
| `TeammateIdle`       |         | Multi-agent idle signal            |
| `UserPromptSubmit`   |         | User submitted a prompt            |
| `WorktreeCreate`     |         | Git worktree created               |
| `WorktreeRemove`     |         | Git worktree removed               |

## Codex hooks

Source: `hooks/codex.json`. All events route to `agent-gate codex-hook`.

Installed object shape in `$HOME/.codex/config.toml`:

```toml
[[hooks.PreToolUse]]
matcher = ".*"

[[hooks.PreToolUse.hooks]]
type = "command"
command = "/Users/agoodkind/.local/bin/agent-gate codex-hook"
```

| Event               | Matcher                  |
| ------------------- | ------------------------ |
| `SessionStart`      | `startup\|resume\|clear` |
| `PreToolUse`        | `.*`                     |
| `PermissionRequest` | `.*`                     |
| `PostToolUse`       | `.*`                     |
| `Stop`              |                          |
| `UserPromptSubmit`  |                          |

Codex hooks require `codex_hooks = true` under `[features]` in
`$HOME/.codex/config.toml`. The local Codex install on this machine is using
that TOML format directly rather than a separate JSON hook file.

## GitHub Copilot Chat hooks

Source: `hooks/copilot.json`. All events route to bare `agent-gate`.
Detection of Copilot-vs-Claude relies on the `COPILOT_OTEL_*` env vars
that the Copilot Chat extension always sets on hook subprocesses. Copilot
uses Claude-style hook event names, but `agent-gate` handles it through a
Copilot adapter so VS Code tool arguments are normalized before rules run.

Installed object shape in `$HOME/.copilot/hooks/agent-gate.json`:

```json
{
  "failClosed": false,
  "matcher": ".*",
  "hooks": [
    {
      "type": "command",
      "command": "/Users/agoodkind/.local/bin/agent-gate"
    }
  ]
}
```

| Event              | Matcher |
| ------------------ | ------- |
| `SessionStart`     |         |
| `SessionEnd`       |         |
| `UserPromptSubmit` |         |
| `PreToolUse`       | `.*`    |
| `PostToolUse`      | `.*`    |
| `PreCompact`       |         |
| `PostCompact`      |         |
| `Notification`     |         |
| `Stop`             |         |

Copilot reads any `*.json` file under `$HOME/.copilot/hooks/`. The
template is written to `agent-gate.json` so it does not collide with
other hook files in that directory.

## Cursor hooks

Cursor user hooks live in `$HOME/.cursor/hooks.json` and use a top-level
wrapper with `version` and `hooks`.

Installed object shape in `$HOME/.cursor/hooks.json`:

```json
{
  "version": 1,
  "hooks": {
    "preToolUse": [
      {
        "command": "/Users/agoodkind/.local/bin/agent-gate",
        "failClosed": false
      }
    ]
  }
}
```

| Event                  | Matcher |
| ---------------------- | ------- |
| `sessionStart`         |         |
| `sessionEnd`           |         |
| `preToolUse`           |         |
| `postToolUse`          |         |
| `postToolUseFailure`   |         |
| `beforeShellExecution` |         |
| `afterShellExecution`  |         |
| `beforeMCPExecution`   |         |
| `afterMCPExecution`    |         |
| `beforeReadFile`       |         |
| `afterFileEdit`        |         |
| `beforeSubmitPrompt`   |         |
| `subagentStart`        |         |
| `subagentStop`         |         |
| `preCompact`           |         |
| `stop`                 |         |
| `afterAgentResponse`   |         |
| `afterAgentThought`    |         |
| `beforeTabFileRead`    |         |
| `afterTabFileEdit`     |         |

## Gemini hooks

Source: `hooks/gemini.json`. All events route to `agent-gate gemini-hook`.

Installed object shape in `$HOME/.gemini/settings.json`:

```json
{
  "failClosed": false,
  "matcher": ".*",
  "hooks": [
    {
      "type": "command",
      "command": "/Users/agoodkind/.local/bin/agent-gate gemini-hook"
    }
  ]
}
```

| Event                 | Matcher                                                         |
| --------------------- | --------------------------------------------------------------- |
| `BeforeTool`          | `.*`                                                            |
| `AfterTool`           | `.*`                                                            |
| `BeforeAgent`         |                                                                 |
| `AfterAgent`          |                                                                 |
| `BeforeModel`         |                                                                 |
| `AfterModel`          |                                                                 |
| `BeforeToolSelection` |                                                                 |
| `SessionStart`        | `startup`, `resume`, `clear`                                    |
| `SessionEnd`          | `exit`, `clear`, `logout`, `prompt_input_exit`, `other`         |
| `Notification`        | `ToolPermission`                                                |
| `PreCompress`         | `auto`, `manual`                                                |

## Updating templates

When new hook events are added or existing matchers change:

1. Edit the relevant file in `hooks/` and use `__AGENT_GATE_BIN__` as the
   placeholder for the binary path.
2. Update the matching table above.
3. Run `make install-hooks` (or `./install.sh --hooks-only`) to apply.

`install.sh` merges only the `.hooks` key in each target file, so any
unrelated user settings (themes, permissions, etc.) are preserved across
re-runs.
