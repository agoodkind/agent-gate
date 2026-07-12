package rules

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

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
	inferVerdicts := e.runInferConcurrently(ctx)
	decision := runEvalMatrix(e.rule.Eval, func(index int, eval config.RuleEval) evalVerdict {
		if eval.Kind == config.EvalKindInfer {
			return inferVerdicts[index]
		}
		if allConditionsMatch(ctx, *e.fields, e.rule, e.rule.Conditions) {
			return verdictBlock
		}
		return verdictAllow
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

// runInferConcurrently evaluates every infer entry against its inference point at
// once and returns each entry's verdict by index. Running the entries together
// keeps the hot-path latency near the slowest call rather than their sum, so the
// recorded-only v4 verify entry does not add to the time the enforcing mini entry
// already takes. Each call records its own trace layer, and recordPointLayer is
// safe to call from several goroutines.
func (e *evalMatrixCondition) runInferConcurrently(ctx context.Context) map[int]evalVerdict {
	verdicts := make(map[int]evalVerdict)
	var mu sync.Mutex
	var wg sync.WaitGroup
	for index := range e.rule.Eval {
		eval := e.rule.Eval[index]
		if eval.Kind != config.EvalKindInfer {
			continue
		}
		wg.Add(1)
		go func(index int, eval config.RuleEval) {
			defer wg.Done()
			// Default the verdict to the entry's on-error outcome so a panic before
			// resolveInferSingle completes respects on_error, failing closed unless
			// the entry opts into fail open.
			verdict := verdictBlock
			if eval.OnError == config.OnErrorOpen {
				verdict = verdictAllow
			}
			defer func() {
				if recovered := recover(); recovered != nil {
					slog.ErrorContext(
						ctx,
						"eval matrix inference goroutine panicked",
						"err", fmt.Errorf("panic: %v", recovered),
						"rule", e.rule.Name, "index", index,
					)
				}
				mu.Lock()
				verdicts[index] = verdict
				mu.Unlock()
			}()
			verdict = e.resolveInferSingle(ctx, index, eval)
		}(index, eval)
	}
	wg.Wait()
	return verdicts
}

// resolveInferSingle routes the command to the evaluator's inference point, judges
// it against the rule's intent, and records the call as a trace layer. On an
// inference failure it fails open when the entry sets on_error = open, and fails
// closed otherwise. The deterministic evaluators stay the fail-closed backstop, so
// a fail-open infer entry only widens coverage without dropping the guard for a
// command a deterministic evaluator already blocks.
func (e *evalMatrixCondition) resolveInferSingle(ctx context.Context, index int, eval config.RuleEval) evalVerdict {
	point, ok := e.rule.EvalInference[eval.Use]
	if !ok {
		return verdictBlock
	}
	runtime := inferRuntimeFromContext(ctx)
	result := runtime.evaluatePoint(ctx, point, e.rule.Intent, e.fields.Command)
	recordPointLayer(ctx, e.rule.Name, index, result)
	if result.errored {
		if eval.OnError == config.OnErrorOpen {
			return verdictAllow
		}
		return verdictBlock
	}
	if result.block {
		return verdictBlock
	}
	return verdictAllow
}
