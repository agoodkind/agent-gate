// Package hook decodes hook payloads emitted by supported agent hosts
// (Claude, Cursor, Codex, Gemini, VS Code, Copilot) into a closed set of
// typed events that the rules engine can evaluate.
package hook

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"unicode"
	"unicode/utf8"
)

// DetectionPayload is the shallow envelope used to identify which agent
// host produced a hook payload before the full schema is resolved.
type DetectionPayload struct {
	HookEventName         string          `json:"hook_event_name"`
	CursorVersion         string          `json:"cursor_version"`
	ConversationID        string          `json:"conversation_id"`
	GenerationID          string          `json:"generation_id"`
	WorkspaceRoots        []string        `json:"workspace_roots"`
	UserEmail             string          `json:"user_email"`
	TranscriptPath        string          `json:"transcript_path"`
	PermissionMode        string          `json:"permission_mode"`
	AgentID               string          `json:"agent_id"`
	AgentType             string          `json:"agent_type"`
	CopilotSessionID      string          `json:"sessionId"`
	CopilotTranscriptPath string          `json:"transcriptPath"`
	CopilotToolName       string          `json:"toolName"`
	CopilotToolUseID      string          `json:"toolUseId"`
	CopilotToolInput      json.RawMessage `json:"toolInput"`
}

// ParseDetectionPayload decodes a [DetectionPayload] from raw JSON bytes.
func ParseDetectionPayload(rawBytes []byte) (DetectionPayload, error) {
	var payload DetectionPayload
	if err := json.Unmarshal(rawBytes, &payload); err != nil {
		slog.Warn("decode detection payload failed", slog.Any("err", err))
		return payload, fmt.Errorf("decode detection payload: %w", err)
	}
	return payload, nil
}

// Detect determines which tool invoked agent-gate from the available evidence.
func Detect(p DetectionPayload, hint System) System {
	return DetectWithEnv(p, hint, os.Getenv)
}

// DetectWithEnv is Detect with an explicit environment source. Hook
// enforcement runs in the daemon, so provider env fingerprints must come from
// the hook subprocess request rather than the daemon process environment.
func DetectWithEnv(p DetectionPayload, hint System, getenv func(string) string) System {
	if getenv == nil {
		getenv = os.Getenv
	}
	environment := make(map[string]string)
	for _, signal := range classificationEnvironmentSignals {
		if value := getenv(signal.name); value != "" {
			environment[signal.name] = value
		}
	}
	rawPayload, err := json.Marshal(p)
	if err != nil {
		slog.Warn("encode detection payload failed", slog.Any("err", err))
		return SystemUnknown
	}
	return Classify(rawPayload, hint, nil, environment).ResolvedSystem()
}

func hasCursorPayload(p DetectionPayload) bool {
	return p.CursorVersion != "" ||
		p.ConversationID != "" ||
		p.GenerationID != "" ||
		len(p.WorkspaceRoots) > 0 ||
		p.UserEmail != ""
}

func hasCopilotPayload(p DetectionPayload) bool {
	return p.CopilotSessionID != "" ||
		p.CopilotTranscriptPath != "" ||
		p.CopilotToolName != "" ||
		p.CopilotToolUseID != "" ||
		(len(p.CopilotToolInput) > 0 && string(p.CopilotToolInput) != "null")
}

func hasCursorEvent(p DetectionPayload) bool {
	name := p.HookEventName
	if name == "" {
		return false
	}
	r, _ := utf8.DecodeRuneInString(name)
	return unicode.IsLower(r)
}

func hasGeminiEvent(p DetectionPayload) bool {
	switch GeminiEvent(p.HookEventName) {
	case GeminiBeforeTool,
		GeminiAfterTool,
		GeminiBeforeAgent,
		GeminiAfterAgent,
		GeminiBeforeModel,
		GeminiAfterModel,
		GeminiBeforeToolSelection,
		GeminiPreCompress:
		return true
	case GeminiSessionStart,
		GeminiSessionEnd,
		GeminiNotification:
		return false
	}
	return false
}

func hasClaudePayload(p DetectionPayload) bool {
	return p.TranscriptPath != "" ||
		p.PermissionMode != "" ||
		p.AgentID != "" ||
		p.AgentType != ""
}
