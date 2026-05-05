package hook

import (
	"context"
	"log/slog"

	"goodkind.io/agent-gate/internal/audit"
	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/rules"
)

// Handle is the provider-aware orchestration entrypoint. The hint is a
// CLI subcommand classification (codex-hook, gemini-hook) and is consulted
// by Detect only as a last resort. Real env or payload fingerprints
// outrank it because subcommand arguments travel with copied configs.
func Handle(ctx context.Context, rawBytes []byte, cfg *config.Config, sink audit.Sink, hint HookSystem) (stdout, stderr []byte, exitCode int) {
	return HandleWithEnv(ctx, rawBytes, cfg, sink, hint, nil)
}

// HandleWithEnv is the parent of [Handle] that lets callers (notably the
// daemon) inject a custom getenv function. The detection layer consults
// getenv to derive provider hints from environment variables.
func HandleWithEnv(ctx context.Context, rawBytes []byte, cfg *config.Config, sink audit.Sink, hint HookSystem, getenv func(string) string) (stdout, stderr []byte, exitCode int) {
	if sink == nil {
		sink = audit.DiscardSink{}
	}
	detectionPayload, err := ParseDetectionPayload(rawBytes)
	if err != nil {
		return nil, []byte("agent-gate: parse stdin JSON: " + err.Error() + "\n"), 2
	}
	system := DetectWithEnv(detectionPayload, hint, getenv)
	payload, err := ParseHookPayload(system, rawBytes)
	if err != nil {
		return nil, []byte("agent-gate: parse typed hook JSON: " + err.Error() + "\n"), 2
	}

	auditReceived(ctx, payload, rawBytes, sink)
	return enforce(ctx, payload, cfg, sink)
}

// CanBlock returns true when the provider can meaningfully change the hook flow.
func CanBlock(system HookSystem, eventName string) bool {
	switch system {
	case SystemClaude, SystemVSCode, SystemCopilot:
		return CanBlockClaude(eventName)
	case SystemCursor:
		return CanBlockCursor(eventName)
	case SystemCodex:
		return CanBlockCodex(eventName)
	case SystemGemini:
		return CanBlockGemini(eventName)
	case SystemUnknown:
		return false
	default:
		return false
	}
}

func auditReceived(ctx context.Context, payload HookPayload, rawBytes []byte, sink audit.Sink) {
	systemStr := payload.System.String()
	eventName := payload.EventName()
	sessionID := payload.SessionID()
	fields := payload.Fields()

	base := []slog.Attr{
		slog.String("system", systemStr),
		slog.String("event", eventName),
		slog.String("session_id", sessionID),
		slog.String("cwd", payload.CWD()),
		slog.String("effective_cwd", fields.String(config.FieldEffectiveCWD)),
	}

	infoAttrs := audit.AttrsFromSlog(append(base, logAttrs(fields)...))
	sink.Log(ctx, systemStr, sessionID, eventName, "info", "hook.received", infoAttrs)

	debugAttrs := audit.AttrsFromSlog([]slog.Attr{
		slog.String("system", systemStr),
		slog.String("event", eventName),
		slog.String("session_id", sessionID),
		slog.String("raw_payload", string(rawBytes)),
	})
	sink.Log(ctx, systemStr, sessionID, eventName, "debug", "hook.raw_payload", debugAttrs)
}

func enforce(ctx context.Context, payload HookPayload, cfg *config.Config, sink audit.Sink) (stdout, stderr []byte, exitCode int) {
	systemStr := payload.System.String()
	eventName := payload.EventName()
	sessionID := payload.SessionID()
	fields := payload.Fields()
	checked := rules.CheckedRuleNames(systemStr, eventName, cfg.Rules)
	violations := rules.EvaluateAll(systemStr, eventName, fields, cfg.Rules)
	blockingViolations := blockingMatches(violations)
	auditOnlyViolations := auditOnlyMatches(violations)

	base := []slog.Attr{
		slog.String("system", systemStr),
		slog.String("event", eventName),
		slog.String("session_id", sessionID),
		slog.String("tool_use_id", fields.ToolUseID),
		slog.String("tool_name", fields.ToolName),
		slog.String("cwd", payload.CWD()),
		slog.String("effective_cwd", fields.String(config.FieldEffectiveCWD)),
		slog.Any("rules_checked", checked),
		slog.String("ti_command", fields.ToolInputCommand),
		slog.String("ti_file_path", fields.ToolInputFilePath),
	}

	if len(blockingViolations) > 0 && CanBlock(payload.System, eventName) {
		diagnostic := rules.FormatViolations(blockingViolations)
		attrs := audit.AttrsFromSlog(append(base,
			slog.String("decision", "block"),
			slog.Any("blocking_rules", matchRuleNames(blockingViolations)),
			slog.String("violation_message", diagnostic),
		))
		sink.Log(ctx, systemStr, sessionID, eventName, "info", "hook.blocked", attrs)
		return blockTextResponse(payload.System, eventName, diagnostic)
	}

	if len(auditOnlyViolations) > 0 {
		attrs := audit.AttrsFromSlog(append(base,
			slog.String("decision", "audit_only"),
			slog.Any("blocking_rules", matchRuleNames(auditOnlyViolations)),
			slog.String("violation_message", rules.FormatViolations(auditOnlyViolations)),
		))
		sink.Log(ctx, systemStr, sessionID, eventName, "info", "hook.audit_violation", attrs)
	}

	allowAttrs := audit.AttrsFromSlog(append(base,
		slog.String("decision", "allow"),
		slog.String("blocking_rule", ""),
		slog.String("violation_message", ""),
	))
	sink.Log(ctx, systemStr, sessionID, eventName, "info", "hook.allowed", allowAttrs)
	return defaultAllow(payload.System), nil, 0
}

func blockingMatches(violations []rules.MatchViolation) []rules.MatchViolation {
	out := make([]rules.MatchViolation, 0, len(violations))
	for _, violation := range violations {
		if !violation.AuditOnly {
			out = append(out, violation)
		}
	}
	return out
}

func auditOnlyMatches(violations []rules.MatchViolation) []rules.MatchViolation {
	out := make([]rules.MatchViolation, 0, len(violations))
	for _, violation := range violations {
		if violation.AuditOnly {
			out = append(out, violation)
		}
	}
	return out
}

func matchRuleNames(violations []rules.MatchViolation) []string {
	seen := make(map[string]bool)
	var names []string
	for _, violation := range violations {
		if seen[violation.RuleName] {
			continue
		}
		seen[violation.RuleName] = true
		names = append(names, violation.RuleName)
	}
	return names
}

func blockTextResponse(system HookSystem, eventName, text string) (stdout, stderr []byte, exitCode int) {
	switch system {
	case SystemCursor:
		return CursorBlockText(text), nil, 0
	case SystemCodex:
		return CodexBlockText(eventName, text), nil, 0
	case SystemGemini:
		return GeminiBlockText(eventName, text), nil, 0
	case SystemUnknown, SystemClaude, SystemVSCode, SystemCopilot:
		return ClaudeAllow(), ClaudeBlockText(text), 2
	default:
		return ClaudeAllow(), ClaudeBlockText(text), 2
	}
}

func defaultAllow(system HookSystem) []byte {
	switch system {
	case SystemCursor:
		return CursorAllow()
	case SystemCodex:
		return CodexAllow()
	case SystemGemini:
		return GeminiAllow()
	case SystemUnknown, SystemClaude, SystemVSCode, SystemCopilot:
		return ClaudeAllow()
	default:
		return ClaudeAllow()
	}
}

func logAttrs(fields rules.FieldSet) []slog.Attr {
	return []slog.Attr{
		slog.String("tool_name", fields.ToolName),
		slog.String("tool_use_id", fields.ToolUseID),
		slog.String("source", fields.Source),
		slog.String("file_path", fields.FilePathValue()),
		slog.String("model", fields.Model),
		slog.String("permission_mode", fields.PermissionMode),
		slog.String("agent_id", fields.AgentID),
		slog.String("agent_type", fields.AgentType),
		slog.String("ti_command", fields.ToolInputCommand),
		slog.String("ti_file_path", fields.ToolInputFilePath),
		slog.String("ti_description", truncate(fields.ToolInputDescription, 200)),
		slog.String("ti_content_snippet", truncate(fields.ToolInputContent, 200)),
		slog.String("ti_old_string_snippet", truncate(fields.ToolInputOldString, 200)),
		slog.String("ti_new_string_snippet", truncate(fields.ToolInputNewString, 200)),
		slog.String("ti_pattern", fields.ToolInputPattern),
		slog.String("ti_url", fields.ToolInputURL),
		slog.String("ti_query", fields.ToolInputQuery),
		slog.String("prompt_snippet", truncate(fields.Prompt, 200)),
		slog.String("message_snippet", truncate(fields.Message, 200)),
		slog.String("reason", truncate(fields.Reason, 200)),
		slog.String("last_assistant_message_snippet", truncate(fields.LastAssistantMessage, 200)),
	}
}
