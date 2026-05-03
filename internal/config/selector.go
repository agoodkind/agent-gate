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

func CompileFieldSelector(path string) FieldSelector {
	switch path {
	case "hook_event_name":
		return FieldHookEventName
	case "session_id":
		return FieldSessionID
	case "conversation_id":
		return FieldConversationID
	case "generation_id":
		return FieldGenerationID
	case "model":
		return FieldModel
	case "cursor_version":
		return FieldCursorVersion
	case "user_email":
		return FieldUserEmail
	case "transcript_path":
		return FieldTranscriptPath
	case "cwd":
		return FieldCWD
	case "effective_cwd":
		return FieldEffectiveCWD
	case "cmd_segments":
		return FieldCmdSegments
	case "permission_mode":
		return FieldPermissionMode
	case "agent_id":
		return FieldAgentID
	case "agent_type":
		return FieldAgentType
	case "turn_id":
		return FieldTurnID
	case "tool_name":
		return FieldToolName
	case "tool_use_id":
		return FieldToolUseID
	case "tool_input.command":
		return FieldToolInputCommand
	case "tool_input.file_path":
		return FieldToolInputFilePath
	case "tool_input.content":
		return FieldToolInputContent
	case "tool_input.old_string":
		return FieldToolInputOldString
	case "tool_input.new_string":
		return FieldToolInputNewString
	case "tool_input.description":
		return FieldToolInputDescription
	case "tool_input.prompt":
		return FieldToolInputPrompt
	case "tool_input.pattern":
		return FieldToolInputPattern
	case "tool_input.path":
		return FieldToolInputPath
	case "tool_input.url":
		return FieldToolInputURL
	case "tool_input.query":
		return FieldToolInputQuery
	case "tool_input.workdir":
		return FieldToolInputWorkdir
	case "tool_input.working_directory":
		return FieldToolInputWorkingDirectory
	case "tool_input.cwd":
		return FieldToolInputCWD
	case "tool_input.directory":
		return FieldToolInputDirectory
	case "file_path":
		return FieldFilePath
	case "path":
		return FieldPath
	case "command":
		return FieldCommand
	case "output":
		return FieldOutput
	case "tool_output":
		return FieldToolOutput
	case "tool_response":
		return FieldToolResponse
	case "prompt":
		return FieldPrompt
	case "text":
		return FieldText
	case "assistant_message":
		return FieldAssistantMessage
	case "last_assistant_message":
		return FieldLastAssistantMessage
	case "status":
		return FieldStatus
	case "reason":
		return FieldReason
	case "error":
		return FieldError
	case "error_type":
		return FieldErrorType
	case "error_message":
		return FieldErrorMessage
	case "failure_type":
		return FieldFailureType
	case "source":
		return FieldSource
	case "notification_type":
		return FieldNotificationType
	case "message":
		return FieldMessage
	case "title":
		return FieldTitle
	case "trigger":
		return FieldTrigger
	case "custom_instructions":
		return FieldCustomInstructions
	case "compact_summary":
		return FieldCompactSummary
	case "memory_type":
		return FieldMemoryType
	case "load_reason":
		return FieldLoadReason
	case "trigger_file_path":
		return FieldTriggerFilePath
	case "parent_file_path":
		return FieldParentFilePath
	case "old_cwd":
		return FieldOldCWD
	case "new_cwd":
		return FieldNewCWD
	case "event":
		return FieldEvent
	case "name":
		return FieldName
	case "worktree_path":
		return FieldWorktreePath
	case "mcp_server_name":
		return FieldMCPServerName
	case "url":
		return FieldURL
	case "timestamp":
		return FieldTimestamp
	case "session_title":
		return FieldSessionTitle
	case "is_interrupt":
		return FieldIsInterrupt
	case "error_details":
		return FieldErrorDetails
	case "mode":
		return FieldMode
	case "action":
		return FieldAction
	case "elicitation_id":
		return FieldElicitationID
	case "task_id":
		return FieldTaskID
	case "task_subject":
		return FieldTaskSubject
	case "task_description":
		return FieldTaskDescription
	case "teammate_name":
		return FieldTeammateName
	case "team_name":
		return FieldTeamName
	case "stop_hook_active":
		return FieldStopHookActive
	case "agent_transcript_path":
		return FieldAgentTranscriptPath
	case "original_request_name":
		return FieldOriginalRequestName
	case "mcp_context":
		return FieldMCPContext
	case "prompt_response":
		return FieldPromptResponse
	case "llm_request":
		return FieldLLMRequest
	case "llm_response":
		return FieldLLMResponse
	case "details":
		return FieldDetails
	case "edits[*].old_string":
		return FieldEditsOldString
	case "edits[*].new_string":
		return FieldEditsNewString
	case "edits[*].old_line":
		return FieldEditsOldLine
	case "edits[*].new_line":
		return FieldEditsNewLine
	case "attachments[*].file_path":
		return FieldAttachmentsFilePath
	case "attachments[*].type":
		return FieldAttachmentsType
	default:
		return FieldSelectorInvalid
	}
}

func CompileFieldSelectorSpecs(paths []string) []FieldSelectorSpec {
	selectors := make([]FieldSelectorSpec, 0, len(paths))
	for _, path := range paths {
		selectors = append(selectors, FieldSelectorSpec{Path: path, Selector: CompileFieldSelector(path)})
	}
	return selectors
}
