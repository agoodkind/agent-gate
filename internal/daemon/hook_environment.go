package daemon

import (
	"context"

	"goodkind.io/agent-gate/api/daemonpb"
	"goodkind.io/agent-gate/internal/hook"
	"goodkind.io/agent-gate/internal/rules/concerns/shellparse"
)

// ResolveHookEnvironment returns the environment names needed to evaluate a hook.
func (s *Server) ResolveHookEnvironment(
	_ context.Context,
	request *daemonpb.ResolveHookEnvironmentRequest,
) (*daemonpb.ResolveHookEnvironmentResponse, error) {
	return &daemonpb.ResolveHookEnvironmentResponse{
		ReferencedNames: referencedHookEnvironment(request),
	}, nil
}

func referencedHookEnvironment(request *daemonpb.ResolveHookEnvironmentRequest) []string {
	environment := request.GetEnvFingerprint()
	wireInput := request.GetRawJson()
	classification := hook.ClassifyWithContext(
		wireInput,
		request.GetProviderHint(),
		request.GetArgv(),
		environment,
		invocationContextFromProto(request.GetInvocationContext()),
	)
	normalizedJSON := wireInput
	if classification.ResolvedSystem() == hook.SystemCopilot {
		var normalizeErr error
		normalizedJSON, normalizeErr = hook.NormalizeCopilotPayload(
			normalizedJSON,
			copilotEventHint(request.GetArgv()),
		)
		if normalizeErr != nil {
			return nil
		}
	}
	payload, err := hook.ParseHookPayload(
		classification.ResolvedSystem(),
		normalizedJSON,
	)
	if err != nil {
		return nil
	}
	return shellparse.ReferencedEnvironmentVariables(payload.Fields().CommandValue())
}
