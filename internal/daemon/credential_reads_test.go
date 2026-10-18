package daemon_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"goodkind.io/agent-gate/api/daemonpb"
	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/daemon"
)

type credentialDaemonToolInput struct {
	Command string `json:"command"`
}

type credentialDaemonPayload struct {
	Event   string                    `json:"hook_event_name"`
	Session string                    `json:"session_id"`
	CWD     string                    `json:"cwd"`
	Tool    string                    `json:"tool_name"`
	Input   credentialDaemonToolInput `json:"tool_input"`
}

func TestCredentialReadDenialPreventsHelperExecution(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(root, "runtime"))
	policy, err := os.ReadFile(filepath.Join("..", "..", "examples", "rules", "no-credential-file-reads.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(config.Path()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config.Path(), policy, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadExisting(config.Path())
	if err != nil {
		t.Fatal(err)
	}
	const canary = "synthetic-daemon-canary-not-a-real-password"
	environmentContent := []byte(strings.Join([]string{"OPENSEARCH_PASSWORD", canary}, "=") + "\n")
	if err := os.WriteFile(filepath.Join(root, ".env"), environmentContent, 0o600); err != nil {
		t.Fatal(err)
	}
	fixture, err := os.ReadFile(filepath.Join("..", "hook", "testdata", "credential_reads", "guarded_helper.sh"))
	if err != nil {
		t.Fatal(err)
	}
	helper := strings.ReplaceAll(string(fixture), "{{ROOT}}", root)
	if err := os.WriteFile(filepath.Join(root, "helper.sh"), []byte(helper), 0o700); err != nil {
		t.Fatal(err)
	}
	commandFixture, err := os.ReadFile(filepath.Join("..", "hook", "testdata", "credential_reads", "run_guarded_helper.sh"))
	if err != nil {
		t.Fatal(err)
	}
	command := strings.ReplaceAll(string(commandFixture), "{{ROOT}}", root)
	raw, err := json.Marshal(credentialDaemonPayload{
		Event: "PreToolUse", Session: "credential-daemon-fixture", CWD: root, Tool: "Bash",
		Input: credentialDaemonToolInput{Command: command},
	})
	if err != nil {
		t.Fatal(err)
	}
	var diagnostics bytes.Buffer
	server, err := daemon.New(context.Background(), slog.New(slog.NewTextHandler(&diagnostics, nil)), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(server.Close)
	response, err := server.EvaluateHook(t.Context(), &daemonpb.EvaluateHookRequest{
		RawJson: raw, ProviderHint: "claude", Cwd: root,
	})
	if err != nil {
		t.Fatal(err)
	}
	var toolOutput bytes.Buffer
	if response.GetExitCode() == 0 {
		process := exec.CommandContext(t.Context(), "bash", "-c", command)
		process.Stdout = &toolOutput
		process.Stderr = &toolOutput
		if err := process.Run(); err != nil {
			t.Fatalf("unexpectedly allowed helper failed: %v", err)
		}
	}
	if response.GetExitCode() != 2 || !strings.Contains(string(response.GetStderrData()), "Credential file reads") {
		t.Fatal("the daemon did not reject the credential helper before execution")
	}
	if _, err := os.Stat(filepath.Join(root, "executed")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the credential helper executed before denial")
	}
	if toolOutput.Len() != 0 {
		t.Fatal("the rejected helper produced tool output")
	}
	for _, output := range []string{diagnostics.String(), string(response.GetStdoutData()), string(response.GetStderrData())} {
		if strings.Contains(output, canary) {
			t.Fatal("denial diagnostics disclosed the credential canary")
		}
	}
}
