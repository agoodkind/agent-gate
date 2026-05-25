package config

// FieldSelector identifies a rule-visible hook payload field without requiring
// runtime string-path navigation.
type FieldSelector int

// Closed enumeration of [FieldSelector] values. Each constant corresponds
// to a single user-facing dotted-path field name; see fieldSelectorByPath
// for the canonical mapping. Names track the JSON path in source payloads.
const (
	// FieldSelectorInvalid is the zero value and represents an unknown path.
	FieldSelectorInvalid FieldSelector = iota
	// FieldHookEventName selects the canonical hook event name.
	FieldHookEventName
	// FieldSessionID selects the session identifier.
	FieldSessionID
	// FieldConversationID selects the conversation identifier.
	FieldConversationID
	// FieldGenerationID selects the generation identifier.
	FieldGenerationID
	// FieldModel selects the model name.
	FieldModel
	// FieldCursorVersion selects the Cursor IDE version string.
	FieldCursorVersion
	// FieldUserEmail selects the authenticated user email.
	FieldUserEmail
	// FieldTranscriptPath selects the conversation transcript path.
	FieldTranscriptPath
	// FieldCWD selects the literal current working directory.
	FieldCWD
	// FieldEffectiveCWD selects the cd-chain-aware working directory.
	FieldEffectiveCWD
	// FieldCmdSegments selects the chained command segments.
	FieldCmdSegments
	// FieldCmdComments selects unquoted shell comments from command fields.
	FieldCmdComments
	// FieldCmdDoubleHyphenProse selects command tokens where ASCII double
	// hyphen appears outside flag or separator syntax.
	FieldCmdDoubleHyphenProse
	// FieldCmdRedirections selects unsafe shell redirections from command
	// fields after stripping comments and quoted script content.
	FieldCmdRedirections
	// FieldPermissionMode selects the active permission mode.
	FieldPermissionMode
	// FieldAgentID selects the agent identifier.
	FieldAgentID
	// FieldAgentType selects the agent type label.
	FieldAgentType
	// FieldTurnID selects the conversational turn identifier.
	FieldTurnID
	// FieldToolName selects the invoked tool name.
	FieldToolName
	// FieldToolUseID selects the tool invocation identifier.
	FieldToolUseID
	// FieldToolInputCommand selects the tool_input.command field.
	FieldToolInputCommand
	// FieldToolInputFilePath selects the tool_input.file_path field.
	FieldToolInputFilePath
	// FieldToolInputContent selects the tool_input.content field.
	FieldToolInputContent
	// FieldToolInputOldString selects the tool_input.old_string field.
	FieldToolInputOldString
	// FieldToolInputNewString selects the tool_input.new_string field.
	FieldToolInputNewString
	// FieldToolInputDescription selects the tool_input.description field.
	FieldToolInputDescription
	// FieldToolInputPrompt selects the tool_input.prompt field.
	FieldToolInputPrompt
	// FieldToolInputPattern selects the tool_input.pattern field.
	FieldToolInputPattern
	// FieldToolInputPath selects the tool_input.path field.
	FieldToolInputPath
	// FieldToolInputURL selects the tool_input.url field.
	FieldToolInputURL
	// FieldToolInputQuery selects the tool_input.query field.
	FieldToolInputQuery
	// FieldToolInputWorkdir selects the tool_input.workdir field.
	FieldToolInputWorkdir
	// FieldToolInputWorkingDirectory selects tool_input.working_directory.
	FieldToolInputWorkingDirectory
	// FieldToolInputCWD selects the tool_input.cwd field.
	FieldToolInputCWD
	// FieldToolInputDirectory selects the tool_input.directory field.
	FieldToolInputDirectory
	// FieldFilePath selects the top-level file_path.
	FieldFilePath
	// FieldPath selects the top-level path.
	FieldPath
	// FieldCommand selects the top-level command.
	FieldCommand
	// FieldOutput selects the top-level output.
	FieldOutput
	// FieldToolOutput selects the tool_output payload.
	FieldToolOutput
	// FieldToolResponse selects the tool_response payload.
	FieldToolResponse
	// FieldPrompt selects the user prompt.
	FieldPrompt
	// FieldText selects a free-form text payload.
	FieldText
	// FieldAssistantMessage selects the latest assistant message.
	FieldAssistantMessage
	// FieldLastAssistantMessage selects the previous assistant message.
	FieldLastAssistantMessage
	// FieldStatus selects a status string.
	FieldStatus
	// FieldReason selects a reason string.
	FieldReason
	// FieldError selects an error string.
	FieldError
	// FieldErrorType selects an error type string.
	FieldErrorType
	// FieldErrorMessage selects an error message.
	FieldErrorMessage
	// FieldFailureType selects a failure type label.
	FieldFailureType
	// FieldSource selects a source label.
	FieldSource
	// FieldNotificationType selects a notification type.
	FieldNotificationType
	// FieldMessage selects a message body.
	FieldMessage
	// FieldTitle selects a title string.
	FieldTitle
	// FieldTrigger selects a trigger label.
	FieldTrigger
	// FieldCustomInstructions selects user custom instructions.
	FieldCustomInstructions
	// FieldCompactSummary selects a compaction summary.
	FieldCompactSummary
	// FieldMemoryType selects a memory type label.
	FieldMemoryType
	// FieldLoadReason selects a load reason.
	FieldLoadReason
	// FieldTriggerFilePath selects the triggering file path.
	FieldTriggerFilePath
	// FieldParentFilePath selects the parent file path.
	FieldParentFilePath
	// FieldOldCWD selects the previous working directory.
	FieldOldCWD
	// FieldNewCWD selects the new working directory.
	FieldNewCWD
	// FieldEvent selects a generic event label.
	FieldEvent
	// FieldName selects a name field.
	FieldName
	// FieldWorktreePath selects a git worktree path.
	FieldWorktreePath
	// FieldMCPServerName selects the MCP server name.
	FieldMCPServerName
	// FieldURL selects a URL.
	FieldURL
	// FieldTimestamp selects a timestamp.
	FieldTimestamp
	// FieldSessionTitle selects the session title.
	FieldSessionTitle
	// FieldIsInterrupt selects the interrupt flag.
	FieldIsInterrupt
	// FieldErrorDetails selects detailed error text.
	FieldErrorDetails
	// FieldMode selects a mode label.
	FieldMode
	// FieldAction selects an action label.
	FieldAction
	// FieldElicitationID selects an elicitation identifier.
	FieldElicitationID
	// FieldTaskID selects a task identifier.
	FieldTaskID
	// FieldTaskSubject selects a task subject.
	FieldTaskSubject
	// FieldTaskDescription selects a task description.
	FieldTaskDescription
	// FieldTeammateName selects a teammate display name.
	FieldTeammateName
	// FieldTeamName selects a team display name.
	FieldTeamName
	// FieldStopHookActive selects the stop hook active flag.
	FieldStopHookActive
	// FieldAgentTranscriptPath selects the agent transcript path.
	FieldAgentTranscriptPath
	// FieldOriginalRequestName selects the original request name.
	FieldOriginalRequestName
	// FieldMCPContext selects MCP context payload text.
	FieldMCPContext
	// FieldPromptResponse selects a prompt response.
	FieldPromptResponse
	// FieldLLMRequest selects the upstream LLM request body.
	FieldLLMRequest
	// FieldLLMResponse selects the upstream LLM response body.
	FieldLLMResponse
	// FieldDetails selects a free-form details string.
	FieldDetails
	// FieldEditsOldString selects edits[*].old_string values, joined.
	FieldEditsOldString
	// FieldEditsNewString selects edits[*].new_string values, joined.
	FieldEditsNewString
	// FieldEditsOldLine selects edits[*].old_line values, joined.
	FieldEditsOldLine
	// FieldEditsNewLine selects edits[*].new_line values, joined.
	FieldEditsNewLine
	// FieldAttachmentsFilePath selects attachments[*].file_path values.
	FieldAttachmentsFilePath
	// FieldAttachmentsType selects attachments[*].type values.
	FieldAttachmentsType
)

// FieldSelectorSpec preserves the user-facing path for diagnostics while using
// a closed selector enum during evaluation.
type FieldSelectorSpec struct {
	Path     string
	Selector FieldSelector
}

// fieldSelectorByPath maps user-facing dotted-path field names to their
// closed [FieldSelector] enum. The map is the source of truth for valid
// field paths; [CompileFieldSelector] is a thin lookup wrapper.
var fieldSelectorByPath = map[string]FieldSelector{
	"hook_event_name":              FieldHookEventName,
	"session_id":                   FieldSessionID,
	"conversation_id":              FieldConversationID,
	"generation_id":                FieldGenerationID,
	"model":                        FieldModel,
	"cursor_version":               FieldCursorVersion,
	"user_email":                   FieldUserEmail,
	"transcript_path":              FieldTranscriptPath,
	"cwd":                          FieldCWD,
	"effective_cwd":                FieldEffectiveCWD,
	"cmd_segments":                 FieldCmdSegments,
	"cmd_comments":                 FieldCmdComments,
	"cmd_double_hyphen_prose":      FieldCmdDoubleHyphenProse,
	"cmd_redirections":             FieldCmdRedirections,
	"permission_mode":              FieldPermissionMode,
	"agent_id":                     FieldAgentID,
	"agent_type":                   FieldAgentType,
	"turn_id":                      FieldTurnID,
	"tool_name":                    FieldToolName,
	"tool_use_id":                  FieldToolUseID,
	"tool_input.command":           FieldToolInputCommand,
	"tool_input.file_path":         FieldToolInputFilePath,
	"tool_input.content":           FieldToolInputContent,
	"tool_input.old_string":        FieldToolInputOldString,
	"tool_input.new_string":        FieldToolInputNewString,
	"tool_input.description":       FieldToolInputDescription,
	"tool_input.prompt":            FieldToolInputPrompt,
	"tool_input.pattern":           FieldToolInputPattern,
	"tool_input.path":              FieldToolInputPath,
	"tool_input.url":               FieldToolInputURL,
	"tool_input.query":             FieldToolInputQuery,
	"tool_input.workdir":           FieldToolInputWorkdir,
	"tool_input.working_directory": FieldToolInputWorkingDirectory,
	"tool_input.cwd":               FieldToolInputCWD,
	"tool_input.directory":         FieldToolInputDirectory,
	"file_path":                    FieldFilePath,
	"path":                         FieldPath,
	"command":                      FieldCommand,
	"output":                       FieldOutput,
	"tool_output":                  FieldToolOutput,
	"tool_response":                FieldToolResponse,
	"prompt":                       FieldPrompt,
	"text":                         FieldText,
	"assistant_message":            FieldAssistantMessage,
	"last_assistant_message":       FieldLastAssistantMessage,
	"status":                       FieldStatus,
	"reason":                       FieldReason,
	"error":                        FieldError,
	"error_type":                   FieldErrorType,
	"error_message":                FieldErrorMessage,
	"failure_type":                 FieldFailureType,
	"source":                       FieldSource,
	"notification_type":            FieldNotificationType,
	"message":                      FieldMessage,
	"title":                        FieldTitle,
	"trigger":                      FieldTrigger,
	"custom_instructions":          FieldCustomInstructions,
	"compact_summary":              FieldCompactSummary,
	"memory_type":                  FieldMemoryType,
	"load_reason":                  FieldLoadReason,
	"trigger_file_path":            FieldTriggerFilePath,
	"parent_file_path":             FieldParentFilePath,
	"old_cwd":                      FieldOldCWD,
	"new_cwd":                      FieldNewCWD,
	"event":                        FieldEvent,
	"name":                         FieldName,
	"worktree_path":                FieldWorktreePath,
	"mcp_server_name":              FieldMCPServerName,
	"url":                          FieldURL,
	"timestamp":                    FieldTimestamp,
	"session_title":                FieldSessionTitle,
	"is_interrupt":                 FieldIsInterrupt,
	"error_details":                FieldErrorDetails,
	"mode":                         FieldMode,
	"action":                       FieldAction,
	"elicitation_id":               FieldElicitationID,
	"task_id":                      FieldTaskID,
	"task_subject":                 FieldTaskSubject,
	"task_description":             FieldTaskDescription,
	"teammate_name":                FieldTeammateName,
	"team_name":                    FieldTeamName,
	"stop_hook_active":             FieldStopHookActive,
	"agent_transcript_path":        FieldAgentTranscriptPath,
	"original_request_name":        FieldOriginalRequestName,
	"mcp_context":                  FieldMCPContext,
	"prompt_response":              FieldPromptResponse,
	"llm_request":                  FieldLLMRequest,
	"llm_response":                 FieldLLMResponse,
	"details":                      FieldDetails,
	"edits[*].old_string":          FieldEditsOldString,
	"edits[*].new_string":          FieldEditsNewString,
	"edits[*].old_line":            FieldEditsOldLine,
	"edits[*].new_line":            FieldEditsNewLine,
	"attachments[*].file_path":     FieldAttachmentsFilePath,
	"attachments[*].type":          FieldAttachmentsType,
}

// CompileFieldSelector returns the [FieldSelector] enum for a dotted-path
// field name. Unknown paths return [FieldSelectorInvalid].
func CompileFieldSelector(path string) FieldSelector {
	if selector, ok := fieldSelectorByPath[path]; ok {
		return selector
	}
	return FieldSelectorInvalid
}

func CompileFieldSelectorSpecs(paths []string) []FieldSelectorSpec {
	selectors := make([]FieldSelectorSpec, 0, len(paths))
	for _, path := range paths {
		selectors = append(selectors, FieldSelectorSpec{Path: path, Selector: CompileFieldSelector(path)})
	}
	return selectors
}
