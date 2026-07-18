package rules

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/pipeline"
	"goodkind.io/gksyntax/shelldecomp"
)

// evalVerdict is a single evaluator's decision within a rule's evaluation matrix.
type evalVerdict int

const (
	// verdictAllow means the evaluator did not find a violation.
	verdictAllow evalVerdict = iota
	// verdictBlock means the evaluator found a violation.
	verdictBlock
)

// evalOutcome is one evaluator's resolved verdict plus whether the underlying call
// errored. verdict carries the on_error-converted verdict, the same value the fold
// used before the fallback role existed. errored records whether the call failed,
// which a deterministic evaluator never does, so the fold can switch to a fallback
// evaluator only when every enforce evaluator errored.
type evalOutcome struct {
	verdict evalVerdict
	errored bool
}

// evaluatorResolver runs the declared evaluator at index and returns its outcome.
// The engine supplies this: a deterministic entry runs the rule's condition
// block, an infer entry calls the named inference point.
type evaluatorResolver func(index int, eval config.RuleEval) evalOutcome

// matrixDecision is the enforced decision for a rule's evaluator matrix.
type matrixDecision struct {
	block bool
}

// runEvalMatrix executes a rule's declared evaluators in order and returns the
// enforced decision. It honors each entry's role: an enforce entry participates
// in the decision, a verify entry runs but never decides, and a fallback entry
// decides only when every enforce entry errored. Enforcing entries are joined by
// the combine operator. Union, the default, blocks when any joins evaluator
// blocks. The and operator blocks only when every joined evaluator blocks.
func runEvalMatrix(evals []config.RuleEval, resolve evaluatorResolver) matrixDecision {
	outcomes := make([]evalOutcome, len(evals))
	for index := range evals {
		outcomes[index] = resolve(index, evals[index])
	}
	return matrixDecision{block: foldMatrixOutcomes(evals, outcomes)}
}

// foldMatrixOutcomes decides the rule from the resolved evaluator outcomes. The
// enforce entries decide unless every one of them errored and a fallback entry
// exists, in which case the deterministic fallback entries decide. With no enforce
// entry the rule does not block. Verify entries never decide.
func foldMatrixOutcomes(evals []config.RuleEval, outcomes []evalOutcome) bool {
	combine := matrixCombineOperator(evals)
	enforce := indicesForRole(evals, config.RoleEnforce)
	if len(enforce) == 0 {
		return false
	}
	if allErrored(enforce, outcomes) {
		fallback := indicesForRole(evals, config.RoleFallback)
		if len(fallback) > 0 {
			return combineBlocks(combine, fallback, outcomes)
		}
	}
	return combineBlocks(combine, enforce, outcomes)
}

// indicesForRole returns the evaluator indices whose role matches, preserving
// declaration order.
func indicesForRole(evals []config.RuleEval, role string) []int {
	var indices []int
	for index := range evals {
		if evals[index].Role == role {
			indices = append(indices, index)
		}
	}
	return indices
}

// allErrored reports whether every listed evaluator errored. It is only called
// with a non-empty index list, so it never reports true for zero evaluators.
func allErrored(indices []int, outcomes []evalOutcome) bool {
	for _, index := range indices {
		if !outcomes[index].errored {
			return false
		}
	}
	return true
}

// combineBlocks applies the combine operator to the listed evaluators' verdicts.
func combineBlocks(combine string, indices []int, outcomes []evalOutcome) bool {
	blockingCount := 0
	for _, index := range indices {
		if outcomes[index].verdict == verdictBlock {
			blockingCount++
		}
	}
	return matrixBlocks(combine, len(indices), blockingCount)
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
	inferOutcomes := e.runInferConcurrently(ctx)
	decision := runEvalMatrix(e.rule.Eval, func(index int, eval config.RuleEval) evalOutcome {
		if eval.Kind == config.EvalKindInfer {
			return inferOutcomes[index]
		}
		if allConditionsMatch(ctx, *e.fields, e.rule, e.rule.Conditions) {
			return evalOutcome{verdict: verdictBlock, errored: false}
		}
		return evalOutcome{verdict: verdictAllow, errored: false}
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

// commandTouchesFiles reports whether the command resolves to at least one file read
// or write target, decomposed with gksyntax and including targets inside embedded
// regions (a heredoc body, a -c script). The judge runs only when it does, so a
// command that operates on no files (a pure pipeline, a no-file command, or an opaque
// command with no resolvable target) never incurs the judge's latency or
// false-positive risk. An empty command is treated as touching no files.
func (e *evalMatrixCondition) commandTouchesFiles() bool {
	command := e.fields.CommandValue()
	if command == "" {
		return false
	}
	return decompositionTouchesFiles(shelldecomp.Parse(command, e.fields.BaseCWD(), ""))
}

// decompositionTouchesFiles reports whether a decomposition, or any of its embedded
// regions, carries a read or write target.
func decompositionTouchesFiles(decomposition *shelldecomp.Decomposition) bool {
	if decomposition == nil {
		return false
	}
	if len(decomposition.ReadTargets()) > 0 || len(decomposition.WriteTargets()) > 0 {
		return true
	}
	for _, region := range decomposition.EmbeddedRegions() {
		if region.Parsed != nil && decompositionTouchesFiles(region.Parsed) {
			return true
		}
	}
	return false
}

// runInferConcurrently evaluates every infer entry against its inference point at
// once and returns each entry's verdict by index. Running the entries together
// keeps the hot-path latency near the slowest call rather than their sum, so the
// recorded-only v4 verify entry does not add to the time the enforcing mini entry
// already takes. Each call records its own trace layer, and recordPointLayer is
// safe to call from several goroutines.
func (e *evalMatrixCondition) runInferConcurrently(ctx context.Context) map[int]evalOutcome {
	outcomes := make(map[int]evalOutcome)
	// When the rule opts into file-scoped judging, scope the synchronous judge to
	// commands that touch concrete files. A command with no resolved file read or
	// write target (a pure pipeline, a no-file command, or an opaque command) is not
	// judged: the judge would add hot-path latency and a false-positive risk to a
	// command that does not operate on files, while still seeing the non-grep file
	// operations (awk, cat, sed, find -exec) that the deterministic patterns miss. A
	// skipped infer entry counts as allow, so the rule falls to its deterministic
	// evaluators, which still run for every command.
	if e.rule.JudgeFileScope && !e.commandTouchesFiles() {
		for index := range e.rule.Eval {
			if e.rule.Eval[index].Kind == config.EvalKindInfer {
				outcomes[index] = evalOutcome{verdict: verdictAllow, errored: false}
			}
		}
		return outcomes
	}
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
			// Default the outcome to errored with the entry's on-error verdict so a
			// panic before resolveInferSingle completes respects on_error, failing
			// closed unless the entry opts into fail open, and counts as an error for
			// the fallback fold.
			outcome := evalOutcome{verdict: verdictBlock, errored: true}
			if eval.OnError == config.OnErrorOpen {
				outcome.verdict = verdictAllow
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
				outcomes[index] = outcome
				mu.Unlock()
			}()
			outcome = e.resolveInferSingle(ctx, index, eval)
		}(index, eval)
	}
	wg.Wait()
	return outcomes
}

// resolveInferSingle routes the command to the evaluator's inference point, judges
// it against the rule's intent, and records the call as a trace layer. On an
// inference failure it fails open when the entry sets on_error = open, and fails
// closed otherwise. The deterministic evaluators stay the fail-closed backstop, so
// a fail-open infer entry only widens coverage without dropping the guard for a
// command a deterministic evaluator already blocks.
func (e *evalMatrixCondition) resolveInferSingle(ctx context.Context, index int, eval config.RuleEval) evalOutcome {
	point, ok := e.rule.EvalInference[eval.Use]
	if !ok {
		// A missing inference point means the judge cannot run, so the call counts as
		// an error and fails closed, keeping the deterministic backstop in charge.
		return evalOutcome{verdict: verdictBlock, errored: true}
	}
	// A fanout=batch entry reads the decision the per-event batch call already made
	// for this rule, so the rule adds no inference call of its own. When no batch
	// result exists (the planner did not run for this point), it falls back to an
	// individual call so the old path keeps working.
	if eval.Fanout == config.FanoutBatch {
		if verdict, found := batchInferenceMemoFromContext(ctx).verdictFor(point, e.rule.Name); found {
			recordPointLayer(ctx, e.rule.Name, index, *verdict)
			return evalOutcome{verdict: applyPointVerdict(eval, *verdict), errored: verdict.errored}
		}
	}
	runtime := inferRuntimeFromContext(ctx)
	// CommandValue prefers ToolInputCommand, where the hook payload carries the
	// command, over the generic Command field, which stays empty for a tool call.
	// Passing the empty Command made the inference service reject the request as
	// invalid_argument, so the enforcer always errored and fell back to on_error.
	result := runtime.evaluatePoint(ctx, point, e.rule.Intent, e.fields.CommandValue())
	recordPointLayer(ctx, e.rule.Name, index, result)
	return evalOutcome{verdict: applyPointVerdict(eval, result), errored: result.errored}
}

// applyPointVerdict folds one inference verdict into the eval matrix. An errored
// call fails open only when the entry sets on_error = open, keeping the
// deterministic evaluators as the fail-closed backstop.
func applyPointVerdict(eval config.RuleEval, result pointVerdict) evalVerdict {
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
