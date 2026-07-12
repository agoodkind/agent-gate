package rules_test

import (
	"context"
	"encoding/json"
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
	// The evaluation store rejects a layer whose input JSON is not valid, so the
	// recorded inference layer must carry a valid, non-empty input.
	if len(inferLayers[0].InputJSON) == 0 || !json.Valid(inferLayers[0].InputJSON) {
		t.Fatalf("layer InputJSON = %q, want valid non-empty JSON", string(inferLayers[0].InputJSON))
	}
}

// TestEvalMatrixInferUsesToolInputCommand confirms the infer evaluator sends the
// command from ToolInputCommand, where the hook payload carries it, rather than
// the generic Command field that stays empty for a tool call. A regression here
// makes the inference service reject the request as invalid_argument.
func TestEvalMatrixInferUsesToolInputCommand(t *testing.T) {
	const cfgBody = `
[inference.dead]
endpoint = "[::1]:1"
model = "test-model"

[[rules]]
name = "matrix-infer-toolinput"
events = ["Stop"]
action = "block"
violation_message = "blocked by infer"
intent = "Do not write into a protected checkout."
[[rules.eval]]
kind = "infer"
role = "enforce"
use = "dead"
`
	cfg := loadRuleConfig(t, cfgBody)
	const command = "vim -es +source /tmp/x.vim -- config.go"
	// Command carried only in ToolInputCommand, not the generic Command field.
	payload := map[string]any{"tool_input_command": command}
	detailed := rules.EvaluateAllDetailed(
		context.Background(), "claude", "Stop", testFields(payload), cfg.Rules, nil, nil, "test",
	)
	var inferLayer *rules.LayerTrace
	for index := range detailed.Trace.Layers {
		if detailed.Trace.Layers[index].Kind == "inference" {
			inferLayer = &detailed.Trace.Layers[index]
		}
	}
	if inferLayer == nil {
		t.Fatalf("no inference layer recorded")
	}
	var decoded struct {
		Input string `json:"input"`
	}
	if err := json.Unmarshal(inferLayer.InputJSON, &decoded); err != nil {
		t.Fatalf("input JSON unmarshal: %v (raw %q)", err, string(inferLayer.InputJSON))
	}
	if decoded.Input != command {
		t.Fatalf("recorded input = %q, want the ToolInputCommand %q", decoded.Input, command)
	}
}

// TestEvalMatrixInferFailsOpen confirms that an infer evaluator with
// on_error = open allows the command when its inference point is unreachable, so
// an inference outage degrades coverage without blocking work. The deterministic
// evaluators remain the fail-closed backstop.
func TestEvalMatrixInferFailsOpen(t *testing.T) {
	const failOpenConfig = `
[inference.dead]
endpoint = "[::1]:1"
model = "test-model"

[[rules]]
name = "matrix-infer-failopen"
events = ["Stop"]
action = "block"
violation_message = "blocked by infer"
intent = "Do not write into a protected checkout."
[[rules.eval]]
kind = "infer"
role = "enforce"
use = "dead"
on_error = "open"
`
	cfg := loadRuleConfig(t, failOpenConfig)
	payload := map[string]any{"command": "echo anything"}
	violations := rules.EvaluateAll(
		context.Background(), "claude", "Stop", testFields(payload), cfg.Rules, nil,
	)
	if len(violations) != 0 {
		t.Fatalf("expected no violation (fail open), got %d: %+v", len(violations), violations)
	}
}

// TestEvalMatrixRecordsBothInferLayers confirms that an enforce and a verify infer
// evaluator both record an inference layer, so the enforced decider and the
// recorded-only training verdict both appear in the decision trace.
func TestEvalMatrixRecordsBothInferLayers(t *testing.T) {
	const bothConfig = `
[inference.decider]
endpoint = "[::1]:1"
model = "gpt-5.4-mini"

[inference.trainer]
endpoint = "[::1]:1"
model = "agentgate/agent-gate-judge-v4"

[[rules]]
name = "matrix-both"
events = ["Stop"]
action = "block"
violation_message = "blocked"
intent = "Do not write into a protected checkout."
[[rules.eval]]
kind = "infer"
role = "enforce"
use = "decider"
on_error = "open"
[[rules.eval]]
kind = "infer"
role = "verify"
use = "trainer"
`
	cfg := loadRuleConfig(t, bothConfig)
	payload := map[string]any{"command": "echo anything"}
	detailed := rules.EvaluateAllDetailed(
		context.Background(), "claude", "Stop", testFields(payload), cfg.Rules, nil, nil, "test",
	)
	models := make(map[string]bool)
	for _, layer := range detailed.Trace.Layers {
		if layer.RuleName == "matrix-both" && layer.Kind == "inference" {
			models[layer.LayerName] = true
		}
	}
	if !models["gpt-5.4-mini"] || !models["agentgate/agent-gate-judge-v4"] {
		t.Fatalf("expected inference layers for both models, got %+v", models)
	}
}
