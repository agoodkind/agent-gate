package hook

import (
	"log/slog"

	"goodkind.io/agent-gate/internal/audit"
	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/rules"
)

// Handle is the central orchestration function.
//
// It receives a decoded RawPayload, detects which system sent it, logs the
// intake, evaluates all configured rules, logs the decision, and returns:
//
//   - stdout: bytes to write to os.Stdout
//   - stderr: bytes to write to os.Stderr
//   - exitCode: process exit code (0 = allow, 2 = block for Claude)
//
// Callers are responsible for writing the returned bytes and calling os.Exit.
func Handle(raw RawPayload, cfg *config.Config, logger *audit.Logger) (stdout, stderr []byte, exitCode int) {
	system := Detect(raw)
	eventName := raw.EventName()

	// Build the base attributes shared by all log entries for this invocation.
	base := []slog.Attr{
		slog.String("system", system.String()),
		slog.String("event", eventName),
		slog.String("session_id", raw.SessionID()),
		slog.String("cwd", raw.CWD()),
	}

	// Add system-specific payload fields to the log.
	var extra []slog.Attr
	switch system {
	case SystemClaude:
		extra = claudeLogAttrs(ParseClaude(raw))
	case SystemCursor:
		extra = cursorLogAttrs(ParseCursor(raw))
	}

	intakeAttrs := append(base, extra...)
	logger.Info("hook.received", intakeAttrs...)

	// Determine which rules are evaluated for this event (for audit completeness).
	checked := rules.CheckedRuleNames(eventName, cfg.Rules)

	// Evaluate rules against the raw payload map.
	violation := rules.Evaluate(eventName, map[string]any(raw), cfg.Rules)

	decisionAttrs := append(intakeAttrs,
		slog.Any("rules_checked", checked),
	)

	if violation != nil {
		logger.Info("hook.blocked",
			append(decisionAttrs,
				slog.String("decision", "block"),
				slog.String("blocking_rule", violation.RuleName),
				slog.String("violation_message", violation.Message),
			)...,
		)

		switch system {
		case SystemCursor:
			if isObservationalCursorEvent(eventName) {
				// Observational events (afterAgentResponse, etc.) cannot block.
				// Persist the violation so the next stop hook can send a followup_message.
				_ = writeFollowup(raw.SessionID(), violation.RuleName, violation.Message)
				return CursorAllow(), nil, 0
			}
			// Cursor expects JSON on stdout; exit 0.
			return CursorBlock(violation.RuleName, violation.Message), nil, 0
		default:
			// Claude (and unknown): write allow JSON to stdout, block message to stderr, exit 2.
			return ClaudeAllow(), ClaudeBlock(violation.RuleName, violation.Message), 2
		}
	}

	// No rule violation. For Cursor stop events, check for a pending followup
	// from a prior observational hook (e.g. afterAgentResponse detected emdashes).
	if system == SystemCursor && eventName == string(CursorStop) {
		if ruleName, message := consumeFollowup(raw.SessionID()); ruleName != "" {
			logger.Info("hook.followup",
				append(decisionAttrs,
					slog.String("decision", "followup"),
					slog.String("blocking_rule", ruleName),
					slog.String("violation_message", message),
				)...,
			)
			return CursorFollowup(ruleName, message), nil, 0
		}
	}

	logger.Info("hook.allowed",
		append(decisionAttrs,
			slog.String("decision", "allow"),
			slog.String("blocking_rule", ""),
			slog.String("violation_message", ""),
		)...,
	)

	switch system {
	case SystemCursor:
		return CursorAllow(), nil, 0
	default:
		return ClaudeAllow(), nil, 0
	}
}

// claudeLogAttrs extracts slog attributes from a parsed Claude payload.
func claudeLogAttrs(cp ClaudePayload) []slog.Attr {
	attrs := []slog.Attr{
		slog.String("tool_name", cp.ToolName),
		slog.String("source", cp.Source),
		slog.String("file_path", cp.FilePath),
	}
	if cmd, ok := cp.ToolInput["command"].(string); ok {
		attrs = append(attrs, slog.String("command", cmd))
	}
	if cp.Prompt != "" {
		attrs = append(attrs, slog.String("prompt_snippet", truncate(cp.Prompt, 120)))
	}
	return attrs
}

// cursorLogAttrs extracts slog attributes from a parsed Cursor payload.
func cursorLogAttrs(cur CursorPayload) []slog.Attr {
	return []slog.Attr{
		slog.String("conversation_id", cur.ConversationID),
		slog.String("generation_id", cur.GenerationID),
		slog.String("command", cur.Command),
		slog.String("tool_name", cur.ToolName),
		slog.String("file_path", cur.FilePath),
	}
}

// truncate returns s shortened to at most n runes, appending "…" if truncated.
func truncate(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n]) + "\u2026"
}
