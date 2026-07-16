package rules

import (
	"testing"

	"goodkind.io/agent-gate/internal/config"
)

// resolverFromVerdicts returns a resolver that yields a fixed verdict per index
// with no error, and records which indices were actually resolved.
func resolverFromVerdicts(verdicts map[int]evalVerdict, ran *[]int) evaluatorResolver {
	outcomes := make(map[int]evalOutcome, len(verdicts))
	for index, verdict := range verdicts {
		outcomes[index] = evalOutcome{verdict: verdict, errored: false}
	}
	return resolverFromOutcomes(outcomes, ran)
}

// resolverFromOutcomes returns a resolver that yields a fixed outcome per index,
// letting a test set the errored bit as well as the verdict, and records which
// indices were actually resolved. An index absent from the map resolves to a
// non-errored allow.
func resolverFromOutcomes(outcomes map[int]evalOutcome, ran *[]int) evaluatorResolver {
	return func(index int, _ config.RuleEval) evalOutcome {
		*ran = append(*ran, index)
		outcome, ok := outcomes[index]
		if !ok {
			outcome = evalOutcome{verdict: verdictAllow, errored: false}
		}
		return outcome
	}
}

func detEval(role string) config.RuleEval {
	return config.RuleEval{Kind: config.EvalKindDeterministic, Role: role, Use: "", Fanout: "", Combine: "", OnError: ""}
}

func inferEval(role, combine string) config.RuleEval {
	return config.RuleEval{Kind: config.EvalKindInfer, Role: role, Use: "local", Fanout: "", Combine: combine, OnError: ""}
}

// blocked builds a non-errored blocking outcome.
func blocked() evalOutcome {
	return evalOutcome{verdict: verdictBlock, errored: false}
}

// allowed builds a non-errored allowing outcome.
func allowed() evalOutcome {
	return evalOutcome{verdict: verdictAllow, errored: false}
}

// erroredAs builds an errored outcome carrying its on_error-converted verdict.
func erroredAs(verdict evalVerdict) evalOutcome {
	return evalOutcome{verdict: verdict, errored: true}
}

// TestRunEvalMatrixModes covers the enforce/verify interaction modes. None of
// these rules declares a fallback evaluator, so they must fold exactly as before
// the fallback role existed.
func TestRunEvalMatrixModes(t *testing.T) {
	cases := []struct {
		name      string
		evals     []config.RuleEval
		verdicts  map[int]evalVerdict
		wantBlock bool
		wantRan   []int
	}{
		{
			name:      "llm verifies in parallel: deterministic decides",
			evals:     []config.RuleEval{detEval(config.RoleEnforce), inferEval(config.RoleVerify, "")},
			verdicts:  map[int]evalVerdict{0: verdictBlock, 1: verdictAllow},
			wantBlock: true,
			wantRan:   []int{0, 1},
		},
		{
			name:      "llm verifies in parallel: infer disagreement does not enforce",
			evals:     []config.RuleEval{detEval(config.RoleEnforce), inferEval(config.RoleVerify, "")},
			verdicts:  map[int]evalVerdict{0: verdictAllow, 1: verdictBlock},
			wantBlock: false,
			wantRan:   []int{0, 1},
		},
		{
			name:      "llm blocks",
			evals:     []config.RuleEval{inferEval(config.RoleEnforce, "")},
			verdicts:  map[int]evalVerdict{0: verdictBlock},
			wantBlock: true,
			wantRan:   []int{0},
		},
		{
			name:      "deterministic verifies, llm blocks",
			evals:     []config.RuleEval{detEval(config.RoleVerify), inferEval(config.RoleEnforce, "")},
			verdicts:  map[int]evalVerdict{0: verdictBlock, 1: verdictAllow},
			wantBlock: false,
			wantRan:   []int{0, 1},
		},
		{
			name:      "union: either enforcing blocks",
			evals:     []config.RuleEval{detEval(config.RoleEnforce), inferEval(config.RoleEnforce, config.CombineUnion)},
			verdicts:  map[int]evalVerdict{0: verdictAllow, 1: verdictBlock},
			wantBlock: true,
			wantRan:   []int{0, 1},
		},
		{
			name:      "and: blocks only when all enforcing block",
			evals:     []config.RuleEval{detEval(config.RoleEnforce), inferEval(config.RoleEnforce, config.CombineAnd)},
			verdicts:  map[int]evalVerdict{0: verdictBlock, 1: verdictAllow},
			wantBlock: false,
			wantRan:   []int{0, 1},
		},
		{
			name:      "and: blocks when all enforcing block",
			evals:     []config.RuleEval{detEval(config.RoleEnforce), inferEval(config.RoleEnforce, config.CombineAnd)},
			verdicts:  map[int]evalVerdict{0: verdictBlock, 1: verdictBlock},
			wantBlock: true,
			wantRan:   []int{0, 1},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			var ran []int
			decision := runEvalMatrix(testCase.evals, resolverFromVerdicts(testCase.verdicts, &ran))
			if decision.block != testCase.wantBlock {
				t.Fatalf("block = %v, want %v", decision.block, testCase.wantBlock)
			}
			if !equalInts(ran, testCase.wantRan) {
				t.Fatalf("resolved indices = %v, want %v", ran, testCase.wantRan)
			}
		})
	}
}

// TestRunEvalMatrixJudgeAuthoritative covers Mode A: one infer enforce evaluator
// (the judge) plus one deterministic fallback evaluator, plus an infer verify
// evaluator for the recorded second model. The judge decides in both directions
// while it is up, and the deterministic fallback decides only when the judge call
// errors.
func TestRunEvalMatrixJudgeAuthoritative(t *testing.T) {
	// index 0: infer enforce (the judge)
	// index 1: deterministic fallback
	// index 2: infer verify (records only, never decides)
	evals := []config.RuleEval{
		inferEval(config.RoleEnforce, config.CombineUnion),
		detEval(config.RoleFallback),
		inferEval(config.RoleVerify, ""),
	}
	cases := []struct {
		name      string
		outcomes  map[int]evalOutcome
		wantBlock bool
	}{
		{
			name:      "judge up allows: overrides deterministic block",
			outcomes:  map[int]evalOutcome{0: allowed(), 1: blocked(), 2: blocked()},
			wantBlock: false,
		},
		{
			name:      "judge up blocks: overrides deterministic allow",
			outcomes:  map[int]evalOutcome{0: blocked(), 1: allowed(), 2: allowed()},
			wantBlock: true,
		},
		{
			name:      "judge errored: deterministic fallback blocks",
			outcomes:  map[int]evalOutcome{0: erroredAs(verdictAllow), 1: blocked(), 2: allowed()},
			wantBlock: true,
		},
		{
			name:      "judge errored: deterministic fallback allows",
			outcomes:  map[int]evalOutcome{0: erroredAs(verdictBlock), 1: allowed(), 2: blocked()},
			wantBlock: false,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			var ran []int
			decision := runEvalMatrix(evals, resolverFromOutcomes(testCase.outcomes, &ran))
			if decision.block != testCase.wantBlock {
				t.Fatalf("block = %v, want %v", decision.block, testCase.wantBlock)
			}
			if !equalInts(ran, []int{0, 1, 2}) {
				t.Fatalf("resolved indices = %v, want [0 1 2]", ran)
			}
		})
	}
}

// TestRunEvalMatrixHardSafetyUnion covers Mode B: a deterministic enforce
// evaluator and an infer enforce evaluator joined by union, with the judge failing
// open and no fallback evaluator. This mode needs no fallback code; the fold must
// keep the deterministic evaluator protecting even when the judge errors.
func TestRunEvalMatrixHardSafetyUnion(t *testing.T) {
	// index 0: deterministic enforce
	// index 1: infer enforce, combine=union, on_error=open
	evals := []config.RuleEval{
		detEval(config.RoleEnforce),
		inferEval(config.RoleEnforce, config.CombineUnion),
	}
	cases := []struct {
		name      string
		outcomes  map[int]evalOutcome
		wantBlock bool
	}{
		{
			name:      "judge allows, deterministic blocks: union blocks",
			outcomes:  map[int]evalOutcome{0: blocked(), 1: allowed()},
			wantBlock: true,
		},
		{
			name:      "judge blocks, deterministic allows: union blocks",
			outcomes:  map[int]evalOutcome{0: allowed(), 1: blocked()},
			wantBlock: true,
		},
		{
			name:      "judge errored fails open, deterministic blocks: still blocks",
			outcomes:  map[int]evalOutcome{0: blocked(), 1: erroredAs(verdictAllow)},
			wantBlock: true,
		},
		{
			name:      "both allow",
			outcomes:  map[int]evalOutcome{0: allowed(), 1: allowed()},
			wantBlock: false,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			var ran []int
			decision := runEvalMatrix(evals, resolverFromOutcomes(testCase.outcomes, &ran))
			if decision.block != testCase.wantBlock {
				t.Fatalf("block = %v, want %v", decision.block, testCase.wantBlock)
			}
			if !equalInts(ran, []int{0, 1}) {
				t.Fatalf("resolved indices = %v, want [0 1]", ran)
			}
		})
	}
}

// TestRunEvalMatrixAllEnforceErroredNoFallback confirms that when every enforce
// evaluator errored and no fallback evaluator exists, the fold decides by the
// enforce evaluators' on_error-converted verdicts, unchanged from before the
// fallback role existed.
func TestRunEvalMatrixAllEnforceErroredNoFallback(t *testing.T) {
	evals := []config.RuleEval{inferEval(config.RoleEnforce, config.CombineUnion)}
	cases := []struct {
		name      string
		outcome   evalOutcome
		wantBlock bool
	}{
		{name: "errored converts to block", outcome: erroredAs(verdictBlock), wantBlock: true},
		{name: "errored converts to allow", outcome: erroredAs(verdictAllow), wantBlock: false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			var ran []int
			decision := runEvalMatrix(evals, resolverFromOutcomes(map[int]evalOutcome{0: testCase.outcome}, &ran))
			if decision.block != testCase.wantBlock {
				t.Fatalf("block = %v, want %v", decision.block, testCase.wantBlock)
			}
		})
	}
}

// TestRunEvalMatrixPartialEnforceErrorSkipsFallback guards the allErrored logic
// against a future "any errored" regression. Two enforce entries share a rule and
// exactly one errors, with a deterministic fallback present. Because the surviving
// enforce entry can still decide, the fold must combine the enforce entries and
// never consult the fallback. The errored entry fails open to allow and the
// surviving entry allows, so a correct fold allows, while a regression that used
// the fallback would block, making wantBlock=false the tight assertion.
func TestRunEvalMatrixPartialEnforceErrorSkipsFallback(t *testing.T) {
	// index 0: infer enforce, errored, on_error open -> allow
	// index 1: infer enforce, up, allows
	// index 2: deterministic fallback that would block if wrongly consulted
	evals := []config.RuleEval{
		inferEval(config.RoleEnforce, config.CombineUnion),
		inferEval(config.RoleEnforce, config.CombineUnion),
		detEval(config.RoleFallback),
	}
	outcomes := map[int]evalOutcome{
		0: erroredAs(verdictAllow),
		1: allowed(),
		2: blocked(),
	}
	var ran []int
	decision := runEvalMatrix(evals, resolverFromOutcomes(outcomes, &ran))
	if decision.block {
		t.Fatal("block = true, want false: a surviving enforce entry must decide, not the fallback")
	}
	if !equalInts(ran, []int{0, 1, 2}) {
		t.Fatalf("resolved indices = %v, want [0 1 2]", ran)
	}
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
