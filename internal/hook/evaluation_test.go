package hook_test

import (
	"context"

	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/hook"
)

var evaluationEnvironmentKeys = []string{
	"CODEX_THREAD_ID",
	"CODEX_CI",
	"COPILOT_OTEL_FILE_EXPORTER_PATH",
	"COPILOT_OTEL_ENABLED",
	"COPILOT_OTEL_EXPORTER_TYPE",
	"CURSOR_VERSION",
	"CURSOR_WORKSPACE_NAME",
	"CURSOR_MODE",
	"GEMINI_CLI",
	"CLAUDE_CODE_ENTRYPOINT",
	"AI_AGENT",
	"VSCODE_PID",
	"VSCODE_IPC_HOOK",
}

func evaluateHot(
	ctx context.Context,
	rawJSON []byte,
	cfg *config.Config,
	hint hook.System,
	getenv func(string) string,
) hook.HotEvaluation {
	return evaluateHotWithEventID(ctx, rawJSON, cfg, hint, getenv, "")
}

func evaluateHotWithEventID(
	ctx context.Context,
	rawJSON []byte,
	cfg *config.Config,
	hint hook.System,
	getenv func(string) string,
	eventID string,
) hook.HotEvaluation {
	environment := make(map[string]string)
	for _, key := range evaluationEnvironmentKeys {
		if value := getenv(key); value != "" {
			environment[key] = value
		}
	}
	classification := hook.Classify(rawJSON, hint, nil, environment)
	return hook.EvaluateClassifiedHotWithEventID(
		ctx,
		hook.EvaluationInput{
			WireBytes:      rawJSON,
			NormalizedJSON: rawJSON,
			Classification: classification,
		},
		cfg,
		getenv,
		eventID,
	)
}
