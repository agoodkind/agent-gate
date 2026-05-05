package hook

import (
	"encoding/json"
	"fmt"
	"strings"

	"goodkind.io/agent-gate/internal/rules"
)

// decodePayload is a generic helper that unmarshals JSON into dst and wraps
// the resulting error with a context message, so call sites can return the
// error directly without spelling out the wrap each time.
func decodePayload[T HookEvent](rawBytes []byte, dst *T) error {
	if err := json.Unmarshal(rawBytes, dst); err != nil {
		return fmt.Errorf("decode hook payload: %w", err)
	}
	return nil
}

// VSCodePayload is the hook payload shape used by VS Code (and the shared
// shape that GitHub Copilot extends).
type VSCodePayload struct {
	HookEvent      ClaudeEvent     `json:"hook_event_name"`
	Session        string          `json:"session_id"`
	TranscriptPath string          `json:"transcript_path"`
	Cwd            string          `json:"cwd"`
	ToolName       string          `json:"tool_name"`
	ToolUseID      string          `json:"tool_use_id"`
	ToolInput      VSCodeToolInput `json:"tool_input"`
	LastAssistant  string          `json:"last_assistant_message"`
	Text           string          `json:"text"`
}

// EventName returns the canonical hook event name.
func (p VSCodePayload) EventName() string { return string(p.HookEvent) }

// SessionID returns the VS Code session identifier.
func (p VSCodePayload) SessionID() string { return p.Session }

// CWD returns the working directory recorded in the payload.
func (p VSCodePayload) CWD() string { return p.Cwd }

// Fields renders the payload as a [rules.FieldSet].
func (p VSCodePayload) Fields() rules.FieldSet {
	var fields rules.FieldSet
	fields.HookEventName = string(p.HookEvent)
	fields.SessionID = p.Session
	fields.TranscriptPath = p.TranscriptPath
	fields.CWD = p.Cwd
	fields.ToolName = p.ToolName
	fields.ToolUseID = p.ToolUseID
	fields.ToolInputFilePath = p.ToolInput.FilePath
	fields.ToolInputCommand = p.ToolInput.Command
	fields.ToolInputContent = p.ToolInput.Content
	fields.ToolInputPrompt = p.ToolInput.Prompt
	fields.LastAssistantMessage = p.LastAssistant
	fields.Text = p.Text
	fields.ToolInputOldString = strings.Join(p.ToolInput.NormalizedOldStrings(), "\n")
	fields.ToolInputNewString = strings.Join(p.ToolInput.NormalizedNewStrings(), "\n")
	fields.EditsOldString = p.ToolInput.NormalizedOldStrings()
	fields.EditsNewString = p.ToolInput.NormalizedNewStrings()
	return fields
}

// CopilotPayload is the GitHub Copilot variant of the VS Code payload,
// adding an explicit assistant message field.
type CopilotPayload struct {
	VSCodePayload
	AssistantMessage string `json:"assistant_message"`
}

// Fields renders the payload as a [rules.FieldSet].
func (p CopilotPayload) Fields() rules.FieldSet {
	fields := p.VSCodePayload.Fields()
	fields.AssistantMessage = p.AssistantMessage
	if fields.LastAssistantMessage == "" {
		fields.LastAssistantMessage = p.LastAssistant
	}
	return fields
}

// ParseHookPayload decodes raw JSON bytes into a typed [HookPayload]
// dispatched on the given agent host. Decoding errors are returned wrapped
// with context so call sites do not need to wrap them again.
func ParseHookPayload(system HookSystem, rawBytes []byte) (HookPayload, error) {
	eventName, err := eventNameFromBytes(rawBytes)
	if err != nil {
		return HookPayload{}, err
	}
	switch system {
	case SystemCursor:
		event, err := parseCursorPayload(eventName, rawBytes)
		return HookPayload{System: system, Event: event}, err
	case SystemClaude:
		event, err := parseClaudePayload(eventName, rawBytes)
		return HookPayload{System: system, Event: event}, err
	case SystemCodex:
		event, err := parseCodexPayload(eventName, rawBytes)
		return HookPayload{System: system, Event: event}, err
	case SystemGemini:
		event, err := parseGeminiPayload(eventName, rawBytes)
		return HookPayload{System: system, Event: event}, err
	case SystemVSCode:
		var event VSCodePayload
		err := decodePayload(rawBytes, &event)
		return HookPayload{System: system, Event: event}, err
	case SystemCopilot:
		var event CopilotPayload
		err := decodePayload(rawBytes, &event)
		return HookPayload{System: system, Event: enrichCopilotPayload(event)}, err
	case SystemUnknown:
		fallthrough
	default:
		var event UnknownPayload
		err := decodePayload(rawBytes, &event)
		return HookPayload{System: system, Event: event}, err
	}
}

func eventNameFromBytes(rawBytes []byte) (string, error) {
	var envelope struct {
		HookEventName string `json:"hook_event_name"`
	}
	if err := json.Unmarshal(rawBytes, &envelope); err != nil {
		return "", fmt.Errorf("decode event name: %w", err)
	}
	return envelope.HookEventName, nil
}

func parseCursorPayload(eventName string, rawBytes []byte) (HookEvent, error) {
	switch CursorEvent(eventName) {
	case CursorSessionStart:
		var payload CursorSessionStartPayload
		return payload, decodePayload(rawBytes, &payload)
	case CursorSessionEnd:
		var payload CursorSessionEndPayload
		return payload, decodePayload(rawBytes, &payload)
	case CursorPreToolUse:
		var payload CursorPreToolUsePayload
		return payload, decodePayload(rawBytes, &payload)
	case CursorPostToolUse:
		var payload CursorPostToolUsePayload
		return payload, decodePayload(rawBytes, &payload)
	case CursorPostToolUseFailure:
		var payload CursorPostToolUseFailurePayload
		return payload, decodePayload(rawBytes, &payload)
	case CursorBeforeShellExecution:
		var payload CursorBeforeShellExecutionPayload
		return payload, decodePayload(rawBytes, &payload)
	case CursorAfterShellExecution:
		var payload CursorAfterShellExecutionPayload
		return payload, decodePayload(rawBytes, &payload)
	case CursorBeforeMCPExecution:
		var payload CursorBeforeMCPExecutionPayload
		return payload, decodePayload(rawBytes, &payload)
	case CursorAfterMCPExecution:
		var payload CursorAfterMCPExecutionPayload
		return payload, decodePayload(rawBytes, &payload)
	case CursorBeforeReadFile:
		var payload CursorBeforeReadFilePayload
		return payload, decodePayload(rawBytes, &payload)
	case CursorBeforeTabFileRead:
		var payload CursorBeforeTabFileReadPayload
		return payload, decodePayload(rawBytes, &payload)
	case CursorAfterFileEdit:
		var payload CursorAfterFileEditPayload
		return payload, decodePayload(rawBytes, &payload)
	case CursorAfterTabFileEdit:
		var payload CursorAfterTabFileEditPayload
		return payload, decodePayload(rawBytes, &payload)
	case CursorBeforeSubmitPrompt:
		var payload CursorBeforeSubmitPromptPayload
		return payload, decodePayload(rawBytes, &payload)
	case CursorSubagentStart:
		var payload CursorSubagentStartPayload
		return payload, decodePayload(rawBytes, &payload)
	case CursorSubagentStop:
		var payload CursorSubagentStopPayload
		return payload, decodePayload(rawBytes, &payload)
	case CursorPreCompact:
		var payload CursorPreCompactPayload
		return payload, decodePayload(rawBytes, &payload)
	case CursorStop:
		var payload CursorStopPayload
		return payload, decodePayload(rawBytes, &payload)
	case CursorAfterAgentResponse:
		var payload CursorAfterAgentResponsePayload
		return payload, decodePayload(rawBytes, &payload)
	case CursorAfterAgentThought:
		var payload CursorAfterAgentThoughtPayload
		return payload, decodePayload(rawBytes, &payload)
	default:
		var payload UnknownPayload
		return payload, decodePayload(rawBytes, &payload)
	}
}

func parseClaudePayload(eventName string, rawBytes []byte) (HookEvent, error) {
	switch ClaudeEvent(eventName) {
	case ClaudeSessionStart:
		var payload ClaudeSessionStartPayload
		return payload, decodePayload(rawBytes, &payload)
	case ClaudeSessionEnd:
		var payload ClaudeSessionEndPayload
		return payload, decodePayload(rawBytes, &payload)
	case ClaudeSetup:
		var payload ClaudeSetupPayload
		return payload, decodePayload(rawBytes, &payload)
	case ClaudePreToolUse:
		var payload ClaudePreToolUsePayload
		return payload, decodePayload(rawBytes, &payload)
	case ClaudePostToolUse:
		var payload ClaudePostToolUsePayload
		return payload, decodePayload(rawBytes, &payload)
	case ClaudePostToolUseFailure:
		var payload ClaudePostToolUseFailurePayload
		return payload, decodePayload(rawBytes, &payload)
	case ClaudePermissionRequest:
		var payload ClaudePermissionRequestPayload
		return payload, decodePayload(rawBytes, &payload)
	case ClaudePermissionDenied:
		var payload ClaudePermissionDeniedPayload
		return payload, decodePayload(rawBytes, &payload)
	case ClaudeUserPromptSubmit:
		var payload ClaudeUserPromptSubmitPayload
		return payload, decodePayload(rawBytes, &payload)
	case ClaudeStop:
		var payload ClaudeStopPayload
		return payload, decodePayload(rawBytes, &payload)
	case ClaudeStopFailure:
		var payload ClaudeStopFailurePayload
		return payload, decodePayload(rawBytes, &payload)
	case ClaudeSubagentStart:
		var payload ClaudeSubagentStartPayload
		return payload, decodePayload(rawBytes, &payload)
	case ClaudeSubagentStop:
		var payload ClaudeSubagentStopPayload
		return payload, decodePayload(rawBytes, &payload)
	case ClaudeTaskCreated:
		var payload ClaudeTaskCreatedPayload
		return payload, decodePayload(rawBytes, &payload)
	case ClaudeTaskCompleted:
		var payload ClaudeTaskCompletedPayload
		return payload, decodePayload(rawBytes, &payload)
	case ClaudeNotification:
		var payload ClaudeNotificationPayload
		return payload, decodePayload(rawBytes, &payload)
	case ClaudePreCompact:
		var payload ClaudePreCompactPayload
		return payload, decodePayload(rawBytes, &payload)
	case ClaudePostCompact:
		var payload ClaudePostCompactPayload
		return payload, decodePayload(rawBytes, &payload)
	case ClaudeInstructionsLoaded:
		var payload ClaudeInstructionsLoadedPayload
		return payload, decodePayload(rawBytes, &payload)
	case ClaudeConfigChange:
		var payload ClaudeConfigChangePayload
		return payload, decodePayload(rawBytes, &payload)
	case ClaudeCwdChanged:
		var payload ClaudeCwdChangedPayload
		return payload, decodePayload(rawBytes, &payload)
	case ClaudeFileChanged:
		var payload ClaudeFileChangedPayload
		return payload, decodePayload(rawBytes, &payload)
	case ClaudeWorktreeCreate:
		var payload ClaudeWorktreeCreatePayload
		return payload, decodePayload(rawBytes, &payload)
	case ClaudeWorktreeRemove:
		var payload ClaudeWorktreeRemovePayload
		return payload, decodePayload(rawBytes, &payload)
	case ClaudeElicitation:
		var payload ClaudeElicitationPayload
		return payload, decodePayload(rawBytes, &payload)
	case ClaudeElicitationResult:
		var payload ClaudeElicitationResultPayload
		return payload, decodePayload(rawBytes, &payload)
	case ClaudeTeammateIdle:
		var payload ClaudeTeammateIdlePayload
		return payload, decodePayload(rawBytes, &payload)
	default:
		var payload UnknownPayload
		return payload, decodePayload(rawBytes, &payload)
	}
}

func parseCodexPayload(eventName string, rawBytes []byte) (HookEvent, error) {
	switch CodexEvent(eventName) {
	case CodexSessionStart:
		var payload CodexSessionStartPayload
		return payload, decodePayload(rawBytes, &payload)
	case CodexPreToolUse:
		var payload CodexPreToolUsePayload
		return payload, decodePayload(rawBytes, &payload)
	case CodexPermissionRequest:
		var payload CodexPermissionRequestPayload
		return payload, decodePayload(rawBytes, &payload)
	case CodexPostToolUse:
		var payload CodexPostToolUsePayload
		return payload, decodePayload(rawBytes, &payload)
	case CodexUserPromptSubmit:
		var payload CodexUserPromptSubmitPayload
		return payload, decodePayload(rawBytes, &payload)
	case CodexStop:
		var payload CodexStopPayload
		return payload, decodePayload(rawBytes, &payload)
	default:
		var payload UnknownPayload
		return payload, decodePayload(rawBytes, &payload)
	}
}

func parseGeminiPayload(eventName string, rawBytes []byte) (HookEvent, error) {
	switch GeminiEvent(eventName) {
	case GeminiBeforeTool:
		var payload GeminiBeforeToolPayload
		return payload, decodePayload(rawBytes, &payload)
	case GeminiAfterTool:
		var payload GeminiAfterToolPayload
		return payload, decodePayload(rawBytes, &payload)
	case GeminiBeforeAgent:
		var payload GeminiBeforeAgentPayload
		return payload, decodePayload(rawBytes, &payload)
	case GeminiAfterAgent:
		var payload GeminiAfterAgentPayload
		return payload, decodePayload(rawBytes, &payload)
	case GeminiBeforeModel:
		var payload GeminiBeforeModelPayload
		return payload, decodePayload(rawBytes, &payload)
	case GeminiBeforeToolSelection:
		var payload GeminiBeforeToolSelectionPayload
		return payload, decodePayload(rawBytes, &payload)
	case GeminiAfterModel:
		var payload GeminiAfterModelPayload
		return payload, decodePayload(rawBytes, &payload)
	case GeminiSessionStart:
		var payload GeminiSessionStartPayload
		return payload, decodePayload(rawBytes, &payload)
	case GeminiSessionEnd:
		var payload GeminiSessionEndPayload
		return payload, decodePayload(rawBytes, &payload)
	case GeminiNotification:
		var payload GeminiNotificationPayload
		return payload, decodePayload(rawBytes, &payload)
	case GeminiPreCompress:
		var payload GeminiPreCompressPayload
		return payload, decodePayload(rawBytes, &payload)
	default:
		var payload UnknownPayload
		return payload, decodePayload(rawBytes, &payload)
	}
}

func enrichCopilotPayload(payload CopilotPayload) CopilotPayload {
	if payload.EventName() != string(ClaudeStop) {
		return payload
	}
	if payload.AssistantMessage != "" || payload.LastAssistant != "" || payload.Text != "" {
		return payload
	}
	if payload.TranscriptPath == "" {
		return payload
	}
	payload.LastAssistant = lastCopilotAssistantMessage(payload.TranscriptPath)
	return payload
}
