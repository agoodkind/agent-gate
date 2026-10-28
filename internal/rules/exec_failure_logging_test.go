package rules_test

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/hotkv"
	"goodkind.io/agent-gate/internal/rules"
)

func TestExecValidatorFailureLogsOneDetailedWarning(t *testing.T) {
	command, err := exec.LookPath("false")
	if err != nil {
		t.Fatalf("find false command: %v", err)
	}
	for _, test := range []struct {
		onError        string
		wantViolations int
	}{
		{onError: "open", wantViolations: 0},
		{onError: "closed", wantViolations: 1},
	} {
		t.Run(test.onError, func(t *testing.T) {
			directory := t.TempDir()
			configPath := filepath.Join(directory, "config.toml")
			body := fmt.Sprintf(`
[performance.timeouts]
exec_background_ms = 1000

[[rules]]
name = "failing-validator"
events = ["PreToolUse"]
action = "block"
violation_message = "validator failed"

[[rules.conditions]]
kind = "exec"
command = [%q]
field_paths = ["tool_input.command"]
block_on = "match"
stdout_json_field = "matched"
stdout_json_equals = "true"
on_error = %q
timeout_ms = 4000
cache_ttl_ms = 0
`, command, test.onError)
			if err := os.WriteFile(configPath, []byte(body), 0o600); err != nil {
				t.Fatalf("write validator config: %v", err)
			}
			configuration, err := config.LoadExisting(configPath)
			if err != nil {
				t.Fatalf("load validator config: %v", err)
			}
			var logOutput bytes.Buffer
			logger := slog.New(slog.NewJSONHandler(&logOutput, nil))
			cache := hotkv.New(hotkv.Options{PruneInterval: 0})
			t.Cleanup(cache.Close)
			runtime := rules.NewExecRuntimeWithCache(nil, logger, cache)
			runtime.SetBackgroundTimeout(configuration.ExecBackgroundTimeout())
			ctx := rules.WithExecRuntime(context.Background(), runtime)
			violations := rules.EvaluateAll(ctx, "claude", "PreToolUse", rules.FieldSet{
				EffectiveCWD:     directory,
				ToolInputCommand: "true",
			}, configuration.Rules, nil)
			if len(violations) != test.wantViolations {
				t.Errorf("on_error = %q returned %d violations, want %d", test.onError, len(violations), test.wantViolations)
			}
			if len(violations) == 1 && violations[0].Message != "validator failed" {
				t.Errorf("violation message = %q, want configured failure message", violations[0].Message)
			}
			logs := logOutput.String()
			const detailedWarning = `"msg":"exec validator expanded command exited nonzero for JSON match"`
			if count := strings.Count(logs, detailedWarning); count != 1 {
				t.Errorf("detailed failure warning count = %d, want 1: %s", count, logs)
			}
			if strings.Contains(logs, `"msg":"exec validator errored"`) {
				t.Errorf("logs contain a duplicate generic failure warning: %s", logs)
			}
			if !strings.Contains(logs, `"exit_code":1`) {
				t.Errorf("detailed failure warning does not include exit code 1: %s", logs)
			}
		})
	}
}
