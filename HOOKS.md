# agent-gate hook reference

This file is the canonical inventory of every hook `agent-gate` registers
across Claude Code, Codex, Gemini, Copilot, and Cursor. The templates that
`install.sh` merges into each tool's config live under `hooks/` and stay in
sync with this list.

## Provider Capability Matrix

`action = "block"` describes the rule's intent. The daemon does the strongest
available thing per (provider, event) pair and emits a config-load WARN when
the rule subscribes to an event that can only observe. See
`internal/hook/capability.go` for the source of truth.

| Provider event | Capability | Source |
|---|---|---|
| Claude `PreToolUse` | block | docs.claude.com/hooks |
| Claude `PostToolUse` | observe | docs.claude.com/hooks |
| Codex `PreToolUse` | block | developers.openai.com/codex/hooks |
| Codex `PostToolUse` | substitute | developers.openai.com/codex/hooks |
| Cursor `preToolUse`, `beforeShellExecution`, `beforeMCPExecution`, `beforeReadFile` | block | cursor.com/docs/agent/hooks |
| Cursor `postToolUse` | substitute (MCP results only) | cursor.com/docs/agent/hooks |
| Cursor `afterShellExecution`, `afterMCPExecution`, `afterFileEdit` | observe | cursor.com/docs/agent/hooks |
| Gemini `BeforeTool` | block | geminicli.com/docs/hooks/reference |
| Gemini `AfterTool` | observe (until source confirms otherwise) | geminicli.com/docs/hooks/reference |

Capability tiers, weakest to strongest:

- **observe**: the tool already ran and the model has already seen the
  output. A block decision is added as extra context but cannot prevent the
  result from reaching the model.
- **substitute**: the tool already ran, but exit 2 replaces the result the
  model sees with the hook's stderr feedback. Codex `PostToolUse` is in this
  tier; Cursor `postToolUse` is in this tier for MCP tool results.
- **block**: the hook fires before the tool runs and a block decision stops
  the tool from executing. All `pre*` and `before*` events on supported
  providers are in this tier.

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

Hook transport failures are fail-open. If the hook process cannot read stdin,
cannot reach the daemon, receives a daemon RPC error, or recovers a hook
invocation panic, it emits the hinted provider's allow response and exits 0.
For hooks without a provider hint, `agent-gate` exits 0 with empty stdout
because no provider-specific allow schema is known at the transport boundary.
Successful daemon policy blocks are not fail-open cases; the hook process
mirrors the daemon's stdout, stderr, and exit code exactly.

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

Source: `hooks/codex.toml`. All events route to `agent-gate codex-hook`.

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
| `SubagentStart`     | `.*`                     |
| `PreToolUse`        | `.*`                     |
| `PermissionRequest` | `.*`                     |
| `PostToolUse`       | `.*`                     |
| `PreCompact`        |                          |
| `PostCompact`       |                          |
| `UserPromptSubmit`  |                          |
| `SubagentStop`      |                          |
| `Stop`              |                          |

Codex hooks require `hooks = true` under `[features]` in
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

Source: `hooks/cursor.json`. All events route to bare `agent-gate`.

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

`install.sh` preserves unrelated user settings across re-runs. JSON-based
tools update only their hook key. Codex updates a marked TOML block in
`$HOME/.codex/config.toml` and ensures `[features] hooks = true`.
