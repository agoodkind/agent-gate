package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const recoveryTestToken = "test-recovery-token"

func TestConfigRecoveryThroughCLI(t *testing.T) {
	binaryPath := buildRecoveryTestBinary(t)

	t.Run("hook permits exact recovery command without daemon", func(t *testing.T) {
		home := newRecoveryTestHome(t)
		candidatePath := filepath.Join(home, "candidate.toml")
		writeRecoveryTestFile(t, candidatePath, "[log]\nlevel = \"debug\"\n")
		commandText := recoveryShellCommand(candidatePath)

		exitCode, stdout, stderr := runRecoveryHook(
			t, binaryPath, home, recoveryTestToken, commandText,
		)

		if exitCode != 0 {
			t.Fatalf("exit code = %d, want 0; stderr = %q", exitCode, stderr)
		}
		if stdout == "" {
			t.Fatal("stdout is empty, want a provider allow response")
		}
		if strings.Contains(stdout+stderr, "no rule was enforced") {
			t.Fatalf(
				"output contains fail-open warning: stdout = %q stderr = %q",
				stdout,
				stderr,
			)
		}
	})

	t.Run("hook requires recovery token", func(t *testing.T) {
		home := newRecoveryTestHome(t)
		commandText := recoveryShellCommand(filepath.Join(home, "candidate.toml"))

		exitCode, _, stderr := runRecoveryHook(t, binaryPath, home, "", commandText)

		if exitCode != 0 {
			t.Fatalf("exit code = %d, want fail-open exit 0", exitCode)
		}
		if !strings.Contains(stderr, "no rule was enforced") {
			t.Fatalf("stderr = %q, want daemon-unavailable warning", stderr)
		}
	})

	t.Run("hook reads recovery token file", func(t *testing.T) {
		home := newRecoveryTestHome(t)
		writeRecoveryTestTokenFile(t, home)
		commandText := recoveryShellCommand(filepath.Join(home, "candidate.toml"))

		exitCode, stdout, stderr := runRecoveryHook(t, binaryPath, home, "", commandText)

		if exitCode != 0 {
			t.Fatalf("exit code = %d, want 0; stderr = %q", exitCode, stderr)
		}
		if stdout == "" {
			t.Fatal("stdout is empty, want a provider allow response")
		}
		if strings.Contains(stdout+stderr, "no rule was enforced") {
			t.Fatalf(
				"output contains fail-open warning: stdout = %q stderr = %q",
				stdout,
				stderr,
			)
		}
	})

	t.Run("hook rejects readable recovery token file", func(t *testing.T) {
		home := newRecoveryTestHome(t)
		tokenPath := writeRecoveryTestTokenFile(t, home)
		if err := os.Chmod(tokenPath, 0o644); err != nil {
			t.Fatalf("change token file mode: %v", err)
		}
		commandText := recoveryShellCommand(filepath.Join(home, "candidate.toml"))

		exitCode, _, stderr := runRecoveryHook(t, binaryPath, home, "", commandText)

		if exitCode != 0 {
			t.Fatalf("exit code = %d, want fail-open exit 0", exitCode)
		}
		if !strings.Contains(stderr, "no rule was enforced") {
			t.Fatalf("stderr = %q, want daemon-unavailable warning", stderr)
		}
	})

	t.Run("hook rejects chained command", func(t *testing.T) {
		home := newRecoveryTestHome(t)
		commandText := recoveryShellCommand(filepath.Join(home, "candidate.toml")) +
			" && touch rejected"

		exitCode, _, stderr := runRecoveryHook(
			t, binaryPath, home, recoveryTestToken, commandText,
		)

		if exitCode != 0 {
			t.Fatalf("exit code = %d, want fail-open exit 0", exitCode)
		}
		if !strings.Contains(stderr, "no rule was enforced") {
			t.Fatalf("stderr = %q, want daemon-unavailable warning", stderr)
		}
	})

	t.Run("hook rejects literal token assignment", func(t *testing.T) {
		home := newRecoveryTestHome(t)
		commandText := "AGENT_GATE_RECOVERY_TOKEN=guessed agent-gate config recover " +
			strconv.Quote(filepath.Join(home, "candidate.toml"))

		exitCode, _, stderr := runRecoveryHook(
			t, binaryPath, home, recoveryTestToken, commandText,
		)

		if exitCode != 0 {
			t.Fatalf("exit code = %d, want fail-open exit 0", exitCode)
		}
		if !strings.Contains(stderr, "no rule was enforced") {
			t.Fatalf("stderr = %q, want daemon-unavailable warning", stderr)
		}
	})

	t.Run("hook rejects additional environment assignment", func(t *testing.T) {
		home := newRecoveryTestHome(t)
		commandText := `AGENT_GATE_RECOVERY_TOKEN="$AGENT_GATE_RECOVERY_TOKEN" EXTRA=value agent-gate config recover ` +
			strconv.Quote(filepath.Join(home, "candidate.toml"))

		exitCode, _, stderr := runRecoveryHook(
			t, binaryPath, home, recoveryTestToken, commandText,
		)

		if exitCode != 0 {
			t.Fatalf("exit code = %d, want fail-open exit 0", exitCode)
		}
		if !strings.Contains(stderr, "no rule was enforced") {
			t.Fatalf("stderr = %q, want daemon-unavailable warning", stderr)
		}
	})

	t.Run("hook rejects alternate executable path", func(t *testing.T) {
		home := newRecoveryTestHome(t)
		commandText := `AGENT_GATE_RECOVERY_TOKEN="$AGENT_GATE_RECOVERY_TOKEN" /tmp/agent-gate config recover ` +
			strconv.Quote(filepath.Join(home, "candidate.toml"))

		exitCode, _, stderr := runRecoveryHook(
			t, binaryPath, home, recoveryTestToken, commandText,
		)

		if exitCode != 0 {
			t.Fatalf("exit code = %d, want fail-open exit 0", exitCode)
		}
		if !strings.Contains(stderr, "no rule was enforced") {
			t.Fatalf("stderr = %q, want daemon-unavailable warning", stderr)
		}
	})

	t.Run("hook rejects output redirection", func(t *testing.T) {
		home := newRecoveryTestHome(t)
		commandText := recoveryShellCommand(filepath.Join(home, "candidate.toml")) +
			" > recovery.log"

		exitCode, _, stderr := runRecoveryHook(
			t, binaryPath, home, recoveryTestToken, commandText,
		)

		if exitCode != 0 {
			t.Fatalf("exit code = %d, want fail-open exit 0", exitCode)
		}
		if !strings.Contains(stderr, "no rule was enforced") {
			t.Fatalf("stderr = %q, want daemon-unavailable warning", stderr)
		}
	})

	t.Run("hook rejects unresolved candidate path", func(t *testing.T) {
		home := newRecoveryTestHome(t)
		commandText := `AGENT_GATE_RECOVERY_TOKEN="$AGENT_GATE_RECOVERY_TOKEN" agent-gate config recover "$(printf candidate.toml)"`

		exitCode, _, stderr := runRecoveryHook(
			t, binaryPath, home, recoveryTestToken, commandText,
		)

		if exitCode != 0 {
			t.Fatalf("exit code = %d, want fail-open exit 0", exitCode)
		}
		if !strings.Contains(stderr, "no rule was enforced") {
			t.Fatalf("stderr = %q, want daemon-unavailable warning", stderr)
		}
	})

	t.Run("hook rejects truncated payload", func(t *testing.T) {
		home := newRecoveryTestHome(t)

		exitCode, _, stderr := runRecoveryProcess(
			t,
			binaryPath,
			home,
			recoveryTestToken,
			[]byte("{"),
			"managed-hook",
			"claude",
		)

		if exitCode != 0 {
			t.Fatalf("exit code = %d, want fail-open exit 0", exitCode)
		}
		if !strings.Contains(stderr, "no rule was enforced") {
			t.Fatalf("stderr = %q, want daemon-unavailable warning", stderr)
		}
	})

	t.Run("hook rejects non-string command", func(t *testing.T) {
		home := newRecoveryTestHome(t)
		payload := []byte(
			`{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":["invalid"]}}`,
		)

		exitCode, _, stderr := runRecoveryProcess(
			t,
			binaryPath,
			home,
			recoveryTestToken,
			payload,
			"managed-hook",
			"claude",
		)

		if exitCode != 0 {
			t.Fatalf("exit code = %d, want fail-open exit 0", exitCode)
		}
		if !strings.Contains(stderr, "no rule was enforced") {
			t.Fatalf("stderr = %q, want daemon-unavailable warning", stderr)
		}
	})

	t.Run("command replaces valid configuration", func(t *testing.T) {
		home := newRecoveryTestHome(t)
		configPath := writeRecoveryTestConfig(t, home, "[log]\nlevel = \"info\"\n")
		candidatePath := filepath.Join(home, "candidate.toml")
		want := "[log]\nlevel = \"debug\"\n"
		writeRecoveryTestFile(t, candidatePath, want)

		exitCode, stdout, stderr := runRecoveryCLI(
			t, binaryPath, home, recoveryTestToken, candidatePath,
		)

		if exitCode != 0 {
			t.Fatalf("exit code = %d, want 0; stderr = %q", exitCode, stderr)
		}
		if !strings.Contains(stdout, "configuration recovered") {
			t.Fatalf("stdout = %q, want recovery confirmation", stdout)
		}
		if strings.Contains(stderr, "failed") {
			t.Fatalf("stderr = %q, want no failure", stderr)
		}
		assertRecoveryTestFile(t, configPath, want)
	})

	t.Run("command requires recovery token", func(t *testing.T) {
		home := newRecoveryTestHome(t)
		original := "[log]\nlevel = \"info\"\n"
		configPath := writeRecoveryTestConfig(t, home, original)
		candidatePath := filepath.Join(home, "candidate.toml")
		writeRecoveryTestFile(t, candidatePath, "[log]\nlevel = \"debug\"\n")

		exitCode, _, stderr := runRecoveryCLI(t, binaryPath, home, "", candidatePath)

		if exitCode != 1 {
			t.Fatalf("exit code = %d, want 1", exitCode)
		}
		if !strings.Contains(stderr, recoveryTokenEnvironment) {
			t.Fatalf("stderr = %q, want recovery variable name", stderr)
		}
		assertRecoveryTestFile(t, configPath, original)
	})

	t.Run("command reads recovery token file", func(t *testing.T) {
		home := newRecoveryTestHome(t)
		writeRecoveryTestTokenFile(t, home)
		configPath := writeRecoveryTestConfig(t, home, "[log]\nlevel = \"info\"\n")
		candidatePath := filepath.Join(home, "candidate.toml")
		want := "[log]\nlevel = \"debug\"\n"
		writeRecoveryTestFile(t, candidatePath, want)

		exitCode, _, stderr := runRecoveryCLI(t, binaryPath, home, "", candidatePath)

		if exitCode != 0 {
			t.Fatalf("exit code = %d, want 0; stderr = %q", exitCode, stderr)
		}
		assertRecoveryTestFile(t, configPath, want)
	})

	t.Run("command preserves configuration after validation failure", func(t *testing.T) {
		home := newRecoveryTestHome(t)
		original := "[log]\nlevel = \"info\"\n"
		configPath := writeRecoveryTestConfig(t, home, original)
		candidatePath := filepath.Join(home, "candidate.toml")
		writeRecoveryTestFile(t, candidatePath, "[[rules]\n")

		exitCode, _, stderr := runRecoveryCLI(
			t, binaryPath, home, recoveryTestToken, candidatePath,
		)

		if exitCode != 1 {
			t.Fatalf("exit code = %d, want 1", exitCode)
		}
		if !strings.Contains(stderr, "validation failed") {
			t.Fatalf("stderr = %q, want validation failure", stderr)
		}
		assertRecoveryTestFile(t, configPath, original)
	})
}

func buildRecoveryTestBinary(t *testing.T) string {
	t.Helper()
	binaryPath := filepath.Join(t.TempDir(), "bin", "agent-gate")
	if err := os.MkdirAll(filepath.Dir(binaryPath), 0o700); err != nil {
		t.Fatalf("create binary directory: %v", err)
	}
	command := exec.CommandContext(t.Context(), "go", "build", "-o", binaryPath, ".")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build agent-gate: %v\n%s", err, output)
	}
	return binaryPath
}

func newRecoveryTestHome(t *testing.T) string {
	t.Helper()
	home, err := os.MkdirTemp("/tmp", "agent-gate-recovery.")
	if err != nil {
		t.Fatalf("create temporary home: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	return home
}

func recoveryShellCommand(candidatePath string) string {
	return `AGENT_GATE_RECOVERY_TOKEN="$AGENT_GATE_RECOVERY_TOKEN" agent-gate config recover ` +
		strconv.Quote(candidatePath)
}

func runRecoveryHook(
	t *testing.T,
	binaryPath string,
	home string,
	token string,
	commandText string,
) (int, string, string) {
	t.Helper()
	payload, err := json.Marshal(struct {
		SessionID     string            `json:"session_id"`
		HookEventName string            `json:"hook_event_name"`
		ToolName      string            `json:"tool_name"`
		CWD           string            `json:"cwd"`
		ToolInput     map[string]string `json:"tool_input"`
	}{
		SessionID:     "recovery-test",
		HookEventName: "PreToolUse",
		ToolName:      "Bash",
		CWD:           home,
		ToolInput:     map[string]string{"command": commandText},
	})
	if err != nil {
		t.Fatalf("encode hook payload: %v", err)
	}
	return runRecoveryProcess(
		t,
		binaryPath,
		home,
		token,
		payload,
		"managed-hook",
		"claude",
	)
}

func runRecoveryCLI(
	t *testing.T,
	binaryPath string,
	home string,
	token string,
	candidatePath string,
) (int, string, string) {
	t.Helper()
	return runRecoveryProcess(
		t,
		binaryPath,
		home,
		token,
		nil,
		"config",
		"recover",
		candidatePath,
	)
}

func runRecoveryProcess(
	t *testing.T,
	binaryPath string,
	home string,
	token string,
	stdin []byte,
	args ...string,
) (int, string, string) {
	t.Helper()
	command := exec.CommandContext(t.Context(), binaryPath, args...)
	command.Env = recoveryTestEnvironment(home, token)
	command.Stdin = bytes.NewReader(stdin)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	if err == nil {
		return 0, stdout.String(), stderr.String()
	}
	exitError, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("run agent-gate: %v", err)
	}
	return exitError.ExitCode(), stdout.String(), stderr.String()
}

func recoveryTestEnvironment(home string, token string) []string {
	prefixes := []string{
		"AGENT_GATE_RECOVERY_TOKEN=",
		"HOME=",
		"XDG_CACHE_HOME=",
		"XDG_CONFIG_HOME=",
		"XDG_RUNTIME_DIR=",
		"XDG_STATE_HOME=",
	}
	environment := make([]string, 0, len(os.Environ())+6)
	for _, entry := range os.Environ() {
		remove := false
		for _, prefix := range prefixes {
			if strings.HasPrefix(entry, prefix) {
				remove = true
				break
			}
		}
		if !remove {
			environment = append(environment, entry)
		}
	}
	environment = append(
		environment,
		"HOME="+home,
		"XDG_CACHE_HOME="+filepath.Join(home, "cache"),
		"XDG_CONFIG_HOME="+filepath.Join(home, "config"),
		"XDG_RUNTIME_DIR="+filepath.Join(home, "runtime"),
		"XDG_STATE_HOME="+filepath.Join(home, "state"),
	)
	if token != "" {
		environment = append(environment, recoveryTokenEnvironment+"="+token)
	}
	return environment
}

func writeRecoveryTestConfig(t *testing.T, home string, content string) string {
	t.Helper()
	path := filepath.Join(home, "config", "agent-gate", "config.toml")
	writeRecoveryTestFile(t, path, content)
	return path
}

func writeRecoveryTestTokenFile(t *testing.T, home string) string {
	t.Helper()
	path := filepath.Join(home, ".secrets", recoveryTokenFileName)
	writeRecoveryTestFile(t, path, recoveryTestToken)
	return path
}

func writeRecoveryTestFile(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("create parent directory: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func assertRecoveryTestFile(t *testing.T, path string, want string) {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if string(content) != want {
		t.Fatalf("%s content = %q, want %q", path, content, want)
	}
}
