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
func Handle(ctx context.Context, raw RawPayload, rawBytes []byte, cfg *config.Config, sink audit.Sink, hint HookSystem) (stdout, stderr []byte, exitCode int) {
	if sink == nil {
		sink = audit.DiscardSink{}
	}
	system := Detect(raw, hint)
	eventName := raw.EventName()

	auditReceived(ctx, raw, rawBytes, system, eventName, sink)
	return enforce(ctx, raw, system, eventName, cfg, sink)
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
	default:
		return false
	}
}

func auditReceived(ctx context.Context, raw RawPayload, rawBytes []byte, system HookSystem, eventName string, sink audit.Sink) {
	systemStr := system.String()
	sessionID := raw.SessionID()

	base := []slog.Attr{
		slog.String("system", systemStr),
		slog.String("event", eventName),
		slog.String("session_id", sessionID),
		slog.String("cwd", raw.CWD()),
	}

	infoAttrs := audit.AttrsFromSlog(append(base, logAttrs(system, raw)...))
	sink.Log(ctx, systemStr, sessionID, eventName, "info", "hook.received", infoAttrs)

	debugAttrs := audit.AttrsFromSlog([]slog.Attr{
		slog.String("system", systemStr),
		slog.String("event", eventName),
		slog.String("session_id", sessionID),
		slog.String("raw_payload", string(rawBytes)),
	})
	sink.Log(ctx, systemStr, sessionID, eventName, "debug", "hook.raw_payload", debugAttrs)
}

func enforce(ctx context.Context, raw RawPayload, system HookSystem, eventName string, cfg *config.Config, sink audit.Sink) (stdout, stderr []byte, exitCode int) {
	systemStr := system.String()
	sessionID := raw.SessionID()
	checked := rules.CheckedRuleNames(systemStr, eventName, cfg.Rules)
	violations := rules.EvaluateAll(systemStr, eventName, map[string]any(raw), cfg.Rules)
	blockingViolations := blockingMatches(violations)
	auditOnlyViolations := auditOnlyMatches(violations)

	base := []slog.Attr{
		slog.String("system", systemStr),
		slog.String("event", eventName),
		slog.String("session_id", sessionID),
		slog.Any("rules_checked", checked),
	}

	if len(blockingViolations) > 0 && CanBlock(system, eventName) {
		diagnostic := rules.FormatViolations(blockingViolations)
		attrs := audit.AttrsFromSlog(append(base,
			slog.String("decision", "block"),
			slog.Any("blocking_rules", matchRuleNames(blockingViolations)),
			slog.String("violation_message", diagnostic),
		))
		sink.Log(ctx, systemStr, sessionID, eventName, "info", "hook.blocked", attrs)
		return blockTextResponse(system, eventName, diagnostic)
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
	return defaultAllow(system), nil, 0
}

func blockingMatches(violations []rules.MatchViolation) []rules.MatchViolation {
	out := make([]rules.MatchViolation, 0, len(violations))
	for _, v := range violations {
		if !v.AuditOnly {
			out = append(out, v)
		}
	}
	return out
}

func auditOnlyMatches(violations []rules.MatchViolation) []rules.MatchViolation {
	out := make([]rules.MatchViolation, 0, len(violations))
	for _, v := range violations {
		if v.AuditOnly {
			out = append(out, v)
		}
	}
	return out
}

func matchRuleNames(violations []rules.MatchViolation) []string {
	seen := make(map[string]bool)
	var names []string
	for _, v := range violations {
		if seen[v.RuleName] {
			continue
		}
		seen[v.RuleName] = true
		names = append(names, v.RuleName)
	}
	return names
}

func blockResponse(system HookSystem, eventName string, v *rules.Violation) (stdout, stderr []byte, exitCode int) {
	return blockTextResponse(system, eventName, "agent-gate: ["+v.RuleName+"] "+v.Message)
}

func blockTextResponse(system HookSystem, eventName, text string) (stdout, stderr []byte, exitCode int) {
	switch system {
	case SystemCursor:
		return CursorBlockText(text), nil, 0
	case SystemCodex:
		return CodexBlockText(eventName, text), nil, 0
	case SystemGemini:
		return GeminiBlockText(eventName, text), nil, 0
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
	default:
		return ClaudeAllow()
	}
}

func logAttrs(system HookSystem, raw RawPayload) []slog.Attr {
	switch system {
	case SystemClaude, SystemVSCode, SystemCopilot:
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
