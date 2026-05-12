package hook

import "goodkind.io/agent-gate/internal/rules"

// ClaudeEnvelope is the common header carried by every Claude hook payload.
type ClaudeEnvelope struct {
	HookEvent      ClaudeEvent `json:"hook_event_name"`
	Session        string      `json:"session_id"`
	TranscriptPath string      `json:"transcript_path"`
	Cwd            string      `json:"cwd"`
	PermissionMode string      `json:"permission_mode"`
	AgentID        string      `json:"agent_id"`
	AgentType      string      `json:"agent_type"`
	Model          string      `json:"model"`
	TurnID         string      `json:"turn_id"`
	Timestamp      string      `json:"timestamp"`
}

// EventName returns the canonical Claude hook event name.
func (e ClaudeEnvelope) EventName() string { return string(e.HookEvent) }

// SessionID returns the Claude session identifier.
func (e ClaudeEnvelope) SessionID() string { return e.Session }

// CWD returns the working directory recorded in the envelope.
func (e ClaudeEnvelope) CWD() string { return e.Cwd }

func (e ClaudeEnvelope) baseFields() rules.FieldSet {
	var fields rules.FieldSet
	fields.HookEventName = string(e.HookEvent)
	fields.SessionID = e.Session
	fields.TranscriptPath = e.TranscriptPath
	fields.CWD = e.Cwd
	fields.PermissionMode = e.PermissionMode
	fields.AgentID = e.AgentID
	fields.AgentType = e.AgentType
	fields.Model = e.Model
	fields.TurnID = e.TurnID
	fields.Timestamp = e.Timestamp
	return fields
}

// ClaudeSessionStartPayload is emitted when a Claude session starts.
type ClaudeSessionStartPayload struct {
	ClaudeEnvelope
	Source string `json:"source"`
}

// ClaudeSessionEndPayload is emitted when a Claude session ends.
type ClaudeSessionEndPayload struct {
	ClaudeEnvelope
	Reason     string `json:"reason"`
	DurationMS int    `json:"duration_ms"`
}

// ClaudeSetupPayload is emitted during environment setup events.
type ClaudeSetupPayload struct {
	ClaudeEnvelope
	Trigger string `json:"trigger"`
}

// ClaudePreToolUsePayload is emitted before a tool is invoked.
type ClaudePreToolUsePayload struct {
	ClaudeEnvelope
	ToolName  string          `json:"tool_name"`
	ToolUseID string          `json:"tool_use_id"`
	ToolInput ClaudeToolInput `json:"tool_input"`
}

// ClaudePostToolUsePayload is emitted after a tool invocation succeeds.
type ClaudePostToolUsePayload struct {
	ClaudeEnvelope
	ToolName     string          `json:"tool_name"`
	ToolUseID    string          `json:"tool_use_id"`
	ToolInput    ClaudeToolInput `json:"tool_input"`
	ToolResponse TextOrObject    `json:"tool_response"`
}

// ClaudePostToolUseFailurePayload is emitted after a tool invocation fails.
type ClaudePostToolUseFailurePayload struct {
	ClaudeEnvelope
	ToolName    string          `json:"tool_name"`
	ToolUseID   string          `json:"tool_use_id"`
	ToolInput   ClaudeToolInput `json:"tool_input"`
	Error       string          `json:"error"`
	ErrorType   string          `json:"error_type"`
	IsInterrupt bool            `json:"is_interrupt"`
}

// ClaudePermissionRequestPayload represents a tool permission request.
type ClaudePermissionRequestPayload struct {
	ClaudeEnvelope
	ToolName              string                 `json:"tool_name"`
	ToolUseID             string                 `json:"tool_use_id"`
	ToolInput             ClaudeToolInput        `json:"tool_input"`
	PermissionSuggestions []PermissionSuggestion `json:"permission_suggestions"`
}

// ClaudePermissionDeniedPayload is emitted when a permission request is denied.
type ClaudePermissionDeniedPayload struct {
	ClaudeEnvelope
	ToolName  string          `json:"tool_name"`
	ToolUseID string          `json:"tool_use_id"`
	ToolInput ClaudeToolInput `json:"tool_input"`
	Reason    string          `json:"reason"`
}

// ClaudeUserPromptSubmitPayload is emitted when the user submits a prompt.
type ClaudeUserPromptSubmitPayload struct {
	ClaudeEnvelope
	Prompt       string `json:"prompt"`
	SessionTitle string `json:"session_title"`
}

// ClaudeStopPayload is emitted when the agent stops normally.
type ClaudeStopPayload struct {
	ClaudeEnvelope
	StopHookActive      bool           `json:"stop_hook_active"`
	LastAssistantOutput NullableString `json:"last_assistant_message"`
}

// ClaudeStopFailurePayload is emitted when the agent stops on error.
type ClaudeStopFailurePayload struct {
	ClaudeEnvelope
	Error                string `json:"error"`
	ErrorDetails         string `json:"error_details"`
	LastAssistantMessage string `json:"last_assistant_message"`
}

// Subagent lifecycle payloads.
type (
	// ClaudeSubagentStartPayload is emitted when a subagent starts.
	ClaudeSubagentStartPayload struct{ ClaudeEnvelope }
	// ClaudeSubagentStopPayload is emitted when a subagent stops.
	ClaudeSubagentStopPayload struct {
		ClaudeEnvelope
		StopHookActive       bool   `json:"stop_hook_active"`
		AgentTranscriptPath  string `json:"agent_transcript_path"`
		LastAssistantMessage string `json:"last_assistant_message"`
	}
)

// ClaudeTaskCreatedPayload is emitted when a teammate task is created.
type ClaudeTaskCreatedPayload struct {
	ClaudeEnvelope
	TaskID          string `json:"task_id"`
	TaskSubject     string `json:"task_subject"`
	TaskDescription string `json:"task_description"`
	TeammateName    string `json:"teammate_name"`
	TeamName        string `json:"team_name"`
}

// Task notification payloads.
type (
	// ClaudeTaskCompletedPayload mirrors [ClaudeTaskCreatedPayload] for completion.
	ClaudeTaskCompletedPayload ClaudeTaskCreatedPayload
	// ClaudeNotificationPayload is emitted for ad-hoc notifications.
	ClaudeNotificationPayload struct {
		ClaudeEnvelope
		NotificationType string `json:"notification_type"`
		Message          string `json:"message"`
		Title            string `json:"title"`
	}
)

// ClaudePreCompactPayload is emitted before context compaction starts.
type ClaudePreCompactPayload struct {
	ClaudeEnvelope
	Trigger            string `json:"trigger"`
	CustomInstructions string `json:"custom_instructions"`
}

// ClaudePostCompactPayload is emitted after context compaction completes.
type ClaudePostCompactPayload struct {
	ClaudeEnvelope
	Trigger        string `json:"trigger"`
	CompactSummary string `json:"compact_summary"`
}

// ClaudeInstructionsLoadedPayload is emitted when instructions load.
type ClaudeInstructionsLoadedPayload struct {
	ClaudeEnvelope
	FilePath        string   `json:"file_path"`
	MemoryType      string   `json:"memory_type"`
	LoadReason      string   `json:"load_reason"`
	Globs           []string `json:"globs"`
	TriggerFilePath string   `json:"trigger_file_path"`
	ParentFilePath  string   `json:"parent_file_path"`
}

// ClaudeConfigChangePayload is emitted when configuration changes.
type ClaudeConfigChangePayload struct {
	ClaudeEnvelope
	Source   string `json:"source"`
	FilePath string `json:"file_path"`
}

// ClaudeCwdChangedPayload is emitted when the working directory changes.
type ClaudeCwdChangedPayload struct {
	ClaudeEnvelope
	OldCWD string `json:"old_cwd"`
	NewCWD string `json:"new_cwd"`
}

// ClaudeFileChangedPayload is emitted when a tracked file changes.
type ClaudeFileChangedPayload struct {
	ClaudeEnvelope
	FilePath string `json:"file_path"`
	Event    string `json:"event"`
}

// ClaudeWorktreeCreatePayload is emitted when a worktree is created.
type ClaudeWorktreeCreatePayload struct {
	ClaudeEnvelope
	Name string `json:"name"`
}

// ClaudeWorktreeRemovePayload is emitted when a worktree is removed.
type ClaudeWorktreeRemovePayload struct {
	ClaudeEnvelope
	WorktreePath string `json:"worktree_path"`
}

// ClaudeElicitationPayload is emitted when an MCP elicitation begins.
type ClaudeElicitationPayload struct {
	ClaudeEnvelope
	MCPServerName string `json:"mcp_server_name"`
	Message       string `json:"message"`
	Mode          string `json:"mode"`
	URL           string `json:"url"`
	ElicitationID string `json:"elicitation_id"`
}

// ClaudeElicitationResultPayload is emitted when elicitation completes.
type ClaudeElicitationResultPayload struct {
	ClaudeEnvelope
	MCPServerName string `json:"mcp_server_name"`
	ElicitationID string `json:"elicitation_id"`
	Mode          string `json:"mode"`
	Action        string `json:"action"`
}

// ClaudeTeammateIdlePayload is emitted when a teammate becomes idle.
type ClaudeTeammateIdlePayload struct {
	ClaudeEnvelope
	TeammateName string `json:"teammate_name"`
	TeamName     string `json:"team_name"`
}

func claudeToolFields(base rules.FieldSet, toolName string, toolUseID string, input ClaudeToolInput) rules.FieldSet {
	base.ToolName = toolName
	base.ToolUseID = toolUseID
	base.ToolInputCommand = input.Command
	base.ToolInputFilePath = input.NormalizedFilePath()
	base.ToolInputContent = input.Content
	base.ToolInputOldString = input.NormalizedOldString()
	base.ToolInputNewString = input.NormalizedNewString()
	base.ToolInputDescription = input.Description
	base.ToolInputPrompt = input.Prompt
	base.ToolInputWorkdir = input.Path
	base.ToolInputPattern = input.Pattern
	base.ToolInputPath = input.Path
	base.ToolInputURL = input.URL
	base.ToolInputQuery = input.Query
	return base
}

// Fields renders the payload as a [rules.FieldSet].
func (p ClaudeSessionStartPayload) Fields() rules.FieldSet {
	fields := p.baseFields()
	fields.Source = p.Source
	return fields
}

// Fields renders the payload as a [rules.FieldSet].
func (p ClaudeSessionEndPayload) Fields() rules.FieldSet {
	fields := p.baseFields()
	fields.Reason = p.Reason
	return fields
}

// Fields renders the payload as a [rules.FieldSet].
func (p ClaudeSetupPayload) Fields() rules.FieldSet {
	fields := p.baseFields()
	fields.Trigger = p.Trigger
	return fields
}

// Fields renders the payload as a [rules.FieldSet].
func (p ClaudePreToolUsePayload) Fields() rules.FieldSet {
	return claudeToolFields(p.baseFields(), p.ToolName, p.ToolUseID, p.ToolInput)
}

// Fields renders the payload as a [rules.FieldSet].
func (p ClaudePostToolUsePayload) Fields() rules.FieldSet {
	fields := claudeToolFields(p.baseFields(), p.ToolName, p.ToolUseID, p.ToolInput)
	fields.ToolResponse = p.ToolResponse.SearchableText()
	return fields
}

// Fields renders the payload as a [rules.FieldSet].
func (p ClaudePostToolUseFailurePayload) Fields() rules.FieldSet {
	fields := claudeToolFields(p.baseFields(), p.ToolName, p.ToolUseID, p.ToolInput)
	fields.Error = p.Error
	fields.ErrorType = p.ErrorType
	fields.IsInterrupt = boolString(p.IsInterrupt)
	return fields
}

// Fields renders the payload as a [rules.FieldSet].
func (p ClaudePermissionRequestPayload) Fields() rules.FieldSet {
	return claudeToolFields(p.baseFields(), p.ToolName, p.ToolUseID, p.ToolInput)
}

// Fields renders the payload as a [rules.FieldSet].
func (p ClaudePermissionDeniedPayload) Fields() rules.FieldSet {
	fields := claudeToolFields(p.baseFields(), p.ToolName, p.ToolUseID, p.ToolInput)
	fields.Reason = p.Reason
	return fields
}

// Fields renders the payload as a [rules.FieldSet].
func (p ClaudeUserPromptSubmitPayload) Fields() rules.FieldSet {
	fields := p.baseFields()
	fields.Prompt = p.Prompt
	fields.SessionTitle = p.SessionTitle
	return fields
}

// Fields renders the payload as a [rules.FieldSet].
func (p ClaudeStopPayload) Fields() rules.FieldSet {
	fields := p.baseFields()
	fields.StopHookActive = boolString(p.StopHookActive)
	fields.LastAssistantMessage = p.LastAssistantOutput.String()
	return fields
}

// Fields renders the payload as a [rules.FieldSet].
func (p ClaudeStopFailurePayload) Fields() rules.FieldSet {
	fields := p.baseFields()
	fields.Error = p.Error
	fields.ErrorDetails = p.ErrorDetails
	fields.LastAssistantMessage = p.LastAssistantMessage
	return fields
}

// Fields renders the payload as a [rules.FieldSet].
func (p ClaudeSubagentStartPayload) Fields() rules.FieldSet { return p.baseFields() }

// Fields renders the payload as a [rules.FieldSet].
func (p ClaudeSubagentStopPayload) Fields() rules.FieldSet {
	fields := p.baseFields()
	fields.StopHookActive = boolString(p.StopHookActive)
	fields.AgentTranscriptPath = p.AgentTranscriptPath
	fields.LastAssistantMessage = p.LastAssistantMessage
	return fields
}

// Fields renders the payload as a [rules.FieldSet].
func (p ClaudeTaskCreatedPayload) Fields() rules.FieldSet {
	fields := p.baseFields()
	fields.TaskID = p.TaskID
	fields.TaskSubject = p.TaskSubject
	fields.TaskDescription = p.TaskDescription
	fields.TeammateName = p.TeammateName
	fields.TeamName = p.TeamName
	return fields
}

// Fields renders the payload as a [rules.FieldSet].
func (p ClaudeTaskCompletedPayload) Fields() rules.FieldSet {
	return ClaudeTaskCreatedPayload(p).Fields()
}

// Fields renders the payload as a [rules.FieldSet].
func (p ClaudeNotificationPayload) Fields() rules.FieldSet {
	fields := p.baseFields()
	fields.NotificationType = p.NotificationType
	fields.Message = p.Message
	fields.Title = p.Title
	return fields
}

// Fields renders the payload as a [rules.FieldSet].
func (p ClaudePreCompactPayload) Fields() rules.FieldSet {
	fields := p.baseFields()
	fields.Trigger = p.Trigger
	fields.CustomInstructions = p.CustomInstructions
	return fields
}

// Fields renders the payload as a [rules.FieldSet].
func (p ClaudePostCompactPayload) Fields() rules.FieldSet {
	fields := p.baseFields()
	fields.Trigger = p.Trigger
	fields.CompactSummary = p.CompactSummary
	return fields
}

// Fields renders the payload as a [rules.FieldSet].
func (p ClaudeInstructionsLoadedPayload) Fields() rules.FieldSet {
	fields := p.baseFields()
	fields.FilePath = p.FilePath
	fields.MemoryType = p.MemoryType
	fields.LoadReason = p.LoadReason
	fields.TriggerFilePath = p.TriggerFilePath
	fields.ParentFilePath = p.ParentFilePath
	return fields
}

// Fields renders the payload as a [rules.FieldSet].
func (p ClaudeConfigChangePayload) Fields() rules.FieldSet {
	fields := p.baseFields()
	fields.Source = p.Source
	fields.FilePath = p.FilePath
	return fields
}

// Fields renders the payload as a [rules.FieldSet].
func (p ClaudeCwdChangedPayload) Fields() rules.FieldSet {
	fields := p.baseFields()
	fields.OldCWD = p.OldCWD
	fields.NewCWD = p.NewCWD
	return fields
}

// Fields renders the payload as a [rules.FieldSet].
func (p ClaudeFileChangedPayload) Fields() rules.FieldSet {
	fields := p.baseFields()
	fields.FilePath = p.FilePath
	fields.Event = p.Event
	return fields
}

// Fields renders the payload as a [rules.FieldSet].
func (p ClaudeWorktreeCreatePayload) Fields() rules.FieldSet {
	fields := p.baseFields()
	fields.Name = p.Name
	return fields
}

// Fields renders the payload as a [rules.FieldSet].
func (p ClaudeWorktreeRemovePayload) Fields() rules.FieldSet {
	fields := p.baseFields()
	fields.WorktreePath = p.WorktreePath
	return fields
}

// Fields renders the payload as a [rules.FieldSet].
func (p ClaudeElicitationPayload) Fields() rules.FieldSet {
	fields := p.baseFields()
	fields.MCPServerName = p.MCPServerName
	fields.Message = p.Message
	fields.Mode = p.Mode
	fields.URL = p.URL
	fields.ToolInputURL = p.URL
	fields.ElicitationID = p.ElicitationID
	return fields
}

// Fields renders the payload as a [rules.FieldSet].
func (p ClaudeElicitationResultPayload) Fields() rules.FieldSet {
	fields := p.baseFields()
	fields.MCPServerName = p.MCPServerName
	fields.ElicitationID = p.ElicitationID
	fields.Mode = p.Mode
	fields.Action = p.Action
	return fields
}

// Fields renders the payload as a [rules.FieldSet].
func (p ClaudeTeammateIdlePayload) Fields() rules.FieldSet {
	fields := p.baseFields()
	fields.TeammateName = p.TeammateName
	fields.TeamName = p.TeamName
	return fields
}

func boolString(value bool) string {
	if value {
		return "true"
	}
	return "false"
}
