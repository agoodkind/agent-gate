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
		extra = claudeLogAttrs(raw)
	case SystemCursor:
		extra = cursorLogAttrs(raw)
	}

	intakeAttrs := append(base, extra...)
	logger.Info("hook.received", intakeAttrs...)

	// Determine which rules are evaluated for this event (for audit completeness).
	systemStr := system.String()
	checked := rules.CheckedRuleNames(systemStr, eventName, cfg.Rules)

	// Evaluate rules against the raw payload map.
	violation := rules.Evaluate(systemStr, eventName, map[string]any(raw), cfg.Rules)

	decisionAttrs := append(intakeAttrs,
		slog.Any("rules_checked", checked),
	)

	if violation != nil {
		decision := "block"
		if violation.AuditOnly {
			decision = "audit_only"
		}

		logger.Info("hook."+decision,
			append(decisionAttrs,
				slog.String("decision", decision),
				slog.String("blocking_rule", violation.RuleName),
				slog.String("violation_message", violation.Message),
			)...,
		)

		if !violation.AuditOnly {
			switch system {
			case SystemCursor:
				// Observational events cannot block, so violations there are
				// audit only. No follow up prompt is injected.
				if isObservationalCursorEvent(eventName) {
					return CursorAllow(), nil, 0
				}
				return CursorBlock(violation.RuleName, violation.Message), nil, 0
			default:
				return ClaudeAllow(), ClaudeBlock(violation.RuleName, violation.Message), 2
			}
		}
		// audit_only: log was written above, fall through to allow.
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

// claudeLogAttrs extracts slog attributes from a Claude payload.
// Logs every field from the payload for full audit trail.
func claudeLogAttrs(raw RawPayload) []slog.Attr {
	attrs := []slog.Attr{
		slog.String("tool_name", strField(raw, "tool_name")),
		slog.String("tool_use_id", strField(raw, "tool_use_id")),
		slog.String("source", strField(raw, "source")),
		slog.String("file_path", strField(raw, "file_path")),
		slog.String("model", strField(raw, "model")),
		slog.String("permission_mode", strField(raw, "permission_mode")),
		slog.String("agent_id", strField(raw, "agent_id")),
		slog.String("agent_type", strField(raw, "agent_type")),
	}

	// Tool input: log command, file_path, description, content snippet, old/new string snippets.
	if ti, ok := raw["tool_input"].(map[string]any); ok {
		attrs = append(attrs,
			slog.String("ti_command", strFromMap(ti, "command")),
			slog.String("ti_file_path", strFromMap(ti, "file_path")),
			slog.String("ti_description", truncate(strFromMap(ti, "description"), 200)),
			slog.String("ti_content_snippet", truncate(strFromMap(ti, "content"), 200)),
			slog.String("ti_old_string_snippet", truncate(strFromMap(ti, "old_string"), 200)),
			slog.String("ti_new_string_snippet", truncate(strFromMap(ti, "new_string"), 200)),
			slog.String("ti_pattern", strFromMap(ti, "pattern")),
			slog.String("ti_url", strFromMap(ti, "url")),
			slog.String("ti_query", strFromMap(ti, "query")),
			slog.String("ti_prompt_snippet", truncate(strFromMap(ti, "prompt"), 200)),
		)
	}

	// Event-specific fields.
	if v := strField(raw, "prompt"); v != "" {
		attrs = append(attrs, slog.String("prompt_snippet", truncate(v, 200)))
	}
	if v := strField(raw, "message"); v != "" {
		attrs = append(attrs, slog.String("message", truncate(v, 200)))
	}
	if v := strField(raw, "error"); v != "" {
		attrs = append(attrs, slog.String("error", v))
	}
	if v := strField(raw, "error_type"); v != "" {
		attrs = append(attrs, slog.String("error_type", v))
	}
	if v := strField(raw, "reason"); v != "" {
		attrs = append(attrs, slog.String("reason", v))
	}
	if v := strField(raw, "change_type"); v != "" {
		attrs = append(attrs, slog.String("change_type", v))
	}
	if v := strField(raw, "trigger"); v != "" {
		attrs = append(attrs, slog.String("trigger", v))
	}
	if v := strField(raw, "memory_type"); v != "" {
		attrs = append(attrs, slog.String("memory_type", v))
	}
	if v := strField(raw, "load_reason"); v != "" {
		attrs = append(attrs, slog.String("load_reason", v))
	}
	if v := strField(raw, "notification_type"); v != "" {
		attrs = append(attrs, slog.String("notification_type", v))
	}
	if v := strField(raw, "last_assistant_message"); v != "" {
		attrs = append(attrs, slog.String("last_assistant_message_snippet", truncate(v, 200)))
	}
	if v := strField(raw, "task_id"); v != "" {
		attrs = append(attrs, slog.String("task_id", v))
	}
	if v := strField(raw, "task_subject"); v != "" {
		attrs = append(attrs, slog.String("task_subject", v))
	}
	if v := strField(raw, "new_cwd"); v != "" {
		attrs = append(attrs, slog.String("new_cwd", v))
	}
	if v := strField(raw, "previous_cwd"); v != "" {
		attrs = append(attrs, slog.String("previous_cwd", v))
	}
	if v := strField(raw, "mcp_server_name"); v != "" {
		attrs = append(attrs, slog.String("mcp_server_name", v))
	}

	return attrs
}

// cursorLogAttrs extracts slog attributes from a Cursor payload.
// Logs every field from the payload for full audit trail.
func cursorLogAttrs(raw RawPayload) []slog.Attr {
	attrs := []slog.Attr{
		slog.String("conversation_id", strField(raw, "conversation_id")),
		slog.String("generation_id", strField(raw, "generation_id")),
		slog.String("tool_name", strField(raw, "tool_name")),
		slog.String("file_path", strField(raw, "file_path")),
		slog.String("command", strField(raw, "command")),
	}

	// Tool input for MCP/tool calls.
	if ti, ok := raw["tool_input"].(map[string]any); ok {
		attrs = append(attrs,
			slog.String("ti_command", strFromMap(ti, "command")),
			slog.String("ti_file_path", strFromMap(ti, "file_path")),
			slog.String("ti_content_snippet", truncate(strFromMap(ti, "content"), 200)),
			slog.String("ti_old_string_snippet", truncate(strFromMap(ti, "old_string"), 200)),
			slog.String("ti_new_string_snippet", truncate(strFromMap(ti, "new_string"), 200)),
		)
	}

	// Agent output fields.
	if v := strField(raw, "text"); v != "" {
		attrs = append(attrs, slog.String("text_snippet", truncate(v, 200)))
	}
	if v := strField(raw, "assistant_message"); v != "" {
		attrs = append(attrs, slog.String("assistant_message_snippet", truncate(v, 200)))
	}
	if v := strField(raw, "last_assistant_message"); v != "" {
		attrs = append(attrs, slog.String("last_assistant_message_snippet", truncate(v, 200)))
	}
	if v := strField(raw, "prompt"); v != "" {
		attrs = append(attrs, slog.String("prompt_snippet", truncate(v, 200)))
	}

	return attrs
}

// strField extracts a string from a RawPayload, returning "" if missing.
func strField(p RawPayload, key string) string {
	v, _ := p[key].(string)
	return v
}

// strFromMap extracts a string from a nested map, returning "" if missing.
func strFromMap(m map[string]any, key string) string {
	v, _ := m[key].(string)
	return v
}

// truncate returns s shortened to at most n runes, appending "…" if truncated.
func truncate(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n]) + "\u2026"
}
