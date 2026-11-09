package daemon

import (
	"context"
	"log/slog"

	"goodkind.io/agent-gate/api/daemonpb"
	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/hook"
	"goodkind.io/agent-gate/internal/intake"
)

func unusableHookResponse(
	ctx context.Context,
	request *daemonpb.EvaluateHookRequest,
	cfg *config.Config,
	log *slog.Logger,
) *daemonpb.EvaluateHookResponse {
	system := hook.SystemFromString(request.GetProviderHint())
	diagnostic := unusableConfigDiagnostic(cfg)
	RecordFailOpen(
		string(hook.FailOpenReasonConfigUnusable),
		system.String(),
		"",
		"",
		request.GetCwd(),
		diagnostic,
	)
	log.ErrorContext(ctx, "config unusable; call allowed without enforcement", "err", diagnostic)

	return failOpenEvaluateHookResponseFor(system, hook.FailOpenReasonConfigUnusable, diagnostic)
}

func prepareHookIntakeRecord(
	input hook.EvaluationInput,
	environment map[string]string,
	normalizationErr error,
) (intake.Record, error) {
	record, intakeErr := buildClassifiedIntakeRecord(
		input.WireBytes,
		input.NormalizedJSON,
		input.Classification,
		environment,
	)
	if normalizationErr != nil {
		intakeErr = normalizationErr
	}

	if intakeErr != nil {
		record = buildInvalidIntakeRecord(input.WireBytes, input.Classification, environment)
	}

	return record, intakeErr
}
