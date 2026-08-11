# Hook payload and response contracts

The daemon preserves each hook request, classifies its provider, then evaluates
a separate normalized payload. The sections below define the resulting payload
and response contracts. The [hook inventory](../HOOKS.md) defines registered
events.

## Preserve hook input

Each hook sends JSON on standard input. Agent Gate stores those exact bytes
before enrichment or normalization. This wire input remains unchanged in intake
and audit records.

Agent Gate derives a separate normalized payload for evaluation. Request working
directories and Copilot field conversion affect only this derived value. Deferred
replay uses the stored wire input, normalized payload, and provider decision.

A genuine empty hook request creates an invalid intake event with a receipt and a
zero-byte payload. Malformed nonempty input also remains durable. Help and invalid
command-line requests never enter hook mode, so they create no intake event.

Seen-event queries omit wire payload bodies. JSON output includes the wire byte
count and hash in the classification input. Use `--include-normalized` to inspect
the derived payload.

## Classify providers

Agent Gate classifies each accepted request from the complete available evidence
set. It records unavailable evidence instead of inventing a value.

| Evidence | Recorded input | Classification role |
| --- | --- | --- |
| Routing | Explicit provider hint, hook subcommand, literal hook tags, and full argument vector | Direct route evidence |
| Payload | Event name, top-level field names, field casing, detected shapes, and provider identifiers | Provider identity and shape evidence |
| Invocation | Working directory and hook executable identity | Supporting request context |
| Process | Parent name and executable path, plus available ancestor names and paths | Supporting provider context |
| Environment | Provider variables inherited from the host and values injected by a hook | Supporting provider context with provenance |
| Registration | Provider tag carried by a managed hook command | Supporting installation context |
| Availability | Missing or unreadable signals and collection errors | Explicit limits on the decision |

The classifier applies this precedence from strongest to weakest:

1. Explicit provider hints and provider-specific hook subcommands.
2. Provider identifiers inside the payload.
3. Provider-specific payload shapes and event identities.
4. Managed registration context.
5. Executable, parent, ancestor, and environment evidence.
6. Shared shapes, event names, and host markers.

The working directory remains supporting context. It does not identify a
provider by itself. Process evidence can resolve a provider only when stronger
evidence is absent and the process identity is specific.

Environment evidence preserves the provenance supplied with each value. The
hook process marks its process environment as inherited because a variable name
cannot prove who injected it. An invocation context can identify a hook-injected
value explicitly. A Claude marker remains Claude evidence when another harness
inherits it. The marker does not override explicit routing, provider
identifiers, or provider-specific payload shape.

Equal strongest evidence for different providers produces an ambiguous result.
Weaker disagreement remains in both the full evidence list and the conflict
list. Missing and unreadable signals remain in the stored input with their
collection status and error source.

Each intake event records one classification object:

| Field | Meaning |
| --- | --- |
| `input` | Routing, payload, invocation, process, environment, registration, availability, wire byte count, and wire hash used by classification. |
| `resolved_provider` | Selected payload and response adapter, or `unknown`. |
| `confidence` | `high`, `medium`, `low`, or `none`, based on evidence strength and conflicts. |
| `evidence` | Every provider candidate with its source, provenance, strength, and result. |
| `conflicts` | Evidence that supports a provider other than the resolved provider. |
| `result` | `resolved`, `ambiguous`, `unknown`, or `invalid`. |

High confidence means route, identifier, or payload evidence selected one
provider without conflicting payload-strength evidence. Medium confidence means
stronger evidence won despite another provider-specific payload or route signal.
Registration or context-only resolution has low confidence. Ambiguous, unknown,
and invalid results have no confidence.

The daemon stores this decision with the intake event. Evaluation and deferred
replay reuse the stored wire input, normalized payload, environment fingerprint,
and classification. Replay does not reinterpret the request from a later process
or environment.

Classification collection failures do not create guessed values. Valid payloads
continue with the remaining evidence. Empty or malformed payloads remain durable
invalid events. Storage failures report the database cause and do not produce a
receipt for an event that was not accepted.

Unknown events retain common event, session, conversation, and working-directory
fields through the fallback payload. Fields not listed below remain in the wire
input but do not define the typed rule surface.

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
`new_string`, `description`, `prompt`, `pattern`, `url`, and `query`
when the invoking tool supplies them. Structured tool responses contribute only
searchable text to rule fields; image bytes remain in raw intake and are not
scanned as output text.

An allow response writes `{}` to standard output and exits 0. A blocking event
writes the diagnostic to standard error and exits 2. Observe-only events are
downgraded before response rendering.

Matching `inject` rules use `hookSpecificOutput.additionalContext` on
`SessionStart`, `Setup`, `SubagentStart`, `UserPromptSubmit`,
`UserPromptExpansion`, `PreToolUse`, `PostToolUse`, `PostToolUseFailure`, and
`PostToolBatch`, `Stop`, and `SubagentStop`. `mutate` uses `updatedInput` with
`permissionDecision: "allow"` for `PreToolUse` and
`updatedToolOutput` for `PostToolUse`.

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

`inject` uses `hookSpecificOutput.additionalContext` on session, subagent,
prompt, and pre or post tool events. `mutate` uses `permissionDecision:
"allow"` with `updatedInput` on `PreToolUse`. Codex currently has no supported
post-tool mutation response.

## Copilot

Copilot uses lower-camel event names and a VS Code-shaped payload:

```typescript
type CopilotPayload = {
  // Added only to normalized evaluation data.
  hook_event_name: string;
  sessionId: string;
  transcriptPath: string;
  cwd: string;
  toolName?: string;
  toolUseId?: string;
  toolInput?: {
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
  assistantMessage?: string;
  lastAssistantMessage?: string;
  prompt?: string;
  transformedPrompt?: string;
};
```

The daemon adds `hook_event_name` to the normalized payload from the event tag in
`managed-hook copilot <event>`. It also normalizes camelCase fields before
evaluation. The wire input remains unchanged. The adapter joins multi-replacement
old and new strings into the corresponding rule fields. `sessionStart`, `subagentStart`,
`postToolUse`, `postToolUseFailure`, and `notification` can return
`additionalContext`. `preToolUse` can return `modifiedArgs`, `postToolUse` can
return `modifiedResult` as a successful `ToolResult` object with `resultType`
and `textResultForLlm`, and
`userPromptTransformed` can return `modifiedTransformedPrompt`. Injection on a
transformed prompt prepends the combined context and two newlines to the prompt.

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

`sessionStart` and `postToolUse` accept `additional_context`. `postToolUse`
also accepts `updated_mcp_tool_output`. Cursor `beforeSubmitPrompt` accepts
submission control and a user-visible message only, so injection and mutation
rules for it render an audited no-op.

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
