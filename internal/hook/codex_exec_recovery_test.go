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

func TestCodexExecRecoveryEnforcesCommandCheckout(t *testing.T) {
	primary, worktree := codexRecoveryCheckouts(t)
	cfg, err := config.LoadExisting("testdata/codex_exec/rules.toml")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name        string
		script      string
		olderScript string
		wantWorkdir string
		wantBlock   bool
	}{
		{name: "neighbor directory", script: "neighbor.js"},
		{name: "second command", script: "second.js", wantWorkdir: worktree},
		{name: "literal constant", script: "shorthand.js", wantWorkdir: worktree},
		{name: "quoted properties", script: "quoted.js", wantWorkdir: worktree},
		{name: "primary checkout", script: "primary.js", wantWorkdir: primary, wantBlock: true},
		{name: "newest call omits directory", script: "missing.js", olderScript: "primary.js"},
		{name: "newest call cannot resolve directory", script: "unresolved.js", olderScript: "primary.js"},
		{name: "duplicate command is ambiguous", script: "duplicate.js", olderScript: "primary.js"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			transcript := filepath.Join(t.TempDir(), "rollout.jsonl")
			writeCodexRecoveryTranscript(t, transcript, primary, worktree, testCase.olderScript, testCase.script)
			raw, err := json.Marshal(hook.CodexPreToolUsePayload{
				CodexEnvelope: hook.CodexEnvelope{HookEvent: hook.CodexPreToolUse, Session: "recovery-test", TranscriptPath: transcript, Cwd: worktree},
				TurnID:        "recovery-turn", ToolName: "Bash",
				ToolInput: hook.CodexToolInput{Command: "git switch --detach origin/main"},
			})
			if err != nil {
				t.Fatal(err)
			}
			evaluation := evaluateHot(context.Background(), raw, cfg, hook.SystemCodex, os.Getenv)
			if got := evaluation.Deferred.Fields.ToolInputWorkdir; got != testCase.wantWorkdir {
				t.Fatalf("recovered workdir = %q, want %q", got, testCase.wantWorkdir)
			}
			blocked := evaluation.Deferred.Decision == hook.ResponseDecisionBlock
			if blocked != testCase.wantBlock {
				t.Fatalf("blocked = %v, want %v; response = %s", blocked, testCase.wantBlock, evaluation.Stdout)
			}
		})
	}
}

func codexRecoveryCheckouts(t *testing.T) (string, string) {
	t.Helper()
	directory := t.TempDir()
	primary := filepath.Join(directory, "primary")
	worktree := filepath.Join(directory, "feature")
	if err := os.Mkdir(primary, 0o700); err != nil {
		t.Fatal(err)
	}
	runGit := func(arguments ...string) {
		t.Helper()
		command := exec.CommandContext(context.Background(), "git", arguments...)
		command.Dir = primary
		command.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_SYSTEM="+os.DevNull)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", arguments, err, output)
		}
	}
	runGit("init", "--initial-branch=main")
	runGit("-c", "user.name=Test", "-c", "user.email=test@example.com", "-c", "commit.gpgsign=false", "commit", "--allow-empty", "-m", "Initialize checkout")
	runGit("worktree", "add", "-b", "feature", worktree)
	return primary, worktree
}

func writeCodexRecoveryTranscript(t *testing.T, path, primary, worktree string, scripts ...string) {
	t.Helper()
	var content strings.Builder
	for _, script := range scripts {
		if script == "" {
			continue
		}
		source, err := os.ReadFile(filepath.Join("testdata/codex_exec", script))
		if err != nil {
			t.Fatal(err)
		}
		input := strings.NewReplacer("PRIMARY", primary, "WORKTREE", worktree).Replace(string(source))
		entry := struct {
			Type    string `json:"type"`
			Payload struct {
				Type     string `json:"type"`
				Name     string `json:"name"`
				Input    string `json:"input"`
				Metadata struct {
					TurnID string `json:"turn_id"`
				} `json:"internal_chat_message_metadata_passthrough"`
			} `json:"payload"`
		}{}
		entry.Type = "response_item"
		entry.Payload.Type = "custom_tool_call"
		entry.Payload.Name = "exec"
		entry.Payload.Input = input
		entry.Payload.Metadata.TurnID = "recovery-turn"
		encoded, err := json.Marshal(entry)
		if err != nil {
			t.Fatal(err)
		}
		content.Write(encoded)
		content.WriteByte('\n')
	}
	if err := os.WriteFile(path, []byte(content.String()), 0o600); err != nil {
		t.Fatal(err)
	}
}
