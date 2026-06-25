package main

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallShellHooksOnlyDelegatesToInstalledBinary(t *testing.T) {
	requireInstallDependency(t, "bash")

	repoRoot := repoRootFromPackage(t)
	tempRoot := t.TempDir()
	homeDir := filepath.Join(tempRoot, "home")
	binDir := filepath.Join(tempRoot, "bin")
	callsPath := filepath.Join(tempRoot, "calls.txt")
	writeFakeAgentGate(t, filepath.Join(binDir, "agent-gate"), callsPath)

	args := []string{
		filepath.Join(repoRoot, "install.sh"),
		"--hooks-only",
		"--bin-dir", binDir,
		"--no-claude",
		"--no-gemini",
		"--no-copilot",
	}
	command := exec.Command("bash", args...)
	command.Dir = repoRoot
	command.Env = append(os.Environ(), "HOME="+homeDir)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("install.sh failed: %v\n%s", err, output)
	}

	content, err := os.ReadFile(callsPath)
	if err != nil {
		t.Fatalf("ReadFile calls: %v", err)
	}
	got := string(content)
	for _, want := range []string{
		"install hooks",
		"--bin-path " + filepath.Join(binDir, "agent-gate"),
		"--templates " + filepath.Join(repoRoot, "hooks"),
		"--no-claude",
		"--no-gemini",
		"--no-copilot",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("delegated command missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "install service") {
		t.Fatalf("hooks-only delegated service install:\n%s", got)
	}
}

func TestRunInstallRequiresBinPath(t *testing.T) {
	testCases := []struct {
		name string
		args []string
		want string
	}{
		{
			name: "hooks",
			args: []string{"hooks"},
			want: "usage: agent-gate install hooks --bin-path PATH",
		},
		{
			name: "service",
			args: []string{"service"},
			want: "usage: agent-gate install service --bin-path PATH",
		},
		{
			name: "all",
			args: []string{"all"},
			want: "usage: agent-gate install all --bin-path PATH",
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			exitCode, stderr := captureInstallStderr(t, testCase.args)
			if exitCode != 2 {
				t.Fatalf("exitCode = %d, want 2", exitCode)
			}
			if !strings.Contains(stderr, testCase.want) {
				t.Fatalf("stderr = %q, want %q", stderr, testCase.want)
			}
		})
	}
}

func repoRootFromPackage(t *testing.T) string {
	t.Helper()
	workingDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	return filepath.Clean(filepath.Join(workingDir, "..", ".."))
}

func writeFakeAgentGate(t *testing.T, path string, callsPath string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll fake binary dir: %v", err)
	}
	script := "#!/usr/bin/env bash\nprintf '%s\\n' \"$*\" >> " + shellQuote(callsPath) + "\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("WriteFile fake binary: %v", err)
	}
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func requireInstallDependency(t *testing.T, name string) {
	t.Helper()
	if _, err := exec.LookPath(name); err != nil {
		t.Skipf("install test dependency %q missing: %v", name, err)
	}
}

func captureInstallStderr(t *testing.T, args []string) (int, string) {
	t.Helper()
	readPipe, writePipe, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	defer func() {
		_ = readPipe.Close()
	}()

	originalStderr := os.Stderr
	os.Stderr = writePipe
	exitCode := runInstall(args)
	_ = writePipe.Close()
	os.Stderr = originalStderr

	output, err := io.ReadAll(readPipe)
	if err != nil {
		t.Fatalf("ReadAll stderr: %v", err)
	}
	return exitCode, string(output)
}
