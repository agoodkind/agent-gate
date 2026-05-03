package rules

import (
	"strings"

	"goodkind.io/agent-gate/internal/config"
)

// FieldSet is the closed collection of values rule selectors can inspect.
type FieldSet struct {
	HookEventName        string
	SessionID            string
	ConversationID       string
	GenerationID         string
	Model                string
	CursorVersion        string
	UserEmail            string
	TranscriptPath       string
	CWD                  string
	EffectiveCWD         string
	PermissionMode       string
	AgentID              string
	AgentType            string
	TurnID               string
	ToolName             string
	ToolUseID            string
	ToolInputCommand     string
	ToolInputFilePath    string
	ToolInputContent     string
	ToolInputOldString   string
	ToolInputNewString   string
	ToolInputDescription string
	ToolInputPrompt      string
	ToolInputPattern     string
	ToolInputPath        string
	ToolInputURL         string
	ToolInputQuery       string
	ToolInputWorkdir     string
	ToolInputWorkingDir  string
	ToolInputCWD         string
	ToolInputDirectory   string
	FilePath             string
	Path                 string
	Command              string
	Output               string
	ToolOutput           string
	ToolResponse         string
	Prompt               string
	Text                 string
	AssistantMessage     string
	LastAssistantMessage string
	Status               string
	Reason               string
	Error                string
	ErrorType            string
	ErrorMessage         string
	FailureType          string
	Source               string
	NotificationType     string
	Message              string
	Title                string
	Trigger              string
	CustomInstructions   string
	CompactSummary       string
	MemoryType           string
	LoadReason           string
	TriggerFilePath      string
	ParentFilePath       string
	OldCWD               string
	NewCWD               string
	Event                string
	Name                 string
	WorktreePath         string
	MCPServerName        string
	URL                  string
	Timestamp            string
	SessionTitle         string
	IsInterrupt          string
	ErrorDetails         string
	Mode                 string
	Action               string
	ElicitationID        string
	TaskID               string
	TaskSubject          string
	TaskDescription      string
	TeammateName         string
	TeamName             string
	StopHookActive       string
	AgentTranscriptPath  string
	OriginalRequestName  string
	MCPContext           string
	PromptResponse       string
	LLMRequest           string
	LLMResponse          string
	Details              string
	EditsOldString       []string
	EditsNewString       []string
	EditsOldLine         []string
	EditsNewLine         []string
	AttachmentsFilePath  []string
	AttachmentsType      []string
}

func (fields FieldSet) FirstString(selectors []config.FieldSelectorSpec) (string, string) {
	for _, selector := range selectors {
		value := fields.String(selector.Selector)
		if value != "" {
			return selector.Path, value
		}
	}
	return "", ""
}

func (fields FieldSet) String(selector config.FieldSelector) string {
	switch selector {
	case config.FieldHookEventName:
		return fields.HookEventName
	case config.FieldSessionID:
		return fields.SessionID
	case config.FieldConversationID:
		return fields.ConversationID
	case config.FieldGenerationID:
		return fields.GenerationID
	case config.FieldModel:
		return fields.Model
	case config.FieldCursorVersion:
		return fields.CursorVersion
	case config.FieldUserEmail:
		return fields.UserEmail
	case config.FieldTranscriptPath:
		return fields.TranscriptPath
	case config.FieldCWD:
		return fields.CWD
	case config.FieldEffectiveCWD:
		return fields.effectiveCWD()
	case config.FieldCmdSegments:
		return fields.CmdSegments()
	case config.FieldPermissionMode:
		return fields.PermissionMode
	case config.FieldAgentID:
		return fields.AgentID
	case config.FieldAgentType:
		return fields.AgentType
	case config.FieldTurnID:
		return fields.TurnID
	case config.FieldToolName:
		return fields.ToolName
	case config.FieldToolUseID:
		return fields.ToolUseID
	case config.FieldToolInputCommand:
		return fields.ToolInputCommand
	case config.FieldToolInputFilePath:
		return fields.ToolInputFilePath
	case config.FieldToolInputContent:
		return fields.ToolInputContent
	case config.FieldToolInputOldString:
		return fields.ToolInputOldString
	case config.FieldToolInputNewString:
		return fields.ToolInputNewString
	case config.FieldToolInputDescription:
		return fields.ToolInputDescription
	case config.FieldToolInputPrompt:
		return fields.ToolInputPrompt
	case config.FieldToolInputPattern:
		return fields.ToolInputPattern
	case config.FieldToolInputPath:
		return fields.ToolInputPath
	case config.FieldToolInputURL:
		return fields.ToolInputURL
	case config.FieldToolInputQuery:
		return fields.ToolInputQuery
	case config.FieldToolInputWorkdir:
		return fields.ToolInputWorkdir
	case config.FieldToolInputWorkingDirectory:
		return fields.ToolInputWorkingDir
	case config.FieldToolInputCWD:
		return fields.ToolInputCWD
	case config.FieldToolInputDirectory:
		return fields.ToolInputDirectory
	case config.FieldFilePath:
		return fields.FilePath
	case config.FieldPath:
		return fields.Path
	case config.FieldCommand:
		return fields.Command
	case config.FieldOutput:
		return fields.Output
	case config.FieldToolOutput:
		return fields.ToolOutput
	case config.FieldToolResponse:
		return fields.ToolResponse
	case config.FieldPrompt:
		return fields.Prompt
	case config.FieldText:
		return fields.Text
	case config.FieldAssistantMessage:
		return fields.AssistantMessage
	case config.FieldLastAssistantMessage:
		return fields.LastAssistantMessage
	case config.FieldStatus:
		return fields.Status
	case config.FieldReason:
		return fields.Reason
	case config.FieldError:
		return fields.Error
	case config.FieldErrorType:
		return fields.ErrorType
	case config.FieldErrorMessage:
		return fields.ErrorMessage
	case config.FieldFailureType:
		return fields.FailureType
	case config.FieldSource:
		return fields.Source
	case config.FieldNotificationType:
		return fields.NotificationType
	case config.FieldMessage:
		return fields.Message
	case config.FieldTitle:
		return fields.Title
	case config.FieldTrigger:
		return fields.Trigger
	case config.FieldCustomInstructions:
		return fields.CustomInstructions
	case config.FieldCompactSummary:
		return fields.CompactSummary
	case config.FieldMemoryType:
		return fields.MemoryType
	case config.FieldLoadReason:
		return fields.LoadReason
	case config.FieldTriggerFilePath:
		return fields.TriggerFilePath
	case config.FieldParentFilePath:
		return fields.ParentFilePath
	case config.FieldOldCWD:
		return fields.OldCWD
	case config.FieldNewCWD:
		return fields.NewCWD
	case config.FieldEvent:
		return fields.Event
	case config.FieldName:
		return fields.Name
	case config.FieldWorktreePath:
		return fields.WorktreePath
	case config.FieldMCPServerName:
		return fields.MCPServerName
	case config.FieldURL:
		return fields.URL
	case config.FieldTimestamp:
		return fields.Timestamp
	case config.FieldSessionTitle:
		return fields.SessionTitle
	case config.FieldIsInterrupt:
		return fields.IsInterrupt
	case config.FieldErrorDetails:
		return fields.ErrorDetails
	case config.FieldMode:
		return fields.Mode
	case config.FieldAction:
		return fields.Action
	case config.FieldElicitationID:
		return fields.ElicitationID
	case config.FieldTaskID:
		return fields.TaskID
	case config.FieldTaskSubject:
		return fields.TaskSubject
	case config.FieldTaskDescription:
		return fields.TaskDescription
	case config.FieldTeammateName:
		return fields.TeammateName
	case config.FieldTeamName:
		return fields.TeamName
	case config.FieldStopHookActive:
		return fields.StopHookActive
	case config.FieldAgentTranscriptPath:
		return fields.AgentTranscriptPath
	case config.FieldOriginalRequestName:
		return fields.OriginalRequestName
	case config.FieldMCPContext:
		return fields.MCPContext
	case config.FieldPromptResponse:
		return fields.PromptResponse
	case config.FieldLLMRequest:
		return fields.LLMRequest
	case config.FieldLLMResponse:
		return fields.LLMResponse
	case config.FieldDetails:
		return fields.Details
	case config.FieldEditsOldString:
		return strings.Join(fields.EditsOldString, "\n")
	case config.FieldEditsNewString:
		return strings.Join(fields.EditsNewString, "\n")
	case config.FieldEditsOldLine:
		return strings.Join(fields.EditsOldLine, "\n")
	case config.FieldEditsNewLine:
		return strings.Join(fields.EditsNewLine, "\n")
	case config.FieldAttachmentsFilePath:
		return strings.Join(fields.AttachmentsFilePath, "\n")
	case config.FieldAttachmentsType:
		return strings.Join(fields.AttachmentsType, "\n")
	default:
		return ""
	}
}

func (fields FieldSet) CommandValue() string {
	if fields.ToolInputCommand != "" {
		return fields.ToolInputCommand
	}
	return fields.Command
}

func (fields FieldSet) FilePathValue() string {
	for _, value := range []string{fields.FilePath, fields.Path, fields.ToolInputFilePath, fields.ToolInputPath} {
		if value != "" {
			return value
		}
	}
	return ""
}

func (fields FieldSet) BaseCWD() string {
	for _, value := range []string{
		fields.EffectiveCWD,
		fields.ToolInputWorkdir,
		fields.ToolInputWorkingDir,
		fields.ToolInputCWD,
		fields.ToolInputDirectory,
		fields.CWD,
	} {
		if value != "" {
			return value
		}
	}
	return ""
}

func CmdSegments(fields FieldSet) string { return fields.CmdSegments() }

func (fields FieldSet) CmdSegments() string {
	command := fields.CommandValue()
	if command == "" {
		return ""
	}
	var segments []string
	for _, segment := range cmdChainRe.Split(command, -1) {
		segment = strings.TrimSpace(segment)
		if segment != "" {
			segments = append(segments, segment)
		}
	}
	return strings.Join(segments, "\n")
}

func (fields FieldSet) effectiveCWD() string {
	cwd := fields.BaseCWD()
	if cwd == "" {
		return ""
	}
	command := fields.CommandValue()
	if command == "" {
		return cwd
	}
	home, err := osUserHomeDir()
	if err != nil {
		home = cwd
	}
	return ApplyCdChain(cwd, home, command)
}
