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

// FirstString returns the user-facing path and value of the first selector
// in selectors that resolves to a non-empty string. Both return values are
// the empty string when no selector matches.
func (fields FieldSet) FirstString(selectors []config.FieldSelectorSpec) (string, string) {
	for _, selector := range selectors {
		value := fields.String(selector.Selector)
		if value != "" {
			return selector.Path, value
		}
	}
	return "", ""
}

// fieldStringAccessors maps each [config.FieldSelector] to a function that
// extracts the corresponding string view from a [FieldSet]. The map is
// populated at init time so [FieldSet.String] becomes a table lookup
// rather than a giant switch.
var fieldStringAccessors = map[config.FieldSelector]func(FieldSet) string{
	config.FieldHookEventName:             func(f FieldSet) string { return f.HookEventName },
	config.FieldSessionID:                 func(f FieldSet) string { return f.SessionID },
	config.FieldConversationID:            func(f FieldSet) string { return f.ConversationID },
	config.FieldGenerationID:              func(f FieldSet) string { return f.GenerationID },
	config.FieldModel:                     func(f FieldSet) string { return f.Model },
	config.FieldCursorVersion:             func(f FieldSet) string { return f.CursorVersion },
	config.FieldUserEmail:                 func(f FieldSet) string { return f.UserEmail },
	config.FieldTranscriptPath:            func(f FieldSet) string { return f.TranscriptPath },
	config.FieldCWD:                       func(f FieldSet) string { return f.CWD },
	config.FieldEffectiveCWD:              func(f FieldSet) string { return f.effectiveCWD() },
	config.FieldCmdSegments:               func(f FieldSet) string { return f.CmdSegments() },
	config.FieldPermissionMode:            func(f FieldSet) string { return f.PermissionMode },
	config.FieldAgentID:                   func(f FieldSet) string { return f.AgentID },
	config.FieldAgentType:                 func(f FieldSet) string { return f.AgentType },
	config.FieldTurnID:                    func(f FieldSet) string { return f.TurnID },
	config.FieldToolName:                  func(f FieldSet) string { return f.ToolName },
	config.FieldToolUseID:                 func(f FieldSet) string { return f.ToolUseID },
	config.FieldToolInputCommand:          func(f FieldSet) string { return f.ToolInputCommand },
	config.FieldToolInputFilePath:         func(f FieldSet) string { return f.ToolInputFilePath },
	config.FieldToolInputContent:          func(f FieldSet) string { return f.ToolInputContent },
	config.FieldToolInputOldString:        func(f FieldSet) string { return f.ToolInputOldString },
	config.FieldToolInputNewString:        func(f FieldSet) string { return f.ToolInputNewString },
	config.FieldToolInputDescription:      func(f FieldSet) string { return f.ToolInputDescription },
	config.FieldToolInputPrompt:           func(f FieldSet) string { return f.ToolInputPrompt },
	config.FieldToolInputPattern:          func(f FieldSet) string { return f.ToolInputPattern },
	config.FieldToolInputPath:             func(f FieldSet) string { return f.ToolInputPath },
	config.FieldToolInputURL:              func(f FieldSet) string { return f.ToolInputURL },
	config.FieldToolInputQuery:            func(f FieldSet) string { return f.ToolInputQuery },
	config.FieldToolInputWorkdir:          func(f FieldSet) string { return f.ToolInputWorkdir },
	config.FieldToolInputWorkingDirectory: func(f FieldSet) string { return f.ToolInputWorkingDir },
	config.FieldToolInputCWD:              func(f FieldSet) string { return f.ToolInputCWD },
	config.FieldToolInputDirectory:        func(f FieldSet) string { return f.ToolInputDirectory },
	config.FieldFilePath:                  func(f FieldSet) string { return f.FilePath },
	config.FieldPath:                      func(f FieldSet) string { return f.Path },
	config.FieldCommand:                   func(f FieldSet) string { return f.Command },
	config.FieldOutput:                    func(f FieldSet) string { return f.Output },
	config.FieldToolOutput:                func(f FieldSet) string { return f.ToolOutput },
	config.FieldToolResponse:              func(f FieldSet) string { return f.ToolResponse },
	config.FieldPrompt:                    func(f FieldSet) string { return f.Prompt },
	config.FieldText:                      func(f FieldSet) string { return f.Text },
	config.FieldAssistantMessage:          func(f FieldSet) string { return f.AssistantMessage },
	config.FieldLastAssistantMessage:      func(f FieldSet) string { return f.LastAssistantMessage },
	config.FieldStatus:                    func(f FieldSet) string { return f.Status },
	config.FieldReason:                    func(f FieldSet) string { return f.Reason },
	config.FieldError:                     func(f FieldSet) string { return f.Error },
	config.FieldErrorType:                 func(f FieldSet) string { return f.ErrorType },
	config.FieldErrorMessage:              func(f FieldSet) string { return f.ErrorMessage },
	config.FieldFailureType:               func(f FieldSet) string { return f.FailureType },
	config.FieldSource:                    func(f FieldSet) string { return f.Source },
	config.FieldNotificationType:          func(f FieldSet) string { return f.NotificationType },
	config.FieldMessage:                   func(f FieldSet) string { return f.Message },
	config.FieldTitle:                     func(f FieldSet) string { return f.Title },
	config.FieldTrigger:                   func(f FieldSet) string { return f.Trigger },
	config.FieldCustomInstructions:        func(f FieldSet) string { return f.CustomInstructions },
	config.FieldCompactSummary:            func(f FieldSet) string { return f.CompactSummary },
	config.FieldMemoryType:                func(f FieldSet) string { return f.MemoryType },
	config.FieldLoadReason:                func(f FieldSet) string { return f.LoadReason },
	config.FieldTriggerFilePath:           func(f FieldSet) string { return f.TriggerFilePath },
	config.FieldParentFilePath:            func(f FieldSet) string { return f.ParentFilePath },
	config.FieldOldCWD:                    func(f FieldSet) string { return f.OldCWD },
	config.FieldNewCWD:                    func(f FieldSet) string { return f.NewCWD },
	config.FieldEvent:                     func(f FieldSet) string { return f.Event },
	config.FieldName:                      func(f FieldSet) string { return f.Name },
	config.FieldWorktreePath:              func(f FieldSet) string { return f.WorktreePath },
	config.FieldMCPServerName:             func(f FieldSet) string { return f.MCPServerName },
	config.FieldURL:                       func(f FieldSet) string { return f.URL },
	config.FieldTimestamp:                 func(f FieldSet) string { return f.Timestamp },
	config.FieldSessionTitle:              func(f FieldSet) string { return f.SessionTitle },
	config.FieldIsInterrupt:               func(f FieldSet) string { return f.IsInterrupt },
	config.FieldErrorDetails:              func(f FieldSet) string { return f.ErrorDetails },
	config.FieldMode:                      func(f FieldSet) string { return f.Mode },
	config.FieldAction:                    func(f FieldSet) string { return f.Action },
	config.FieldElicitationID:             func(f FieldSet) string { return f.ElicitationID },
	config.FieldTaskID:                    func(f FieldSet) string { return f.TaskID },
	config.FieldTaskSubject:               func(f FieldSet) string { return f.TaskSubject },
	config.FieldTaskDescription:           func(f FieldSet) string { return f.TaskDescription },
	config.FieldTeammateName:              func(f FieldSet) string { return f.TeammateName },
	config.FieldTeamName:                  func(f FieldSet) string { return f.TeamName },
	config.FieldStopHookActive:            func(f FieldSet) string { return f.StopHookActive },
	config.FieldAgentTranscriptPath:       func(f FieldSet) string { return f.AgentTranscriptPath },
	config.FieldOriginalRequestName:       func(f FieldSet) string { return f.OriginalRequestName },
	config.FieldMCPContext:                func(f FieldSet) string { return f.MCPContext },
	config.FieldPromptResponse:            func(f FieldSet) string { return f.PromptResponse },
	config.FieldLLMRequest:                func(f FieldSet) string { return f.LLMRequest },
	config.FieldLLMResponse:               func(f FieldSet) string { return f.LLMResponse },
	config.FieldDetails:                   func(f FieldSet) string { return f.Details },
	config.FieldEditsOldString:            func(f FieldSet) string { return strings.Join(f.EditsOldString, "\n") },
	config.FieldEditsNewString:            func(f FieldSet) string { return strings.Join(f.EditsNewString, "\n") },
	config.FieldEditsOldLine:              func(f FieldSet) string { return strings.Join(f.EditsOldLine, "\n") },
	config.FieldEditsNewLine:              func(f FieldSet) string { return strings.Join(f.EditsNewLine, "\n") },
	config.FieldAttachmentsFilePath:       func(f FieldSet) string { return strings.Join(f.AttachmentsFilePath, "\n") },
	config.FieldAttachmentsType:           func(f FieldSet) string { return strings.Join(f.AttachmentsType, "\n") },
}

// String returns the string view of fields selected by the given
// [config.FieldSelector]. Unknown selectors yield the empty string.
func (fields FieldSet) String(selector config.FieldSelector) string {
	if accessor, ok := fieldStringAccessors[selector]; ok {
		return accessor(fields)
	}
	return ""
}

// CommandValue returns the most specific command string available, preferring
// the explicit tool input command over the generic command field.
func (fields FieldSet) CommandValue() string {
	if fields.ToolInputCommand != "" {
		return fields.ToolInputCommand
	}
	return fields.Command
}

// FilePathValue returns the first non-empty file path candidate from the
// payload, walking explicit fields before tool input fallbacks.
func (fields FieldSet) FilePathValue() string {
	for _, value := range []string{fields.FilePath, fields.Path, fields.ToolInputFilePath, fields.ToolInputPath} {
		if value != "" {
			return value
		}
	}
	return ""
}

// BaseCWD returns the most specific working directory candidate from the
// payload before any cd-chain rewriting is applied.
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

// CmdSegments is a free-function alias for [FieldSet.CmdSegments].
func CmdSegments(fields FieldSet) string { return fields.CmdSegments() }

// CmdSegments splits the command into newline-joined chained segments.
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
	home, err := ReadUserHomeDir()
	if err != nil {
		home = cwd
	}
	return ApplyCdChain(cwd, home, command)
}
