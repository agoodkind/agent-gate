package hook

import (
	"encoding/json"
	"fmt"
	"log/slog"
)

// NormalizeCopilotPayload adds the event template hint and converts the
// camelCase fields emitted by Copilot CLI hooks into the daemon's canonical
// transport shape. The hook process only forwards the hint; all payload
// normalization remains daemon-owned.
func NormalizeCopilotPayload(raw []byte, eventHint string) ([]byte, error) {
	if eventHint == "" {
		return raw, nil
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(raw, &payload); err != nil {
		slog.Warn("decode Copilot payload failed", "err", err)
		return nil, fmt.Errorf("decode Copilot payload: %w", err)
	}
	if _, ok := payload["hook_event_name"]; !ok {
		event, err := json.Marshal(eventHint)
		if err != nil {
			slog.Warn("encode Copilot event hint failed", "err", err)
			return nil, fmt.Errorf("encode Copilot event hint: %w", err)
		}
		payload["hook_event_name"] = event
	}
	copyCopilotField(payload, "sessionId", "session_id")
	copyCopilotField(payload, "transcriptPath", "transcript_path")
	copyCopilotField(payload, "toolName", "tool_name")
	copyCopilotField(payload, "toolUseId", "tool_use_id")
	copyCopilotField(payload, "toolInput", "tool_input")
	copyCopilotField(payload, "toolArgs", "tool_input")
	copyCopilotField(payload, "toolOutput", "tool_output")
	copyCopilotField(payload, "toolResponse", "tool_response")
	copyCopilotField(payload, "toolResult", "tool_response")
	copyCopilotField(payload, "assistantMessage", "assistant_message")
	copyCopilotField(payload, "lastAssistantMessage", "last_assistant_message")
	normalized, err := json.Marshal(payload)
	if err != nil {
		slog.Warn("encode normalized Copilot payload failed", "err", err)
		return nil, fmt.Errorf("encode normalized Copilot payload: %w", err)
	}
	return normalized, nil
}

func copyCopilotField(payload map[string]json.RawMessage, source string, target string) {
	if _, exists := payload[target]; exists {
		return
	}
	if value, ok := payload[source]; ok {
		payload[target] = value
	}
}
