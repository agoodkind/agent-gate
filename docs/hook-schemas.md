# Hook Event Schemas

Complete JSON payload schemas for supported hook events in Claude Code, Cursor, Codex, and Gemini CLI.

**Sources:**
- Claude Code: https://code.claude.com/docs/en/hooks
- Cursor: https://cursor.com/docs/hooks
- Codex: local integration notes in this repository
- Gemini CLI: local integration notes in this repository

Last verified: 2026-04-15.

---

## Codex

### Common fields

```typescript
{
  session_id: string;
  transcript_path: string | null;
  cwd: string;
  hook_event_name: string;
  model: string;
}
```

### Supported events

```typescript
type CodexEvent =
  | "SessionStart"
  | "PreToolUse"
  | "PermissionRequest"
  | "PostToolUse"
  | "UserPromptSubmit"
  | "Stop";
```

### Important event-specific fields

```typescript
// SessionStart
{ source: "startup" | "resume" | "clear" }

// PreToolUse / PermissionRequest / PostToolUse
{ turn_id: string; tool_name: string; tool_use_id?: string; tool_input: object; tool_response?: object }

// UserPromptSubmit
{ turn_id: string; prompt: string }

// Stop
{ turn_id: string; stop_hook_active: boolean; last_assistant_message?: string }
```

---

## Gemini CLI

### Common fields

```typescript
{
  session_id: string;
  transcript_path: string;
  cwd: string;
  hook_event_name: string;
  timestamp: string;
}
```

### Supported events

```typescript
type GeminiEvent =
  | "BeforeTool"
  | "AfterTool"
  | "BeforeAgent"
  | "AfterAgent"
  | "BeforeModel"
  | "BeforeToolSelection"
  | "AfterModel"
  | "SessionStart"
  | "SessionEnd"
  | "Notification"
  | "PreCompress";
```

### Important event-specific fields

```typescript
// BeforeTool / AfterTool
{ tool_name: string; tool_input: object; tool_response?: object; mcp_context?: object; original_request_name?: string }

// BeforeAgent
{ prompt: string }

// AfterAgent
{ prompt: string; prompt_response: string; stop_hook_active: boolean }

// BeforeModel / BeforeToolSelection / AfterModel
{ llm_request: object; llm_response?: object }

// SessionStart / SessionEnd / Notification / PreCompress
{ source?: "startup" | "resume" | "clear"; reason?: string; notification_type?: string; message?: string; trigger?: "auto" | "manual" }
```

---

## Claude Code (26 events)

Source: https://code.claude.com/docs/en/hooks

### Common fields (all events)

```typescript
{
  session_id: string;
  transcript_path: string;
  cwd: string;
  hook_event_name: string;
  permission_mode?: "default" | "plan" | "acceptEdits" | "auto" | "dontAsk" | "bypassPermissions";
  agent_id?: string;
  agent_type?: string;
}
```

### SessionStart

```typescript
{
  source: "startup" | "resume" | "clear" | "compact";
  model: string;
  agent_type?: string;
}
```

### SessionEnd

```typescript
{
  reason: "clear" | "resume" | "logout" | "prompt_input_exit" | "bypass_permissions_disabled" | "other";
}
```

### UserPromptSubmit

```typescript
{
  prompt: string;
}
```

### PreToolUse

```typescript
{
  tool_name: string;
  tool_input: ToolInput; // varies by tool, see below
  tool_use_id: string;
}
```

**Tool input by tool_name:**

```typescript
// Bash
{ command: string; description?: string; timeout?: number; run_in_background?: boolean; }

// Write
{ file_path: string; content: string; }

// Edit
{ file_path: string; old_string: string; new_string: string; replace_all?: boolean; }

// Read
{ file_path: string; offset?: number; limit?: number; }

// Glob
{ pattern: string; path?: string; }

// Grep
{ pattern: string; path?: string; glob?: string; output_mode?: "content" | "files_with_matches" | "count"; "-i"?: boolean; multiline?: boolean; }

// WebFetch
{ url: string; prompt: string; }

// WebSearch
{ query: string; allowed_domains?: string[]; blocked_domains?: string[]; }

// Agent
{ prompt: string; description: string; subagent_type: string; model?: string; }

// AskUserQuestion
{ questions: Array<{ question: string; header: string; options: Array<{ label: string }>; multiSelect?: boolean; }>; answers?: Record<string, string>; }

// ExitPlanMode
{}
```

### PostToolUse

```typescript
{
  tool_name: string;
  tool_input: ToolInput;
  tool_response: any;
  tool_use_id: string;
}
```

### PostToolUseFailure

```typescript
{
  tool_name: string;
  tool_input: ToolInput;
  tool_use_id: string;
  error: string;
  is_interrupt?: boolean;
}
```

### PermissionRequest

```typescript
{
  tool_name: string;
  tool_input: ToolInput;
  permission_suggestions?: Array<{
    type: "addRules" | "replaceRules" | "removeRules" | "setMode" | "addDirectories" | "removeDirectories";
    rules?: Array<{ toolName: string; ruleContent?: string }>;
    behavior?: "allow" | "deny" | "ask";
    mode?: "default" | "acceptEdits" | "dontAsk" | "bypassPermissions" | "plan";
    directories?: string[];
    destination: "session" | "localSettings" | "projectSettings" | "userSettings";
  }>;
}
```

### PermissionDenied

```typescript
{
  tool_name: string;
  tool_input: ToolInput;
  tool_use_id: string;
  reason: string;
}
```

### Notification

```typescript
{
  message: string;
  title?: string;
  notification_type: "permission_prompt" | "idle_prompt" | "auth_success" | "elicitation_dialog";
}
```

### SubagentStart

```typescript
{
  agent_id: string;
  agent_type: string;
}
```

### SubagentStop

```typescript
{
  stop_hook_active: boolean;
  agent_id: string;
  agent_type: string;
  agent_transcript_path: string;
  last_assistant_message: string;
}
```

### TaskCreated

```typescript
{
  task_id: string;
  task_subject: string;
  task_description?: string;
  teammate_name?: string;
  team_name?: string;
}
```

### TaskCompleted

```typescript
{
  task_id: string;
  task_subject: string;
  task_description?: string;
  teammate_name?: string;
  team_name?: string;
}
```

### Stop

```typescript
{
  // No additional fields beyond common.
}
```

### StopFailure

```typescript
{
  error_type: "rate_limit" | "authentication_failed" | "billing_error" | "invalid_request" | "server_error" | "max_output_tokens" | "unknown";
}
```

### TeammateIdle

```typescript
{
  teammate_name?: string;
  team_name?: string;
}
```

### InstructionsLoaded

```typescript
{
  file_path: string;
  memory_type: "User" | "Project" | "Local" | "Managed";
  load_reason: "session_start" | "nested_traversal" | "path_glob_match" | "include" | "compact";
  globs?: string[];
  trigger_file_path?: string;
  parent_file_path?: string;
}
```

### ConfigChange

```typescript
{
  // Common fields only.
}
```

### CwdChanged

```typescript
{
  new_cwd: string;
  previous_cwd: string;
}
```

### FileChanged

```typescript
{
  file_path: string;
  change_type: "created" | "modified" | "deleted";
}
```

### WorktreeCreate

```typescript
{
  isolation_type: "worktree";
  parent_worktree_path?: string;
}
```

### WorktreeRemove

```typescript
{
  worktree_path: string;
}
```

### PreCompact

```typescript
{
  trigger: "manual" | "auto";
}
```

### PostCompact

```typescript
{
  trigger: "manual" | "auto";
}
```

### Elicitation

```typescript
{
  mcp_server_name: string;
  tool_name: string;
  elicitation_request: {
    type: string;
    prompt: string;
    fields: Array<{
      name: string;
      type: string;
      description?: string;
      required?: boolean;
    }>;
  };
}
```

### ElicitationResult

```typescript
{
  mcp_server_name: string;
  tool_name: string;
  user_response: Record<string, any>;
}
```

---

## Cursor (20 events)

Source: https://cursor.com/docs/hooks

### Common fields (all events)

```typescript
{
  conversation_id: string;
  generation_id: string;
  model: string;
  hook_event_name: string;
  cursor_version: string;
  workspace_roots: string[];
  user_email: string | null;
  transcript_path: string | null;
}
```

### sessionStart

```typescript
{
  session_id: string;
  is_background_agent: boolean;
  composer_mode: string | null;
}
```

### sessionEnd

```typescript
{
  session_id: string;
  reason: "completed" | "aborted" | "error" | "window_close" | "user_close";
  duration_ms: number;
  is_background_agent: boolean;
  final_status: string;
  error_message: string | null;
}
```

### preToolUse

```typescript
{
  tool_name: string;
  tool_input: object;
  tool_use_id: string;
  cwd: string;
  model: string;
  agent_message: string;
}
```

### postToolUse

```typescript
{
  tool_name: string;
  tool_input: object;
  tool_output: string;
  tool_use_id: string;
  cwd: string;
  duration: number;
  model: string;
}
```

### postToolUseFailure

```typescript
{
  tool_name: string;
  tool_input: object;
  tool_use_id: string;
  cwd: string;
  error_message: string;
  failure_type: "timeout" | "error" | "permission_denied";
  duration: number;
  is_interrupt: boolean;
}
```

### subagentStart

```typescript
{
  subagent_id: string;
  subagent_type: string;
  task: string;
  parent_conversation_id: string;
  tool_call_id: string;
  subagent_model: string;
  is_parallel_worker: boolean;
  git_branch: string | null;
}
```

### subagentStop

```typescript
{
  subagent_type: string;
  status: "completed" | "error" | "aborted";
  task: string;
  description: string;
  summary: string;
  duration_ms: number;
  message_count: number;
  tool_call_count: number;
  loop_count: number;
  modified_files: string[];
  agent_transcript_path: string | null;
}
```

### beforeShellExecution

```typescript
{
  command: string;
  cwd: string;
  sandbox: boolean;
}
```

### afterShellExecution

```typescript
{
  command: string;
  output: string;
  duration: number;
  sandbox: boolean;
}
```

### beforeMCPExecution

```typescript
{
  tool_name: string;
  tool_input: string;
  url?: string;     // for HTTP MCP servers
  command?: string;  // for stdio MCP servers
}
```

### afterMCPExecution

```typescript
{
  tool_name: string;
  tool_input: string;
  result_json: string;
  duration: number;
}
```

### beforeReadFile

```typescript
{
  file_path: string;
  content: string;
  attachments: Array<{
    type: "file" | "rule";
    file_path: string;
  }>;
}
```

### afterFileEdit

```typescript
{
  file_path: string;
  edits: Array<{
    old_string: string;
    new_string: string;
  }>;
}
```

### beforeSubmitPrompt

```typescript
{
  prompt: string;
  attachments: Array<{
    type: "file" | "rule";
    file_path: string;
  }>;
}
```

### preCompact

```typescript
{
  trigger: "auto" | "manual";
  context_usage_percent: number;
  context_tokens: number;
  context_window_size: number;
  message_count: number;
  messages_to_compact: number;
  is_first_compaction: boolean;
}
```

### stop

```typescript
{
  status: "completed" | "aborted" | "error";
  loop_count: number;
}
```

### afterAgentResponse

```typescript
{
  text: string;
}
```

### afterAgentThought

```typescript
{
  text: string;
  duration_ms: number | null;
}
```

### beforeTabFileRead

```typescript
{
  file_path: string;
  content: string;
}
```

### afterTabFileEdit

```typescript
{
  file_path: string;
  edits: Array<{
    old_string: string;
    new_string: string;
    range: {
      start_line_number: number;
      start_column: number;
      end_line_number: number;
      end_column: number;
    };
    old_line: string;
    new_line: string;
  }>;
}
```
