package hook_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/hook"
)

func TestCredentialReadHelperInspectionLimitsDenyBeforeExecution(t *testing.T) {
	cfg, err := config.LoadExisting(filepath.Join("..", "..", "examples", "rules", "no-credential-file-reads.toml"))
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	const sourceLimit = 1 << 20
	const helperCount = 10
	body := readCredentialFixture(t, "permitted_source.sh", root)
	large := strings.Repeat(body, sourceLimit/len(body)+1)
	if err := os.WriteFile(filepath.Join(root, "large.sh"), []byte(large), 0o700); err != nil {
		t.Fatal(err)
	}
	for index := range helperCount {
		source := body
		if index < helperCount-1 {
			source = strings.ReplaceAll(readCredentialFixture(t, "invoke_next.sh", root), "{{NEXT}}", filepath.Join(root, fmt.Sprintf("nested-%d.sh", index+1)))
		}
		if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("nested-%d.sh", index)), []byte(source), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"large.sh", "nested-0.sh"} {
		t.Run(name, func(t *testing.T) {
			raw, err := json.Marshal(credentialReadPayload{
				Event: "PreToolUse", Session: "source-budget", Tool: "Bash", CWD: root,
				Input: credentialReadInput{Command: "bash " + filepath.Join(root, name)},
			})
			if err != nil {
				t.Fatal(err)
			}
			result := hook.EvaluateClassifiedHotWithEventID(context.Background(), hook.EvaluationInput{
				WireBytes: raw, NormalizedJSON: raw, Classification: hook.Classify(raw, hook.SystemClaude, nil, nil),
			}, cfg, os.Getenv, "source-budget")
			if result.ExitCode != 2 || !strings.Contains(string(result.Stderr), "Credential file reads") {
				t.Fatalf("the inspection limit did not reject helper execution: %s", result.Stderr)
			}
		})
	}
}
