package hook

import (
	"os"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Detect determines which tool invoked agent-gate by running a priority
// chain of fingerprints over the environment, the payload, and an
// optional CLI subcommand hint. The first layer that returns a positive
// match wins.
//
// Priority order:
//
//  1. Codex env (CODEX_THREAD_ID or CODEX_CI). Direct invoker wins even
//     when claude env is also inherited.
//  2. Copilot env (COPILOT_OTEL_*). Copilot Chat shares Claude's payload
//     shape including transcript_path, so its check must run before any
//     Claude marker test.
//  3. Cursor env, payload markers, or camel-case event name.
//  4. Gemini env or Gemini-specific event name. The payload's timestamp
//     field is intentionally NOT a Gemini signal because Copilot uses it
//     too.
//  5. Claude env (CLAUDE_CODE_ENTRYPOINT, AI_AGENT=claude-code/...).
//  6. Claude payload markers (transcript_path, permission_mode, agent_id,
//     agent_type).
//  7. VS Code env (VSCODE_PID) when nothing else matched. Catches generic
//     VS Code extensions that are not Copilot.
//  8. CLI subcommand hint (codex-hook, gemini-hook). Last because the
//     argument travels with copied configs and proves nothing about the
//     real caller.
//  9. SystemUnknown.
//
// CLAUDECODE=1 is intentionally not a primary claude signal because it
// is inherited by any subprocess of a claude shell. CLAUDE_CODE_ENTRYPOINT
// is set fresh by claude itself on each invocation, so it is robust.
func Detect(p RawPayload, hint HookSystem) HookSystem {
	if hasCodexEnv() {
		return SystemCodex
	}
	if hasCopilotEnv() {
		return SystemCopilot
	}
	if hasCursorEnv() || hasCursorPayload(p) || hasCursorEvent(p) {
		return SystemCursor
	}
	if hasGeminiEnv() || hasGeminiEvent(p) {
		return SystemGemini
	}
	if hasClaudeEnv() {
		return SystemClaude
	}
	if hasClaudePayload(p) {
		return SystemClaude
	}
	if hasVSCodeEnv() {
		return SystemVSCode
	}
	if hint != SystemUnknown {
		return hint
	}
	return SystemUnknown
}

func hasCodexEnv() bool {
	return os.Getenv("CODEX_THREAD_ID") != "" || os.Getenv("CODEX_CI") != ""
}

// hasCopilotEnv detects GitHub Copilot Chat by its OpenTelemetry env vars.
// Empirically every Copilot hook fire ships COPILOT_OTEL_FILE_EXPORTER_PATH,
// COPILOT_OTEL_ENABLED, and COPILOT_OTEL_EXPORTER_TYPE. Any one is enough.
func hasCopilotEnv() bool {
	return os.Getenv("COPILOT_OTEL_FILE_EXPORTER_PATH") != "" ||
		os.Getenv("COPILOT_OTEL_ENABLED") != "" ||
		os.Getenv("COPILOT_OTEL_EXPORTER_TYPE") != ""
}

func hasCursorEnv() bool {
	return os.Getenv("CURSOR_VERSION") != "" ||
		os.Getenv("CURSOR_WORKSPACE_NAME") != "" ||
		os.Getenv("CURSOR_MODE") != ""
}

func hasCursorPayload(p RawPayload) bool {
	for _, key := range []string{
		"cursor_version",
		"conversation_id",
		"generation_id",
		"workspace_roots",
		"user_email",
	} {
		if _, ok := p[key]; ok {
			return true
		}
	}
	return false
}

func hasCursorEvent(p RawPayload) bool {
	name := p.EventName()
	if name == "" {
		return false
	}
	r, _ := utf8.DecodeRuneInString(name)
	return unicode.IsLower(r)
}

func hasGeminiEnv() bool {
	return os.Getenv("GEMINI_CLI") != ""
}

// hasGeminiEvent matches the event names unique to the Gemini CLI hook
// protocol. It does NOT match generic Claude-shaped event names like
// PreToolUse because those are also fired by Claude, Codex, and Copilot.
func hasGeminiEvent(p RawPayload) bool {
	switch p.EventName() {
	case "BeforeTool",
		"AfterTool",
		"BeforeAgent",
		"AfterAgent",
		"BeforeModel",
		"AfterModel",
		"BeforeToolSelection",
		"PreCompress":
		return true
	}
	return false
}

func hasClaudeEnv() bool {
	if os.Getenv("CLAUDE_CODE_ENTRYPOINT") != "" {
		return true
	}
	if v := os.Getenv("AI_AGENT"); strings.HasPrefix(v, "claude-code/") {
		return true
	}
	return false
}

func hasClaudePayload(p RawPayload) bool {
	for _, key := range []string{
		"transcript_path",
		"permission_mode",
		"agent_id",
		"agent_type",
	} {
		if _, ok := p[key]; ok {
			return true
		}
	}
	return false
}

// hasVSCodeEnv detects a VS Code extension host invocation that is not
// Copilot Chat (Copilot is filtered out earlier in the chain). VSCODE_PID
// is set whenever an extension or the VS Code main process spawns a
// subprocess. TERM_PROGRAM=vscode is intentionally not used because hooks
// fired from the extension host (the relevant case) inherit no terminal
// environment.
func hasVSCodeEnv() bool {
	return os.Getenv("VSCODE_PID") != "" ||
		os.Getenv("VSCODE_IPC_HOOK") != ""
}
