package rules_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/rules"
)

func loadRuleConfig(t *testing.T, body string) *config.Config {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := config.LoadExisting(path)
	if err != nil {
		t.Fatalf("LoadExisting: %v", err)
	}
	return cfg
}

// TestEvalMatrixRoutingThroughEngine confirms a rule that declares an evaluator
// matrix routes through it in EvaluateAll, and that the declared role decides:
// an enforce deterministic evaluator blocks a matching command, a verify
// evaluator records without enforcing, and a non-matching command does not block.
func TestEvalMatrixRoutingThroughEngine(t *testing.T) {
	const enforceConfig = `
[[rules]]
name = "matrix-enforce"
events = ["Stop"]
action = "block"
violation_message = "blocked by matrix"
[[rules.conditions]]
kind = "regex"
field_paths = ["tool_input.command", "command"]
pattern = "SECRET"
[[rules.eval]]
kind = "deterministic"
role = "enforce"
`
	const verifyConfig = `
[[rules]]
name = "matrix-verify"
events = ["Stop"]
action = "block"
violation_message = "blocked by matrix"
[[rules.conditions]]
kind = "regex"
field_paths = ["tool_input.command", "command"]
pattern = "SECRET"
[[rules.eval]]
kind = "deterministic"
role = "verify"
`
	cases := []struct {
		name      string
		config    string
		command   string
		wantBlock bool
		wantRule  string
	}{
		{name: "enforce blocks matching", config: enforceConfig, command: "echo SECRET", wantBlock: true, wantRule: "matrix-enforce"},
		{name: "enforce allows non-matching", config: enforceConfig, command: "echo hello", wantBlock: false, wantRule: ""},
		{name: "verify does not enforce", config: verifyConfig, command: "echo SECRET", wantBlock: false, wantRule: ""},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			cfg := loadRuleConfig(t, testCase.config)
			payload := map[string]any{"command": testCase.command}
			violations := rules.EvaluateAll(
				context.Background(), "claude", "Stop", testFields(payload), cfg.Rules, nil,
			)
			if testCase.wantBlock {
				if len(violations) == 0 {
					t.Fatalf("expected a violation, got none")
				}
				if violations[0].RuleName != testCase.wantRule {
					t.Fatalf("rule = %q, want %q", violations[0].RuleName, testCase.wantRule)
				}
				return
			}
			if len(violations) != 0 {
				t.Fatalf("expected no violation, got %d: %+v", len(violations), violations)
			}
		})
	}
}

// TestEvalMatrixInferFailsClosed confirms that an infer evaluator whose inference
// point is unreachable blocks, so an inference outage cannot silently open the
// guard for a protected write.
func TestEvalMatrixInferFailsClosed(t *testing.T) {
	const failClosedConfig = `
[inference.dead]
endpoint = "[::1]:1"
model = "test-model"

[[rules]]
name = "matrix-infer-failclosed"
events = ["Stop"]
action = "block"
violation_message = "blocked by infer"
intent = "Do not write into a protected checkout."
[[rules.eval]]
kind = "infer"
role = "enforce"
use = "dead"
`
	cfg := loadRuleConfig(t, failClosedConfig)
	payload := map[string]any{"command": "echo anything"}
	violations := rules.EvaluateAll(
		context.Background(), "claude", "Stop", testFields(payload), cfg.Rules, nil,
	)
	if len(violations) == 0 {
		t.Fatalf("expected a fail-closed violation when the inference point is unreachable")
	}
	if violations[0].RuleName != "matrix-infer-failclosed" {
		t.Fatalf("rule = %q, want matrix-infer-failclosed", violations[0].RuleName)
	}
}

// TestEvalMatrixInferRecordsLayer confirms an infer evaluator records an
// inference layer in the decision trace, so the LLM verdict per rule appears in
// gate_evaluation_layers. The point is unreachable here, so the recorded layer
// carries an error status.
func TestEvalMatrixInferRecordsLayer(t *testing.T) {
	const failClosedConfig = `
[inference.dead]
endpoint = "[::1]:1"
model = "test-model"

[[rules]]
name = "matrix-infer-record"
events = ["Stop"]
action = "block"
violation_message = "blocked by infer"
intent = "Do not write into a protected checkout."
[[rules.eval]]
kind = "infer"
role = "enforce"
use = "dead"
`
	cfg := loadRuleConfig(t, failClosedConfig)
	payload := map[string]any{"command": "echo anything"}
	detailed := rules.EvaluateAllDetailed(
		context.Background(), "claude", "Stop", testFields(payload), cfg.Rules, nil, nil, "test",
	)
	var inferLayers []rules.LayerTrace
	for _, layer := range detailed.Trace.Layers {
		if layer.RuleName == "matrix-infer-record" && layer.Kind == "inference" {
			inferLayers = append(inferLayers, layer)
		}
	}
	if len(inferLayers) == 0 {
		t.Fatalf("expected an inference layer for the matrix rule, got layers %+v", detailed.Trace.Layers)
	}
	if inferLayers[0].ServiceName != "inference" {
		t.Fatalf("layer ServiceName = %q, want inference", inferLayers[0].ServiceName)
	}
}
