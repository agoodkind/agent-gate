package rules

import (
	"context"

	"goodkind.io/agent-gate/internal/config"
	regexconcern "goodkind.io/agent-gate/internal/rules/concerns/regex"
)

// allConditionsMatch returns true when every condition in the slice matches
// the payload (AND semantics). A condition matches when:
//   - Its Pattern is set and matches the extracted field value, AND
//   - Its NotPattern is unset or does not match the extracted field value.
//
// Pattern and NotPattern check empty extracted values. Optional fields such as
// tool_name can use not_pattern alone.
func allConditionsMatch(
	ctx context.Context,
	fields FieldSet,
	rule *config.Rule,
	conditions []config.Condition,
) bool {
	condCtx := conditionContext{commandCwds: nil}
	if !collectCommandConditionContext(fields, conditions, &condCtx) {
		collectRemainingInferenceSkips(ctx, rule, conditions, 0)

		return false
	}

	for conditionIndex := range conditions {
		if ctx.Err() != nil {
			collectRemainingInferenceSkips(ctx, rule, conditions, conditionIndex)

			return false
		}

		c := &conditions[conditionIndex]
		if !conditionGateMatches(ctx, fields, rule, conditionIndex, c, condCtx) {
			collectRemainingInferenceSkips(ctx, rule, conditions, conditionIndex+1)

			return false
		}
	}

	return true
}

func collectRemainingInferenceSkips(
	ctx context.Context,
	rule *config.Rule,
	conditions []config.Condition,
	startIndex int,
) {
	for conditionIndex := startIndex; conditionIndex < len(conditions); conditionIndex++ {
		condition := &conditions[conditionIndex]
		if conditionKind(condition) == config.ConditionKindInfer {
			collectSkippedInferenceCondition(
				ctx,
				rule,
				conditionIndex,
				condition,
				skipPriorConditionNonmatch,
			)
		}
	}
}

func conditionGateMatches(
	ctx context.Context,
	fields FieldSet,
	rule *config.Rule,
	conditionIndex int,
	condition *config.Condition,
	conditionContextValue conditionContext,
) bool {
	switch conditionKind(condition) {
	case config.ConditionKindRegex:
		accessor := conditionFieldAccessor{fields: &fields, condition: condition}

		return regexconcern.ConditionMatch(accessor, condition)
	case config.ConditionKindCommand:
		return true
	case config.ConditionKindProject:
		return projectConditionMatch(fields, condition, conditionContextValue)
	case config.ConditionKindDiff:
		return diffConditionGateMatch(fields, condition)
	case config.ConditionKindShellWrite:
		return shellWriteConditionGateMatch(fields, condition)
	case config.ConditionKindShellRead:
		return shellReadConditionGateMatch(fields, condition)
	case config.ConditionKindGitDefaultBranch, config.ConditionKindGitPrimaryCheckout,
		config.ConditionKindGitRefMove:
		fields = structuralCommandFields(ctx, fields)

		return gitConditionMatch(
			fields,
			condition,
			conditionContextValue,
			gitStateReaderFromContext(ctx),
		)
	case config.ConditionKindExec:
		return execConditionGateMatch(ctx, fields, rule, conditionIndex, condition)
	case config.ConditionKindInfer:
		return inferConditionGateMatch(ctx, fields, rule, conditionIndex, condition)
	default:
		return false
	}
}

func collectCommandConditionContext(
	fields FieldSet,
	conditions []config.Condition,
	condCtx *conditionContext,
) bool {
	for i := range conditions {
		c := &conditions[i]
		if conditionKind(c) != config.ConditionKindCommand {
			continue
		}

		cwds, ok := commandConditionCwds(fields, c)
		if !ok {
			return false
		}

		condCtx.commandCwds = append(condCtx.commandCwds, cwds...)
	}

	return true
}
