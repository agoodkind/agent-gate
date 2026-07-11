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
	detectionPayload, err := hook.ParseDetectionPayload(request.GetRawJson())
	if err != nil {
		return nil
	}
	system := hook.DetectWithEnv(
		detectionPayload,
		hook.SystemFromString(request.GetProviderHint()),
		func(key string) string { return environment[key] },
	)
	payload, err := hook.ParseHookPayload(system, request.GetRawJson())
	if err != nil {
		return nil
	}
	return shellparse.ReferencedEnvironmentVariables(payload.Fields().CommandValue())
}
