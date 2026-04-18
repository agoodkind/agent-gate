package hook

import (
	"fmt"
	"slices"

	"goodkind.io/agent-gate/internal/config"
)

// EventSchema is the set of valid dot-path field names for a single event.
// Virtual fields (effective_cwd, cmd_segments) are handled separately and are
// always considered valid; they do not appear in this map.
type EventSchema map[string]bool

// makeSchema builds an EventSchema from a base slice plus optional extra paths.
func makeSchema(base []string, extra ...string) EventSchema {
	s := make(EventSchema, len(base)+len(extra))
	for _, p := range base {
		s[p] = true
	}
	for _, p := range extra {
		s[p] = true
	}
	return s
}

// virtualFields lists the virtual dot-paths synthesised by the rules engine.
// They are always valid regardless of event, so schema checks skip them.
var virtualFields = []string{"effective_cwd", "cmd_segments"}

// ── Cursor ──────────────────────────────────────────────────────────────────

// cursorEnvelope contains fields present on every Cursor event.
var cursorEnvelope = []string{
	"hook_event_name",
	"session_id",
	"conversation_id",
	"generation_id",
	"model",
	"cursor_version",
	"user_email",
	"transcript_path",
}

// toolInputPaths are the tool_input sub-fields sent on preToolUse / postToolUse.
var toolInputPaths = []string{
	"tool_input.file_path",
	"tool_input.content",
	"tool_input.command",
	"tool_input.old_string",
	"tool_input.new_string",
	"tool_input.pattern",
	"tool_input.path",
	"tool_input.url",
	"tool_input.query",
}

var cursorSchema map[CursorEvent]EventSchema

func init() {
	toolUseBase := append(cursorEnvelope,
		append(toolInputPaths,
			"tool_name",
			"cwd",
		)...,
	)

	cursorSchema = map[CursorEvent]EventSchema{
		CursorSessionStart: makeSchema(cursorEnvelope),
		CursorSessionEnd:   makeSchema(cursorEnvelope),

		CursorPreToolUse:        makeSchema(toolUseBase),
		CursorPostToolUse:       makeSchema(toolUseBase, "tool_output"),
		CursorPostToolUseFailure: makeSchema(toolUseBase, "error"),

		CursorBeforeShellExecution: makeSchema(cursorEnvelope, "command", "cwd"),
		CursorAfterShellExecution:  makeSchema(cursorEnvelope, "command", "cwd", "output"),

		CursorBeforeMCPExecution: makeSchema(cursorEnvelope, "tool_name", "cwd"),
		CursorAfterMCPExecution:  makeSchema(cursorEnvelope, "tool_name", "cwd", "tool_output"),

		CursorBeforeReadFile:   makeSchema(cursorEnvelope, "file_path", "cwd"),
		CursorBeforeTabFileRead: makeSchema(cursorEnvelope, "file_path", "cwd"),

		CursorAfterFileEdit: makeSchema(cursorEnvelope,
			"file_path",
			"edits[*].old_string",
			"edits[*].new_string",
		),
		CursorAfterTabFileEdit: makeSchema(cursorEnvelope,
			"file_path",
			"edits[*].old_string",
			"edits[*].new_string",
		),

		CursorBeforeSubmitPrompt: makeSchema(cursorEnvelope, "prompt", "text", "cwd"),

		CursorSubagentStart: makeSchema(cursorEnvelope),
		CursorSubagentStop:  makeSchema(cursorEnvelope),

		CursorPreCompact: makeSchema(cursorEnvelope),

		CursorStop: makeSchema(cursorEnvelope, "status"),

		CursorAfterAgentResponse: makeSchema(cursorEnvelope, "text", "assistant_message"),
		CursorAfterAgentThought:  makeSchema(cursorEnvelope, "text", "assistant_message"),
	}
}

// ── Claude ───────────────────────────────────────────────────────────────────

// claudeEnvelope contains fields present on every Claude hook event.
// Source: baseHookInput in claude-code-hooks-extracted.schema.json.
var claudeEnvelope = []string{
	"hook_event_name",
	"session_id",
	"transcript_path",
	"cwd",
	"permission_mode",
	"agent_id",
	"agent_type",
}

// claudeToolInputPaths are the tool_input sub-fields for Claude tool events.
var claudeToolInputPaths = []string{
	"tool_input.command",
	"tool_input.file_path",
	"tool_input.content",
	"tool_input.old_string",
	"tool_input.new_string",
	"tool_input.description",
	"tool_input.prompt",
	"tool_input.url",
	"tool_input.query",
	"tool_input.pattern",
}

var claudeSchema map[ClaudeEvent]EventSchema

func init() {
	claudeToolUseBase := append(claudeEnvelope,
		append(claudeToolInputPaths,
			"tool_name",
			"tool_use_id",
		)...,
	)

	claudeSchema = map[ClaudeEvent]EventSchema{
		// source: startup | resume | clear | compact; model present on subagent starts.
		ClaudeSessionStart: makeSchema(claudeEnvelope, "source", "model"),
		// reason: clear | resume | logout | prompt_input_exit | other | bypass_permissions_disabled
		ClaudeSessionEnd: makeSchema(claudeEnvelope, "reason"),
		// trigger: init | maintenance
		ClaudeSetup: makeSchema(claudeEnvelope, "trigger"),

		ClaudePreToolUse:         makeSchema(claudeToolUseBase),
		ClaudePostToolUse:        makeSchema(claudeToolUseBase, "tool_response"),
		ClaudePostToolUseFailure: makeSchema(claudeToolUseBase, "error", "error_type", "is_interrupt"),
		ClaudePermissionRequest:  makeSchema(claudeToolUseBase, "permission_suggestions"),
		ClaudePermissionDenied:   makeSchema(claudeToolUseBase, "reason"),

		ClaudeUserPromptSubmit: makeSchema(claudeEnvelope, "prompt", "session_title"),

		// stop_hook_active distinguishes whether a Stop hook is already running.
		ClaudeStop:        makeSchema(claudeEnvelope, "stop_hook_active", "last_assistant_message"),
		ClaudeStopFailure: makeSchema(claudeEnvelope, "error", "error_details", "last_assistant_message"),

		ClaudeSubagentStart: makeSchema(claudeEnvelope),
		ClaudeSubagentStop: makeSchema(claudeEnvelope,
			"stop_hook_active", "agent_transcript_path", "last_assistant_message",
		),
		ClaudeTaskCreated: makeSchema(claudeEnvelope,
			"task_id", "task_subject", "task_description", "teammate_name", "team_name",
		),
		ClaudeTaskCompleted: makeSchema(claudeEnvelope,
			"task_id", "task_subject", "task_description", "teammate_name", "team_name",
		),

		ClaudeNotification: makeSchema(claudeEnvelope, "notification_type", "message", "title"),

		// trigger: manual | auto
		ClaudePreCompact:  makeSchema(claudeEnvelope, "trigger", "custom_instructions"),
		ClaudePostCompact: makeSchema(claudeEnvelope, "trigger", "compact_summary"),

		// memory_type: User | Project | Local | Managed
		// load_reason: session_start | nested_traversal | path_glob_match | include | compact
		ClaudeInstructionsLoaded: makeSchema(claudeEnvelope,
			"file_path", "memory_type", "load_reason",
			"globs", "trigger_file_path", "parent_file_path",
		),
		// source: user_settings | project_settings | local_settings | policy_settings | skills
		ClaudeConfigChange: makeSchema(claudeEnvelope, "source", "file_path"),

		// old_cwd is the previous directory; new_cwd is where Claude moved to.
		ClaudeCwdChanged: makeSchema(claudeEnvelope, "old_cwd", "new_cwd"),

		// event: change | add | unlink
		ClaudeFileChanged: makeSchema(claudeEnvelope, "file_path", "event"),

		// WorktreeCreate carries the worktree name; WorktreeRemove carries the path.
		ClaudeWorktreeCreate: makeSchema(claudeEnvelope, "name"),
		ClaudeWorktreeRemove: makeSchema(claudeEnvelope, "worktree_path"),

		// Elicitation is an MCP server requesting structured input from the user.
		ClaudeElicitation: makeSchema(claudeEnvelope,
			"mcp_server_name", "message", "mode", "url", "elicitation_id",
		),
		ClaudeElicitationResult: makeSchema(claudeEnvelope,
			"mcp_server_name", "elicitation_id", "mode", "action",
		),

		ClaudeTeammateIdle: makeSchema(claudeEnvelope, "teammate_name", "team_name"),
	}
}

// ── Public API ───────────────────────────────────────────────────────────────

// ValidPaths returns the EventSchema for the given system ("claude"/"cursor")
// and event name. Returns nil if the system or event is unknown.
func ValidPaths(system, eventName string) EventSchema {
	switch system {
	case "cursor":
		return cursorSchema[CursorEvent(eventName)]
	case "claude":
		return claudeSchema[ClaudeEvent(eventName)]
	default:
		return nil
	}
}

// ValidateConfig checks every rule's field_paths against the schema for all
// applicable events. Returns a (possibly empty) slice of validation errors.
func ValidateConfig(cfg *config.Config) []error {
	var errs []error
	for i := range cfg.Rules {
		r := &cfg.Rules[i]

		// Collect all field_paths from the rule (top-level and per-condition).
		var allPaths []string
		allPaths = append(allPaths, r.FieldPaths...)
		for j := range r.Conditions {
			allPaths = append(allPaths, r.Conditions[j].FieldPaths...)
		}
		if len(allPaths) == 0 {
			continue
		}

		// Determine the applicable (system, event) pairs.
		type pair struct{ system, event string }
		var applicable []pair

		addEvents := func(system string, events []string) {
			for _, ev := range events {
				applicable = append(applicable, pair{system, ev})
			}
		}

		allEmpty := len(r.Events) == 0 && len(r.ClaudeEvents) == 0 && len(r.CursorEvents) == 0

		if allEmpty {
			// Applies to every known event on both systems.
			for ev := range cursorSchema {
				applicable = append(applicable, pair{"cursor", string(ev)})
			}
			for ev := range claudeSchema {
				applicable = append(applicable, pair{"claude", string(ev)})
			}
		} else {
			// Shared events apply to both systems.
			for _, ev := range r.Events {
				applicable = append(applicable, pair{"cursor", ev})
				applicable = append(applicable, pair{"claude", ev})
			}
			addEvents("claude", r.ClaudeEvents)
			addEvents("cursor", r.CursorEvents)
		}

		// For each field_path, verify it is valid for at least one applicable event.
		for _, fp := range allPaths {
			if slices.Contains(virtualFields, fp) {
				continue // virtual fields are always valid
			}
			valid := false
			for _, p := range applicable {
				schema := ValidPaths(p.system, p.event)
				if schema == nil {
					continue
				}
				if schema[fp] {
					valid = true
					break
				}
			}
			if !valid {
				errs = append(errs, fmt.Errorf("rule %q: field_path %q is not valid for any applicable event", r.Name, fp))
			}
		}
	}
	return errs
}
