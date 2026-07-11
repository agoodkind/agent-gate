package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGitConditionKindsLoad(t *testing.T) {
	path := writeGitConditionConfig(t, `
[[rules]]
name = "git conditions"
events = ["AnyEvent"]
violation_message = "matched"

[[rules.conditions]]
kind = "git_primary_checkout"
field_paths = ["tool_input.file_path"]

[[rules.conditions]]
kind = "git_ref_move"
`)
	cfg, err := LoadExisting(path)
	if err != nil {
		t.Fatalf("LoadExisting() error = %v", err)
	}
	if got := cfg.Rules[0].Conditions[0].Kind; got != string(ConditionKindGitPrimaryCheckout) {
		t.Fatalf("primary kind = %q", got)
	}
	if got := cfg.Rules[0].Conditions[1].Kind; got != string(ConditionKindGitRefMove) {
		t.Fatalf("ref move kind = %q", got)
	}
}

func TestGitRefMoveRejectsFieldPaths(t *testing.T) {
	path := writeGitConditionConfig(t, `
[[rules]]
name = "invalid git ref move"
events = ["AnyEvent"]
violation_message = "matched"

[[rules.conditions]]
kind = "git_ref_move"
field_paths = ["tool_input.command"]
`)
	_, err := LoadExisting(path)
	if err == nil || !strings.Contains(err.Error(), "git_ref_move does not accept field_paths") {
		t.Fatalf("LoadExisting() error = %v", err)
	}
}

func writeGitConditionConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	return path
}
