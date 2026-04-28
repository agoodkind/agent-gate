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
//  2. Cursor env, payload markers, or camel-case event name.
//  3. Gemini env, payload markers, or known event name.
//  4. Claude env (CLAUDE_CODE_ENTRYPOINT, AI_AGENT=claude-code/...).
//  5. Claude payload markers (transcript_path, permission_mode, agent_id,
//     agent_type).
//  6. VS Code env (TERM_PROGRAM=vscode) when nothing else matched.
//  7. CLI subcommand hint (codex-hook, gemini-hook). Last because the
//     argument travels with copied configs and proves nothing about the
//     real caller.
//  8. SystemUnknown.
//
// CLAUDECODE=1 is intentionally not a primary claude signal because it
// is inherited by any subprocess of a claude shell. CLAUDE_CODE_ENTRYPOINT
// is set fresh by claude itself on each invocation, so it is robust.
func Detect(p RawPayload, hint HookSystem) HookSystem {
	if hasCodexEnv() {
		return SystemCodex
	}
	if hasCursorEnv() || hasCursorPayload(p) || hasCursorEvent(p) {
		return SystemCursor
	}
	if hasGeminiEnv() || hasGeminiPayload(p) || hasGeminiEvent(p) {
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

func hasGeminiPayload(p RawPayload) bool {
	if _, ok := p["mcp_context"]; ok {
		return true
	}
	if ts, ok := p["timestamp"].(string); ok && ts != "" {
		return true
	}
	return false
}

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

func hasVSCodeEnv() bool {
	return os.Getenv("TERM_PROGRAM") == "vscode"
}
