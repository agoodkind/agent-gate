# Hook payload and response contracts

The daemon parses the provider payload fields below into typed adapters. Fields
not listed here can remain in the raw intake payload, but they do not define the
current typed rule surface. [../HOOKS.md](../HOOKS.md) lists which events the
installer registers.

All providers send JSON on standard input. The daemon selects an adapter from
the explicit hook subcommand, provider environment signals, and payload
fingerprints. Unknown events retain their common event, session, conversation,
and working-directory fields through the fallback payload.

## Claude

Claude payloads share this envelope:

```typescript
type ClaudeEnvelope = {
  hook_event_name: string;
  session_id: string;
  transcript_path: string;
  cwd: string;
  permission_mode?: string;
  agent_id?: string;
  agent_type?: string;
  model?: string;
  turn_id?: string;
  timestamp?: string;
};
```

The typed adapter recognizes these event-specific fields:

| Event | Additional fields parsed by agent-gate |
| --- | --- |
| `SessionStart` | `source` |
| `SessionEnd` | `reason`, `duration_ms` |
| `Setup` | `trigger` |
| `PreToolUse` | `tool_name`, `tool_use_id`, `tool_input` |
| `PostToolUse` | tool fields plus `tool_response` |
| `PostToolUseFailure` | tool fields plus `error`, `error_type`, `is_interrupt` |
| `PermissionRequest` | tool fields plus `permission_suggestions` |
| `PermissionDenied` | tool fields plus `reason` |
| `UserPromptSubmit` | `prompt`, `session_title` |
| `Stop` | `stop_hook_active`, nullable `last_assistant_message` |
| `StopFailure` | `error`, `error_details`, `last_assistant_message` |
| `SubagentStart` | envelope fields |
| `SubagentStop` | `stop_hook_active`, `agent_transcript_path`, `last_assistant_message` |
| `TaskCreated`, `TaskCompleted` | `task_id`, `task_subject`, `task_description`, `teammate_name`, `team_name` |
| `Notification` | `notification_type`, `message`, `title` |
| `PreCompact` | `trigger`, `custom_instructions` |
| `PostCompact` | `trigger`, `compact_summary` |
| `InstructionsLoaded` | `file_path`, `memory_type`, `load_reason`, `globs`, `trigger_file_path`, `parent_file_path` |
| `ConfigChange` | `source`, `file_path` |
| `CwdChanged` | `old_cwd`, `new_cwd` |
| `FileChanged` | `file_path`, `event` |
| `WorktreeCreate` | `name` |
| `WorktreeRemove` | `worktree_path` |
| `Elicitation` | `mcp_server_name`, `message`, `mode`, `url`, `elicitation_id` |
| `ElicitationResult` | `mcp_server_name`, `elicitation_id`, `mode`, `action` |
| `TeammateIdle` | `teammate_name`, `team_name` |
| `PostToolBatch` | `tool_calls[]` with tool name, id, input, and response |
| `UserPromptExpansion` | `expansion_type`, `command_name`, `command_args`, `command_source`, `prompt` |
| `MessageDisplay` | `message_id`, `index`, `final`, `delta` |

Claude tool inputs expose `command`, `file_path`, `content`, `old_string`,
`new_string`, `description`, `prompt`, `path`, `pattern`, `url`, and `query`
when the invoking tool supplies them. Structured tool responses contribute only
searchable text to rule fields; image bytes remain in raw intake and are not
scanned as output text.

An allow response writes `{}` to standard output and exits 0. A blocking event
writes the diagnostic to standard error and exits 2. Observe-only events are
downgraded before response rendering.

## Codex

Codex payloads share this envelope:

```typescript
type CodexEnvelope = {
  hook_event_name: string;
  session_id: string;
  transcript_path: string;
  cwd: string;
  model: string;
};
```

| Event | Additional fields parsed by agent-gate |
| --- | --- |
| `SessionStart` | `source` |
| `PreToolUse`, `PermissionRequest` | `turn_id`, `tool_name`, `tool_use_id`, `tool_input` |
| `PostToolUse` | tool fields plus `tool_response` |
| `UserPromptSubmit` | `turn_id`, `prompt` |
| `Stop` | `turn_id`, `stop_hook_active`, `last_assistant_message` |
| `PreCompact`, `PostCompact` | `turn_id`, `trigger` |
| `SubagentStart` | `turn_id`, `permission_mode`, `agent_id`, `agent_type` |
| `SubagentStop` | start fields plus `stop_hook_active`, `agent_transcript_path`, `last_assistant_message` |

Codex tool inputs expose `command`, `file_path`, `content`, `old_string`,
`new_string`, `description`, `prompt`, `workdir`, `directory`, `path`,
`pattern`, `url`, and `query` when present.

Codex allow responses write `{}` and exit 0. Blocks also exit 0 and use the
event-specific JSON channel:

- `PreToolUse` sets `hookSpecificOutput.permissionDecision` to `deny` and adds
  `permissionDecisionReason`.
- `PermissionRequest` sets `hookSpecificOutput.decision.behavior` to `deny` and
  adds its message.
- `PostToolUse` sets `continue` to false, `decision` to `block`, and carries the
  diagnostic in `stopReason` and `reason`.
- `UserPromptSubmit` sets `decision` to `block` and carries `reason`.
- Lifecycle events render `{}` because they are observe-only.

## Copilot

Copilot uses Claude-style event names and a VS Code-shaped payload:

```typescript
type CopilotPayload = {
  hook_event_name: string;
  session_id: string;
  transcript_path: string;
  cwd: string;
  tool_name?: string;
  tool_use_id?: string;
  tool_input?: {
    command?: string;
    filePath?: string;
    content?: string;
    prompt?: string;
    oldString?: string;
    newString?: string;
    replacements?: Array<{
      filePath?: string;
      oldString?: string;
      newString?: string;
    }>;
  };
  text?: string;
  assistant_message?: string;
  last_assistant_message?: string;
};
```

The adapter joins multi-replacement old and new strings into the corresponding
rule fields. On `Stop`, it reads the last assistant message from the referenced
JSONL transcript only when the payload omits assistant text. Copilot uses the
same allow and block response shapes as Claude.

## Cursor

Cursor payloads share this envelope:

```typescript
type CursorEnvelope = {
  hook_event_name: string;
  session_id?: string;
  conversation_id: string;
  generation_id: string;
  model: string;
  cursor_version: string;
  workspace_roots: string[];
  user_email: string;
  transcript_path: string | null;
};
```

| Event | Additional fields parsed by agent-gate |
| --- | --- |
| `sessionStart` | envelope fields |
| `sessionEnd` | `reason`, `final_status` |
| `preToolUse` | `tool_name`, `tool_use_id`, `tool_input`, `cwd`, `duration` |
| `postToolUse` | tool fields plus `tool_output`, `duration` |
| `postToolUseFailure` | tool fields plus `error_message`, `failure_type`, `is_interrupt`, `duration` |
| `beforeShellExecution` | `command`, `cwd`, `sandbox` |
| `afterShellExecution` | `command`, `cwd`, `output`, `sandbox`, `duration` |
| `beforeMCPExecution` | `tool_name`, `tool_use_id`, object or string `tool_input`, `cwd` |
| `afterMCPExecution` | MCP fields plus `tool_output`, `result_json` |
| `beforeReadFile`, `beforeTabFileRead` | `file_path`, `cwd` |
| `afterFileEdit`, `afterTabFileEdit` | `file_path`, `edits[]` |
| `beforeSubmitPrompt` | `prompt`, `text`, `cwd`, `attachments[]` |
| `subagentStart` | `subagent_id`, `subagent_type`, `task`, `parent_conversation_id`, `tool_call_id`, worker flags |
| `subagentStop` | subagent identity plus `description`, `agent_transcript_path`, counts, and duration |
| `preCompact` | `trigger`, context counts, token counts, `is_first_compaction` |
| `stop` | `status`, `loop_count`, `composer_mode`, token counts |
| `afterAgentResponse` | `text`, `assistant_message`, token counts |
| `afterAgentThought` | `text`, `assistant_message` |

Cursor tool input objects expose `command`, `file_path`, `content`, `pattern`,
`url`, `query`, `workdir`, `working_directory`, `directory`, and `cwd` when
present. MCP inputs may arrive as an object, a JSON-encoded string, or plain
text. Malformed JSON strings remain available as content.

Allow responses write `{"permission":"allow"}` and exit 0. Block responses
write `permission: "deny"`, copy the diagnostic to `user_message` and
`agent_message`, and exit 0. The capability layer prevents observe-only events
from receiving a deny response.

## Gemini

Gemini payloads share this envelope:

```typescript
type GeminiEnvelope = {
  hook_event_name: string;
  session_id: string;
  transcript_path: string;
  cwd: string;
  timestamp: string;
};
```

| Event | Additional fields parsed by agent-gate |
| --- | --- |
| `BeforeTool` | `tool_name`, `tool_input`, `mcp_context`, `original_request_name` |
| `AfterTool` | tool fields plus `tool_response` |
| `BeforeAgent` | `prompt` |
| `AfterAgent` | `prompt`, `prompt_response`, `stop_hook_active` |
| `BeforeModel`, `BeforeToolSelection` | `llm_request` |
| `AfterModel` | `llm_request`, `llm_response` |
| `SessionStart` | `source` |
| `SessionEnd` | `reason` |
| `Notification` | `notification_type`, `message`, `details` |
| `PreCompress` | `trigger` |

Gemini tool inputs expose `command`, `file_path`, `content`, `old_string`,
`new_string`, `description`, `workdir`, `directory`, `path`, `pattern`, `url`,
and `query` when present.

Gemini allow responses write `{}` and exit 0. A `BeforeTool` block writes
`{"decision":"deny","reason":"..."}` and exits 0. Other registered Gemini
events are observe-only in the current capability table.

## Rule-visible virtual fields

The typed payload fields above can be combined with daemon-computed selectors:

- `effective_cwd` chooses the operation-level directory, applies shell `cd`
  transitions, and falls back to the payload directory.
- `cmd_segments` exposes parsed shell command segments.
- `cmd_comments` and `cmd_double_hyphen_prose` isolate prose-like command text.
- `cmd_redirections` exposes direct output redirections.
- `cmd_write_targets` exposes parsed local write targets.

The annotated [../config.toml.example](../config.toml.example) shows how these
selectors participate in deterministic, external-validator, and inference
conditions.
