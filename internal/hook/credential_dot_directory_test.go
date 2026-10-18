package hook_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/hook"
)

func TestCredentialReadRuleProtectsDotSecretDirectories(t *testing.T) {
	cfg, err := config.LoadExisting(filepath.Join("..", "..", "examples", "rules", "no-credential-file-reads.toml"))
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	directory := filepath.Join(root, ".secrets")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	const canary = "synthetic-dot-directory-canary"
	protected := filepath.Join(directory, "agent_gate_recovery_token")
	if err := os.WriteFile(protected, []byte(canary), 0o600); err != nil {
		t.Fatal(err)
	}
	helper := filepath.Join(root, "dot-helper.sh")
	if err := os.WriteFile(helper, []byte(readCredentialFixture(t, "read_dot_secret.sh", root)), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, tool string
		input      credentialReadInput
		blocked    bool
	}{
		{"native extensionless token", "Read", credentialReadInput{FilePath: protected}, true},
		{"helper extensionless token", "Bash", credentialReadInput{Command: "bash " + helper}, true},
		{"dot credential directory", "Read", credentialReadInput{FilePath: filepath.Join(root, ".credentials", "service_login")}, true},
		{"source template exception", "Read", credentialReadInput{FilePath: filepath.Join(directory, "service.env.j2")}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw, err := json.Marshal(credentialReadPayload{
				Event: "PreToolUse", Session: "dot-secret-directory", Tool: test.tool, CWD: root, Input: test.input,
			})
			if err != nil {
				t.Fatal(err)
			}
			result := hook.EvaluateClassifiedHotWithEventID(context.Background(), hook.EvaluationInput{
				WireBytes: raw, NormalizedJSON: raw, Classification: hook.Classify(raw, hook.SystemClaude, nil, nil),
			}, cfg, os.Getenv, "dot-secret-directory")
			if (result.ExitCode == 2) != test.blocked {
				t.Fatalf("dot-directory denial = %t, want %t", result.ExitCode == 2, test.blocked)
			}
			if strings.Contains(string(result.Stdout)+string(result.Stderr), canary) {
				t.Fatal("the hook disclosed synthetic dot-directory contents")
			}
		})
	}
}
