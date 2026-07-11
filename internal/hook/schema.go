package hook

import (
	"fmt"
	"slices"

	"goodkind.io/agent-gate/internal/config"
)

// EventSchema is the set of valid dot-path field names for a single event.
// Virtual fields are handled separately and are always considered valid; they
// do not appear in this map.
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

func combinePaths(parts ...[]string) []string {
	total := 0
	for _, part := range parts {
		total += len(part)
	}

	paths := make([]string, 0, total)
	for _, part := range parts {
		paths = append(paths, part...)
	}
	return paths
}

// virtualFields lists the virtual dot-paths synthesized by the rules engine.
// They are always valid regardless of event, so schema checks skip them.
var virtualFields = []string{
	"effective_cwd",
	"cmd_segments",
	"cmd_comments",
	"cmd_double_hyphen_prose",
	"cmd_redirections",
	"cmd_write_targets",
}

type schemaSystem string

const (
	schemaSystemClaude schemaSystem = "claude"
	schemaSystemCodex  schemaSystem = "codex"
	schemaSystemCursor schemaSystem = "cursor"
	schemaSystemGemini schemaSystem = "gemini"
)

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
	cursorSchema = buildCursorSchema()
	claudeSchema = buildClaudeSchema()
	codexSchema = buildCodexSchema()
	geminiSchema = buildGeminiSchema()
}

func buildCursorSchema() map[CursorEvent]EventSchema {
	toolUseBase := combinePaths(cursorEnvelope, toolInputPaths, []string{"tool_name", "cwd"})

	return map[CursorEvent]EventSchema{
		CursorSessionStart: makeSchema(cursorEnvelope),
		CursorSessionEnd:   makeSchema(cursorEnvelope),

		CursorPreToolUse:         makeSchema(toolUseBase),
		CursorPostToolUse:        makeSchema(toolUseBase, "tool_output"),
		CursorPostToolUseFailure: makeSchema(toolUseBase, "error"),

		CursorBeforeShellExecution: makeSchema(cursorEnvelope, "command", "cwd"),
		CursorAfterShellExecution:  makeSchema(cursorEnvelope, "command", "cwd", "output"),

		CursorBeforeMCPExecution: makeSchema(cursorEnvelope, "tool_name", "cwd"),
		CursorAfterMCPExecution:  makeSchema(cursorEnvelope, "tool_name", "cwd", "tool_output"),

		CursorBeforeReadFile:    makeSchema(cursorEnvelope, "file_path", "cwd"),
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

var (
	claudeSchema map[ClaudeEvent]EventSchema
	codexSchema  map[CodexEvent]EventSchema
	geminiSchema map[GeminiEvent]EventSchema
)

func buildClaudeSchema() map[ClaudeEvent]EventSchema {
	claudeToolUseBase := combinePaths(claudeEnvelope, claudeToolInputPaths, []string{
		"tool_name",
		"tool_use_id",
	})

	return map[ClaudeEvent]EventSchema{
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

		// Paths captured from live payloads of these installed-schema events. A
		// PostToolBatch surfaces its first tool call through the standard tool
		// paths; MessageDisplay surfaces the streamed delta as assistant text.
		ClaudePostToolBatch:       makeSchema(claudeToolUseBase, "tool_response"),
		ClaudeUserPromptExpansion: makeSchema(claudeEnvelope, "prompt"),
		ClaudeMessageDisplay:      makeSchema(claudeEnvelope, "text", "assistant_message"),
	}
}

func buildCodexSchema() map[CodexEvent]EventSchema {
	codexEnvelope := []string{
		"hook_event_name",
		"session_id",
		"transcript_path",
		"cwd",
		"model",
	}
	codexToolInputPaths := []string{
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
	codexToolBase := combinePaths(codexEnvelope, codexToolInputPaths, []string{
		"turn_id",
		"tool_name",
		"tool_use_id",
	})

	return map[CodexEvent]EventSchema{
		CodexSessionStart:      makeSchema(codexEnvelope, "source"),
		CodexPreToolUse:        makeSchema(codexToolBase),
		CodexPermissionRequest: makeSchema(codexToolBase, "tool_input.description"),
		CodexPostToolUse:       makeSchema(codexToolBase, "tool_response"),
		CodexUserPromptSubmit:  makeSchema(codexEnvelope, "turn_id", "prompt"),
		CodexStop:              makeSchema(codexEnvelope, "turn_id", "stop_hook_active", "last_assistant_message"),

		// Paths from live codex-native samples (PreCompact/PostCompact/
		// SubagentStart); SubagentStop mirrors Claude pending a verified sample.
		CodexPreCompact:    makeSchema(codexEnvelope, "turn_id", "trigger"),
		CodexPostCompact:   makeSchema(codexEnvelope, "turn_id", "trigger"),
		CodexSubagentStart: makeSchema(codexEnvelope, "turn_id", "permission_mode", "agent_id", "agent_type"),
		CodexSubagentStop: makeSchema(codexEnvelope,
			"turn_id", "permission_mode", "agent_id", "agent_type",
			"stop_hook_active", "agent_transcript_path", "last_assistant_message",
		),
	}
}

func buildGeminiSchema() map[GeminiEvent]EventSchema {
	geminiEnvelope := []string{
		"hook_event_name",
		"session_id",
		"transcript_path",
		"cwd",
		"timestamp",
	}
	geminiToolInputPaths := []string{
		"tool_input.file_path",
		"tool_input.content",
		"tool_input.command",
		"tool_input.old_string",
		"tool_input.new_string",
		"tool_input.pattern",
		"tool_input.path",
		"tool_input.url",
		"tool_input.query",
		"tool_input.description",
	}
	geminiToolBase := combinePaths(geminiEnvelope, geminiToolInputPaths, []string{
		"tool_name",
		"mcp_context",
		"original_request_name",
	})

	return map[GeminiEvent]EventSchema{
		GeminiBeforeTool:          makeSchema(geminiToolBase),
		GeminiAfterTool:           makeSchema(geminiToolBase, "tool_response"),
		GeminiBeforeAgent:         makeSchema(geminiEnvelope, "prompt"),
		GeminiAfterAgent:          makeSchema(geminiEnvelope, "prompt", "prompt_response", "stop_hook_active"),
		GeminiBeforeModel:         makeSchema(geminiEnvelope, "llm_request"),
		GeminiBeforeToolSelection: makeSchema(geminiEnvelope, "llm_request"),
		GeminiAfterModel:          makeSchema(geminiEnvelope, "llm_request", "llm_response"),
		GeminiSessionStart:        makeSchema(geminiEnvelope, "source"),
		GeminiSessionEnd:          makeSchema(geminiEnvelope, "reason"),
		GeminiNotification:        makeSchema(geminiEnvelope, "notification_type", "message", "details"),
		GeminiPreCompress:         makeSchema(geminiEnvelope, "trigger"),
	}
}

// ── Public API ───────────────────────────────────────────────────────────────

// ValidPaths returns the EventSchema for the given system ("claude"/"cursor")
// and event name. Returns nil if the system or event is unknown.
func ValidPaths(system, eventName string) EventSchema {
	switch schemaSystem(system) {
	case schemaSystemCursor:
		return cursorSchema[CursorEvent(eventName)]
	case schemaSystemClaude:
		return claudeSchema[ClaudeEvent(eventName)]
	case schemaSystemCodex:
		return codexSchema[CodexEvent(eventName)]
	case schemaSystemGemini:
		return geminiSchema[GeminiEvent(eventName)]
	default:
		return nil
	}
}

// ValidateConfig checks every rule's field_paths against the schema for all
// applicable events. Returns a (possibly empty) slice of validation errors.
func ValidateConfig(cfg *config.Config) []error {
	var errs []error
	for i := range cfg.Rules {
		errs = append(errs, validateRuleConfig(&cfg.Rules[i])...)
	}
	return errs
}

func validateRuleConfig(r *config.Rule) []error {
	errs := validateConditionKinds(r)
	allPaths := collectRuleFieldPaths(r)
	if len(allPaths) == 0 {
		return errs
	}

	applicable := applicableEventPairs(r)
	for _, fieldPath := range allPaths {
		errs = append(errs, validateFieldPathForRule(r, fieldPath, applicable)...)
	}
	return errs
}

func collectRuleFieldPaths(r *config.Rule) []string {
	allPaths := make([]string, 0, len(r.FieldPaths))
	allPaths = append(allPaths, r.FieldPaths...)
	for j := range r.Conditions {
		condition := &r.Conditions[j]
		allPaths = append(allPaths, condition.FieldPaths...)
		if config.ConditionKind(condition.Kind) == config.ConditionKindInfer {
			allPaths = append(allPaths, condition.InputField, condition.CacheKey)
			if condition.ContextSource != "" {
				allPaths = append(allPaths, condition.ContextWorkspaceField, condition.ContextSessionField)
			}
		}
	}
	return allPaths
}

func validateConditionKinds(r *config.Rule) []error {
	var errs []error
	for j := range r.Conditions {
		if isKnownConditionKind(r.Conditions[j].Kind) {
			continue
		}
		errs = append(errs, fmt.Errorf("rule %q: condition %d has unknown kind %q", r.Name, j, r.Conditions[j].Kind))
	}
	return errs
}

func isKnownConditionKind(kind string) bool {
	switch config.ConditionKind(kind) {
	case "", config.ConditionKindRegex, config.ConditionKindCommand, config.ConditionKindProject,
		config.ConditionKindDiff, config.ConditionKindShellRead, config.ConditionKindShellWrite,
		config.ConditionKindExec, config.ConditionKindComposer, config.ConditionKindGitDefaultBranch,
		config.ConditionKindGitPrimaryCheckout, config.ConditionKindGitRefMove,
		config.ConditionKindInfer:
		return true
	default:
		return false
	}
}

type schemaEventPair struct {
	system string
	event  string
}

func applicableEventPairs(r *config.Rule) []schemaEventPair {
	if ruleHasNoEventFilters(r) {
		return allEventPairs()
	}

	applicable := make([]schemaEventPair, 0, len(r.Events)*4+
		len(r.ClaudeEvents)+len(r.CursorEvents)+len(r.CodexEvents)+len(r.GeminiEvents))
	for _, ev := range r.Events {
		applicable = append(applicable,
			schemaEventPair{"cursor", ev},
			schemaEventPair{"claude", ev},
			schemaEventPair{"codex", ev},
			schemaEventPair{"gemini", ev},
		)
	}
	applicable = appendEventPairs(applicable, "claude", r.ClaudeEvents)
	applicable = appendEventPairs(applicable, "cursor", r.CursorEvents)
	applicable = appendEventPairs(applicable, "codex", r.CodexEvents)
	applicable = appendEventPairs(applicable, "gemini", r.GeminiEvents)
	return applicable
}

func ruleHasNoEventFilters(r *config.Rule) bool {
	return len(r.Events) == 0 &&
		len(r.ClaudeEvents) == 0 &&
		len(r.CursorEvents) == 0 &&
		len(r.CodexEvents) == 0 &&
		len(r.GeminiEvents) == 0
}

func allEventPairs() []schemaEventPair {
	applicable := make([]schemaEventPair, 0,
		len(cursorSchema)+len(claudeSchema)+len(codexSchema)+len(geminiSchema))
	applicable = appendSchemaEvents(applicable, "cursor", cursorSchema)
	applicable = appendSchemaEvents(applicable, "claude", claudeSchema)
	applicable = appendSchemaEvents(applicable, "codex", codexSchema)
	applicable = appendSchemaEvents(applicable, "gemini", geminiSchema)
	return applicable
}

func appendEventPairs(applicable []schemaEventPair, system string, events []string) []schemaEventPair {
	for _, ev := range events {
		applicable = append(applicable, schemaEventPair{system, ev})
	}
	return applicable
}

func appendSchemaEvents[E ~string](applicable []schemaEventPair, system string, schema map[E]EventSchema) []schemaEventPair {
	for ev := range schema {
		applicable = append(applicable, schemaEventPair{system, string(ev)})
	}
	return applicable
}

func validateFieldPathForRule(r *config.Rule, fieldPath string, applicable []schemaEventPair) []error {
	if config.CompileFieldSelector(fieldPath) == config.FieldSelectorInvalid {
		return []error{fmt.Errorf("rule %q: field_path %q has no typed selector", r.Name, fieldPath)}
	}
	if slices.Contains(virtualFields, fieldPath) {
		return nil
	}
	if fieldPathApplies(fieldPath, applicable) {
		return nil
	}
	return []error{fmt.Errorf("rule %q: field_path %q is not valid for any applicable event", r.Name, fieldPath)}
}

func fieldPathApplies(fieldPath string, applicable []schemaEventPair) bool {
	for _, p := range applicable {
		schema := ValidPaths(p.system, p.event)
		if schema != nil && schema[fieldPath] {
			return true
		}
	}
	return false
}
