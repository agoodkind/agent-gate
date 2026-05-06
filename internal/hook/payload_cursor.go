package hook

import "goodkind.io/agent-gate/internal/rules"

type CursorEnvelope struct {
	ConversationID string         `json:"conversation_id"`
	GenerationID   string         `json:"generation_id"`
	Model          string         `json:"model"`
	Session        string         `json:"session_id"`
	HookEvent      CursorEvent    `json:"hook_event_name"`
	CursorVersion  string         `json:"cursor_version"`
	WorkspaceRoots []string       `json:"workspace_roots"`
	UserEmail      string         `json:"user_email"`
	TranscriptPath NullableString `json:"transcript_path"`
}

func (e CursorEnvelope) EventName() string { return string(e.HookEvent) }
func (e CursorEnvelope) SessionID() string { return e.Session }
func (e CursorEnvelope) CWD() string       { return "" }

func (e CursorEnvelope) baseFields() rules.FieldSet {
	return rules.FieldSet{
		HookEventName:  string(e.HookEvent),
		SessionID:      e.Session,
		ConversationID: e.ConversationID,
		GenerationID:   e.GenerationID,
		Model:          e.Model,
		CursorVersion:  e.CursorVersion,
		UserEmail:      e.UserEmail,
		TranscriptPath: e.TranscriptPath.String(),
	}
}

type (
	CursorSessionStartPayload struct{ CursorEnvelope }
	CursorSessionEndPayload   struct {
		CursorEnvelope
		Reason      string `json:"reason"`
		FinalStatus string `json:"final_status"`
	}
)

type CursorPreToolUsePayload struct {
	CursorEnvelope
	ToolName  string          `json:"tool_name"`
	ToolUseID string          `json:"tool_use_id"`
	ToolInput CursorToolInput `json:"tool_input"`
	Cwd       string          `json:"cwd"`
	Duration  Number          `json:"duration"`
}
type CursorPostToolUsePayload struct {
	CursorEnvelope
	ToolName   string          `json:"tool_name"`
	ToolUseID  string          `json:"tool_use_id"`
	ToolInput  CursorToolInput `json:"tool_input"`
	ToolOutput string          `json:"tool_output"`
	Duration   Number          `json:"duration"`
	Cwd        string          `json:"cwd"`
}
type CursorPostToolUseFailurePayload struct {
	CursorEnvelope
	ToolName     string          `json:"tool_name"`
	ToolUseID    string          `json:"tool_use_id"`
	ToolInput    CursorToolInput `json:"tool_input"`
	ErrorMessage string          `json:"error_message"`
	FailureType  string          `json:"failure_type"`
	IsInterrupt  bool            `json:"is_interrupt"`
	Duration     Number          `json:"duration"`
	Cwd          string          `json:"cwd"`
}
type CursorBeforeShellExecutionPayload struct {
	CursorEnvelope
	Command string `json:"command"`
	Cwd     string `json:"cwd"`
	Sandbox bool   `json:"sandbox"`
}
type CursorAfterShellExecutionPayload struct {
	CursorEnvelope
	Command  string `json:"command"`
	Cwd      string `json:"cwd"`
	Output   string `json:"output"`
	Sandbox  bool   `json:"sandbox"`
	Duration Number `json:"duration"`
}
type CursorBeforeMCPExecutionPayload struct {
	CursorEnvelope
	ToolName  string          `json:"tool_name"`
	ToolInput CursorToolInput `json:"tool_input"`
	ToolUseID string          `json:"tool_use_id"`
	Cwd       string          `json:"cwd"`
}
type CursorAfterMCPExecutionPayload struct {
	CursorEnvelope
	ToolName   string          `json:"tool_name"`
	ToolInput  CursorToolInput `json:"tool_input"`
	ToolUseID  string          `json:"tool_use_id"`
	ToolOutput string          `json:"tool_output"`
	ResultJSON string          `json:"result_json"`
	Cwd        string          `json:"cwd"`
}
type CursorBeforeReadFilePayload struct {
	CursorEnvelope
	FilePath string `json:"file_path"`
	Cwd      string `json:"cwd"`
}
type CursorBeforeTabFileReadPayload struct {
	CursorEnvelope
	FilePath string `json:"file_path"`
	Cwd      string `json:"cwd"`
}
type CursorAfterFileEditPayload struct {
	CursorEnvelope
	FilePath string `json:"file_path"`
	Edits    []Edit `json:"edits"`
}
type CursorAfterTabFileEditPayload struct {
	CursorEnvelope
	FilePath string `json:"file_path"`
	Edits    []Edit `json:"edits"`
}
type CursorBeforeSubmitPromptPayload struct {
	CursorEnvelope
	Prompt      string       `json:"prompt"`
	Text        string       `json:"text"`
	Cwd         string       `json:"cwd"`
	Attachments []Attachment `json:"attachments"`
}
type CursorSubagentStartPayload struct {
	CursorEnvelope
	SubagentID           string `json:"subagent_id"`
	SubagentType         string `json:"subagent_type"`
	Task                 string `json:"task"`
	ParentConversationID string `json:"parent_conversation_id"`
	ToolCallID           string `json:"tool_call_id"`
	IsParallelWorker     bool   `json:"is_parallel_worker"`
	IsBackgroundAgent    bool   `json:"is_background_agent"`
}
type CursorSubagentStopPayload struct {
	CursorEnvelope
	SubagentID           string         `json:"subagent_id"`
	SubagentType         string         `json:"subagent_type"`
	Task                 string         `json:"task"`
	ParentConversationID string         `json:"parent_conversation_id"`
	Description          string         `json:"description"`
	AgentTranscriptPath  NullableString `json:"agent_transcript_path"`
	MessageCount         int            `json:"message_count"`
	ToolCallCount        int            `json:"tool_call_count"`
	DurationMS           int            `json:"duration_ms"`
}
type CursorPreCompactPayload struct {
	CursorEnvelope
	Trigger             string `json:"trigger"`
	ContextUsagePercent Number `json:"context_usage_percent"`
	ContextTokens       int    `json:"context_tokens"`
	ContextWindowSize   int    `json:"context_window_size"`
	MessagesToCompact   int    `json:"messages_to_compact"`
	IsFirstCompaction   bool   `json:"is_first_compaction"`
}
type CursorStopPayload struct {
	CursorEnvelope
	Status           string `json:"status"`
	LoopCount        int    `json:"loop_count"`
	ComposerMode     string `json:"composer_mode"`
	InputTokens      int    `json:"input_tokens"`
	OutputTokens     int    `json:"output_tokens"`
	CacheReadTokens  int    `json:"cache_read_tokens"`
	CacheWriteTokens int    `json:"cache_write_tokens"`
}
type CursorAfterAgentResponsePayload struct {
	CursorEnvelope
	Text             string `json:"text"`
	AssistantMessage string `json:"assistant_message"`
	InputTokens      int    `json:"input_tokens"`
	OutputTokens     int    `json:"output_tokens"`
	CacheReadTokens  int    `json:"cache_read_tokens"`
	CacheWriteTokens int    `json:"cache_write_tokens"`
}
type CursorAfterAgentThoughtPayload struct {
	CursorEnvelope
	Text             string `json:"text"`
	AssistantMessage string `json:"assistant_message"`
}

func cursorToolFields(base rules.FieldSet, toolName string, toolUseID string, input CursorToolInput, cwd string) rules.FieldSet {
	base.ToolName = toolName
	base.ToolUseID = toolUseID
	base.CWD = cwd
	base.ToolInputCommand = input.Command
	base.ToolInputFilePath = input.FilePath
	base.ToolInputContent = input.Content
	base.ToolInputPattern = input.Pattern
	base.ToolInputURL = input.URL
	base.ToolInputQuery = input.Query
	base.ToolInputWorkdir = input.Workdir
	base.ToolInputWorkingDir = input.WorkingDirectory
	base.ToolInputDirectory = input.Directory
	base.ToolInputCWD = input.CWD
	return base
}

func cursorEditStrings(edits []Edit, extract func(Edit) string) []string {
	values := make([]string, 0, len(edits))
	for _, edit := range edits {
		if value := extract(edit); value != "" {
			values = append(values, value)
		}
	}
	return values
}

func cursorAttachmentStrings(attachments []Attachment, extract func(Attachment) string) []string {
	values := make([]string, 0, len(attachments))
	for _, attachment := range attachments {
		if value := extract(attachment); value != "" {
			values = append(values, value)
		}
	}
	return values
}

func (p CursorSessionStartPayload) Fields() rules.FieldSet { return p.baseFields() }
func (p CursorSessionEndPayload) Fields() rules.FieldSet {
	fields := p.baseFields()
	fields.Reason = p.Reason
	fields.Status = p.FinalStatus
	return fields
}

func (p CursorPreToolUsePayload) Fields() rules.FieldSet {
	return cursorToolFields(p.baseFields(), p.ToolName, p.ToolUseID, p.ToolInput, p.Cwd)
}

func (p CursorPostToolUsePayload) Fields() rules.FieldSet {
	fields := cursorToolFields(p.baseFields(), p.ToolName, p.ToolUseID, p.ToolInput, p.Cwd)
	fields.ToolOutput = p.ToolOutput
	return fields
}

func (p CursorPostToolUseFailurePayload) Fields() rules.FieldSet {
	fields := cursorToolFields(p.baseFields(), p.ToolName, p.ToolUseID, p.ToolInput, p.Cwd)
	fields.ErrorMessage = p.ErrorMessage
	fields.FailureType = p.FailureType
	fields.IsInterrupt = boolString(p.IsInterrupt)
	return fields
}

func (p CursorBeforeShellExecutionPayload) Fields() rules.FieldSet {
	fields := p.baseFields()
	fields.Command = p.Command
	fields.CWD = p.Cwd
	return fields
}

func (p CursorAfterShellExecutionPayload) Fields() rules.FieldSet {
	fields := p.baseFields()
	fields.Command = p.Command
	fields.CWD = p.Cwd
	fields.Output = p.Output
	return fields
}

func (p CursorBeforeMCPExecutionPayload) Fields() rules.FieldSet {
	return cursorToolFields(p.baseFields(), p.ToolName, p.ToolUseID, p.ToolInput, p.Cwd)
}

func (p CursorAfterMCPExecutionPayload) Fields() rules.FieldSet {
	fields := cursorToolFields(p.baseFields(), p.ToolName, p.ToolUseID, p.ToolInput, p.Cwd)
	fields.ToolOutput = p.ToolOutput
	return fields
}

func (p CursorBeforeReadFilePayload) Fields() rules.FieldSet {
	fields := p.baseFields()
	fields.FilePath = p.FilePath
	fields.CWD = p.Cwd
	return fields
}

func (p CursorBeforeTabFileReadPayload) Fields() rules.FieldSet {
	fields := p.baseFields()
	fields.FilePath = p.FilePath
	fields.CWD = p.Cwd
	return fields
}

func (p CursorAfterFileEditPayload) Fields() rules.FieldSet {
	fields := p.baseFields()
	fields.FilePath = p.FilePath
	fields.EditsOldString = cursorEditStrings(p.Edits, func(edit Edit) string { return edit.OldString })
	fields.EditsNewString = cursorEditStrings(p.Edits, func(edit Edit) string { return edit.NewString })
	fields.EditsOldLine = cursorEditStrings(p.Edits, func(edit Edit) string { return edit.OldLine })
	fields.EditsNewLine = cursorEditStrings(p.Edits, func(edit Edit) string { return edit.NewLine })
	return fields
}

func (p CursorAfterTabFileEditPayload) Fields() rules.FieldSet {
	return CursorAfterFileEditPayload(p).Fields()
}

func (p CursorBeforeSubmitPromptPayload) Fields() rules.FieldSet {
	fields := p.baseFields()
	fields.Prompt = p.Prompt
	fields.Text = p.Text
	fields.CWD = p.Cwd
	fields.AttachmentsFilePath = cursorAttachmentStrings(p.Attachments, func(attachment Attachment) string { return attachment.FilePath })
	fields.AttachmentsType = cursorAttachmentStrings(p.Attachments, func(attachment Attachment) string { return attachment.Type })
	return fields
}

func (p CursorSubagentStartPayload) Fields() rules.FieldSet {
	fields := p.baseFields()
	fields.TaskID = p.SubagentID
	fields.TaskSubject = p.Task
	fields.AgentType = p.SubagentType
	return fields
}

func (p CursorSubagentStopPayload) Fields() rules.FieldSet {
	fields := p.baseFields()
	fields.TaskID = p.SubagentID
	fields.TaskSubject = p.Task
	fields.TaskDescription = p.Description
	fields.AgentType = p.SubagentType
	fields.AgentTranscriptPath = p.AgentTranscriptPath.String()
	return fields
}

func (p CursorPreCompactPayload) Fields() rules.FieldSet {
	fields := p.baseFields()
	fields.Trigger = p.Trigger
	return fields
}

func (p CursorStopPayload) Fields() rules.FieldSet {
	fields := p.baseFields()
	fields.Status = p.Status
	return fields
}

func (p CursorAfterAgentResponsePayload) Fields() rules.FieldSet {
	fields := p.baseFields()
	fields.Text = p.Text
	fields.AssistantMessage = p.AssistantMessage
	return fields
}

func (p CursorAfterAgentThoughtPayload) Fields() rules.FieldSet {
	fields := p.baseFields()
	fields.Text = p.Text
	fields.AssistantMessage = p.AssistantMessage
	return fields
}
