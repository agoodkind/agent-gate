package hook

import "goodkind.io/agent-gate/internal/rules"

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

func (e ClaudeEnvelope) EventName() string { return string(e.HookEvent) }
func (e ClaudeEnvelope) SessionID() string { return e.Session }
func (e ClaudeEnvelope) CWD() string       { return e.Cwd }

func (e ClaudeEnvelope) baseFields() rules.FieldSet {
	return rules.FieldSet{
		HookEventName:  string(e.HookEvent),
		SessionID:      e.Session,
		TranscriptPath: e.TranscriptPath,
		CWD:            e.Cwd,
		PermissionMode: e.PermissionMode,
		AgentID:        e.AgentID,
		AgentType:      e.AgentType,
		Model:          e.Model,
		TurnID:         e.TurnID,
		Timestamp:      e.Timestamp,
	}
}

type ClaudeSessionStartPayload struct {
	ClaudeEnvelope
	Source string `json:"source"`
}
type ClaudeSessionEndPayload struct {
	ClaudeEnvelope
	Reason     string `json:"reason"`
	DurationMS int    `json:"duration_ms"`
}
type ClaudeSetupPayload struct {
	ClaudeEnvelope
	Trigger string `json:"trigger"`
}
type ClaudePreToolUsePayload struct {
	ClaudeEnvelope
	ToolName  string          `json:"tool_name"`
	ToolUseID string          `json:"tool_use_id"`
	ToolInput ClaudeToolInput `json:"tool_input"`
}
type ClaudePostToolUsePayload struct {
	ClaudeEnvelope
	ToolName     string          `json:"tool_name"`
	ToolUseID    string          `json:"tool_use_id"`
	ToolInput    ClaudeToolInput `json:"tool_input"`
	ToolResponse TextOrObject    `json:"tool_response"`
}
type ClaudePostToolUseFailurePayload struct {
	ClaudeEnvelope
	ToolName    string          `json:"tool_name"`
	ToolUseID   string          `json:"tool_use_id"`
	ToolInput   ClaudeToolInput `json:"tool_input"`
	Error       string          `json:"error"`
	ErrorType   string          `json:"error_type"`
	IsInterrupt bool            `json:"is_interrupt"`
}
type ClaudePermissionRequestPayload struct {
	ClaudeEnvelope
	ToolName              string                 `json:"tool_name"`
	ToolUseID             string                 `json:"tool_use_id"`
	ToolInput             ClaudeToolInput        `json:"tool_input"`
	PermissionSuggestions []PermissionSuggestion `json:"permission_suggestions"`
}
type ClaudePermissionDeniedPayload struct {
	ClaudeEnvelope
	ToolName  string          `json:"tool_name"`
	ToolUseID string          `json:"tool_use_id"`
	ToolInput ClaudeToolInput `json:"tool_input"`
	Reason    string          `json:"reason"`
}
type ClaudeUserPromptSubmitPayload struct {
	ClaudeEnvelope
	Prompt       string `json:"prompt"`
	SessionTitle string `json:"session_title"`
}
type ClaudeStopPayload struct {
	ClaudeEnvelope
	StopHookActive      bool           `json:"stop_hook_active"`
	LastAssistantOutput NullableString `json:"last_assistant_message"`
}
type ClaudeStopFailurePayload struct {
	ClaudeEnvelope
	Error                string `json:"error"`
	ErrorDetails         string `json:"error_details"`
	LastAssistantMessage string `json:"last_assistant_message"`
}
type (
	ClaudeSubagentStartPayload struct{ ClaudeEnvelope }
	ClaudeSubagentStopPayload  struct {
		ClaudeEnvelope
		StopHookActive       bool   `json:"stop_hook_active"`
		AgentTranscriptPath  string `json:"agent_transcript_path"`
		LastAssistantMessage string `json:"last_assistant_message"`
	}
)

type ClaudeTaskCreatedPayload struct {
	ClaudeEnvelope
	TaskID          string `json:"task_id"`
	TaskSubject     string `json:"task_subject"`
	TaskDescription string `json:"task_description"`
	TeammateName    string `json:"teammate_name"`
	TeamName        string `json:"team_name"`
}
type (
	ClaudeTaskCompletedPayload ClaudeTaskCreatedPayload
	ClaudeNotificationPayload  struct {
		ClaudeEnvelope
		NotificationType string `json:"notification_type"`
		Message          string `json:"message"`
		Title            string `json:"title"`
	}
)

type ClaudePreCompactPayload struct {
	ClaudeEnvelope
	Trigger            string `json:"trigger"`
	CustomInstructions string `json:"custom_instructions"`
}
type ClaudePostCompactPayload struct {
	ClaudeEnvelope
	Trigger        string `json:"trigger"`
	CompactSummary string `json:"compact_summary"`
}
type ClaudeInstructionsLoadedPayload struct {
	ClaudeEnvelope
	FilePath        string   `json:"file_path"`
	MemoryType      string   `json:"memory_type"`
	LoadReason      string   `json:"load_reason"`
	Globs           []string `json:"globs"`
	TriggerFilePath string   `json:"trigger_file_path"`
	ParentFilePath  string   `json:"parent_file_path"`
}
type ClaudeConfigChangePayload struct {
	ClaudeEnvelope
	Source   string `json:"source"`
	FilePath string `json:"file_path"`
}
type ClaudeCwdChangedPayload struct {
	ClaudeEnvelope
	OldCWD string `json:"old_cwd"`
	NewCWD string `json:"new_cwd"`
}
type ClaudeFileChangedPayload struct {
	ClaudeEnvelope
	FilePath string `json:"file_path"`
	Event    string `json:"event"`
}
type ClaudeWorktreeCreatePayload struct {
	ClaudeEnvelope
	Name string `json:"name"`
}
type ClaudeWorktreeRemovePayload struct {
	ClaudeEnvelope
	WorktreePath string `json:"worktree_path"`
}
type ClaudeElicitationPayload struct {
	ClaudeEnvelope
	MCPServerName string `json:"mcp_server_name"`
	Message       string `json:"message"`
	Mode          string `json:"mode"`
	URL           string `json:"url"`
	ElicitationID string `json:"elicitation_id"`
}
type ClaudeElicitationResultPayload struct {
	ClaudeEnvelope
	MCPServerName string `json:"mcp_server_name"`
	ElicitationID string `json:"elicitation_id"`
	Mode          string `json:"mode"`
	Action        string `json:"action"`
}
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

func (p ClaudeSessionStartPayload) Fields() rules.FieldSet {
	fields := p.baseFields()
	fields.Source = p.Source
	return fields
}

func (p ClaudeSessionEndPayload) Fields() rules.FieldSet {
	fields := p.baseFields()
	fields.Reason = p.Reason
	return fields
}

func (p ClaudeSetupPayload) Fields() rules.FieldSet {
	fields := p.baseFields()
	fields.Trigger = p.Trigger
	return fields
}

func (p ClaudePreToolUsePayload) Fields() rules.FieldSet {
	return claudeToolFields(p.baseFields(), p.ToolName, p.ToolUseID, p.ToolInput)
}

func (p ClaudePostToolUsePayload) Fields() rules.FieldSet {
	fields := claudeToolFields(p.baseFields(), p.ToolName, p.ToolUseID, p.ToolInput)
	fields.ToolResponse = p.ToolResponse.String()
	return fields
}

func (p ClaudePostToolUseFailurePayload) Fields() rules.FieldSet {
	fields := claudeToolFields(p.baseFields(), p.ToolName, p.ToolUseID, p.ToolInput)
	fields.Error = p.Error
	fields.ErrorType = p.ErrorType
	fields.IsInterrupt = boolString(p.IsInterrupt)
	return fields
}

func (p ClaudePermissionRequestPayload) Fields() rules.FieldSet {
	return claudeToolFields(p.baseFields(), p.ToolName, p.ToolUseID, p.ToolInput)
}

func (p ClaudePermissionDeniedPayload) Fields() rules.FieldSet {
	fields := claudeToolFields(p.baseFields(), p.ToolName, p.ToolUseID, p.ToolInput)
	fields.Reason = p.Reason
	return fields
}

func (p ClaudeUserPromptSubmitPayload) Fields() rules.FieldSet {
	fields := p.baseFields()
	fields.Prompt = p.Prompt
	fields.SessionTitle = p.SessionTitle
	return fields
}

func (p ClaudeStopPayload) Fields() rules.FieldSet {
	fields := p.baseFields()
	fields.StopHookActive = boolString(p.StopHookActive)
	fields.LastAssistantMessage = p.LastAssistantOutput.String()
	return fields
}

func (p ClaudeStopFailurePayload) Fields() rules.FieldSet {
	fields := p.baseFields()
	fields.Error = p.Error
	fields.ErrorDetails = p.ErrorDetails
	fields.LastAssistantMessage = p.LastAssistantMessage
	return fields
}
func (p ClaudeSubagentStartPayload) Fields() rules.FieldSet { return p.baseFields() }
func (p ClaudeSubagentStopPayload) Fields() rules.FieldSet {
	fields := p.baseFields()
	fields.StopHookActive = boolString(p.StopHookActive)
	fields.AgentTranscriptPath = p.AgentTranscriptPath
	fields.LastAssistantMessage = p.LastAssistantMessage
	return fields
}

func (p ClaudeTaskCreatedPayload) Fields() rules.FieldSet {
	fields := p.baseFields()
	fields.TaskID = p.TaskID
	fields.TaskSubject = p.TaskSubject
	fields.TaskDescription = p.TaskDescription
	fields.TeammateName = p.TeammateName
	fields.TeamName = p.TeamName
	return fields
}

func (p ClaudeTaskCompletedPayload) Fields() rules.FieldSet {
	return ClaudeTaskCreatedPayload(p).Fields()
}

func (p ClaudeNotificationPayload) Fields() rules.FieldSet {
	fields := p.baseFields()
	fields.NotificationType = p.NotificationType
	fields.Message = p.Message
	fields.Title = p.Title
	return fields
}

func (p ClaudePreCompactPayload) Fields() rules.FieldSet {
	fields := p.baseFields()
	fields.Trigger = p.Trigger
	fields.CustomInstructions = p.CustomInstructions
	return fields
}

func (p ClaudePostCompactPayload) Fields() rules.FieldSet {
	fields := p.baseFields()
	fields.Trigger = p.Trigger
	fields.CompactSummary = p.CompactSummary
	return fields
}

func (p ClaudeInstructionsLoadedPayload) Fields() rules.FieldSet {
	fields := p.baseFields()
	fields.FilePath = p.FilePath
	fields.MemoryType = p.MemoryType
	fields.LoadReason = p.LoadReason
	fields.TriggerFilePath = p.TriggerFilePath
	fields.ParentFilePath = p.ParentFilePath
	return fields
}

func (p ClaudeConfigChangePayload) Fields() rules.FieldSet {
	fields := p.baseFields()
	fields.Source = p.Source
	fields.FilePath = p.FilePath
	return fields
}

func (p ClaudeCwdChangedPayload) Fields() rules.FieldSet {
	fields := p.baseFields()
	fields.OldCWD = p.OldCWD
	fields.NewCWD = p.NewCWD
	return fields
}

func (p ClaudeFileChangedPayload) Fields() rules.FieldSet {
	fields := p.baseFields()
	fields.FilePath = p.FilePath
	fields.Event = p.Event
	return fields
}

func (p ClaudeWorktreeCreatePayload) Fields() rules.FieldSet {
	fields := p.baseFields()
	fields.Name = p.Name
	return fields
}

func (p ClaudeWorktreeRemovePayload) Fields() rules.FieldSet {
	fields := p.baseFields()
	fields.WorktreePath = p.WorktreePath
	return fields
}

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

func (p ClaudeElicitationResultPayload) Fields() rules.FieldSet {
	fields := p.baseFields()
	fields.MCPServerName = p.MCPServerName
	fields.ElicitationID = p.ElicitationID
	fields.Mode = p.Mode
	fields.Action = p.Action
	return fields
}

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
