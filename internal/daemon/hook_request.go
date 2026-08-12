package daemon

import (
	"goodkind.io/agent-gate/api/daemonpb"
	"goodkind.io/agent-gate/internal/hook"
	"goodkind.io/agent-gate/internal/intake"
)

func prepareHookEvaluationInput(
	request *daemonpb.EvaluateHookRequest,
) (hook.EvaluationInput, error) {
	wireInput := cloneBytes(request.GetRawJson())
	classification := hook.ClassifyWithContext(
		wireInput,
		request.GetProviderHint(),
		request.GetArgv(),
		request.GetEnvFingerprint(),
		invocationContextFromProto(request.GetInvocationContext()),
	)
	normalizedJSON := cloneBytes(wireInput)
	if cwd := request.GetCwd(); cwd != "" {
		normalizedJSON = injectCWD(normalizedJSON, cwd)
	}
	if classification.ResolvedSystem() == hook.SystemCopilot {
		var err error
		normalizedJSON, err = hook.NormalizeCopilotPayload(
			normalizedJSON,
			copilotEventHint(request.GetArgv()),
		)
		if err != nil {
			return hook.EvaluationInput{
				WireBytes:      wireInput,
				NormalizedJSON: cloneBytes(wireInput),
				Classification: classification,
			}, wrapServerError("normalize Copilot payload", err)
		}
	}
	return hook.EvaluationInput{
		WireBytes:      wireInput,
		NormalizedJSON: normalizedJSON,
		Classification: classification,
	}, nil
}

func prepareIntakeRecord(
	evaluationInput hook.EvaluationInput,
	normalizationErr error,
	envFingerprint map[string]string,
) (intake.Record, error) {
	record, intakeErr := buildClassifiedIntakeRecord(
		evaluationInput.WireBytes,
		evaluationInput.NormalizedJSON,
		evaluationInput.Classification,
		envFingerprint,
	)
	if normalizationErr != nil {
		intakeErr = normalizationErr
	}
	if intakeErr == nil {
		return record, nil
	}
	return buildInvalidIntakeRecord(
		evaluationInput.WireBytes,
		evaluationInput.Classification,
		envFingerprint,
	), intakeErr
}
