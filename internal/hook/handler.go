package hook

import (
	"context"
	"log/slog"

	"goodkind.io/agent-gate/internal/audit"
	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/rules"
)

// Handle is the central orchestration function. It always audits the full
// payload, then conditionally enforces rules only on events that can block.
//
//   - stdout: bytes to write to os.Stdout
//   - stderr: bytes to write to os.Stderr
//   - exitCode: process exit code (0 = allow, 2 = block for Claude)
func Handle(ctx context.Context, raw RawPayload, rawBytes []byte, cfg *config.Config, loggers *audit.Loggers) (stdout, stderr []byte, exitCode int) {
	system := Detect(raw)
	eventName := raw.EventName()
	logger := loggers.For(system.String())

	// Step 1: Audit always runs, unconditionally. Logs the full structured
	// payload plus the raw JSON bytes at debug level for field discovery.
	auditReceived(ctx, raw, rawBytes, system, eventName, logger)

	// Step 2: Enforce evaluates rules on all events; only block on CanBlock events.
	return enforce(ctx, raw, system, eventName, cfg, logger)
}

// CanBlock returns true when exit code 2 or permission:"deny" actually
// prevents the action for the given system and event.
func CanBlock(system HookSystem, eventName string) bool {
	switch system {
	case SystemClaude:
		return CanBlockClaude(eventName)
	case SystemCursor:
		return CanBlockCursor(eventName)
	default:
		return false
	}
}

// auditReceived logs hook.received for every invocation, plus a debug-level
// hook.raw_payload entry that contains the full unparsed JSON. Useful for
// discovering undocumented fields from new Cursor/Claude versions.
func auditReceived(ctx context.Context, raw RawPayload, rawBytes []byte, system HookSystem, eventName string, logger *audit.Logger) {
	base := []slog.Attr{
		slog.String("system", system.String()),
		slog.String("event", eventName),
		slog.String("session_id", raw.SessionID()),
		slog.String("cwd", raw.CWD()),
	}

	var extra []slog.Attr
	switch system {
	case SystemClaude:
		extra = claudeLogAttrs(raw)
	case SystemCursor:
		extra = cursorLogAttrs(raw)
	}

	logger.InfoContext(ctx, "hook.received", append(base, extra...)...)

	// Raw payload at debug, logged when config log.level = "debug".
	// This gives the exact bytes Cursor/Claude sent, enabling field discovery
	// without needing to instrument the calling tool.
	logger.DebugContext(ctx, "hook.raw_payload",
		slog.String("system", system.String()),
		slog.String("event", eventName),
		slog.String("session_id", raw.SessionID()),
		slog.String("raw_payload", string(rawBytes)),
	)
}

// enforce evaluates rules and returns the appropriate response. Only called
// for events where CanBlock is true.
func enforce(ctx context.Context, raw RawPayload, system HookSystem, eventName string, cfg *config.Config, logger *audit.Logger) (stdout, stderr []byte, exitCode int) {
	systemStr := system.String()
	checked := rules.CheckedRuleNames(systemStr, eventName, cfg.Rules)
	violation := rules.Evaluate(systemStr, eventName, map[string]any(raw), cfg.Rules)

	base := []slog.Attr{
		slog.String("system", systemStr),
		slog.String("event", eventName),
		slog.String("session_id", raw.SessionID()),
		slog.Any("rules_checked", checked),
	}

	if violation != nil && !violation.AuditOnly && CanBlock(system, eventName) {
		logger.InfoContext(ctx, "hook.blocked",
			append(base,
				slog.String("decision", "block"),
				slog.String("blocking_rule", violation.RuleName),
				slog.String("violation_message", violation.Message),
			)...,
		)
		return blockResponse(system, violation)
	}

	if violation != nil {
		logger.InfoContext(ctx, "hook.audit_violation",
			append(base,
				slog.String("decision", "audit_only"),
				slog.String("blocking_rule", violation.RuleName),
				slog.String("violation_message", violation.Message),
			)...,
		)
	}

	logger.InfoContext(ctx, "hook.allowed",
		append(base,
			slog.String("decision", "allow"),
			slog.String("blocking_rule", ""),
			slog.String("violation_message", ""),
		)...,
	)
	return defaultAllow(system), nil, 0
}

// blockResponse builds the stdout/stderr/exitCode for a blocking violation.
func blockResponse(system HookSystem, v *rules.Violation) (stdout, stderr []byte, exitCode int) {
	switch system {
	case SystemCursor:
		return CursorBlock(v.RuleName, v.Message), nil, 0
	default:
		return ClaudeAllow(), ClaudeBlock(v.RuleName, v.Message), 2
	}
}

// defaultAllow returns a system-appropriate allow response.
func defaultAllow(system HookSystem) []byte {
	if system == SystemCursor {
		return CursorAllow()
	}
	return ClaudeAllow()
}

// claudeLogAttrs extracts slog attributes from a Claude payload.
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

	for _, key := range []string{
		"prompt", "message", "error", "error_type", "reason",
		"change_type", "trigger", "memory_type", "load_reason",
		"notification_type", "task_id", "task_subject",
		"new_cwd", "previous_cwd", "mcp_server_name",
	} {
		if v := strField(raw, key); v != "" {
			attrs = append(attrs, slog.String(key, truncate(v, 200)))
		}
	}

	if v := strField(raw, "last_assistant_message"); v != "" {
		attrs = append(attrs, slog.String("last_assistant_message_snippet", truncate(v, 200)))
	}

	return attrs
}

// cursorLogAttrs extracts slog attributes from a Cursor payload.
func cursorLogAttrs(raw RawPayload) []slog.Attr {
	attrs := []slog.Attr{
		slog.String("conversation_id", strField(raw, "conversation_id")),
		slog.String("generation_id", strField(raw, "generation_id")),
		slog.String("tool_name", strField(raw, "tool_name")),
		slog.String("file_path", strField(raw, "file_path")),
		slog.String("command", strField(raw, "command")),
	}

	if ti, ok := raw["tool_input"].(map[string]any); ok {
		attrs = append(attrs,
			slog.String("ti_command", strFromMap(ti, "command")),
			slog.String("ti_file_path", strFromMap(ti, "file_path")),
			slog.String("ti_content_snippet", truncate(strFromMap(ti, "content"), 200)),
			slog.String("ti_old_string_snippet", truncate(strFromMap(ti, "old_string"), 200)),
			slog.String("ti_new_string_snippet", truncate(strFromMap(ti, "new_string"), 200)),
		)
	}

	// afterFileEdit sends edits as an array; log the first new_string snippet.
	if edits, ok := raw["edits"].([]any); ok && len(edits) > 0 {
		if edit, ok := edits[0].(map[string]any); ok {
			attrs = append(attrs,
				slog.String("edit0_old_snippet", truncate(strFromMap(edit, "old_string"), 200)),
				slog.String("edit0_new_snippet", truncate(strFromMap(edit, "new_string"), 200)),
			)
		}
	}

	for _, key := range []string{"text", "prompt", "assistant_message", "last_assistant_message"} {
		if v := strField(raw, key); v != "" {
			attrs = append(attrs, slog.String(key+"_snippet", truncate(v, 200)))
		}
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
