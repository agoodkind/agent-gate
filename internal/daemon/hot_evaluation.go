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
	SystemError  string
	ErrorMessage string
}

// durableWriteCeiling bounds a durable audit write once it is detached from the
// caller. It is generous because the write is small and the point is to survive
// a caller that left, not to race a deadline.
const durableWriteCeiling = 30 * time.Second

func (s *Server) commitHotEvaluation(
	ctx context.Context,
	input hotEvaluationCommitInput,
) *daemonpb.EvaluateHookResponse {
	// The verdict is already decided by the time this runs, so the caller going
	// away must not erase it. Cancelling here discards a computed block and the
	// audit then shows nothing, which is the record saying a call was fine when
	// a rule had in fact matched it.
	//
	// Measured on 2026-08-05: under load an evaluation exceeded the 10 second
	// validator budget, the caller's 12 second deadline expired two seconds
	// later, and the transaction could not begin. 92 such overruns are on
	// record since 2026-07-30, 51 of them on the search rule.
	//
	// runExpandedCommands already detaches for the same reason. Values on the
	// context are kept so tracing and logging still correlate.
	commitCtx, cancelCommit := context.WithTimeout(
		context.WithoutCancel(ctx), durableWriteCeiling,
	)
	defer cancelCommit()
	ctx = commitCtx

	result := input.Result
	systemError := input.SystemError
	errorMessage := input.ErrorMessage
	configHash, err := input.Snapshot.cfg.Identity()
	if err != nil {
		systemError = "config_identity_failed"
		errorMessage = err.Error()
		configHash = "unknown"
		result = failOpenHotEvaluation(result)
	}
	record := buildHotEvaluationRecord(hotEvaluationRecordInput{
		ReceiptID: input.AppendResult.ReceiptID, EventID: input.AppendResult.EventID,
		Intake: input.Intake, ConfigHash: configHash,
		EngineVersion: gkversion.Version, EngineCommit: gkversion.Commit,
		EngineBuildHash: version.BuildHash(), StartedAt: input.StartedAt,
		CompletedAt: hotEvalNow(), Result: result, SystemError: systemError,
		ErrorMessage: errorMessage,
	})
	if input.Snapshot.evaluationRecorder == nil {
		s.logHotEvaluationFailure(
			ctx, input, record.Evaluation.EvaluationID, "evaluation_recorder_unavailable",
		)
		return s.discardedVerdict(ctx, input, "evaluation recorder unavailable")
	}
	deferredPending := result.Deferred.Valid && systemError == ""
	if err := input.Snapshot.evaluationRecorder.CommitHotEvaluation(
		ctx,
		input.AppendResult.EventID,
		input.AppendResult.ReceiptID,
		deferredPending,
		record,
	); err != nil {
		result = failOpenHotEvaluation(result)
		failureRecord := buildHotEvaluationRecord(hotEvaluationRecordInput{
			ReceiptID: input.AppendResult.ReceiptID, EventID: input.AppendResult.EventID,
			Intake: input.Intake, ConfigHash: configHash,
			EngineVersion: gkversion.Version, EngineCommit: gkversion.Commit,
			EngineBuildHash: version.BuildHash(), StartedAt: input.StartedAt,
			CompletedAt: hotEvalNow(), Result: result,
			SystemError: "hot_evaluation_commit_failed", ErrorMessage: err.Error(),
		})
		if fallbackErr := input.Snapshot.evaluationRecorder.RecordCompleted(
			ctx, failureRecord,
		); fallbackErr != nil {
			s.logHotEvaluationFailure(
				ctx, input, failureRecord.Evaluation.EvaluationID,
				"fallback_evaluation_persistence_failed",
			)
		}
		s.logHotEvaluationFailure(
			ctx, input, record.Evaluation.EvaluationID, "hot_evaluation_commit_failed",
		)
		return s.discardedVerdict(ctx, input, err.Error())
	}
	if systemError == "" && len(result.Stdout) > 0 {
		for _, output := range result.TemporalResponseOutputs {
			input.Snapshot.execRuntime.ObserveResponseOutput(
				result.Deferred.SystemString,
				result.Deferred.Fields,
				result.Deferred.EventName,
				output.Action,
				output.Target,
				input.AppendResult.ReceiptID,
				output.Output,
			)
		}
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

// discardedVerdict renders and records an allow for a call the rules did decide,
// whose verdict could not be persisted and was therefore dropped.
//
// This is the fail-open that most needs saying out loud: unlike a call that was
// never evaluated, here a rule may have produced a block that the agent will
// never see. Returning a bare allow would present a discarded block as
// compliance.
func (s *Server) discardedVerdict(
	ctx context.Context,
	input hotEvaluationCommitInput,
	diagnostic string,
) *daemonpb.EvaluateHookResponse {
	system := hook.SystemFromString(input.Intake.System)
	RecordFailOpen(
		string(hook.FailOpenReasonVerdictNotRecorded), system.String(),
		input.Intake.EventName, input.Intake.ToolName, "", diagnostic,
	)
	if s != nil && s.log != nil {
		s.log.ErrorContext(ctx, "verdict discarded; call allowed without enforcement",
			"err", diagnostic)
	}
	return failOpenEvaluateHookResponseFor(
		system, hook.FailOpenReasonVerdictNotRecorded, diagnostic,
	)
}
