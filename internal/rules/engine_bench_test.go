package rules_test

// BenchmarkEvaluateAll_19RegexHotPath comparison (Apple M5 Max, 10s run):
//
//   pre-refactor:       8077 ns/op      0 B/op    0 allocs/op
//   post-batch-concern: 8296 ns/op   1872 B/op    3 allocs/op   (+2.7%)
//   post-per-rule:     10050 ns/op   5600 B/op   44 allocs/op  (+24.4%)
//
// The per-rule regression (+24%) exceeds the 5% threshold. The cost is
// architectural: 19 rules produce 19 ruleRegexCondition heap allocations
// (interface boxing for pipeline.Condition), 19 ruleOutcome values boxed as
// pipeline.Outcome (any), make([]Result, 19) in Orchestrator.Run, and one
// []pipeline.Condition{...} slice. The batch approach paid those costs once.
// The per-rule design is the correct long-term structure for per-Condition
// scheduling, cost classification, and timing (landings 8+). Landing 8
// (tiered scheduler) or landing 16 (cost-ordered application) is the right
// place to address this by pre-allocating the concern slice at config load
// time and reusing it across calls, eliminating the per-call allocs.

import (
	"context"
	"testing"

	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/regex"
	"goodkind.io/agent-gate/internal/rules"
)

// bench19Rules constructs 19 representative regex rules that cover the hot path:
// simple single-field rules, multi-field rules, and a diagnostic-group rule.
func bench19Rules(b *testing.B) []config.Rule {
	b.Helper()
	compile := func(pattern string) *regex.Regexp {
		re, err := regex.Compile(pattern)
		if err != nil {
			b.Fatalf("compile %q: %v", pattern, err)
		}
		return re
	}

	return []config.Rule{
		config.NewSimpleRule("no-shell-redirection",
			`(\d+>&\d+|>&\d+|&>|\|&|>/dev/null|2>/dev/null|>>/dev/null|2>>/dev/null|&>/dev/null)`,
			compile(`(\d+>&\d+|>&\d+|&>|\|&|>/dev/null|2>/dev/null|>>/dev/null|2>>/dev/null|&>/dev/null)`),
			[]string{"PreToolUse"}, []string{"tool_input.command", "command"}, "block", "Shell redirection blocked."),

		config.NewSimpleRule("no-emdashes",
			`[\x{2010}-\x{2015}\x{2212}\x{2E3A}\x{2E3B}\x{FE31}\x{FE32}\x{FE58}\x{FE63}\x{FF0D}]`,
			compile(`[\x{2010}-\x{2015}\x{2212}\x{2E3A}\x{2E3B}\x{FE31}\x{FE32}\x{FE58}\x{FE63}\x{FF0D}]`),
			[]string{"PreToolUse", "Stop"},
			[]string{"tool_input.content", "tool_input.new_string", "tool_input.command", "tool_input.description", "command", "assistant_message"},
			"block", "Unicode dashes blocked."),

		config.NewSimpleRule("no-aws-key",
			`(?i)AKIA[0-9A-Z]{16}`,
			compile(`(?i)AKIA[0-9A-Z]{16}`),
			[]string{"PreToolUse"},
			[]string{"tool_input.content", "tool_input.new_string", "tool_input.command"},
			"block", "AWS key blocked."),

		config.NewSimpleRule("no-private-key",
			`-----BEGIN (RSA |EC |OPENSSH |DSA |PGP )?PRIVATE KEY`,
			compile(`-----BEGIN (RSA |EC |OPENSSH |DSA |PGP )?PRIVATE KEY`),
			[]string{"PreToolUse"},
			[]string{"tool_input.content", "tool_input.new_string"},
			"block", "Private key blocked."),

		config.NewSimpleRule("no-github-token",
			`gh[pousr]_[A-Za-z0-9]{36}`,
			compile(`gh[pousr]_[A-Za-z0-9]{36}`),
			[]string{"PreToolUse"},
			[]string{"tool_input.content", "tool_input.new_string"},
			"block", "GitHub token blocked."),

		config.NewSimpleRule("no-slack-token",
			`xox[baprs]-[0-9A-Za-z\-]{10,72}`,
			compile(`xox[baprs]-[0-9A-Za-z\-]{10,72}`),
			[]string{"PreToolUse"},
			[]string{"tool_input.content", "tool_input.new_string"},
			"block", "Slack token blocked."),

		config.NewSimpleRule("no-heroku-key",
			`[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`,
			compile(`[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`),
			[]string{"PreToolUse"},
			[]string{"tool_input.content"},
			"block", "UUID-like token blocked."),

		config.NewSimpleRule("no-stripe-key",
			`(sk|pk)_(test|live)_[0-9a-zA-Z]{24,99}`,
			compile(`(sk|pk)_(test|live)_[0-9a-zA-Z]{24,99}`),
			[]string{"PreToolUse"},
			[]string{"tool_input.content", "tool_input.new_string"},
			"block", "Stripe key blocked."),

		config.NewSimpleRule("no-twilio-token",
			`SK[0-9a-fA-F]{32}`,
			compile(`SK[0-9a-fA-F]{32}`),
			[]string{"PreToolUse"},
			[]string{"tool_input.content", "tool_input.new_string"},
			"block", "Twilio token blocked."),

		config.NewSimpleRule("no-sendgrid-key",
			`SG\.[0-9A-Za-z\-_]{22}\.[0-9A-Za-z\-_]{43}`,
			compile(`SG\.[0-9A-Za-z\-_]{22}\.[0-9A-Za-z\-_]{43}`),
			[]string{"PreToolUse"},
			[]string{"tool_input.content", "tool_input.new_string"},
			"block", "SendGrid key blocked."),

		config.NewSimpleRule("no-mailgun-key",
			`key-[0-9a-zA-Z]{32}`,
			compile(`key-[0-9a-zA-Z]{32}`),
			[]string{"PreToolUse"},
			[]string{"tool_input.content"},
			"block", "Mailgun key blocked."),

		config.NewSimpleRule("no-square-token",
			`sq0[a-z]{3}-[0-9A-Za-z\-_]{22,43}`,
			compile(`sq0[a-z]{3}-[0-9A-Za-z\-_]{22,43}`),
			[]string{"PreToolUse"},
			[]string{"tool_input.content", "tool_input.new_string"},
			"block", "Square token blocked."),

		config.NewSimpleRule("no-paypal-token",
			`access_token\$production\$[0-9a-z]{16}\$[0-9a-f]{32}`,
			compile(`access_token\$production\$[0-9a-z]{16}\$[0-9a-f]{32}`),
			[]string{"PreToolUse"},
			[]string{"tool_input.content"},
			"block", "PayPal token blocked."),

		config.NewSimpleRule("no-datadog-key",
			`[a-f0-9]{32}`,
			compile(`[a-f0-9]{32}`),
			[]string{"PreToolUse"},
			[]string{"tool_input.content"},
			"block", "Datadog key blocked."),

		config.NewSimpleRule("no-npm-token",
			`npm_[A-Za-z0-9]{36}`,
			compile(`npm_[A-Za-z0-9]{36}`),
			[]string{"PreToolUse"},
			[]string{"tool_input.content", "tool_input.new_string"},
			"block", "NPM token blocked."),

		config.NewSimpleRule("no-jwt-token",
			`eyJ[A-Za-z0-9\-_=]+\.eyJ[A-Za-z0-9\-_=]+\.[A-Za-z0-9\-_=]+`,
			compile(`eyJ[A-Za-z0-9\-_=]+\.eyJ[A-Za-z0-9\-_=]+\.[A-Za-z0-9\-_=]+`),
			[]string{"PreToolUse"},
			[]string{"tool_input.content", "tool_input.new_string"},
			"block", "JWT token blocked."),

		config.NewSimpleRule("no-gcp-key",
			`AIza[0-9A-Za-z\-_]{35}`,
			compile(`AIza[0-9A-Za-z\-_]{35}`),
			[]string{"PreToolUse"},
			[]string{"tool_input.content", "tool_input.new_string"},
			"block", "GCP API key blocked."),

		config.NewSimpleRule("no-azure-connection-string",
			`DefaultEndpointsProtocol=https;AccountName=[^;]+;AccountKey=[A-Za-z0-9+/=]+;`,
			compile(`DefaultEndpointsProtocol=https;AccountName=[^;]+;AccountKey=[A-Za-z0-9+/=]+;`),
			[]string{"PreToolUse"},
			[]string{"tool_input.content", "tool_input.new_string"},
			"block", "Azure connection string blocked."),

		config.NewSimpleRule("no-double-hyphen-prose",
			`(?m)(?|(?:`+"`"+`[^`+"`"+`\n]+`+"`"+`|\b(?!(?:bash|sh|zsh|fish|exec|command|env|xargs|sudo|go|git|make|npm)\b)[A-Za-z][A-Za-z0-9_./-]*)\s+(--)`+
				`(?=\s+[A-Za-z][A-Za-z0-9_./-]*)(?![^\n]*\s--[A-Za-z0-9_][A-Za-z0-9_-]*(?:=|\b))|`+
				`(?<!-)\b(?!(?:bash|sh|zsh|fish|exec|command|env|xargs|sudo|go|git|make|npm)\b)[A-Za-z][A-Za-z0-9_./-]*(--)`+
				`(?=[A-Za-z][A-Za-z0-9_./-]*))`,
			compile(`(?m)(?|(?:`+"`"+`[^`+"`"+`\n]+`+"`"+`|\b(?!(?:bash|sh|zsh|fish|exec|command|env|xargs|sudo|go|git|make|npm)\b)[A-Za-z][A-Za-z0-9_./-]*)\s+(--)`+
				`(?=\s+[A-Za-z][A-Za-z0-9_./-]*)(?![^\n]*\s--[A-Za-z0-9_][A-Za-z0-9_-]*(?:=|\b))|`+
				`(?<!-)\b(?!(?:bash|sh|zsh|fish|exec|command|env|xargs|sudo|go|git|make|npm)\b)[A-Za-z][A-Za-z0-9_./-]*(--)`+
				`(?=[A-Za-z][A-Za-z0-9_./-]*))`),
			[]string{"PreToolUse", "Stop"},
			[]string{"tool_input.content", "tool_input.new_string", "assistant_message"},
			"block", "Double hyphen prose blocked."),
	}
}

// benchFields returns a realistic FieldSet with content in the most-checked fields.
func benchFields() rules.FieldSet {
	content := "This is a normal code change with standard identifiers and no secrets. " +
		"function processPayment(amount: number, currency: string): Promise<void> { " +
		"const result = await stripe.charges.create({ amount, currency }); " +
		"return result; }"
	return rules.FieldSet{
		ToolInputContent:   content,
		ToolInputNewString: "updated: " + content,
		ToolInputCommand:   "go build ./...",
		Command:            "",
		AssistantMessage:   "",
	}
}

// BenchmarkEvaluateAll_19RegexHotPath measures EvaluateAll over 19 regex rules
// on a clean payload (no violations expected). This is the hot path for the daemon.
//
// pre-refactor:  (recorded below after first run)
// post-refactor: (recorded below after landing-5 refactor)
// delta:         (recorded below)
func BenchmarkEvaluateAll_19RegexHotPath(b *testing.B) {
	ctx := context.Background()
	rulesSlice := bench19Rules(b)
	fields := benchFields()

	b.ResetTimer()
	b.ReportAllocs()
	for range b.N {
		got := rules.EvaluateAll(ctx, "claude", "PreToolUse", fields, rulesSlice, nil)
		if len(got) != 0 {
			b.Fatalf("expected 0 violations, got %d", len(got))
		}
	}
}
