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
// routes the command to its inference point.
func (e *evalMatrixCondition) resolveEvaluator(ctx context.Context, index int, eval config.RuleEval) evalVerdict {
	if eval.Kind == config.EvalKindDeterministic {
		if allConditionsMatch(ctx, *e.fields, e.rule, e.rule.Conditions) {
			return verdictBlock
		}
		return verdictAllow
	}
	return e.resolveInfer(ctx, index, eval)
}

// resolveInfer routes the command to the evaluator's inference point, judging it
// against the rule's intent, and records each inference call as a trace layer. A
// result whose confidence is below the point's threshold escalates to the
// declared escalation point. Any inference failure fails closed (block), so an
// inference outage cannot silently open the guard for a protected write.
func (e *evalMatrixCondition) resolveInfer(ctx context.Context, index int, eval config.RuleEval) evalVerdict {
	point, ok := e.rule.EvalInference[eval.Use]
	if !ok {
		return verdictBlock
	}
	runtime := inferRuntimeFromContext(ctx)
	input := e.fields.Command
	result := runtime.evaluatePoint(ctx, point, e.rule.Intent, input)
	recordPointLayer(ctx, e.rule.Name, index, result)
	if result.errored {
		return verdictBlock
	}
	if eval.EscalateTo != "" && result.confidencePresent && result.confidence < point.ConfidenceThreshold {
		if escalated, present := e.rule.EvalInference[eval.EscalateTo]; present {
			escalatedResult := runtime.evaluatePoint(ctx, escalated, e.rule.Intent, input)
			recordPointLayer(ctx, e.rule.Name, index+escalationTraceOffset, escalatedResult)
			if escalatedResult.errored {
				return verdictBlock
			}
			result = escalatedResult
		}
	}
	if result.block {
		return verdictBlock
	}
	return verdictAllow
}
