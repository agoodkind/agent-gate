package hook

import (
	"encoding/json"
	"strings"

	"goodkind.io/agent-gate/internal/rules"
)

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

func (p VSCodePayload) EventName() string { return string(p.HookEvent) }
func (p VSCodePayload) SessionID() string { return p.Session }
func (p VSCodePayload) CWD() string       { return p.Cwd }
func (p VSCodePayload) Fields() rules.FieldSet {
	fields := rules.FieldSet{
		HookEventName:        string(p.HookEvent),
		SessionID:            p.Session,
		TranscriptPath:       p.TranscriptPath,
		CWD:                  p.Cwd,
		ToolName:             p.ToolName,
		ToolUseID:            p.ToolUseID,
		ToolInputFilePath:    p.ToolInput.FilePath,
		ToolInputCommand:     p.ToolInput.Command,
		ToolInputContent:     p.ToolInput.Content,
		ToolInputPrompt:      p.ToolInput.Prompt,
		LastAssistantMessage: p.LastAssistant,
		Text:                 p.Text,
	}
	fields.ToolInputOldString = strings.Join(p.ToolInput.NormalizedOldStrings(), "\n")
	fields.ToolInputNewString = strings.Join(p.ToolInput.NormalizedNewStrings(), "\n")
	fields.EditsOldString = p.ToolInput.NormalizedOldStrings()
	fields.EditsNewString = p.ToolInput.NormalizedNewStrings()
	return fields
}

type CopilotPayload struct {
	VSCodePayload
	AssistantMessage string `json:"assistant_message"`
}

func (p CopilotPayload) Fields() rules.FieldSet {
	fields := p.VSCodePayload.Fields()
	fields.AssistantMessage = p.AssistantMessage
	if fields.LastAssistantMessage == "" {
		fields.LastAssistantMessage = p.LastAssistant
	}
	return fields
}

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
		err := json.Unmarshal(rawBytes, &event)
		return HookPayload{System: system, Event: event}, err
	case SystemCopilot:
		var event CopilotPayload
		err := json.Unmarshal(rawBytes, &event)
		return HookPayload{System: system, Event: enrichCopilotPayload(event)}, err
	default:
		var event UnknownPayload
		err := json.Unmarshal(rawBytes, &event)
		return HookPayload{System: system, Event: event}, err
	}
}

func eventNameFromBytes(rawBytes []byte) (string, error) {
	var envelope struct {
		HookEventName string `json:"hook_event_name"`
	}
	if err := json.Unmarshal(rawBytes, &envelope); err != nil {
		return "", err
	}
	return envelope.HookEventName, nil
}

func parseCursorPayload(eventName string, rawBytes []byte) (HookEvent, error) {
	switch CursorEvent(eventName) {
	case CursorSessionStart:
		var payload CursorSessionStartPayload
		return payload, json.Unmarshal(rawBytes, &payload)
	case CursorSessionEnd:
		var payload CursorSessionEndPayload
		return payload, json.Unmarshal(rawBytes, &payload)
	case CursorPreToolUse:
		var payload CursorPreToolUsePayload
		return payload, json.Unmarshal(rawBytes, &payload)
	case CursorPostToolUse:
		var payload CursorPostToolUsePayload
		return payload, json.Unmarshal(rawBytes, &payload)
	case CursorPostToolUseFailure:
		var payload CursorPostToolUseFailurePayload
		return payload, json.Unmarshal(rawBytes, &payload)
	case CursorBeforeShellExecution:
		var payload CursorBeforeShellExecutionPayload
		return payload, json.Unmarshal(rawBytes, &payload)
	case CursorAfterShellExecution:
		var payload CursorAfterShellExecutionPayload
		return payload, json.Unmarshal(rawBytes, &payload)
	case CursorBeforeMCPExecution:
		var payload CursorBeforeMCPExecutionPayload
		return payload, json.Unmarshal(rawBytes, &payload)
	case CursorAfterMCPExecution:
		var payload CursorAfterMCPExecutionPayload
		return payload, json.Unmarshal(rawBytes, &payload)
	case CursorBeforeReadFile:
		var payload CursorBeforeReadFilePayload
		return payload, json.Unmarshal(rawBytes, &payload)
	case CursorBeforeTabFileRead:
		var payload CursorBeforeTabFileReadPayload
		return payload, json.Unmarshal(rawBytes, &payload)
	case CursorAfterFileEdit:
		var payload CursorAfterFileEditPayload
		return payload, json.Unmarshal(rawBytes, &payload)
	case CursorAfterTabFileEdit:
		var payload CursorAfterTabFileEditPayload
		return payload, json.Unmarshal(rawBytes, &payload)
	case CursorBeforeSubmitPrompt:
		var payload CursorBeforeSubmitPromptPayload
		return payload, json.Unmarshal(rawBytes, &payload)
	case CursorSubagentStart:
		var payload CursorSubagentStartPayload
		return payload, json.Unmarshal(rawBytes, &payload)
	case CursorSubagentStop:
		var payload CursorSubagentStopPayload
		return payload, json.Unmarshal(rawBytes, &payload)
	case CursorPreCompact:
		var payload CursorPreCompactPayload
		return payload, json.Unmarshal(rawBytes, &payload)
	case CursorStop:
		var payload CursorStopPayload
		return payload, json.Unmarshal(rawBytes, &payload)
	case CursorAfterAgentResponse:
		var payload CursorAfterAgentResponsePayload
		return payload, json.Unmarshal(rawBytes, &payload)
	case CursorAfterAgentThought:
		var payload CursorAfterAgentThoughtPayload
		return payload, json.Unmarshal(rawBytes, &payload)
	default:
		var payload UnknownPayload
		return payload, json.Unmarshal(rawBytes, &payload)
	}
}

func parseClaudePayload(eventName string, rawBytes []byte) (HookEvent, error) {
	switch ClaudeEvent(eventName) {
	case ClaudeSessionStart:
		var payload ClaudeSessionStartPayload
		return payload, json.Unmarshal(rawBytes, &payload)
	case ClaudeSessionEnd:
		var payload ClaudeSessionEndPayload
		return payload, json.Unmarshal(rawBytes, &payload)
	case ClaudeSetup:
		var payload ClaudeSetupPayload
		return payload, json.Unmarshal(rawBytes, &payload)
	case ClaudePreToolUse:
		var payload ClaudePreToolUsePayload
		return payload, json.Unmarshal(rawBytes, &payload)
	case ClaudePostToolUse:
		var payload ClaudePostToolUsePayload
		return payload, json.Unmarshal(rawBytes, &payload)
	case ClaudePostToolUseFailure:
		var payload ClaudePostToolUseFailurePayload
		return payload, json.Unmarshal(rawBytes, &payload)
	case ClaudePermissionRequest:
		var payload ClaudePermissionRequestPayload
		return payload, json.Unmarshal(rawBytes, &payload)
	case ClaudePermissionDenied:
		var payload ClaudePermissionDeniedPayload
		return payload, json.Unmarshal(rawBytes, &payload)
	case ClaudeUserPromptSubmit:
		var payload ClaudeUserPromptSubmitPayload
		return payload, json.Unmarshal(rawBytes, &payload)
	case ClaudeStop:
		var payload ClaudeStopPayload
		return payload, json.Unmarshal(rawBytes, &payload)
	case ClaudeStopFailure:
		var payload ClaudeStopFailurePayload
		return payload, json.Unmarshal(rawBytes, &payload)
	case ClaudeSubagentStart:
		var payload ClaudeSubagentStartPayload
		return payload, json.Unmarshal(rawBytes, &payload)
	case ClaudeSubagentStop:
		var payload ClaudeSubagentStopPayload
		return payload, json.Unmarshal(rawBytes, &payload)
	case ClaudeTaskCreated:
		var payload ClaudeTaskCreatedPayload
		return payload, json.Unmarshal(rawBytes, &payload)
	case ClaudeTaskCompleted:
		var payload ClaudeTaskCompletedPayload
		return payload, json.Unmarshal(rawBytes, &payload)
	case ClaudeNotification:
		var payload ClaudeNotificationPayload
		return payload, json.Unmarshal(rawBytes, &payload)
	case ClaudePreCompact:
		var payload ClaudePreCompactPayload
		return payload, json.Unmarshal(rawBytes, &payload)
	case ClaudePostCompact:
		var payload ClaudePostCompactPayload
		return payload, json.Unmarshal(rawBytes, &payload)
	case ClaudeInstructionsLoaded:
		var payload ClaudeInstructionsLoadedPayload
		return payload, json.Unmarshal(rawBytes, &payload)
	case ClaudeConfigChange:
		var payload ClaudeConfigChangePayload
		return payload, json.Unmarshal(rawBytes, &payload)
	case ClaudeCwdChanged:
		var payload ClaudeCwdChangedPayload
		return payload, json.Unmarshal(rawBytes, &payload)
	case ClaudeFileChanged:
		var payload ClaudeFileChangedPayload
		return payload, json.Unmarshal(rawBytes, &payload)
	case ClaudeWorktreeCreate:
		var payload ClaudeWorktreeCreatePayload
		return payload, json.Unmarshal(rawBytes, &payload)
	case ClaudeWorktreeRemove:
		var payload ClaudeWorktreeRemovePayload
		return payload, json.Unmarshal(rawBytes, &payload)
	case ClaudeElicitation:
		var payload ClaudeElicitationPayload
		return payload, json.Unmarshal(rawBytes, &payload)
	case ClaudeElicitationResult:
		var payload ClaudeElicitationResultPayload
		return payload, json.Unmarshal(rawBytes, &payload)
	case ClaudeTeammateIdle:
		var payload ClaudeTeammateIdlePayload
		return payload, json.Unmarshal(rawBytes, &payload)
	default:
		var payload UnknownPayload
		return payload, json.Unmarshal(rawBytes, &payload)
	}
}

func parseCodexPayload(eventName string, rawBytes []byte) (HookEvent, error) {
	switch CodexEvent(eventName) {
	case CodexSessionStart:
		var payload CodexSessionStartPayload
		return payload, json.Unmarshal(rawBytes, &payload)
	case CodexPreToolUse:
		var payload CodexPreToolUsePayload
		return payload, json.Unmarshal(rawBytes, &payload)
	case CodexPermissionRequest:
		var payload CodexPermissionRequestPayload
		return payload, json.Unmarshal(rawBytes, &payload)
	case CodexPostToolUse:
		var payload CodexPostToolUsePayload
		return payload, json.Unmarshal(rawBytes, &payload)
	case CodexUserPromptSubmit:
		var payload CodexUserPromptSubmitPayload
		return payload, json.Unmarshal(rawBytes, &payload)
	case CodexStop:
		var payload CodexStopPayload
		return payload, json.Unmarshal(rawBytes, &payload)
	default:
		var payload UnknownPayload
		return payload, json.Unmarshal(rawBytes, &payload)
	}
}

func parseGeminiPayload(eventName string, rawBytes []byte) (HookEvent, error) {
	switch GeminiEvent(eventName) {
	case GeminiBeforeTool:
		var payload GeminiBeforeToolPayload
		return payload, json.Unmarshal(rawBytes, &payload)
	case GeminiAfterTool:
		var payload GeminiAfterToolPayload
		return payload, json.Unmarshal(rawBytes, &payload)
	case GeminiBeforeAgent:
		var payload GeminiBeforeAgentPayload
		return payload, json.Unmarshal(rawBytes, &payload)
	case GeminiAfterAgent:
		var payload GeminiAfterAgentPayload
		return payload, json.Unmarshal(rawBytes, &payload)
	case GeminiBeforeModel:
		var payload GeminiBeforeModelPayload
		return payload, json.Unmarshal(rawBytes, &payload)
	case GeminiBeforeToolSelection:
		var payload GeminiBeforeToolSelectionPayload
		return payload, json.Unmarshal(rawBytes, &payload)
	case GeminiAfterModel:
		var payload GeminiAfterModelPayload
		return payload, json.Unmarshal(rawBytes, &payload)
	case GeminiSessionStart:
		var payload GeminiSessionStartPayload
		return payload, json.Unmarshal(rawBytes, &payload)
	case GeminiSessionEnd:
		var payload GeminiSessionEndPayload
		return payload, json.Unmarshal(rawBytes, &payload)
	case GeminiNotification:
		var payload GeminiNotificationPayload
		return payload, json.Unmarshal(rawBytes, &payload)
	case GeminiPreCompress:
		var payload GeminiPreCompressPayload
		return payload, json.Unmarshal(rawBytes, &payload)
	default:
		var payload UnknownPayload
		return payload, json.Unmarshal(rawBytes, &payload)
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
