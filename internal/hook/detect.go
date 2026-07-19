// Package hook decodes hook payloads emitted by supported agent hosts
// (Claude, Cursor, Codex, Gemini, VS Code, Copilot) into a closed set of
// typed events that the rules engine can evaluate.
package hook

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"unicode"
	"unicode/utf8"
)

// DetectionPayload is the shallow envelope used to identify which agent
// host produced a hook payload before the full schema is resolved.
type DetectionPayload struct {
	HookEventName  string   `json:"hook_event_name"`
	CursorVersion  string   `json:"cursor_version"`
	ConversationID string   `json:"conversation_id"`
	GenerationID   string   `json:"generation_id"`
	WorkspaceRoots []string `json:"workspace_roots"`
	UserEmail      string   `json:"user_email"`
	TranscriptPath string   `json:"transcript_path"`
	PermissionMode string   `json:"permission_mode"`
	AgentID        string   `json:"agent_id"`
	AgentType      string   `json:"agent_type"`
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

// Detect determines which tool invoked agent-gate by running a priority
// chain of fingerprints over the environment, the payload, and an
// optional CLI subcommand hint. The first layer that returns a positive
// match wins.
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
	if hasCodexEnv(getenv) {
		return SystemCodex
	}
	if hasCopilotEnv(getenv) {
		return SystemCopilot
	}
	// Copilot's lower-camel event names overlap Cursor's event fingerprint.
	// The managed Copilot template supplies a provider hint, so honor it before
	// considering that ambiguous payload-only Cursor signal.
	if hint == SystemCopilot {
		return SystemCopilot
	}
	if hasCursorEnv(getenv) || hasCursorPayload(p) || hasCursorEvent(p) {
		return SystemCursor
	}
	if hasGeminiEnv(getenv) || hasGeminiEvent(p) {
		return SystemGemini
	}
	if hint != SystemUnknown {
		return hint
	}
	if hasClaudeEnv(getenv) {
		return SystemClaude
	}
	if hasClaudePayload(p) {
		return SystemClaude
	}
	if hasVSCodeEnv(getenv) {
		return SystemVSCode
	}
	return SystemUnknown
}

func hasCodexEnv(getenv func(string) string) bool {
	return getenv("CODEX_THREAD_ID") != "" || getenv("CODEX_CI") != ""
}

// hasCopilotEnv detects GitHub Copilot Chat by its OpenTelemetry env vars.
func hasCopilotEnv(getenv func(string) string) bool {
	return getenv("COPILOT_OTEL_FILE_EXPORTER_PATH") != "" ||
		getenv("COPILOT_OTEL_ENABLED") != "" ||
		getenv("COPILOT_OTEL_EXPORTER_TYPE") != ""
}

func hasCursorEnv(getenv func(string) string) bool {
	return getenv("CURSOR_VERSION") != "" ||
		getenv("CURSOR_WORKSPACE_NAME") != "" ||
		getenv("CURSOR_MODE") != ""
}

func hasCursorPayload(p DetectionPayload) bool {
	return p.CursorVersion != "" ||
		p.ConversationID != "" ||
		p.GenerationID != "" ||
		len(p.WorkspaceRoots) > 0 ||
		p.UserEmail != ""
}

func hasCursorEvent(p DetectionPayload) bool {
	name := p.HookEventName
	if name == "" {
		return false
	}
	r, _ := utf8.DecodeRuneInString(name)
	return unicode.IsLower(r)
}

func hasGeminiEnv(getenv func(string) string) bool {
	return getenv("GEMINI_CLI") != ""
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

func hasClaudeEnv(getenv func(string) string) bool {
	if getenv("CLAUDE_CODE_ENTRYPOINT") != "" {
		return true
	}
	if value := getenv("AI_AGENT"); strings.HasPrefix(value, "claude-code/") {
		return true
	}
	return false
}

func hasClaudePayload(p DetectionPayload) bool {
	return p.TranscriptPath != "" ||
		p.PermissionMode != "" ||
		p.AgentID != "" ||
		p.AgentType != ""
}

// hasVSCodeEnv detects a VS Code extension host invocation that is not
// Copilot Chat (Copilot is filtered out earlier in the chain).
func hasVSCodeEnv(getenv func(string) string) bool {
	return getenv("VSCODE_PID") != "" ||
		getenv("VSCODE_IPC_HOOK") != ""
}
