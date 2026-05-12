package hook

import "goodkind.io/agent-gate/internal/rules"

// GeminiEnvelope is the common header carried by every Gemini hook payload.
type GeminiEnvelope struct {
	HookEvent      GeminiEvent `json:"hook_event_name"`
	Session        string      `json:"session_id"`
	TranscriptPath string      `json:"transcript_path"`
	Cwd            string      `json:"cwd"`
	Timestamp      string      `json:"timestamp"`
}

// EventName returns the canonical Gemini hook event name.
func (e GeminiEnvelope) EventName() string { return string(e.HookEvent) }

// SessionID returns the Gemini session identifier.
func (e GeminiEnvelope) SessionID() string { return e.Session }

// CWD returns the working directory recorded in the envelope.
func (e GeminiEnvelope) CWD() string { return e.Cwd }

func (e GeminiEnvelope) baseFields() rules.FieldSet {
	var fields rules.FieldSet
	fields.HookEventName = string(e.HookEvent)
	fields.SessionID = e.Session
	fields.TranscriptPath = e.TranscriptPath
	fields.CWD = e.Cwd
	fields.Timestamp = e.Timestamp
	return fields
}

// GeminiBeforeToolPayload is emitted before a tool is invoked.
type GeminiBeforeToolPayload struct {
	GeminiEnvelope
	ToolName            string          `json:"tool_name"`
	MCPContext          string          `json:"mcp_context"`
	OriginalRequestName string          `json:"original_request_name"`
	ToolInput           GeminiToolInput `json:"tool_input"`
}

// GeminiAfterToolPayload is emitted after a tool invocation completes.
type GeminiAfterToolPayload struct {
	GeminiEnvelope
	ToolName            string          `json:"tool_name"`
	MCPContext          string          `json:"mcp_context"`
	OriginalRequestName string          `json:"original_request_name"`
	ToolInput           GeminiToolInput `json:"tool_input"`
	ToolResponse        TextOrObject    `json:"tool_response"`
}

// GeminiBeforeAgentPayload is emitted before agent processing begins.
type GeminiBeforeAgentPayload struct {
	GeminiEnvelope
	Prompt string `json:"prompt"`
}

// GeminiAfterAgentPayload is emitted after agent processing completes.
type GeminiAfterAgentPayload struct {
	GeminiEnvelope
	Prompt         string `json:"prompt"`
	PromptResponse string `json:"prompt_response"`
	StopHookActive bool   `json:"stop_hook_active"`
}

// GeminiBeforeModelPayload is emitted before a model request is dispatched.
type GeminiBeforeModelPayload struct {
	GeminiEnvelope
	LLMRequest LLMRequest `json:"llm_request"`
}

// Gemini model and tool-selection payloads.
type (
	// GeminiBeforeToolSelectionPayload mirrors [GeminiBeforeModelPayload].
	GeminiBeforeToolSelectionPayload GeminiBeforeModelPayload
	// GeminiAfterModelPayload is emitted after a model request returns.
	GeminiAfterModelPayload struct {
		GeminiEnvelope
		LLMRequest  LLMRequest  `json:"llm_request"`
		LLMResponse LLMResponse `json:"llm_response"`
	}
)

// GeminiSessionStartPayload is emitted when a Gemini session starts.
type GeminiSessionStartPayload struct {
	GeminiEnvelope
	Source string `json:"source"`
}

// GeminiSessionEndPayload is emitted when a Gemini session ends.
type GeminiSessionEndPayload struct {
	GeminiEnvelope
	Reason string `json:"reason"`
}

// GeminiNotificationPayload is emitted for ad-hoc notifications.
type GeminiNotificationPayload struct {
	GeminiEnvelope
	NotificationType string `json:"notification_type"`
	Message          string `json:"message"`
	Details          string `json:"details"`
}

// GeminiPreCompressPayload is emitted before context compression starts.
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

// Fields renders the payload as a [rules.FieldSet].
func (p GeminiBeforeToolPayload) Fields() rules.FieldSet {
	return geminiToolFields(p.baseFields(), p.ToolName, p.OriginalRequestName, p.MCPContext, p.ToolInput)
}

// Fields renders the payload as a [rules.FieldSet].
func (p GeminiAfterToolPayload) Fields() rules.FieldSet {
	fields := geminiToolFields(p.baseFields(), p.ToolName, p.OriginalRequestName, p.MCPContext, p.ToolInput)
	fields.ToolResponse = p.ToolResponse.SearchableText()
	return fields
}

// Fields renders the payload as a [rules.FieldSet].
func (p GeminiBeforeAgentPayload) Fields() rules.FieldSet {
	fields := p.baseFields()
	fields.Prompt = p.Prompt
	return fields
}

// Fields renders the payload as a [rules.FieldSet].
func (p GeminiAfterAgentPayload) Fields() rules.FieldSet {
	fields := p.baseFields()
	fields.Prompt = p.Prompt
	fields.PromptResponse = p.PromptResponse
	fields.StopHookActive = boolString(p.StopHookActive)
	return fields
}

// Fields renders the payload as a [rules.FieldSet].
func (p GeminiBeforeModelPayload) Fields() rules.FieldSet {
	fields := p.baseFields()
	fields.LLMRequest = p.LLMRequest.Text
	return fields
}

// Fields renders the payload as a [rules.FieldSet].
func (p GeminiBeforeToolSelectionPayload) Fields() rules.FieldSet {
	payload := GeminiBeforeModelPayload(p)
	return payload.Fields()
}

// Fields renders the payload as a [rules.FieldSet].
func (p GeminiAfterModelPayload) Fields() rules.FieldSet {
	fields := p.baseFields()
	fields.LLMRequest = p.LLMRequest.Text
	fields.LLMResponse = p.LLMResponse.Text
	return fields
}

// Fields renders the payload as a [rules.FieldSet].
func (p GeminiSessionStartPayload) Fields() rules.FieldSet {
	fields := p.baseFields()
	fields.Source = p.Source
	return fields
}

// Fields renders the payload as a [rules.FieldSet].
func (p GeminiSessionEndPayload) Fields() rules.FieldSet {
	fields := p.baseFields()
	fields.Reason = p.Reason
	return fields
}

// Fields renders the payload as a [rules.FieldSet].
func (p GeminiNotificationPayload) Fields() rules.FieldSet {
	fields := p.baseFields()
	fields.NotificationType = p.NotificationType
	fields.Message = p.Message
	fields.Details = p.Details
	return fields
}

// Fields renders the payload as a [rules.FieldSet].
func (p GeminiPreCompressPayload) Fields() rules.FieldSet {
	fields := p.baseFields()
	fields.Trigger = p.Trigger
	return fields
}
