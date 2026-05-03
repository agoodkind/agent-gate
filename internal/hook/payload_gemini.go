package hook

import "goodkind.io/agent-gate/internal/rules"

type GeminiEnvelope struct {
	HookEvent      GeminiEvent `json:"hook_event_name"`
	Session        string      `json:"session_id"`
	TranscriptPath string      `json:"transcript_path"`
	Cwd            string      `json:"cwd"`
	Timestamp      string      `json:"timestamp"`
}

func (e GeminiEnvelope) EventName() string { return string(e.HookEvent) }
func (e GeminiEnvelope) SessionID() string { return e.Session }
func (e GeminiEnvelope) CWD() string       { return e.Cwd }

func (e GeminiEnvelope) baseFields() rules.FieldSet {
	return rules.FieldSet{
		HookEventName:  string(e.HookEvent),
		SessionID:      e.Session,
		TranscriptPath: e.TranscriptPath,
		CWD:            e.Cwd,
		Timestamp:      e.Timestamp,
	}
}

type GeminiBeforeToolPayload struct {
	GeminiEnvelope
	ToolName            string          `json:"tool_name"`
	MCPContext          string          `json:"mcp_context"`
	OriginalRequestName string          `json:"original_request_name"`
	ToolInput           GeminiToolInput `json:"tool_input"`
}
type GeminiAfterToolPayload struct {
	GeminiEnvelope
	ToolName            string          `json:"tool_name"`
	MCPContext          string          `json:"mcp_context"`
	OriginalRequestName string          `json:"original_request_name"`
	ToolInput           GeminiToolInput `json:"tool_input"`
	ToolResponse        TextOrObject    `json:"tool_response"`
}
type GeminiBeforeAgentPayload struct {
	GeminiEnvelope
	Prompt string `json:"prompt"`
}
type GeminiAfterAgentPayload struct {
	GeminiEnvelope
	Prompt         string `json:"prompt"`
	PromptResponse string `json:"prompt_response"`
	StopHookActive bool   `json:"stop_hook_active"`
}
type GeminiBeforeModelPayload struct {
	GeminiEnvelope
	LLMRequest LLMRequest `json:"llm_request"`
}
type GeminiBeforeToolSelectionPayload GeminiBeforeModelPayload
type GeminiAfterModelPayload struct {
	GeminiEnvelope
	LLMRequest  LLMRequest  `json:"llm_request"`
	LLMResponse LLMResponse `json:"llm_response"`
}
type GeminiSessionStartPayload struct {
	GeminiEnvelope
	Source string `json:"source"`
}
type GeminiSessionEndPayload struct {
	GeminiEnvelope
	Reason string `json:"reason"`
}
type GeminiNotificationPayload struct {
	GeminiEnvelope
	NotificationType string `json:"notification_type"`
	Message          string `json:"message"`
	Details          string `json:"details"`
}
type GeminiPreCompressPayload struct {
	GeminiEnvelope
	Trigger string `json:"trigger"`
}

func geminiToolFields(base rules.FieldSet, toolName string, originalRequestName string, mcpContext string, input GeminiToolInput) rules.FieldSet {
	base.ToolName = toolName
	base.OriginalRequestName = originalRequestName
	base.MCPContext = mcpContext
	base.ToolInputCommand = input.Command
	base.ToolInputFilePath = input.FilePath
	base.ToolInputContent = input.Content
	base.ToolInputOldString = input.OldString
	base.ToolInputNewString = input.NewString
	base.ToolInputDescription = input.Description
	base.ToolInputWorkdir = input.Workdir
	base.ToolInputDirectory = input.Directory
	base.ToolInputPattern = input.Pattern
	base.ToolInputPath = input.Path
	base.ToolInputURL = input.URL
	base.ToolInputQuery = input.Query
	return base
}

func (p GeminiBeforeToolPayload) Fields() rules.FieldSet {
	return geminiToolFields(p.baseFields(), p.ToolName, p.OriginalRequestName, p.MCPContext, p.ToolInput)
}
func (p GeminiAfterToolPayload) Fields() rules.FieldSet {
	fields := geminiToolFields(p.baseFields(), p.ToolName, p.OriginalRequestName, p.MCPContext, p.ToolInput)
	fields.ToolResponse = p.ToolResponse.String()
	return fields
}
func (p GeminiBeforeAgentPayload) Fields() rules.FieldSet {
	fields := p.baseFields()
	fields.Prompt = p.Prompt
	return fields
}
func (p GeminiAfterAgentPayload) Fields() rules.FieldSet {
	fields := p.baseFields()
	fields.Prompt = p.Prompt
	fields.PromptResponse = p.PromptResponse
	fields.StopHookActive = boolString(p.StopHookActive)
	return fields
}
func (p GeminiBeforeModelPayload) Fields() rules.FieldSet {
	fields := p.baseFields()
	fields.LLMRequest = p.LLMRequest.Text
	return fields
}
func (p GeminiBeforeToolSelectionPayload) Fields() rules.FieldSet {
	payload := GeminiBeforeModelPayload(p)
	return payload.Fields()
}
func (p GeminiAfterModelPayload) Fields() rules.FieldSet {
	fields := p.baseFields()
	fields.LLMRequest = p.LLMRequest.Text
	fields.LLMResponse = p.LLMResponse.Text
	return fields
}
func (p GeminiSessionStartPayload) Fields() rules.FieldSet {
	fields := p.baseFields()
	fields.Source = p.Source
	return fields
}
func (p GeminiSessionEndPayload) Fields() rules.FieldSet {
	fields := p.baseFields()
	fields.Reason = p.Reason
	return fields
}
func (p GeminiNotificationPayload) Fields() rules.FieldSet {
	fields := p.baseFields()
	fields.NotificationType = p.NotificationType
	fields.Message = p.Message
	fields.Details = p.Details
	return fields
}
func (p GeminiPreCompressPayload) Fields() rules.FieldSet {
	fields := p.baseFields()
	fields.Trigger = p.Trigger
	return fields
}
