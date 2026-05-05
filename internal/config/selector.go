package config

// FieldSelector identifies a rule-visible hook payload field without requiring
// runtime string-path navigation.
type FieldSelector int

const (
	FieldSelectorInvalid FieldSelector = iota
	FieldHookEventName
	FieldSessionID
	FieldConversationID
	FieldGenerationID
	FieldModel
	FieldCursorVersion
	FieldUserEmail
	FieldTranscriptPath
	FieldCWD
	FieldEffectiveCWD
	FieldCmdSegments
	FieldPermissionMode
	FieldAgentID
	FieldAgentType
	FieldTurnID
	FieldToolName
	FieldToolUseID
	FieldToolInputCommand
	FieldToolInputFilePath
	FieldToolInputContent
	FieldToolInputOldString
	FieldToolInputNewString
	FieldToolInputDescription
	FieldToolInputPrompt
	FieldToolInputPattern
	FieldToolInputPath
	FieldToolInputURL
	FieldToolInputQuery
	FieldToolInputWorkdir
	FieldToolInputWorkingDirectory
	FieldToolInputCWD
	FieldToolInputDirectory
	FieldFilePath
	FieldPath
	FieldCommand
	FieldOutput
	FieldToolOutput
	FieldToolResponse
	FieldPrompt
	FieldText
	FieldAssistantMessage
	FieldLastAssistantMessage
	FieldStatus
	FieldReason
	FieldError
	FieldErrorType
	FieldErrorMessage
	FieldFailureType
	FieldSource
	FieldNotificationType
	FieldMessage
	FieldTitle
	FieldTrigger
	FieldCustomInstructions
	FieldCompactSummary
	FieldMemoryType
	FieldLoadReason
	FieldTriggerFilePath
	FieldParentFilePath
	FieldOldCWD
	FieldNewCWD
	FieldEvent
	FieldName
	FieldWorktreePath
	FieldMCPServerName
	FieldURL
	FieldTimestamp
	FieldSessionTitle
	FieldIsInterrupt
	FieldErrorDetails
	FieldMode
	FieldAction
	FieldElicitationID
	FieldTaskID
	FieldTaskSubject
	FieldTaskDescription
	FieldTeammateName
	FieldTeamName
	FieldStopHookActive
	FieldAgentTranscriptPath
	FieldOriginalRequestName
	FieldMCPContext
	FieldPromptResponse
	FieldLLMRequest
	FieldLLMResponse
	FieldDetails
	FieldEditsOldString
	FieldEditsNewString
	FieldEditsOldLine
	FieldEditsNewLine
	FieldAttachmentsFilePath
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
