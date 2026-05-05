package hook

import "goodkind.io/agent-gate/internal/rules"

// CodexEnvelope is the common header carried by every Codex hook payload.
type CodexEnvelope struct {
	HookEvent      CodexEvent `json:"hook_event_name"`
	Session        string     `json:"session_id"`
	TranscriptPath string     `json:"transcript_path"`
	Cwd            string     `json:"cwd"`
	Model          string     `json:"model"`
}

// EventName returns the canonical Codex hook event name.
func (e CodexEnvelope) EventName() string { return string(e.HookEvent) }

// SessionID returns the Codex session identifier.
func (e CodexEnvelope) SessionID() string { return e.Session }

// CWD returns the working directory recorded in the envelope.
func (e CodexEnvelope) CWD() string { return e.Cwd }

func (e CodexEnvelope) baseFields() rules.FieldSet {
	var fields rules.FieldSet
	fields.HookEventName = string(e.HookEvent)
	fields.SessionID = e.Session
	fields.TranscriptPath = e.TranscriptPath
	fields.CWD = e.Cwd
	fields.Model = e.Model
	return fields
}

// CodexSessionStartPayload is emitted when a Codex session starts.
type CodexSessionStartPayload struct {
	CodexEnvelope
	Source string `json:"source"`
}

// CodexPreToolUsePayload is emitted before a tool is invoked.
type CodexPreToolUsePayload struct {
	CodexEnvelope
	TurnID    string         `json:"turn_id"`
	ToolName  string         `json:"tool_name"`
	ToolUseID string         `json:"tool_use_id"`
	ToolInput CodexToolInput `json:"tool_input"`
}

// Codex tool-permission and post-tool payloads.
type (
	// CodexPermissionRequestPayload is emitted on a permission prompt.
	CodexPermissionRequestPayload CodexPreToolUsePayload
	// CodexPostToolUsePayload is emitted after a tool invocation completes.
	CodexPostToolUsePayload struct {
		CodexEnvelope
		TurnID       string         `json:"turn_id"`
		ToolName     string         `json:"tool_name"`
		ToolUseID    string         `json:"tool_use_id"`
		ToolInput    CodexToolInput `json:"tool_input"`
		ToolResponse TextOrObject   `json:"tool_response"`
	}
)

// CodexUserPromptSubmitPayload is emitted when the user submits a prompt.
type CodexUserPromptSubmitPayload struct {
	CodexEnvelope
	TurnID string `json:"turn_id"`
	Prompt string `json:"prompt"`
}

// CodexStopPayload is emitted when a Codex turn stops.
type CodexStopPayload struct {
	CodexEnvelope
	TurnID               string `json:"turn_id"`
	StopHookActive       bool   `json:"stop_hook_active"`
	LastAssistantMessage string `json:"last_assistant_message"`
}

func codexToolFields(base rules.FieldSet, turnID string, toolName string, toolUseID string, input CodexToolInput) rules.FieldSet {
	base.TurnID = turnID
	base.ToolName = toolName
	base.ToolUseID = toolUseID
	base.ToolInputCommand = input.Command
	base.ToolInputFilePath = input.FilePath
	base.ToolInputContent = input.Content
	base.ToolInputOldString = input.OldString
	base.ToolInputNewString = input.NewString
	base.ToolInputDescription = input.Description
	base.ToolInputPrompt = input.Prompt
	base.ToolInputWorkdir = input.Workdir
	base.ToolInputDirectory = input.Directory
	base.ToolInputPattern = input.Pattern
	base.ToolInputPath = input.Path
	base.ToolInputURL = input.URL
	base.ToolInputQuery = input.Query
	return base
}

// Fields renders the payload as a [rules.FieldSet].
func (p CodexSessionStartPayload) Fields() rules.FieldSet {
	fields := p.baseFields()
	fields.Source = p.Source
	return fields
}

// Fields renders the payload as a [rules.FieldSet].
func (p CodexPreToolUsePayload) Fields() rules.FieldSet {
	return codexToolFields(p.baseFields(), p.TurnID, p.ToolName, p.ToolUseID, p.ToolInput)
}

// Fields renders the payload as a [rules.FieldSet].
func (p CodexPermissionRequestPayload) Fields() rules.FieldSet {
	payload := CodexPreToolUsePayload(p)
	return payload.Fields()
}

// Fields renders the payload as a [rules.FieldSet].
func (p CodexPostToolUsePayload) Fields() rules.FieldSet {
	fields := codexToolFields(p.baseFields(), p.TurnID, p.ToolName, p.ToolUseID, p.ToolInput)
	fields.ToolResponse = p.ToolResponse.String()
	return fields
}

// Fields renders the payload as a [rules.FieldSet].
func (p CodexUserPromptSubmitPayload) Fields() rules.FieldSet {
	fields := p.baseFields()
	fields.TurnID = p.TurnID
	fields.Prompt = p.Prompt
	return fields
}

// Fields renders the payload as a [rules.FieldSet].
func (p CodexStopPayload) Fields() rules.FieldSet {
	fields := p.baseFields()
	fields.TurnID = p.TurnID
	fields.StopHookActive = boolString(p.StopHookActive)
	fields.LastAssistantMessage = p.LastAssistantMessage
	return fields
}
