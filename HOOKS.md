# Hook inventory

`agent-gate` installs the registered events below for Claude Code, Codex,
Cursor, Gemini CLI, and GitHub Copilot Chat. The files under `hooks/` are the
installation templates. [docs/hook-schemas.md](docs/hook-schemas.md) describes
the payload fields the daemon parses and the responses it renders.

The provider contracts are
[Claude Code hooks](https://code.claude.com/docs/en/hooks),
[Codex hooks](https://learn.chatgpt.com/docs/hooks),
[Cursor hooks](https://cursor.com/docs/hooks),
[Gemini CLI hooks](https://geminicli.com/docs/hooks/reference/), and
[GitHub Copilot hooks](https://docs.github.com/en/enterprise-cloud@latest/copilot/reference/hooks-reference).

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
| Copilot `preToolUse` | block |
| Copilot `postToolUse`, `sessionStart`, `subagentStart`, `notification`, `userPromptTransformed` | observe |
| Cursor `preToolUse`, `beforeShellExecution`, `beforeMCPExecution`, `beforeReadFile`, `beforeSubmitPrompt`, `beforeTabFileRead` | block |
| Cursor `postToolUse` | substitute supported tool output |
| Cursor post and agent-output events | observe |
| Gemini `BeforeTool` | block |
| Gemini `AfterTool` and lifecycle events | observe |

Block events fire before the protected action and can stop it. Substitute
events run after the action but can replace the result the agent sees. Observe
events can record or add context, but cannot undo the action or hide its result.
Pairs absent from the capability table default to observe.

## Model-facing response actions

`action = "inject"` and `action = "mutate"` run through the same rule
evaluator as `block` and `audit`. They use the same event filters, provider
filters, conditions, inference, exec gates, cache, environment guard, and
trace. `output` supplies static text and `output_file` reads static text
relative to the configuration file. The fields are mutually exclusive. A
matching `exec` condition replaces that fallback with its complete stdout.

Injection outputs join in configuration order with one blank line. The last
valid mutation for a target replaces its value. A blocking decision suppresses
all response effects. Empty, errored, invalid, and unsupported effects are
audited as no-ops without recording their content.

| Provider and event | Injection | Mutation |
| --- | --- | --- |
| Claude `SessionStart`, `Setup`, `SubagentStart`, `UserPromptSubmit`, `UserPromptExpansion`, `PreToolUse`, `PostToolUse`, `PostToolUseFailure`, `PostToolBatch`, `Stop`, `SubagentStop` | `hookSpecificOutput.additionalContext` | `PreToolUse.permissionDecision = "allow"` with `updatedInput`, `PostToolUse.updatedToolOutput` |
| Codex `SessionStart`, `SubagentStart`, `UserPromptSubmit`, `PreToolUse`, `PostToolUse` | `hookSpecificOutput.additionalContext` | `PreToolUse.updatedInput` |
| Cursor `sessionStart`, `postToolUse` | `additional_context` | `postToolUse.updated_mcp_tool_output` |
| Cursor `stop` | submits a new prompt through `followup_message` | none |
| Copilot `sessionStart`, `subagentStart`, `postToolUse`, `postToolUseFailure`, `notification` | `additionalContext` | `preToolUse.modifiedArgs`, `postToolUse.modifiedResult` |
| Copilot `userPromptTransformed` | prepends context through `modifiedTransformedPrompt` | replaces through `modifiedTransformedPrompt` |

Cursor `beforeSubmitPrompt` remains configurable for response rules, but it
warns and returns a no-op because Cursor does not currently expose a
model-facing response field for that event.

## Managed installation

| Provider | Installed config | Hook command | Managed ownership |
| --- | --- | --- | --- |
| Claude | `$HOME/.claude/settings.json` | installed binary | Replaces prior agent-gate commands inside `hooks`; preserves unrelated settings and hooks. |
| Codex | `$HOME/.codex/config.toml` | installed binary plus `codex-hook` | Replaces the marked agent-gate block and sets `[features] hooks = true`; preserves content outside the block. |
| Cursor | `$HOME/.cursor/hooks.json` | installed binary | Replaces prior agent-gate commands inside the top-level `hooks` object; preserves unrelated settings and hooks. |
| Gemini | `$HOME/.gemini/settings.json` | installed binary plus `gemini-hook` | Replaces prior agent-gate commands inside `hooks`; preserves unrelated settings and hooks. |
| Copilot | `$HOME/.copilot/hooks/agent-gate.json` | installed binary plus `copilot-hook <event>` | Owns and replaces this dedicated file. Other files in the hooks directory remain untouched. |

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
| `Setup` | none |
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

Copilot uses lower-camel event names and VS Code-shaped tool inputs. Each
managed command carries the event name through `copilot-hook <event>` because
`userPromptTransformed` does not identify itself in its payload. The daemon
uses that hint to normalize camelCase fields before rule evaluation.

| Event | Matcher |
| --- | --- |
| `sessionStart` | none |
| `subagentStart` | none |
| `userPromptTransformed` | none |
| `preToolUse` | `.*` |
| `postToolUse` | `.*` |
| `postToolUseFailure` | `.*` |
| `notification` | none |

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
