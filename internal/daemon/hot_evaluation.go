package daemon

import (
	"context"
	"log/slog"
	"time"

	"goodkind.io/agent-gate/api/daemonpb"
	"goodkind.io/agent-gate/internal/hook"
	"goodkind.io/agent-gate/internal/intake"
	"goodkind.io/agent-gate/internal/version"
	gkversion "goodkind.io/gklog/version"
)

type hotEvaluationCommitInput struct {
	Log          *slog.Logger
	Snapshot     *runtimeSnapshot
	Intake       intake.Record
	AppendResult intake.AppendResult
	StartedAt    time.Time
	Result       hook.HotEvaluation
}

func (s *Server) commitHotEvaluation(
	ctx context.Context,
	input hotEvaluationCommitInput,
) *daemonpb.EvaluateHookResponse {
	result := input.Result
	systemError := ""
	if err := markDeferredReplayPending(
		ctx, input.Log, input.Snapshot, input.AppendResult, result.Deferred,
	); err != nil {
		systemError = "deferred_pending_failed"
		result = failOpenHotEvaluation(result)
	}
	configHash, err := input.Snapshot.cfg.Identity()
	if err != nil {
		systemError = "config_identity_failed"
		configHash = "unknown"
		result = failOpenHotEvaluation(result)
	}
	record := buildHotEvaluationRecord(hotEvaluationRecordInput{
		ReceiptID: input.AppendResult.ReceiptID, EventID: input.AppendResult.EventID,
		Intake: input.Intake, ConfigHash: configHash,
		EngineVersion: gkversion.Version, EngineCommit: gkversion.Commit,
		EngineBuildHash: version.BuildHash(), StartedAt: input.StartedAt,
		CompletedAt: hotEvalNow(), Result: result, SystemError: systemError,
	})
	if input.Snapshot.evaluationRecorder == nil {
		s.logHotEvaluationFailure(
			ctx, input, record.Evaluation.EvaluationID, "evaluation_recorder_unavailable",
		)
		return failOpenEvaluateHookResponse()
	}
	if err := input.Snapshot.evaluationRecorder.RecordCompleted(ctx, record); err != nil {
		s.logHotEvaluationFailure(
			ctx, input, record.Evaluation.EvaluationID, "evaluation_persistence_failed",
		)
		return failOpenEvaluateHookResponse()
	}
	if systemError == "" {
		enqueueDeferredReplay(input.Snapshot, input.AppendResult, result.Deferred)
	}
	return &daemonpb.EvaluateHookResponse{
		ExitCode: clampExitCode(result.ExitCode), StdoutData: append([]byte(nil), result.Stdout...),
		StderrData: append([]byte(nil), result.Stderr...),
	}
}

func (s *Server) logHotEvaluationFailure(
	ctx context.Context,
	input hotEvaluationCommitInput,
	evaluationID string,
	statusClass string,
) {
	input.Log.WarnContext(
		ctx,
		"record hot evaluation failed; failing open",
		"receipt_id", input.AppendResult.ReceiptID,
		"event_id", input.AppendResult.EventID,
		"evaluation_id", evaluationID,
		"status_class", statusClass,
	)
}
