package daemon

import (
	"context"
	"fmt"
	"log/slog"

	"goodkind.io/agent-gate/api/daemonpb"
	"goodkind.io/agent-gate/internal/hook"
	"goodkind.io/agent-gate/internal/intake"
)

func shouldDeferHookEvaluation(system hook.System, eventName string, intakeErr error) bool {
	if intakeErr != nil || system == hook.SystemUnknown || hook.CanBlock(system, eventName) {
		return false
	}
	return eventName == "SessionEnd" || eventName == "Stop"
}

func appendHookIntake(
	ctx context.Context,
	log *slog.Logger,
	store intakeStore,
	record intake.Record,
	deferEvaluation bool,
) (intake.AppendResult, error) {
	if deferEvaluation {
		result, err := store.AppendPending(ctx, record)
		if err != nil {
			log.WarnContext(ctx, "append pending hook intake failed; failing open", "err", err)
			return intake.AppendResult{}, fmt.Errorf("append pending hook intake: %w", err)
		}
		return result, nil
	}
	result, err := store.Append(ctx, record)
	if err != nil {
		log.WarnContext(ctx, "append hook intake failed; failing open", "err", err)
		return intake.AppendResult{}, fmt.Errorf("append hook intake: %w", err)
	}
	return result, nil
}

func deferObservedHook(
	snapshot *runtimeSnapshot,
	appendResult intake.AppendResult,
	system hook.System,
	eventName string,
) *daemonpb.EvaluateHookResponse {
	// Durable intake lets observe-only events finish outside the host's
	// shutdown path without losing their audit evaluation.
	if snapshot.deferredProcessor != nil {
		var emptyDeferredEvent hook.DeferredAuditEvent
		snapshot.deferredProcessor.Enqueue(
			appendResult.ReceiptID,
			appendResult.EventID,
			emptyDeferredEvent,
		)
	}
	response := hook.RenderResponse(hook.ResponseRequest{
		System:         system,
		EventName:      eventName,
		Decision:       hook.ResponseDecisionAllow,
		DiagnosticText: "",
		EventID:        "",
		Footer:         "",
		FailOpenReason: "",
		ContextText:    "",
		MutationText:   "",
		PromptText:     "",
	})
	return &daemonpb.EvaluateHookResponse{
		ExitCode:   clampExitCode(response.ExitCode),
		StdoutData: append([]byte(nil), response.Stdout...),
		StderrData: append([]byte(nil), response.Stderr...),
	}
}
