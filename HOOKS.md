# Hook inventory

`agent-gate` installs the registered events below for Claude Code, Codex,
Cursor, Gemini CLI, and GitHub Copilot Chat. The files under `hooks/` are the
installation templates. [docs/hook-schemas.md](docs/hook-schemas.md) describes
the payload fields the daemon parses and the responses it renders.

## Provider capabilities

A rule's `action = "block"` expresses policy intent. The event capability
determines what the provider can enforce. The daemon warns during config load
when a blocking rule targets an observe-only event.

| Provider and event | Capability |
| --- | --- |
| Claude `PreToolUse`, `PermissionRequest`, `UserPromptSubmit` | block |
| Claude `PostToolUse`, `PostToolUseFailure`, `PermissionDenied`, `Stop`, `StopFailure`, `SubagentStop` | observe |
| Codex `PreToolUse`, `PermissionRequest`, `UserPromptSubmit` | block |
| Codex `PostToolUse` | substitute the result shown to the agent |
| Codex lifecycle events | observe |
| Copilot `PreToolUse`, `UserPromptSubmit` | block |
| Copilot `PostToolUse`, `Stop` | observe |
| Cursor `preToolUse`, `beforeShellExecution`, `beforeMCPExecution`, `beforeReadFile`, `beforeSubmitPrompt`, `beforeTabFileRead` | block |
| Cursor `postToolUse` | substitute supported tool output |
| Cursor post and agent-output events | observe |
| Gemini `BeforeTool` | block |
| Gemini `AfterTool` and lifecycle events | observe |

Block events fire before the protected action and can stop it. Substitute
events run after the action but can replace the result the agent sees. Observe
events can record or add context, but cannot undo the action or hide its result.
Pairs absent from the capability table default to observe.

## Managed installation

| Provider | Installed config | Hook command | Managed ownership |
| --- | --- | --- | --- |
| Claude | `$HOME/.claude/settings.json` | installed binary | Replaces prior agent-gate commands inside `hooks`; preserves unrelated settings and hooks. |
| Codex | `$HOME/.codex/config.toml` | installed binary plus `codex-hook` | Replaces the marked agent-gate block and sets `[features] hooks = true`; preserves content outside the block. |
| Cursor | `$HOME/.cursor/hooks.json` | installed binary | Replaces prior agent-gate commands inside the top-level `hooks` object; preserves unrelated settings and hooks. |
| Gemini | `$HOME/.gemini/settings.json` | installed binary plus `gemini-hook` | Replaces prior agent-gate commands inside `hooks`; preserves unrelated settings and hooks. |
| Copilot | `$HOME/.copilot/hooks/agent-gate.json` | installed binary | Owns and replaces this dedicated file. Other files in the hooks directory remain untouched. |

Every JSON template sets `failClosed: false`. Writes are atomic, and malformed
existing JSON stops installation before the target changes. The template
placeholder `__AGENT_GATE_BIN__` becomes the absolute installed binary path.

Reinstall every hook template around the current binary:

```sh
agent-gate install hooks --bin-path "$(command -v agent-gate)"
```

Reinstall one provider by opting out of the other four:

```sh
agent-gate install hooks --bin-path "$(command -v agent-gate)" --no-codex --no-cursor --no-gemini --no-copilot
agent-gate install hooks --bin-path "$(command -v agent-gate)" --no-claude --no-cursor --no-gemini --no-copilot
agent-gate install hooks --bin-path "$(command -v agent-gate)" --no-claude --no-codex --no-gemini --no-copilot
agent-gate install hooks --bin-path "$(command -v agent-gate)" --no-claude --no-codex --no-cursor --no-copilot
agent-gate install hooks --bin-path "$(command -v agent-gate)" --no-claude --no-codex --no-cursor --no-gemini
```

The commands above select Claude, Codex, Cursor, Gemini, and Copilot in that
order.

## Claude

Template: `hooks/claude.json`

Registered events:

| Event | Matcher |
| --- | --- |
| `ConfigChange` | none |
| `CwdChanged` | none |
| `Elicitation` | none |
| `ElicitationResult` | none |
| `FileChanged` | none |
| `InstructionsLoaded` | none |
| `Notification` | none |
| `PermissionDenied` | `.*` |
| `PermissionRequest` | `.*` |
| `PostCompact` | none |
| `PostToolUse` | `.*` |
| `PostToolUseFailure` | `.*` |
| `PreCompact` | none |
| `PreToolUse` | `.*` |
| `SessionEnd` | none |
| `SessionStart` | none |
| `Stop` | none |
| `StopFailure` | none |
| `SubagentStart` | none |
| `SubagentStop` | none |
| `TaskCompleted` | none |
| `TaskCreated` | none |
| `TeammateIdle` | none |
| `UserPromptSubmit` | none |

## Codex

Template: `hooks/codex.toml`

| Event | Matcher |
| --- | --- |
| `SessionStart` | `startup\|resume\|clear` |
| `SubagentStart` | `.*` |
| `PreToolUse` | `.*` |
| `PermissionRequest` | `.*` |
| `PostToolUse` | `.*` |
| `PreCompact` | none |
| `PostCompact` | none |
| `UserPromptSubmit` | none |
| `SubagentStop` | none |
| `Stop` | none |

## Copilot

Template: `hooks/copilot.json`

Copilot uses Claude-style event names with VS Code-shaped tool inputs. The
adapter uses the Copilot environment fingerprint and normalizes those tool
arguments before rule evaluation.

| Event | Matcher |
| --- | --- |
| `SessionStart` | none |
| `SessionEnd` | none |
| `UserPromptSubmit` | none |
| `PreToolUse` | `.*` |
| `PostToolUse` | `.*` |
| `PreCompact` | none |
| `PostCompact` | none |
| `Notification` | none |
| `Stop` | none |

## Cursor

Template: `hooks/cursor.json`

Cursor registers these events without matchers:

| Event | Event | Event | Event |
| --- | --- | --- | --- |
| `sessionStart` | `sessionEnd` | `preToolUse` | `postToolUse` |
| `postToolUseFailure` | `beforeShellExecution` | `afterShellExecution` | `beforeMCPExecution` |
| `afterMCPExecution` | `beforeReadFile` | `afterFileEdit` | `beforeSubmitPrompt` |
| `subagentStart` | `subagentStop` | `preCompact` | `stop` |
| `afterAgentResponse` | `afterAgentThought` | `beforeTabFileRead` | `afterTabFileEdit` |

## Gemini

Template: `hooks/gemini.json`

| Event | Matcher |
| --- | --- |
| `BeforeTool` | `.*` |
| `AfterTool` | `.*` |
| `BeforeAgent` | none |
| `AfterAgent` | none |
| `BeforeModel` | none |
| `BeforeToolSelection` | none |
| `AfterModel` | none |
| `SessionStart` | `startup`, `resume`, `clear` |
| `SessionEnd` | `exit`, `clear`, `logout`, `prompt_input_exit`, `other` |
| `Notification` | `ToolPermission` |
| `PreCompress` | `auto`, `manual` |
