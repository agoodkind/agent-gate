package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"goodkind.io/agent-gate/internal/config"
)

func writeTempConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

// TestLoadParsesInferenceMatrix confirms a config with named inference points and
// a per-rule eval block loads and exposes the parsed values.
func TestLoadParsesInferenceMatrix(t *testing.T) {
	body := `
[inference.mini]
endpoint = "[::1]:5401"
model = "gpt-5.4-mini"
confidence_source = "logprob"

[inference.v4]
endpoint = "[::1]:5401"
model = "agentgate/agent-gate-judge-v4"

[[rules]]
name = "example"
events = ["PreToolUse"]
violation_message = "no"
intent = "Do not write into a protected checkout."

[[rules.conditions]]
kind = "regex"
field_paths = ["tool_input.command"]
pattern = "x"

[[rules.eval]]
kind = "deterministic"
role = "enforce"

[[rules.eval]]
kind = "infer"
role = "enforce"
use = "mini"
on_error = "open"
combine = "union"

[[rules.eval]]
kind = "infer"
role = "verify"
use = "v4"
`
	cfg, err := config.LoadExisting(writeTempConfig(t, body))
	if err != nil {
		t.Fatalf("LoadExisting: %v", err)
	}
	point, ok := cfg.Inference["mini"]
	if !ok {
		t.Fatalf("inference point mini missing; have %v", cfg.Inference)
	}
	if point.Model != "gpt-5.4-mini" || point.ConfidenceSource != config.ConfidenceLogprob {
		t.Fatalf("mini point = %#v", point)
	}
	if len(cfg.Rules) != 1 || len(cfg.Rules[0].Eval) != 3 {
		t.Fatalf("rules/eval = %#v", cfg.Rules)
	}
	enforce := cfg.Rules[0].Eval[1]
	if enforce.Use != "mini" || enforce.Role != config.RoleEnforce || enforce.OnError != config.OnErrorOpen {
		t.Fatalf("enforce eval = %#v", enforce)
	}
	verify := cfg.Rules[0].Eval[2]
	if verify.Use != "v4" || verify.Role != config.RoleVerify {
		t.Fatalf("verify eval = %#v", verify)
	}
	if _, ok := cfg.Rules[0].EvalInference["mini"]; !ok {
		t.Fatalf("EvalInference missing mini: %#v", cfg.Rules[0].EvalInference)
	}
	if _, ok := cfg.Rules[0].EvalInference["v4"]; !ok {
		t.Fatalf("EvalInference missing v4: %#v", cfg.Rules[0].EvalInference)
	}
}

// TestLoadRejectsBadInferenceMatrix pins the validation errors so a malformed
// schema fails the load (and therefore a hot reload) rather than passing silently.
func TestLoadRejectsBadInferenceMatrix(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{
			name: "point missing endpoint",
			body: `[inference.local]
model = "m"`,
			want: "endpoint is required",
		},
		{
			name: "point missing model",
			body: `[inference.local]
endpoint = "[::1]:5401"`,
			want: "model is required",
		},
		{
			name: "bad confidence source",
			body: `[inference.local]
endpoint = "[::1]:5401"
model = "m"
confidence_source = "vibes"`,
			want: "confidence_source",
		},
		{
			name: "threshold out of range",
			body: `[inference.local]
endpoint = "[::1]:5401"
model = "m"
confidence_threshold = 1.5`,
			want: "confidence_threshold",
		},
		{
			name: "eval bad kind",
			body: `[[rules]]
name = "r"
[[rules.eval]]
kind = "psychic"
role = "enforce"`,
			want: "kind",
		},
		{
			name: "eval bad role",
			body: `[[rules]]
name = "r"
[[rules.eval]]
kind = "deterministic"
role = "vibe"`,
			want: "role",
		},
		{
			name: "infer without use",
			body: `[[rules]]
name = "r"
[[rules.eval]]
kind = "infer"
role = "enforce"`,
			want: "requires use",
		},
		{
			name: "use references unknown point",
			body: `[[rules]]
name = "r"
[[rules.eval]]
kind = "infer"
role = "enforce"
use = "ghost"`,
			want: "not a declared inference point",
		},
		{
			name: "deterministic with use",
			body: `[inference.local]
endpoint = "[::1]:5401"
model = "m"
[[rules]]
name = "r"
[[rules.eval]]
kind = "deterministic"
role = "enforce"
use = "local"`,
			want: "does not accept use",
		},
		{
			name: "deterministic with fanout",
			body: `[[rules]]
name = "r"
[[rules.eval]]
kind = "deterministic"
role = "enforce"
fanout = "batch"`,
			want: "does not accept use, fanout, or on_error",
		},
		{
			name: "output_field source without confidence_field",
			body: `[inference.local]
endpoint = "[::1]:5401"
model = "m"
confidence_source = "output_field"`,
			want: "confidence_field is required",
		},
		{
			name: "invalid context_on_error",
			body: `[inference.local]
endpoint = "[::1]:5401"
model = "m"
context_endpoint = "[::1]:5402"
context_workspace_field = "cwd"
context_on_error = "sometimes"`,
			want: "context_on_error",
		},
		{
			name: "negative context_turn_budget",
			body: `[inference.local]
endpoint = "[::1]:5401"
model = "m"
context_turn_budget = -1`,
			want: "context_turn_budget",
		},
		{
			name: "deterministic eval without conditions",
			body: `[[rules]]
name = "r"
events = ["PreToolUse"]
pattern = "x"
[[rules.eval]]
kind = "deterministic"
role = "enforce"`,
			want: "requires the rule to declare conditions",
		},
		{
			name: "infer without intent",
			body: `[inference.local]
endpoint = "[::1]:5401"
model = "m"
[[rules]]
name = "r"
[[rules.eval]]
kind = "infer"
role = "enforce"
use = "local"`,
			want: "requires intent",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := config.LoadExisting(writeTempConfig(t, testCase.body))
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", testCase.want)
			}
			if !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("error %q does not contain %q", err.Error(), testCase.want)
			}
		})
	}
}

// TestLoadWithoutEvalUnchanged confirms a rule with no eval block still loads, so
// the schema addition is inert for existing configs.
func TestLoadWithoutEvalUnchanged(t *testing.T) {
	body := `
[[rules]]
name = "plain"
events = ["PreToolUse"]
field_paths = ["tool_input.command"]
pattern = "rm -rf"
action = "block"
violation_message = "no"
`
	cfg, err := config.LoadExisting(writeTempConfig(t, body))
	if err != nil {
		t.Fatalf("LoadExisting: %v", err)
	}
	if len(cfg.Rules) != 1 || len(cfg.Rules[0].Eval) != 0 {
		t.Fatalf("rules = %#v", cfg.Rules)
	}
}

// TestLoadParsesFallbackRole confirms a judge-authoritative rule loads: an infer
// enforce evaluator plus a deterministic fallback evaluator that decides only when
// the judge call errors.
func TestLoadParsesFallbackRole(t *testing.T) {
	body := `
[inference.mini]
endpoint = "[::1]:5401"
model = "gpt-5.4-mini"

[[rules]]
name = "judge-authoritative"
events = ["PreToolUse"]
violation_message = "no"
intent = "Do not write into a protected checkout."

[[rules.conditions]]
kind = "regex"
field_paths = ["tool_input.command"]
pattern = "x"

[[rules.eval]]
kind = "infer"
role = "enforce"
use = "mini"

[[rules.eval]]
kind = "deterministic"
role = "fallback"
`
	cfg, err := config.LoadExisting(writeTempConfig(t, body))
	if err != nil {
		t.Fatalf("LoadExisting: %v", err)
	}
	if len(cfg.Rules) != 1 || len(cfg.Rules[0].Eval) != 2 {
		t.Fatalf("rules/eval = %#v", cfg.Rules)
	}
	fallback := cfg.Rules[0].Eval[1]
	if fallback.Role != config.RoleFallback || fallback.Kind != config.EvalKindDeterministic {
		t.Fatalf("fallback eval = %#v", fallback)
	}
}
