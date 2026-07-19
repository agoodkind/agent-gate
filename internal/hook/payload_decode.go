package hook

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"goodkind.io/agent-gate/internal/rules"
)

// decodePayload is a generic helper that unmarshals JSON into dst and wraps
// the resulting error with a context message, so call sites can return the
// error directly without spelling out the wrap each time.
func decodePayload[T Event](rawBytes []byte, dst *T) error {
	if err := json.Unmarshal(rawBytes, dst); err != nil {
		slog.Warn("decode hook payload failed", slog.Any("err", err))
		return fmt.Errorf("decode hook payload: %w", err)
	}
	return nil
}

// decodeJSONPayload decodes rawBytes into a fresh value of the concrete payload
// type T and returns it as an [Event], so per-event switch arms collapse to a
// single return expression instead of a var-declare-then-return pair.
func decodeJSONPayload[T Event](rawBytes []byte) (Event, error) {
	var payload T
	return payload, decodePayload(rawBytes, &payload)
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
	AssistantMessage  string `json:"assistant_message"`
	Prompt            string `json:"prompt"`
	TransformedPrompt string `json:"transformedPrompt"`
}

// Fields renders the payload as a [rules.FieldSet].
func (p CopilotPayload) Fields() rules.FieldSet {
	fields := p.VSCodePayload.Fields()
	fields.AssistantMessage = p.AssistantMessage
	if fields.LastAssistantMessage == "" {
		fields.LastAssistantMessage = p.LastAssistant
	}
	if p.TransformedPrompt != "" {
		fields.Prompt = p.TransformedPrompt
	} else if p.Prompt != "" {
		fields.Prompt = p.Prompt
	}
	return fields
}

// ParseHookPayload decodes raw JSON bytes into a typed [Payload]
// dispatched on the given agent host. Decoding errors are returned wrapped
// with context so call sites do not need to wrap them again.
func ParseHookPayload(system System, rawBytes []byte) (Payload, error) {
	eventName, err := eventNameFromBytes(rawBytes)
	if err != nil {
		return Payload{}, err
	}
	switch system {
	case SystemCursor:
		event, err := parseCursorPayload(eventName, rawBytes)
		return Payload{System: system, Event: event}, err
	case SystemClaude:
		event, err := parseClaudePayload(eventName, rawBytes)
		return Payload{System: system, Event: event}, err
	case SystemCodex:
		event, err := parseCodexPayload(eventName, rawBytes)
		return Payload{System: system, Event: event}, err
	case SystemGemini:
		event, err := parseGeminiPayload(eventName, rawBytes)
		return Payload{System: system, Event: event}, err
	case SystemVSCode:
		var event VSCodePayload
		err := decodePayload(rawBytes, &event)
		return Payload{System: system, Event: event}, err
	case SystemCopilot:
		var event CopilotPayload
		err := decodePayload(rawBytes, &event)
		return Payload{System: system, Event: enrichCopilotPayload(event)}, err
	case SystemUnknown:
		fallthrough
	default:
		var event UnknownPayload
		err := decodePayload(rawBytes, &event)
		return Payload{System: system, Event: event}, err
	}
}

func eventNameFromBytes(rawBytes []byte) (string, error) {
	var envelope struct {
		HookEventName string `json:"hook_event_name"`
	}
	if err := json.Unmarshal(rawBytes, &envelope); err != nil {
		slog.Warn("decode event name failed", slog.Any("err", err))
		return "", fmt.Errorf("decode event name: %w", err)
	}
	return envelope.HookEventName, nil
}

func parseCursorPayload(eventName string, rawBytes []byte) (Event, error) {
	switch CursorEvent(eventName) {
	case CursorSessionStart:
		return decodeJSONPayload[CursorSessionStartPayload](rawBytes)
	case CursorSessionEnd:
		return decodeJSONPayload[CursorSessionEndPayload](rawBytes)
	case CursorPreToolUse:
		return decodeJSONPayload[CursorPreToolUsePayload](rawBytes)
	case CursorPostToolUse:
		return decodeJSONPayload[CursorPostToolUsePayload](rawBytes)
	case CursorPostToolUseFailure:
		return decodeJSONPayload[CursorPostToolUseFailurePayload](rawBytes)
	case CursorBeforeShellExecution:
		return decodeJSONPayload[CursorBeforeShellExecutionPayload](rawBytes)
	case CursorAfterShellExecution:
		return decodeJSONPayload[CursorAfterShellExecutionPayload](rawBytes)
	case CursorBeforeMCPExecution:
		return decodeJSONPayload[CursorBeforeMCPExecutionPayload](rawBytes)
	case CursorAfterMCPExecution:
		return decodeJSONPayload[CursorAfterMCPExecutionPayload](rawBytes)
	case CursorBeforeReadFile:
		return decodeJSONPayload[CursorBeforeReadFilePayload](rawBytes)
	case CursorBeforeTabFileRead:
		return decodeJSONPayload[CursorBeforeTabFileReadPayload](rawBytes)
	case CursorAfterFileEdit:
		return decodeJSONPayload[CursorAfterFileEditPayload](rawBytes)
	case CursorAfterTabFileEdit:
		return decodeJSONPayload[CursorAfterTabFileEditPayload](rawBytes)
	case CursorBeforeSubmitPrompt:
		return decodeJSONPayload[CursorBeforeSubmitPromptPayload](rawBytes)
	case CursorSubagentStart:
		return decodeJSONPayload[CursorSubagentStartPayload](rawBytes)
	case CursorSubagentStop:
		return decodeJSONPayload[CursorSubagentStopPayload](rawBytes)
	case CursorPreCompact:
		return decodeJSONPayload[CursorPreCompactPayload](rawBytes)
	case CursorStop:
		return decodeJSONPayload[CursorStopPayload](rawBytes)
	case CursorAfterAgentResponse:
		return decodeJSONPayload[CursorAfterAgentResponsePayload](rawBytes)
	case CursorAfterAgentThought:
		return decodeJSONPayload[CursorAfterAgentThoughtPayload](rawBytes)
	default:
		return decodeJSONPayload[UnknownPayload](rawBytes)
	}
}

// claudeDecoders maps each Claude event to its concrete payload decoder. A map
// dispatch keeps decoding flat: a single switch over 30-plus events exceeds the
// cyclomatic limit, and the set grows as Claude Code adds events, so a new event
// is one entry here plus its typed payload.
var claudeDecoders = map[ClaudeEvent]func([]byte) (Event, error){
	ClaudeSessionStart:        decodeJSONPayload[ClaudeSessionStartPayload],
	ClaudeSessionEnd:          decodeJSONPayload[ClaudeSessionEndPayload],
	ClaudeSetup:               decodeJSONPayload[ClaudeSetupPayload],
	ClaudePreToolUse:          decodeJSONPayload[ClaudePreToolUsePayload],
	ClaudePostToolUse:         decodeJSONPayload[ClaudePostToolUsePayload],
	ClaudePostToolUseFailure:  decodeJSONPayload[ClaudePostToolUseFailurePayload],
	ClaudePermissionRequest:   decodeJSONPayload[ClaudePermissionRequestPayload],
	ClaudePermissionDenied:    decodeJSONPayload[ClaudePermissionDeniedPayload],
	ClaudeUserPromptSubmit:    decodeJSONPayload[ClaudeUserPromptSubmitPayload],
	ClaudeStop:                decodeJSONPayload[ClaudeStopPayload],
	ClaudeStopFailure:         decodeJSONPayload[ClaudeStopFailurePayload],
	ClaudeSubagentStart:       decodeJSONPayload[ClaudeSubagentStartPayload],
	ClaudeSubagentStop:        decodeJSONPayload[ClaudeSubagentStopPayload],
	ClaudeTaskCreated:         decodeJSONPayload[ClaudeTaskCreatedPayload],
	ClaudeTaskCompleted:       decodeJSONPayload[ClaudeTaskCompletedPayload],
	ClaudeNotification:        decodeJSONPayload[ClaudeNotificationPayload],
	ClaudePreCompact:          decodeJSONPayload[ClaudePreCompactPayload],
	ClaudePostCompact:         decodeJSONPayload[ClaudePostCompactPayload],
	ClaudeInstructionsLoaded:  decodeJSONPayload[ClaudeInstructionsLoadedPayload],
	ClaudeConfigChange:        decodeJSONPayload[ClaudeConfigChangePayload],
	ClaudeCwdChanged:          decodeJSONPayload[ClaudeCwdChangedPayload],
	ClaudeFileChanged:         decodeJSONPayload[ClaudeFileChangedPayload],
	ClaudeWorktreeCreate:      decodeJSONPayload[ClaudeWorktreeCreatePayload],
	ClaudeWorktreeRemove:      decodeJSONPayload[ClaudeWorktreeRemovePayload],
	ClaudeElicitation:         decodeJSONPayload[ClaudeElicitationPayload],
	ClaudeElicitationResult:   decodeJSONPayload[ClaudeElicitationResultPayload],
	ClaudeTeammateIdle:        decodeJSONPayload[ClaudeTeammateIdlePayload],
	ClaudePostToolBatch:       decodeJSONPayload[ClaudePostToolBatchPayload],
	ClaudeUserPromptExpansion: decodeJSONPayload[ClaudeUserPromptExpansionPayload],
	ClaudeMessageDisplay:      decodeJSONPayload[ClaudeMessageDisplayPayload],
}

func parseClaudePayload(eventName string, rawBytes []byte) (Event, error) {
	if decode, ok := claudeDecoders[ClaudeEvent(eventName)]; ok {
		return decode(rawBytes)
	}
	return decodeJSONPayload[UnknownPayload](rawBytes)
}

func parseCodexPayload(eventName string, rawBytes []byte) (Event, error) {
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
	case CodexPreCompact:
		var payload CodexPreCompactPayload
		return payload, decodePayload(rawBytes, &payload)
	case CodexPostCompact:
		var payload CodexPostCompactPayload
		return payload, decodePayload(rawBytes, &payload)
	case CodexSubagentStart:
		var payload CodexSubagentStartPayload
		return payload, decodePayload(rawBytes, &payload)
	case CodexSubagentStop:
		var payload CodexSubagentStopPayload
		return payload, decodePayload(rawBytes, &payload)
	default:
		var payload UnknownPayload
		return payload, decodePayload(rawBytes, &payload)
	}
}

func parseGeminiPayload(eventName string, rawBytes []byte) (Event, error) {
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
