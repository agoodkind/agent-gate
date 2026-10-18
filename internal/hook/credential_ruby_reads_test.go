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

func TestCredentialRubyReadHookVerdicts(t *testing.T) {
	cfg, err := config.LoadExisting(filepath.Join("..", "..", "examples", "rules", "no-credential-file-reads.toml"))
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	const canary = "synthetic-ruby-canary-not-a-real-password"
	if err := os.WriteFile(filepath.Join(root, ".env"), []byte(canary), 0o600); err != nil {
		t.Fatal(err)
	}
	const ordinary = "ordinary Ruby helper executed\n"
	if err := os.WriteFile(filepath.Join(root, "ordinary.txt"), []byte(ordinary), 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name    string
		fixture string
		path    string
		blocked bool
	}{
		{"single quoted credential", "read_file_single.rb", ".env", true},
		{"double quoted credential", "read_file_double.rb", ".env", true},
		{"multiline credential", "read_file_multiline.rb", ".env", true},
		{"single quoted ordinary helper", "read_file_single.rb", "ordinary.txt", false},
		{"double quoted ordinary helper", "read_file_double.rb", "ordinary.txt", false},
		{"multiline ordinary helper", "read_file_multiline.rb", "ordinary.txt", false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			helper := filepath.Join(root, "helper.rb")
			body := strings.ReplaceAll(readCredentialFixture(t, test.fixture, root), "{{PATH}}", test.path)
			if err := os.WriteFile(helper, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			raw, err := json.Marshal(credentialReadPayload{
				Event: "PreToolUse", Session: "credential-ruby", Tool: "Bash", CWD: root,
				Input: credentialReadInput{Command: "ruby " + helper},
			})
			if err != nil {
				t.Fatal(err)
			}
			classification := hook.Classify(raw, hook.SystemClaude, nil, nil)
			result := hook.EvaluateClassifiedHotWithEventID(context.Background(), hook.EvaluationInput{
				WireBytes: raw, NormalizedJSON: raw, Classification: classification,
			}, cfg, os.Getenv, "credential-ruby-event")
			t.Logf("public hook exit code = %d", result.ExitCode)
			for _, output := range [][]byte{result.Stdout, result.Stderr} {
				if strings.Contains(string(output), canary) || strings.Contains(string(output), body) {
					t.Fatal("hook diagnostics disclosed protected content or helper source")
				}
			}
			if test.blocked {
				if result.ExitCode != 2 || !strings.Contains(string(result.Stderr), "Credential file reads") {
					t.Fatalf("credential helper hook exit code = %d, want 2 with a credential read reason", result.ExitCode)
				}
				return
			}
			if result.ExitCode != 0 {
				t.Fatalf("ordinary helper hook exit code = %d, want 0", result.ExitCode)
			}
			process := exec.CommandContext(t.Context(), "ruby", helper)
			process.Dir = root
			output, err := process.CombinedOutput()
			if err != nil {
				t.Fatalf("permitted ordinary Ruby helper failed: %v", err)
			}
			if string(output) != ordinary {
				t.Fatal("permitted ordinary Ruby helper did not produce the expected text")
			}
		})
	}
}
