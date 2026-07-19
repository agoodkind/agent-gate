package hook

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"goodkind.io/agent-gate/internal/audit"
	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/rules"
)

// EvaluateHot performs only provider detection, typed parsing, rule
// evaluation, block diagnostics, and response rendering.
func EvaluateHot(ctx context.Context, rawBytes []byte, cfg *config.Config, hint System, getenv func(string) string) HotEvaluation {
	return EvaluateHotWithEventID(ctx, rawBytes, cfg, hint, getenv, "")
}

// EvaluateHotWithEventID is EvaluateHot with the durable intake event_id that
// is included in block responses for query lookup.
func EvaluateHotWithEventID(ctx context.Context, rawBytes []byte, cfg *config.Config, hint System, getenv func(string) string, eventID string) HotEvaluation {
	detectionPayload, err := ParseDetectionPayload(rawBytes)
	if err != nil {
		return HotEvaluation{
			Stdout:   nil,
			Stderr:   []byte("agent-gate: parse stdin JSON: " + err.Error() + "\n"),
			ExitCode: 2,
			Deferred: emptyDeferredAuditEvent(SystemUnknown),
			Trace:    emptyDecisionTrace(),
		}
	}
	system := DetectWithEnv(detectionPayload, hint, getenv)
	payload, err := ParseHookPayload(system, rawBytes)
	if err != nil {
		return HotEvaluation{
			Stdout:   nil,
			Stderr:   []byte("agent-gate: parse typed hook JSON: " + err.Error() + "\n"),
			ExitCode: 2,
			Deferred: emptyDeferredAuditEvent(system),
			Trace:    emptyDecisionTrace(),
		}
	}

	return evaluatePayloadHot(ctx, payload, rawBytes, cfg, getenv, eventID)
}

func emptyDecisionTrace() rules.DecisionTrace {
	return rules.DecisionTrace{
		Deterministic: rules.DeterministicTrace{
			StartedAt: time.Time{}, CompletedAt: time.Time{},
			InputJSON: json.RawMessage(`{}`), OutputJSON: json.RawMessage(`{}`),
			InputHash: "", OutputHash: "", ServiceName: "agent-gate",
			ServiceVersion: "", CheckedRules: nil,
		},
		Layers: nil,
	}
}

func emptyDeferredAuditEvent(system System) DeferredAuditEvent {
	var fields rules.FieldSet
	return DeferredAuditEvent{
		Valid:               false,
		RawBytes:            nil,
		System:              system,
		SystemString:        system.String(),
		EventName:           "",
		SessionID:           "",
		EventID:             "",
		CWD:                 "",
		Fields:              fields,
		Rules:               nil,
		BlockingViolations:  nil,
		AuditOnlyViolations: nil,
		InferenceTraces:     nil,
		Trace:               emptyDecisionTrace(),
		Decision:            ResponseDecisionAllow,
		DiagnosticText:      "",
	}
}

// CanBlock returns true when the provider can meaningfully change the hook flow.
func CanBlock(system System, eventName string) bool {
	return LookupCapability(system, eventName) != CapabilityObserve
}

func evaluatePayloadHot(ctx context.Context, payload Payload, rawBytes []byte, cfg *config.Config, getenv func(string) string, eventID string) HotEvaluation {
	systemStr := payload.System.String()
	eventName := payload.EventName()
	fields := payload.Fields()
	ruleSet := rulesForConfig(cfg)
	staged := evaluateStagedRules(
		ctx,
		cfg,
		systemStr,
		eventName,
		fields,
		ruleSet,
		getenv,
		compactTraceJSON(rawBytes),
	)
	violations := staged.violations
	blockingViolations := blockingMatches(violations)
	auditOnlyViolations := auditOnlyMatches(violations)
	canBlock := CanBlock(payload.System, eventName)
	if len(blockingViolations) > 0 && !canBlock {
		auditOnlyViolations = append(auditOnlyViolations, blockingViolations...)
		blockingViolations = nil
	}

	decision := ResponseDecisionAllow
	diagnostic := ""
	if len(blockingViolations) > 0 && canBlock {
		decision = ResponseDecisionBlock
		diagnostic := rules.FormatViolations(blockingViolations)
		if footer := cfg.BlockFooter(); footer != "" {
			diagnostic = diagnostic + "\n\n" + footer
		}
		response := RenderResponse(ResponseRequest{
			System:         payload.System,
			EventName:      eventName,
			Decision:       ResponseDecisionBlock,
			DiagnosticText: diagnostic,
			EventID:        eventID,
			FailOpenReason: "",
		})
		return HotEvaluation{
			Stdout:   response.Stdout,
			Stderr:   response.Stderr,
			ExitCode: response.ExitCode,
			Deferred: newDeferredAuditEvent(rawBytes, payload, fields, ruleSet, blockingViolations, auditOnlyViolations, staged.trace, decision, diagnostic, eventID),
			Trace:    staged.trace,
		}
	}

	response := RenderResponse(ResponseRequest{
		System:         payload.System,
		EventName:      eventName,
		Decision:       ResponseDecisionAllow,
		DiagnosticText: "",
		EventID:        eventID,
		FailOpenReason: "",
	})
	return HotEvaluation{
		Stdout:   response.Stdout,
		Stderr:   response.Stderr,
		ExitCode: response.ExitCode,
		Deferred: newDeferredAuditEvent(rawBytes, payload, fields, ruleSet, blockingViolations, auditOnlyViolations, staged.trace, decision, diagnostic, eventID),
		Trace:    staged.trace,
	}
}

func rulesForConfig(cfg *config.Config) []config.Rule {
	if cfg == nil {
		return nil
	}
	return cfg.Rules
}

func newDeferredAuditEvent(
	rawBytes []byte,
	payload Payload,
	fields rules.FieldSet,
	ruleSet []config.Rule,
	blockingViolations []rules.Violation,
	auditOnlyViolations []rules.Violation,
	trace rules.DecisionTrace,
	decision ResponseDecision,
	diagnosticText string,
	eventID string,
) DeferredAuditEvent {
	return DeferredAuditEvent{
		Valid:               true,
		RawBytes:            rawBytes,
		System:              payload.System,
		SystemString:        payload.System.String(),
		EventName:           payload.EventName(),
		SessionID:           payload.SessionID(),
		EventID:             eventID,
		CWD:                 payload.CWD(),
		Fields:              fields,
		Rules:               ruleSet,
		BlockingViolations:  blockingViolations,
		AuditOnlyViolations: auditOnlyViolations,
		InferenceTraces:     nil,
		Trace:               trace,
		Decision:            decision,
		DiagnosticText:      diagnosticText,
	}
}

// WriteDeferredAudit performs audit normalization, enrichment, and logging
// after the hook response decision has already been rendered.
func WriteDeferredAudit(ctx context.Context, event DeferredAuditEvent, sink audit.Sink) {
	if sink == nil || !event.Valid {
		return
	}

	if shouldWriteReceivedAudit(event) {
		auditReceivedFields(ctx, event, sink)
	}
	writeDecisionAudit(ctx, event, sink)
}

func shouldWriteReceivedAudit(event DeferredAuditEvent) bool {
	return event.Decision == ResponseDecisionBlock
}

func auditReceivedFields(ctx context.Context, event DeferredAuditEvent, sink audit.Sink) {
	base := []slog.Attr{
		slog.String("system", event.SystemString),
		slog.String("event", event.EventName),
		slog.String("session_id", event.SessionID),
		slog.String("cwd", event.CWD),
		slog.String("effective_cwd", event.Fields.String(config.FieldEffectiveCWD)),
	}

	infoAttrs := audit.AttrsFromSlog(append(base, logAttrs(event.Fields)...))
	sink.Log(ctx, event.SystemString, event.SessionID, event.EventName, "info", "hook.received", infoAttrs)
}

func writeDecisionAudit(ctx context.Context, event DeferredAuditEvent, sink audit.Sink) {
	checked := rules.CheckedRuleNames(event.SystemString, event.EventName, event.Rules)
	base := []slog.Attr{
		slog.String("system", event.SystemString),
		slog.String("event", event.EventName),
		slog.String("session_id", event.SessionID),
		slog.String("tool_use_id", event.Fields.ToolUseID),
		slog.String("tool_name", event.Fields.ToolName),
		slog.String("cwd", event.CWD),
		slog.String("effective_cwd", event.Fields.String(config.FieldEffectiveCWD)),
		slog.Any("rules_checked", checked),
		slog.String("ti_command", event.Fields.ToolInputCommand),
		slog.String("ti_file_path", event.Fields.ToolInputFilePath),
	}

	if event.Decision == ResponseDecisionBlock {
		attrs := audit.AttrsFromSlog(append(base,
			slog.String("decision", "block"),
			slog.Any("blocking_rules", matchRuleNames(event.BlockingViolations)),
			slog.String("violation_message", event.DiagnosticText),
		))
		sink.Log(ctx, event.SystemString, event.SessionID, event.EventName, "info", "hook.blocked", attrs)
		return
	}

	if len(event.AuditOnlyViolations) > 0 {
		attrs := audit.AttrsFromSlog(append(base,
			slog.String("decision", "audit_only"),
			slog.Any("blocking_rules", matchRuleNames(event.AuditOnlyViolations)),
			slog.String("violation_message", rules.FormatViolations(event.AuditOnlyViolations)),
		))
		sink.Log(ctx, event.SystemString, event.SessionID, event.EventName, "info", "hook.audit_violation", attrs)
	}

	allowAttrs := audit.AttrsFromSlog(append(base,
		slog.String("decision", "allow"),
		slog.String("blocking_rule", ""),
		slog.String("violation_message", ""),
	))
	sink.Log(ctx, event.SystemString, event.SessionID, event.EventName, "info", "hook.allowed", allowAttrs)
}

func blockingMatches(violations []rules.Violation) []rules.Violation {
	out := make([]rules.Violation, 0, len(violations))
	for _, violation := range violations {
		if !violation.AuditOnly {
			out = append(out, violation)
		}
	}
	return out
}

func auditOnlyMatches(violations []rules.Violation) []rules.Violation {
	out := make([]rules.Violation, 0, len(violations))
	for _, violation := range violations {
		if violation.AuditOnly {
			out = append(out, violation)
		}
	}
	return out
}

func matchRuleNames(violations []rules.Violation) []string {
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
		slog.String("ti_description", truncate(fields.ToolInputDescription)),
		slog.String("ti_content_snippet", truncate(fields.ToolInputContent)),
		slog.String("ti_old_string_snippet", truncate(fields.ToolInputOldString)),
		slog.String("ti_new_string_snippet", truncate(fields.ToolInputNewString)),
		slog.String("ti_pattern", fields.ToolInputPattern),
		slog.String("ti_url", fields.ToolInputURL),
		slog.String("ti_query", fields.ToolInputQuery),
		slog.String("prompt_snippet", truncate(fields.Prompt)),
		slog.String("message_snippet", truncate(fields.Message)),
		slog.String("reason", truncate(fields.Reason)),
		slog.String("last_assistant_message_snippet", truncate(fields.LastAssistantMessage)),
	}
}
