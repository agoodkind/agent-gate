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

type credentialReadInput struct {
	Command   string `json:"command,omitempty" exhaustruct:"optional"`
	FilePath  string `json:"file_path,omitempty" exhaustruct:"optional"`
	Content   string `json:"content,omitempty" exhaustruct:"optional"`
	NewString string `json:"new_string,omitempty" exhaustruct:"optional"`
}

type credentialReadPayload struct {
	Event   string              `json:"hook_event_name"`
	Session string              `json:"session_id"`
	Tool    string              `json:"tool_name"`
	CWD     string              `json:"cwd"`
	Input   credentialReadInput `json:"tool_input"`
}

func TestCredentialReadRuleBlocksBeforeToolExecution(t *testing.T) {
	rulePath := filepath.Join("..", "..", "examples", "rules", "no-credential-file-reads.toml")
	cfg, err := config.LoadExisting(rulePath)
	if err != nil {
		t.Fatal(err)
	}
	if errs := hook.ValidateConfig(cfg); len(errs) != 0 {
		t.Fatalf("credential policy is invalid: %v", errs)
	}
	root := t.TempDir()
	const canary = "synthetic-credential-canary-not-a-real-password"
	environmentContent := []byte(strings.Join([]string{"OPENSEARCH_PASSWORD", canary}, "=") + "\n")
	if err := os.WriteFile(filepath.Join(root, ".env"), environmentContent, 0o600); err != nil {
		t.Fatal(err)
	}
	helper := readCredentialFixture(t, "load_env.sh", root)
	if err := os.WriteFile(filepath.Join(root, "helper.sh"), []byte(helper), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "helper.py"), []byte(readCredentialFixture(t, "read_env.py", root)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, ".env"), filepath.Join(root, "linked.sh")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "tack.env.j2"), []byte("OPENSEARCH_PASSWORD={{ secret_reference }}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name    string
		tool    string
		input   credentialReadInput
		blocked bool
	}{
		{"native env", "Read", credentialReadInput{FilePath: filepath.Join(root, ".env")}, true},
		{"runtime secret", "Read", credentialReadInput{FilePath: "/run/secrets/service_password"}, true},
		{"direct shell", "Bash", credentialReadInput{Command: readCredentialFixture(t, "direct.sh", root)}, true},
		{"base64 read", "Bash", credentialReadInput{Command: readCredentialFixture(t, "base64.sh", root)}, true},
		{"git diff protected source", "Bash", credentialReadInput{Command: readCredentialFixture(t, "git_diff.sh", root)}, true},
		{"copy protected source", "Bash", credentialReadInput{Command: readCredentialFixture(t, "copy.sh", root)}, true},
		{"tar protected source", "Bash", credentialReadInput{Command: readCredentialFixture(t, "tar.sh", root)}, true},
		{"zip protected source", "Bash", credentialReadInput{Command: readCredentialFixture(t, "zip.sh", root)}, true},
		{"container protected source", "Bash", credentialReadInput{Command: readCredentialFixture(t, "container.sh", root)}, true},
		{"credential file variable read", "Bash", credentialReadInput{Command: readCredentialFixture(t, "file_variable.sh", root)}, true},
		{"unresolved file read", "Bash", credentialReadInput{Command: readCredentialFixture(t, "opaque_file.sh", root)}, true},
		{"native symlink", "Read", credentialReadInput{FilePath: filepath.Join(root, "linked.sh")}, true},
		{"remote shell", "Bash", credentialReadInput{Command: readCredentialFixture(t, "remote.sh", root)}, true},
		{"local helper", "Bash", credentialReadInput{Command: readCredentialFixture(t, "local_helper.sh", root)}, true},
		{"local python helper", "Bash", credentialReadInput{Command: readCredentialFixture(t, "python_helper.sh", root)}, true},
		{"remote helper", "Bash", credentialReadInput{Command: readCredentialFixture(t, "remote_helper.sh", root)}, true},
		{"historical helper write", "Write", credentialReadInput{FilePath: filepath.Join(root, "new-helper.sh"), Content: helper}, true},
		{"historical helper patch", "apply_patch", credentialReadInput{Command: readCredentialFixture(t, "add_helper.patch", root)}, true},
		{"python helper write", "Write", credentialReadInput{FilePath: filepath.Join(root, "new-helper.py"), Content: readCredentialFixture(t, "read_env.py", root)}, true},
		{"python edit replaces safe source", "Edit", credentialReadInput{FilePath: filepath.Join(root, "safe.py"), NewString: readCredentialFixture(t, "read_env.py", root)}, true},
		{"template native", "Read", credentialReadInput{FilePath: filepath.Join(root, "tack.env.j2")}, false},
		{"template under secret source directory", "Read", credentialReadInput{FilePath: filepath.Join(root, "secrets", "tack.env.j2")}, false},
		{"template shell", "Bash", credentialReadInput{Command: readCredentialFixture(t, "template.sh", root)}, false},
		{"git source template", "Bash", credentialReadInput{Command: readCredentialFixture(t, "git_template.sh", root)}, false},
		{"variable references", "Bash", credentialReadInput{Command: readCredentialFixture(t, "reference.sh", root)}, false},
		{"new python source references file name", "Write", credentialReadInput{FilePath: filepath.Join(root, "safe.py"), Content: readCredentialFixture(t, "safe.py", root)}, false},
		{"new shell source references file name", "Write", credentialReadInput{FilePath: filepath.Join(root, "safe.sh"), Content: readCredentialFixture(t, "safe.sh", root)}, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			payload := credentialReadPayload{Event: "PreToolUse", Session: "credential-fixture", Tool: test.tool, CWD: root, Input: test.input}
			raw, err := json.Marshal(payload)
			if err != nil {
				t.Fatal(err)
			}
			classification := hook.Classify(raw, hook.SystemClaude, nil, nil)
			result := hook.EvaluateClassifiedHotWithEventID(context.Background(), hook.EvaluationInput{
				WireBytes: raw, NormalizedJSON: raw, Classification: classification,
			}, cfg, os.Getenv, "credential-fixture-event")
			if got := result.ExitCode == 2; got != test.blocked {
				t.Fatalf("blocked = %t, want %t; diagnostics=%s", got, test.blocked, result.Stderr)
			}
			for _, output := range [][]byte{result.Stdout, result.Stderr} {
				if strings.Contains(string(output), canary) || strings.Contains(string(output), helper) {
					t.Fatal("rejection diagnostics disclosed protected content")
				}
			}
			if test.blocked && !strings.Contains(string(result.Stderr), "Credential file reads") {
				t.Fatalf("rejection omitted its reason: %s", result.Stderr)
			}
		})
	}
}

func readCredentialFixture(t *testing.T, name, root string) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", "credential_reads", name))
	if err != nil {
		t.Fatal(err)
	}
	return strings.ReplaceAll(string(body), "{{ROOT}}", root)
}
