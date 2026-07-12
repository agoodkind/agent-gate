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
[inference.local]
endpoint = "[::1]:5401"
model = "agentgate/agent-gate-judge-v4"
confidence_source = "output_field"
confidence_field = "confidence"
confidence_threshold = 0.7

[inference.escalation]
endpoint = "[::1]:5401"
model = "gpt-5.4-mini"
confidence_source = "logprob"

[[rules]]
name = "example"
events = ["PreToolUse"]
violation_message = "no"

[[rules.eval]]
kind = "deterministic"
role = "verify"

[[rules.eval]]
kind = "infer"
role = "enforce"
use = "local"
escalate_to = "escalation"
fanout = "batch"
combine = "union"
`
	cfg, err := config.LoadExisting(writeTempConfig(t, body))
	if err != nil {
		t.Fatalf("LoadExisting: %v", err)
	}
	point, ok := cfg.Inference["local"]
	if !ok {
		t.Fatalf("inference point local missing; have %v", cfg.Inference)
	}
	if point.Model != "agentgate/agent-gate-judge-v4" || point.ConfidenceThreshold != 0.7 {
		t.Fatalf("local point = %#v", point)
	}
	if len(cfg.Rules) != 1 || len(cfg.Rules[0].Eval) != 2 {
		t.Fatalf("rules/eval = %#v", cfg.Rules)
	}
	infer := cfg.Rules[0].Eval[1]
	if infer.Use != "local" || infer.EscalateTo != "escalation" || infer.Combine != config.CombineUnion {
		t.Fatalf("infer eval = %#v", infer)
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
