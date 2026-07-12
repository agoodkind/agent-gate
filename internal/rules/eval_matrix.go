package rules

import (
	"context"

	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/pipeline"
)

// evalVerdict is a single evaluator's decision within a rule's evaluation matrix.
type evalVerdict int

const (
	// verdictAllow means the evaluator did not find a violation.
	verdictAllow evalVerdict = iota
	// verdictBlock means the evaluator found a violation.
	verdictBlock
)

// evaluatorResolver runs the declared evaluator at index and returns its verdict.
// The engine supplies this: a deterministic entry runs the rule's condition
// block, an infer entry calls the named inference point.
type evaluatorResolver func(index int, eval config.RuleEval) evalVerdict

// matrixDecision is the enforced decision for a rule's evaluator matrix.
type matrixDecision struct {
	block bool
}

// runEvalMatrix executes a rule's declared evaluators in order and returns the
// enforced decision. It honors each entry's role: an enforce entry participates
// in the decision, and a verify entry runs but does not decide. Enforcing entries
// are joined by the combine operator. Union, the default, blocks when any
// enforcing evaluator blocks. The and operator blocks only when every enforcing
// evaluator blocks.
func runEvalMatrix(evals []config.RuleEval, resolve evaluatorResolver) matrixDecision {
	state := matrixRunState{enforcingCount: 0, blockingCount: 0}
	for index := range evals {
		runMatrixEntry(index, evals[index], resolve, &state)
	}
	block := matrixBlocks(matrixCombineOperator(evals), state.enforcingCount, state.blockingCount)
	return matrixDecision{block: block}
}

// matrixRunState accumulates the enforcing and blocking tallies the combine
// operator joins across evaluator entries.
type matrixRunState struct {
	enforcingCount int
	blockingCount  int
}

// runMatrixEntry resolves one evaluator entry and folds its verdict into state.
func runMatrixEntry(index int, eval config.RuleEval, resolve evaluatorResolver, state *matrixRunState) {
	verdict := resolve(index, eval)
	if evaluatorEnforces(eval) {
		state.enforcingCount++
		if verdict == verdictBlock {
			state.blockingCount++
		}
	}
}

// evaluatorEnforces reports whether an evaluator participates in the decision. An
// enforce entry does; a verify entry never does.
func evaluatorEnforces(eval config.RuleEval) bool {
	return eval.Role == config.RoleEnforce
}

// matrixCombineOperator returns the combine operator for the rule, taken from the
// first evaluator that declares one and defaulting to union.
func matrixCombineOperator(evals []config.RuleEval) string {
	for index := range evals {
		if evals[index].Combine != "" {
			return evals[index].Combine
		}
	}
	return config.CombineUnion
}

// matrixBlocks applies the combine operator to the enforcing tally. With no
// enforcing evaluator the rule does not block. Union blocks when any enforcing
// evaluator blocked; the and operator blocks only when every enforcing evaluator
// blocked.
func matrixBlocks(combine string, enforcingCount, blockingCount int) bool {
	if enforcingCount == 0 {
		return false
	}
	if combine == config.CombineAnd {
		return blockingCount == enforcingCount
	}
	return blockingCount > 0
}

// evalMatrixCondition is a pipeline.Condition for a rule that declares an
// evaluator matrix ([[rules.eval]]). It runs the declared evaluators in order,
// honoring each entry's role and the combine operator, and emits one violation
// when the matrix blocks.
type evalMatrixCondition struct {
	name   string
	fields *FieldSet
	rule   *config.Rule
}

func (e *evalMatrixCondition) Profile() pipeline.Profile {
	return pipeline.Profile{
		Name:         e.name,
		Cost:         pipeline.CostCheap,
		Idempotent:   true,
		MemoLifetime: pipeline.MemoEvent,
	}
}

func (e *evalMatrixCondition) Execute(ctx context.Context, _ pipeline.Input) (pipeline.Outcome, error) {
	decision := runEvalMatrix(e.rule.Eval, func(index int, eval config.RuleEval) evalVerdict {
		return e.resolveEvaluator(ctx, index, eval)
	})
	if !decision.block {
		return ruleOutcome{
			violations:       nil,
			rule:             e.rule,
			isConditionBased: true,
			gateMatched:      false,
		}, nil
	}
	return ruleOutcome{
		violations:       []Violation{conditionFallbackViolation(*e.fields, e.rule)},
		rule:             e.rule,
		isConditionBased: true,
		gateMatched:      true,
	}, nil
}

// resolveEvaluator runs one declared evaluator. A deterministic entry runs the
// rule's condition block and blocks when every condition matches. An infer entry
// fails closed until inference routing is wired, so an enabled-but-unrouted infer
// evaluator blocks rather than silently allowing a protected write.
func (e *evalMatrixCondition) resolveEvaluator(ctx context.Context, _ int, eval config.RuleEval) evalVerdict {
	if eval.Kind == config.EvalKindDeterministic {
		if allConditionsMatch(ctx, *e.fields, e.rule, e.rule.Conditions) {
			return verdictBlock
		}
		return verdictAllow
	}
	return verdictBlock
}
