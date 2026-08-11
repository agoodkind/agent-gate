package daemon

import (
	"context"

	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/hook"
)

func evaluateHotWithEventIDForTest(
	ctx context.Context,
	rawJSON []byte,
	cfg *config.Config,
	system hook.System,
	getenv func(string) string,
	eventID string,
) hook.HotEvaluation {
	classification := hook.Classify(rawJSON, system, nil, nil)
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
