package hook

import "goodkind.io/agent-gate/internal/rules"

type CodexEnvelope struct {
	HookEvent      CodexEvent `json:"hook_event_name"`
	Session        string     `json:"session_id"`
	TranscriptPath string     `json:"transcript_path"`
	Cwd            string     `json:"cwd"`
	Model          string     `json:"model"`
}

func (e CodexEnvelope) EventName() string { return string(e.HookEvent) }
func (e CodexEnvelope) SessionID() string { return e.Session }
func (e CodexEnvelope) CWD() string       { return e.Cwd }

func (e CodexEnvelope) baseFields() rules.FieldSet {
	return rules.FieldSet{
		HookEventName:  string(e.HookEvent),
		SessionID:      e.Session,
		TranscriptPath: e.TranscriptPath,
		CWD:            e.Cwd,
		Model:          e.Model,
	}
}

type CodexSessionStartPayload struct {
	CodexEnvelope
	Source string `json:"source"`
}
type CodexPreToolUsePayload struct {
	CodexEnvelope
	TurnID    string         `json:"turn_id"`
	ToolName  string         `json:"tool_name"`
	ToolUseID string         `json:"tool_use_id"`
	ToolInput CodexToolInput `json:"tool_input"`
}
type (
	CodexPermissionRequestPayload CodexPreToolUsePayload
	CodexPostToolUsePayload       struct {
		CodexEnvelope
		TurnID       string         `json:"turn_id"`
		ToolName     string         `json:"tool_name"`
		ToolUseID    string         `json:"tool_use_id"`
		ToolInput    CodexToolInput `json:"tool_input"`
		ToolResponse TextOrObject   `json:"tool_response"`
	}
)

type CodexUserPromptSubmitPayload struct {
	CodexEnvelope
	TurnID string `json:"turn_id"`
	Prompt string `json:"prompt"`
}
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

func (p CodexSessionStartPayload) Fields() rules.FieldSet {
	fields := p.baseFields()
	fields.Source = p.Source
	return fields
}

func (p CodexPreToolUsePayload) Fields() rules.FieldSet {
	return codexToolFields(p.baseFields(), p.TurnID, p.ToolName, p.ToolUseID, p.ToolInput)
}

func (p CodexPermissionRequestPayload) Fields() rules.FieldSet {
	payload := CodexPreToolUsePayload(p)
	return payload.Fields()
}

func (p CodexPostToolUsePayload) Fields() rules.FieldSet {
	fields := codexToolFields(p.baseFields(), p.TurnID, p.ToolName, p.ToolUseID, p.ToolInput)
	fields.ToolResponse = p.ToolResponse.String()
	return fields
}

func (p CodexUserPromptSubmitPayload) Fields() rules.FieldSet {
	fields := p.baseFields()
	fields.TurnID = p.TurnID
	fields.Prompt = p.Prompt
	return fields
}

func (p CodexStopPayload) Fields() rules.FieldSet {
	fields := p.baseFields()
	fields.TurnID = p.TurnID
	fields.StopHookActive = boolString(p.StopHookActive)
	fields.LastAssistantMessage = p.LastAssistantMessage
	return fields
}
