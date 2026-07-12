package rules

import (
	"testing"

	"goodkind.io/agent-gate/internal/config"
)

// resolverFromVerdicts returns a resolver that yields a fixed verdict per index,
// and records which indices were actually resolved.
func resolverFromVerdicts(verdicts map[int]evalVerdict, ran *[]int) evaluatorResolver {
	return func(index int, _ config.RuleEval) evalVerdict {
		*ran = append(*ran, index)
		verdict, ok := verdicts[index]
		if !ok {
			verdict = verdictAllow
		}
		return verdict
	}
}

func detEval(role string) config.RuleEval {
	return config.RuleEval{Kind: config.EvalKindDeterministic, Role: role, Use: "", EscalateTo: "", Fanout: "", Combine: ""}
}

func inferEval(role, combine string) config.RuleEval {
	return config.RuleEval{Kind: config.EvalKindInfer, Role: role, Use: "local", EscalateTo: "", Fanout: "", Combine: combine}
}

// TestRunEvalMatrixModes covers the five declared interaction modes.
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
