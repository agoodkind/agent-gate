package hook_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/hook"
)

func TestCodexExecRecoveryEnforcesCredentialPolicy(t *testing.T) {
	cfg, err := config.LoadExisting(filepath.Join("..", "..", "examples", "rules", "no-credential-file-reads.toml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		fixture, script, command, transcript string
		blocked                              bool
	}{
		{"permitted.rb", "helper.rb", "ruby helper.rb", "credential.js", false},
		{"protected.sh", "helper.sh", "bash helper.sh", "credential_shell.js", true},
	} {
		t.Run(test.fixture, func(t *testing.T) {
			root := t.TempDir()
			workdir := filepath.Join(root, "worktree")
			if err := os.Mkdir(workdir, 0o700); err != nil {
				t.Fatal(err)
			}
			body, err := os.ReadFile(filepath.Join("testdata", "codex_exec", test.fixture))
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(workdir, test.script), body, 0o600); err != nil {
				t.Fatal(err)
			}
			const canary = "synthetic-codex-workdir-canary"
			if err := os.WriteFile(filepath.Join(workdir, ".env"), []byte(canary), 0o600); err != nil {
				t.Fatal(err)
			}
			transcript := filepath.Join(root, "rollout.jsonl")
			writeCodexRecoveryTranscript(t, transcript, root, workdir, test.transcript)
			raw, err := json.Marshal(hook.CodexPreToolUsePayload{
				CodexEnvelope: hook.CodexEnvelope{HookEvent: hook.CodexPreToolUse, Session: "credential-recovery", TranscriptPath: transcript, Cwd: root},
				TurnID:        "recovery-turn", ToolName: "Bash", ToolInput: hook.CodexToolInput{Command: test.command},
			})
			if err != nil {
				t.Fatal(err)
			}
			evaluation := evaluateHot(context.Background(), raw, cfg, hook.SystemCodex, os.Getenv)
			if evaluation.Deferred.Fields.ToolInputWorkdir != workdir {
				t.Fatal("the hook did not recover the command workdir")
			}
			output := string(evaluation.Stdout) + string(evaluation.Stderr)
			if strings.Contains(output, canary) {
				t.Fatal("the hook disclosed synthetic credential contents")
			}
			if test.blocked {
				if evaluation.Deferred.Decision != hook.ResponseDecisionBlock || !strings.Contains(output, "Credential file reads") {
					t.Fatalf("the credential policy did not reject the source reader: %s", output)
				}
				return
			}
			if evaluation.Deferred.Decision != hook.ResponseDecisionAllow {
				t.Fatalf("the credential policy rejected ordinary Ruby source: %s", output)
			}
			process := exec.CommandContext(t.Context(), "ruby", "helper.rb")
			process.Dir = workdir
			result, err := process.CombinedOutput()
			if err != nil {
				t.Fatalf("run the permitted Ruby helper: %v: %s", err, result)
			}
			if string(result) != "allowed\n" {
				t.Fatal("the permitted Ruby helper did not produce its expected output")
			}
		})
	}
}
