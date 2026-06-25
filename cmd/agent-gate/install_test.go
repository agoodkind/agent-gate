package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallHooksUpdatesCodexTomlIdempotently(t *testing.T) {
	requireInstallDependency(t, "bash")
	requireInstallDependency(t, "curl")
	requireInstallDependency(t, "jq")
	requireInstallDependency(t, "tar")

	repoRoot := repoRootFromPackage(t)
	tempRoot := t.TempDir()
	homeDir := filepath.Join(tempRoot, "home")
	binDir := filepath.Join(tempRoot, "bin")
	codexDir := filepath.Join(homeDir, ".codex")
	if err := os.MkdirAll(codexDir, 0o755); err != nil {
		t.Fatalf("MkdirAll codex dir: %v", err)
	}

	initialConfig := `model = "gpt-test"

[features]
hooks = false
other = true

# BEGIN agent-gate managed hooks
old = "block"
# END agent-gate managed hooks
`
	configPath := filepath.Join(codexDir, "config.toml")
	if err := os.WriteFile(configPath, []byte(initialConfig), 0o600); err != nil {
		t.Fatalf("WriteFile config.toml: %v", err)
	}

	runInstallHooks(t, repoRoot, homeDir, binDir, "--no-claude", "--no-gemini", "--no-copilot", "--no-cursor")
	runInstallHooks(t, repoRoot, homeDir, binDir, "--no-claude", "--no-gemini", "--no-copilot", "--no-cursor")

	content, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("ReadFile config.toml: %v", err)
	}
	got := string(content)
	if strings.Count(got, "# BEGIN agent-gate managed hooks") != 1 {
		t.Fatalf("managed codex hook block count = %d, want 1\n%s", strings.Count(got, "# BEGIN agent-gate managed hooks"), got)
	}
	if strings.Count(got, "# END agent-gate managed hooks") != 1 {
		t.Fatalf("managed codex hook end count = %d, want 1\n%s", strings.Count(got, "# END agent-gate managed hooks"), got)
	}
	if !strings.Contains(got, "model = \"gpt-test\"") {
		t.Fatalf("config lost unrelated model setting:\n%s", got)
	}
	if !strings.Contains(got, "other = true") {
		t.Fatalf("config lost unrelated features setting:\n%s", got)
	}
	if !strings.Contains(got, "hooks = true") {
		t.Fatalf("config did not force hooks = true:\n%s", got)
	}
	if strings.Contains(got, "old = \"block\"") {
		t.Fatalf("stale managed codex block survived replacement:\n%s", got)
	}
	wantCommand := `command = "` + filepath.Join(binDir, "agent-gate") + ` codex-hook"`
	if !strings.Contains(got, wantCommand) {
		t.Fatalf("config missing rendered codex hook command %q:\n%s", wantCommand, got)
	}
	info, err := os.Stat(configPath)
	if err != nil {
		t.Fatalf("Stat config.toml: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("config.toml mode = %#o, want 0600", info.Mode().Perm())
	}
}

func TestInstallHooksUpdatesCodexTomlWithCommentedFeaturesHeader(t *testing.T) {
	requireInstallDependency(t, "bash")
	requireInstallDependency(t, "curl")
	requireInstallDependency(t, "jq")
	requireInstallDependency(t, "tar")

	repoRoot := repoRootFromPackage(t)
	tempRoot := t.TempDir()
	homeDir := filepath.Join(tempRoot, "home")
	binDir := filepath.Join(tempRoot, "bin")
	codexDir := filepath.Join(homeDir, ".codex")
	if err := os.MkdirAll(codexDir, 0o755); err != nil {
		t.Fatalf("MkdirAll codex dir: %v", err)
	}

	configPath := filepath.Join(codexDir, "config.toml")
	initialConfig := `[features]  # keep comment
hooks = false
`
	if err := os.WriteFile(configPath, []byte(initialConfig), 0o600); err != nil {
		t.Fatalf("WriteFile config.toml: %v", err)
	}

	runInstallHooks(t, repoRoot, homeDir, binDir, "--no-claude", "--no-gemini", "--no-copilot", "--no-cursor")

	content, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("ReadFile config.toml: %v", err)
	}
	got := string(content)
	if strings.Count(got, "[features]") != 1 {
		t.Fatalf("features table count = %d, want 1\n%s", strings.Count(got, "[features]"), got)
	}
	if !strings.Contains(got, "hooks = true") {
		t.Fatalf("config did not force hooks = true:\n%s", got)
	}
}

func TestInstallHooksUpdatesCursorHooksJSONIdempotently(t *testing.T) {
	requireInstallDependency(t, "bash")
	requireInstallDependency(t, "curl")
	requireInstallDependency(t, "jq")
	requireInstallDependency(t, "tar")

	repoRoot := repoRootFromPackage(t)
	tempRoot := t.TempDir()
	homeDir := filepath.Join(tempRoot, "home")
	binDir := filepath.Join(tempRoot, "bin")
	cursorDir := filepath.Join(homeDir, ".cursor")
	if err := os.MkdirAll(cursorDir, 0o755); err != nil {
		t.Fatalf("MkdirAll cursor dir: %v", err)
	}

	initialCursorConfig := `{"version":7,"theme":"kept","hooks":{"oldEvent":[{"command":"old"}]}}`
	cursorPath := filepath.Join(cursorDir, "hooks.json")
	if err := os.WriteFile(cursorPath, []byte(initialCursorConfig), 0o600); err != nil {
		t.Fatalf("WriteFile hooks.json: %v", err)
	}

	runInstallHooks(t, repoRoot, homeDir, binDir, "--no-claude", "--no-codex", "--no-gemini", "--no-copilot")
	firstContent, err := os.ReadFile(cursorPath)
	if err != nil {
		t.Fatalf("ReadFile hooks.json after first install: %v", err)
	}
	runInstallHooks(t, repoRoot, homeDir, binDir, "--no-claude", "--no-codex", "--no-gemini", "--no-copilot")
	secondContent, err := os.ReadFile(cursorPath)
	if err != nil {
		t.Fatalf("ReadFile hooks.json after second install: %v", err)
	}
	if string(firstContent) != string(secondContent) {
		t.Fatalf("cursor hooks update is not idempotent\nfirst:\n%s\nsecond:\n%s", firstContent, secondContent)
	}

	var payload struct {
		Version int    `json:"version"`
		Theme   string `json:"theme"`
		Hooks   map[string][]struct {
			Command    string `json:"command"`
			FailClosed bool   `json:"failClosed"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(secondContent, &payload); err != nil {
		t.Fatalf("Unmarshal cursor hooks.json: %v\n%s", err, secondContent)
	}
	if payload.Version != 7 {
		t.Fatalf("cursor version = %d, want 7", payload.Version)
	}
	if payload.Theme != "kept" {
		t.Fatalf("cursor theme = %q, want kept", payload.Theme)
	}
	if _, ok := payload.Hooks["oldEvent"]; ok {
		t.Fatalf("cursor hooks preserved replaced managed event set: %v", payload.Hooks)
	}
	preToolUse := payload.Hooks["preToolUse"]
	if len(preToolUse) != 1 {
		t.Fatalf("preToolUse hooks = %d, want 1", len(preToolUse))
	}
	if preToolUse[0].Command != filepath.Join(binDir, "agent-gate") {
		t.Fatalf("preToolUse command = %q, want %q", preToolUse[0].Command, filepath.Join(binDir, "agent-gate"))
	}
	if preToolUse[0].FailClosed {
		t.Fatal("preToolUse failClosed = true, want false")
	}
	info, err := os.Stat(cursorPath)
	if err != nil {
		t.Fatalf("Stat hooks.json: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("hooks.json mode = %#o, want 0600", info.Mode().Perm())
	}
}

func runInstallHooks(t *testing.T, repoRoot string, homeDir string, binDir string, extraArgs ...string) {
	t.Helper()
	args := []string{
		filepath.Join(repoRoot, "install.sh"),
		"--hooks-only",
		"--templates", filepath.Join(repoRoot, "hooks"),
	}
	args = append(args, extraArgs...)
	cmd := exec.Command("bash", args...)
	cmd.Dir = repoRoot
	cmd.Env = append(os.Environ(),
		"HOME="+homeDir,
		"XDG_BIN_HOME="+binDir,
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("install.sh failed: %v\n%s", err, output)
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

func requireInstallDependency(t *testing.T, name string) {
	t.Helper()
	if _, err := exec.LookPath(name); err != nil {
		t.Skipf("install test dependency %q missing: %v", name, err)
	}
}
