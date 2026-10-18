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
	"goodkind.io/agent-gate/internal/rules"
)

func TestCredentialReadSourceInspectionUsesActivePolicy(t *testing.T) {
	for _, policy := range []string{"source_policy.toml", "source_condition_policy.toml"} {
		t.Run(policy, func(t *testing.T) {
			testCredentialSourcePolicy(t, policy)
		})
	}
}

func testCredentialSourcePolicy(t *testing.T, policy string) {
	t.Helper()
	cfg, err := config.LoadExisting(filepath.Join("testdata", "credential_reads", policy))
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	for _, name := range []string{"controlled-source.sh", "controlled-source.template.sh", "ordinary.sh"} {
		fixture := "controlled_source.sh"
		if name != "controlled-source.sh" {
			fixture = "permitted_source.sh"
		}
		if err := os.WriteFile(filepath.Join(root, name), []byte(readCredentialFixture(t, fixture, root)), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(root, "controlled-source.sh"), filepath.Join(root, "alias.sh")); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name, path, event string
		provider          hook.System
		disabled          bool
		message           string
	}{
		{"configured path", "controlled-source.sh", "PreToolUse", hook.SystemClaude, false, "The configured source path"},
		{"resolved symlink", "alias.sh", "PreToolUse", hook.SystemClaude, false, "The configured source path"},
		{"template exception", "controlled-source.template.sh", "PreToolUse", hook.SystemClaude, false, ""},
		{"ordinary source", "ordinary.sh", "PreToolUse", hook.SystemClaude, false, ""},
		{"disabled rule", "controlled-source.sh", "PreToolUse", hook.SystemClaude, true, "Helper inspection observed"},
		{"other event", "controlled-source.sh", "PermissionRequest", hook.SystemClaude, false, "Helper inspection observed"},
		{"disabled provider", "controlled-source.sh", "PreToolUse", hook.SystemCodex, false, "Helper inspection observed"},
		{"second provider", "controlled-source.sh", "BeforeTool", hook.SystemGemini, false, "The configured source path"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			raw, err := json.Marshal(credentialReadPayload{
				Event: test.event, Session: "source-policy", Tool: "Bash", CWD: root,
				Input: credentialReadInput{Command: "bash " + filepath.Join(root, test.path)},
			})
			if err != nil {
				t.Fatal(err)
			}
			getenv := func(name string) string {
				if name == "SOURCE_POLICY_DISABLED" && test.disabled {
					return "1"
				}
				return ""
			}
			result := hook.EvaluateClassifiedHotWithEventID(context.Background(), hook.EvaluationInput{
				WireBytes: raw, NormalizedJSON: raw, Classification: hook.Classify(raw, test.provider, nil, nil),
			}, cfg, getenv, "source-policy")
			output := string(result.Stdout) + string(result.Stderr)
			if test.message == "" {
				if result.Deferred.Decision == hook.ResponseDecisionBlock {
					t.Fatalf("the policy rejected permitted source: %s", output)
				}
				return
			}
			if result.Deferred.Decision != hook.ResponseDecisionBlock || !strings.Contains(output, test.message) {
				t.Fatalf("the response did not apply the active source policy: %s", output)
			}
			if test.message == "The configured source path" && strings.Contains(output, "Helper inspection observed") {
				t.Fatal("the selector inspected protected helper contents")
			}
		})
	}
}

func TestCredentialReadSourceWithoutPolicyRemainsUninspected(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "ordinary.sh")
	if err := os.WriteFile(path, []byte(readCredentialFixture(t, "controlled_source.sh", root)), 0o700); err != nil {
		t.Fatal(err)
	}
	fields := rules.FieldSet{CWD: root, ToolInputCommand: "bash " + path}
	targets := fields.CredentialReadTargets()
	if !strings.Contains(targets, "<uninspected-script>") || strings.Contains(targets, "inspection-marker") {
		t.Fatal("source inspection read a helper without evaluation policy")
	}
}
