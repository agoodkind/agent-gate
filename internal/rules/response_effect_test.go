package rules_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/rules"
	execconcern "goodkind.io/agent-gate/internal/rules/concerns/exec"
)

func TestResponseEffectUsesCompleteMatchingExecStdout(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	contents := `[[rules]]
name = "dynamic-context"
events = ["PreToolUse"]
action = "inject"
output = "fallback"

[[rules.conditions]]
kind = "exec"
command = ["validator"]

[[rules.conditions]]
kind = "exec"
command = ["validator-last"]
`
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("WriteFile config: %v", err)
	}
	cfg, err := config.LoadExisting(path)
	if err != nil {
		t.Fatalf("LoadExisting() error: %v", err)
	}
	for index := range cfg.Rules[0].Conditions {
		if got := cfg.Rules[0].Conditions[index].BlockOn; got != config.BlockOnZero {
			t.Fatalf("BlockOn = %q, want %q", got, config.BlockOnZero)
		}
	}
	runtime := rules.NewExecRuntime(&responseEffectRunner{}, nil)
	detailed := rules.EvaluateAllDetailed(
		rules.WithExecRuntime(context.Background(), runtime),
		"codex",
		"PreToolUse",
		rules.FieldSet{},
		cfg.Rules,
		nil,
		nil,
		"test",
	)
	if len(detailed.Violations) != 0 {
		t.Fatalf("Violations = %#v, want none", detailed.Violations)
	}
	if len(detailed.Effects) != 1 {
		t.Fatalf("Effects = %#v", detailed.Effects)
	}
	if detailed.Effects[0].Output != "last first line\nlast second line\n" {
		t.Fatalf("effect output = %q", detailed.Effects[0].Output)
	}
}

type responseEffectRunner struct{}

func (responseEffectRunner) Run(
	_ context.Context,
	command []string,
	_ time.Duration,
	_ []byte,
	_ []string,
) (execconcern.RunResult, error) {
	if command[0] == "validator-last" {
		return execconcern.RunResult{ExitCode: 0, Stdout: "last first line\nlast second line\n"}, nil
	}
	return execconcern.RunResult{ExitCode: 0, Stdout: "first line\nsecond line\n"}, nil
}
