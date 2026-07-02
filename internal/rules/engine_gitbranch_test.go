package rules_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"goodkind.io/agent-gate/internal/rules"
)

// makeGitRepo creates a committed git repo and returns its path. When
// onDefaultBranch is false the repo is left on a feature branch, so the
// git_default_branch condition must not match it.
func makeGitRepo(t *testing.T, onDefaultBranch bool) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary not available")
	}
	dir := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) {
		full := append([]string{
			"-c", "user.email=test@example.com",
			"-c", "user.name=Test",
			"-c", "commit.gpgsign=false",
			"-c", "init.defaultBranch=main",
		}, args...)
		cmd := exec.Command("git", full...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_CONFIG_GLOBAL="+os.DevNull,
			"GIT_CONFIG_SYSTEM="+os.DevNull,
			"GIT_TERMINAL_PROMPT=0",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "f.txt")
	run("commit", "-qm", "init")
	if !onDefaultBranch {
		run("checkout", "-q", "-b", "feature")
	}
	return dir
}

// The git_default_branch condition blocks an Edit to a file whose repo is on its
// default branch, and allows the same edit when the repo is on a feature branch.
func TestEvaluateAll_GitDefaultBranch_FileWrite(t *testing.T) {
	const tomlBody = `
[[rules]]
name = "default-branch-no-file-writes"
claude_events = ["PreToolUse"]
action = "block"
violation_message = "no writes while on the default branch"

[[rules.conditions]]
kind = "regex"
field_paths = ["tool_name"]
pattern = '^(Edit|Write)$'

[[rules.conditions]]
kind = "git_default_branch"
field_paths = ["tool_input.file_path"]
`
	cfg := loadTOML(t, tomlBody)

	onMain := makeGitRepo(t, true)
	onFeature := makeGitRepo(t, false)

	cases := []struct {
		name     string
		repo     string
		relative bool
		blocked  bool
	}{
		{name: "edit file in repo on default branch", repo: onMain, blocked: true},
		{name: "edit file in repo on feature branch", repo: onFeature, blocked: false},
		{name: "relative file_path resolved against cwd on default", repo: onMain, relative: true, blocked: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			filePath := filepath.Join(tc.repo, "f.txt")
			if tc.relative {
				filePath = "f.txt"
			}
			fields := rules.FieldSet{
				ToolName:          "Edit",
				ToolInputFilePath: filePath,
				CWD:               tc.repo,
			}
			got := rules.EvaluateAll(context.Background(), "claude", "PreToolUse", fields, cfg.Rules, nil)
			if (len(got) > 0) != tc.blocked {
				t.Fatalf("blocked=%v, got %d violations: %#v", tc.blocked, len(got), got)
			}
		})
	}
}

// A write hidden inside bash -c is no longer opaque: shelldecomp parses the
// embedded body, cmd_write_targets surfaces the real file, and git_default_branch
// blocks it on a default-branch repo while allowing it on a feature branch.
func TestEvaluateAll_GitDefaultBranch_EmbeddedShellWrite(t *testing.T) {
	const tomlBody = `
[[rules]]
name = "default-branch-no-shell-writes"
claude_events = ["PreToolUse"]
action = "block"
violation_message = "no shell writes while on the default branch"

[[rules.conditions]]
kind = "regex"
field_paths = ["tool_name"]
pattern = '(?i)^(bash|shell)$'

[[rules.conditions]]
kind = "git_default_branch"
field_paths = ["cmd_write_targets"]
`
	cfg := loadTOML(t, tomlBody)

	onMain := makeGitRepo(t, true)
	onFeature := makeGitRepo(t, false)

	cases := []struct {
		name    string
		repo    string
		blocked bool
	}{
		{name: "bash -c redirect into default-branch repo", repo: onMain, blocked: true},
		{name: "bash -c redirect into feature-branch repo", repo: onFeature, blocked: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fields := rules.FieldSet{
				ToolName:         "Bash",
				ToolInputCommand: "bash -c \"cd " + tc.repo + " && echo x > f.txt\"",
				CWD:              t.TempDir(),
			}
			got := rules.EvaluateAll(context.Background(), "claude", "PreToolUse", fields, cfg.Rules, nil)
			if (len(got) > 0) != tc.blocked {
				t.Fatalf("blocked=%v, got %d violations: %#v", tc.blocked, len(got), got)
			}
		})
	}
}

// The git_default_branch condition resolves the target repo from a git verb's
// -C flag, so the verdict follows the -C repo's branch regardless of the shell
// cwd (here a throwaway /tmp that is not a repo).
func TestEvaluateAll_GitDefaultBranch_GitVerbDashC(t *testing.T) {
	const tomlBody = `
[[rules]]
name = "default-branch-no-git-local-work"
claude_events = ["PreToolUse"]
action = "block"
violation_message = "no git local work while on the default branch"

[[rules.conditions]]
kind = "command"
argv0 = "git"
subcommands = ["commit", "add"]
strip_env = true
cwd_flags = ["-C"]

[[rules.conditions]]
kind = "git_default_branch"
`
	cfg := loadTOML(t, tomlBody)

	onMain := makeGitRepo(t, true)
	onFeature := makeGitRepo(t, false)

	cases := []struct {
		name    string
		repo    string
		blocked bool
	}{
		{name: "git -C repo-on-default commit", repo: onMain, blocked: true},
		{name: "git -C repo-on-feature commit", repo: onFeature, blocked: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fields := rules.FieldSet{
				ToolName:         "Bash",
				ToolInputCommand: "git -C " + tc.repo + " commit --allow-empty -m x",
				CWD:              t.TempDir(),
			}
			got := rules.EvaluateAll(context.Background(), "claude", "PreToolUse", fields, cfg.Rules, nil)
			if (len(got) > 0) != tc.blocked {
				t.Fatalf("blocked=%v, got %d violations: %#v", tc.blocked, len(got), got)
			}
		})
	}
}
