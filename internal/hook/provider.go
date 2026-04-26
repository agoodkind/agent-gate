package hook

import (
	"context"
	"log/slog"

	"goodkind.io/agent-gate/internal/audit"
	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/rules"
)

// HandleWithOverride is the provider-aware orchestration entrypoint.
func HandleWithOverride(ctx context.Context, raw RawPayload, rawBytes []byte, cfg *config.Config, loggers *audit.Loggers, forced HookSystem) (stdout, stderr []byte, exitCode int) {
	system := DetectWithOverride(raw, forced)
	eventName := raw.EventName()
	logger := loggers.For(system.String())

	auditReceived(ctx, raw, rawBytes, system, eventName, logger)
	return enforce(ctx, raw, system, eventName, cfg, logger)
}

// Handle is preserved for callers that rely on autodetection.
func Handle(ctx context.Context, raw RawPayload, rawBytes []byte, cfg *config.Config, loggers *audit.Loggers) (stdout, stderr []byte, exitCode int) {
	return HandleWithOverride(ctx, raw, rawBytes, cfg, loggers, SystemUnknown)
}

// CanBlock returns true when the provider can meaningfully change the hook flow.
func CanBlock(system HookSystem, eventName string) bool {
	switch system {
	case SystemClaude:
		return CanBlockClaude(eventName)
	case SystemCursor:
		return CanBlockCursor(eventName)
	case SystemCodex:
		return CanBlockCodex(eventName)
	case SystemGemini:
		return CanBlockGemini(eventName)
	default:
		return false
	}
}

func auditReceived(ctx context.Context, raw RawPayload, rawBytes []byte, system HookSystem, eventName string, logger *audit.Logger) {
	base := []slog.Attr{
		slog.String("system", system.String()),
		slog.String("event", eventName),
		slog.String("session_id", raw.SessionID()),
		slog.String("cwd", raw.CWD()),
	}

	logger.InfoContext(ctx, "hook.received", append(base, logAttrs(system, raw)...)...)
	logger.DebugContext(ctx, "hook.raw_payload",
		slog.String("system", system.String()),
		slog.String("event", eventName),
		slog.String("session_id", raw.SessionID()),
		slog.String("raw_payload", string(rawBytes)),
	)
}

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
		return blockResponse(system, eventName, violation)
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

func blockResponse(system HookSystem, eventName string, v *rules.Violation) (stdout, stderr []byte, exitCode int) {
	switch system {
	case SystemCursor:
		return CursorBlock(v.RuleName, v.Message), nil, 0
	case SystemCodex:
		return CodexBlock(eventName, v.RuleName, v.Message), nil, 0
	case SystemGemini:
		return GeminiBlock(eventName, v.RuleName, v.Message), nil, 0
	default:
		return ClaudeAllow(), ClaudeBlock(v.RuleName, v.Message), 2
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
	default:
		return ClaudeAllow()
	}
}

func logAttrs(system HookSystem, raw RawPayload) []slog.Attr {
	switch system {
	case SystemClaude:
		return claudeLogAttrs(raw)
	case SystemCursor:
		return cursorLogAttrs(raw)
	case SystemCodex:
		return codexLogAttrs(raw)
	case SystemGemini:
		return geminiLogAttrs(raw)
	default:
		return nil
	}
}
