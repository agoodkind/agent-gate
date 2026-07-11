package hook

import (
	"context"

	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/rules"
)

func evaluateStagedRules(
	ctx context.Context,
	cfg *config.Config,
	system string,
	eventName string,
	fields rules.FieldSet,
	ruleSet []config.Rule,
	getenv func(string) string,
) []rules.Violation {
	deterministicRules, inferenceRules := partitionInferenceRules(ruleSet)
	violations := rules.EvaluateAll(
		ctx,
		system,
		eventName,
		fields,
		deterministicRules,
		getenv,
	)
	if len(blockingMatches(violations)) > 0 || len(inferenceRules) == 0 {
		return violations
	}

	inferenceCtx, cancel := context.WithTimeout(ctx, cfg.HookInferencePhaseTimeout())
	defer cancel()
	for i := range inferenceRules {
		if inferenceCtx.Err() != nil {
			break
		}
		inferenceViolations := rules.EvaluateAll(
			inferenceCtx,
			system,
			eventName,
			fields,
			[]config.Rule{inferenceRules[i]},
			getenv,
		)
		violations = append(violations, inferenceViolations...)
	}
	return violations
}
