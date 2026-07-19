package rules

import (
	"context"

	"goodkind.io/agent-gate/internal/config"
)

// EvaluateAll returns enforcement violations for legacy rule tests. Production
// callers use EvaluateAllDetailed so response effects and traces stay attached
// to the single shared evaluation pass.
func EvaluateAll(
	ctx context.Context,
	system string,
	eventName string,
	fields FieldSet,
	rulesSlice []config.Rule,
	getenv func(string) string,
) []Violation {
	return EvaluateAllDetailed(
		ctx, system, eventName, fields, rulesSlice, getenv, nil, "",
	).Violations
}
