package installer

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestInstallHooksUpdatesCursorJSONIdempotently(t *testing.T) {
	binPath := writeExecutable(t, filepath.Join(t.TempDir(), "agent-gate"))
	homeDir := t.TempDir()
	cursorDir := filepath.Join(homeDir, ".cursor")
	if err := os.MkdirAll(cursorDir, 0o755); err != nil {
		t.Fatalf("MkdirAll cursor dir: %v", err)
	}
	cursorPath := filepath.Join(cursorDir, "hooks.json")
	initialContent := `{"version":7,"theme":"kept","hooks":{"oldEvent":[{"command":"old"}]}}`
	if err := os.WriteFile(cursorPath, []byte(initialContent), 0o600); err != nil {
		t.Fatalf("WriteFile hooks.json: %v", err)
	}

	options := DefaultHooksOptions(binPath)
	options.HomeDir = homeDir
	options.InstallClaude = false
	options.InstallCodex = false
	options.InstallGemini = false
	options.InstallCopilot = false
	if err := InstallHooks(options); err != nil {
		t.Fatalf("InstallHooks first run: %v", err)
	}
	firstContent, err := os.ReadFile(cursorPath)
	if err != nil {
		t.Fatalf("ReadFile first hooks.json: %v", err)
	}
	if err := InstallHooks(options); err != nil {
		t.Fatalf("InstallHooks second run: %v", err)
	}
	secondContent, err := os.ReadFile(cursorPath)
	if err != nil {
		t.Fatalf("ReadFile second hooks.json: %v", err)
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
		t.Fatalf("cursor hooks preserved replaced event set: %v", payload.Hooks)
	}
	preToolUse := payload.Hooks["preToolUse"]
	if len(preToolUse) != 1 {
		t.Fatalf("preToolUse hooks = %d, want 1", len(preToolUse))
	}
	if preToolUse[0].Command != binPath {
		t.Fatalf("preToolUse command = %q, want %q", preToolUse[0].Command, binPath)
	}
	if preToolUse[0].FailClosed {
		t.Fatal("preToolUse failClosed = true, want false")
	}
	assertFileMode(t, cursorPath, 0o600)
	if _, err := os.Stat(filepath.Join(homeDir, ".claude", "settings.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("claude settings exists after opt-out: %v", err)
	}
}

func TestInstallHooksUpdatesCodexTomlIdempotently(t *testing.T) {
	binPath := writeExecutable(t, filepath.Join(t.TempDir(), "agent-gate"))
	homeDir := t.TempDir()
	codexDir := filepath.Join(homeDir, ".codex")
	if err := os.MkdirAll(codexDir, 0o755); err != nil {
		t.Fatalf("MkdirAll codex dir: %v", err)
	}
	configPath := filepath.Join(codexDir, "config.toml")
	initialContent := `model = "gpt-test"

[features]
hooks = false
other = true

# BEGIN agent-gate managed hooks
old = "block"
# END agent-gate managed hooks
`
	if err := os.WriteFile(configPath, []byte(initialContent), 0o600); err != nil {
		t.Fatalf("WriteFile config.toml: %v", err)
	}

	options := DefaultHooksOptions(binPath)
	options.HomeDir = homeDir
	options.InstallClaude = false
	options.InstallCursor = false
	options.InstallGemini = false
	options.InstallCopilot = false
	if err := InstallHooks(options); err != nil {
		t.Fatalf("InstallHooks first run: %v", err)
	}
	if err := InstallHooks(options); err != nil {
		t.Fatalf("InstallHooks second run: %v", err)
	}

	content, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("ReadFile config.toml: %v", err)
	}
	got := string(content)
	if strings.Count(got, codexManagedBlockStart) != 1 {
		t.Fatalf("managed block count = %d, want 1\n%s", strings.Count(got, codexManagedBlockStart), got)
	}
	if strings.Count(got, codexManagedBlockEnd) != 1 {
		t.Fatalf("managed block end count = %d, want 1\n%s", strings.Count(got, codexManagedBlockEnd), got)
	}
	for _, want := range []string{
		`model = "gpt-test"`,
		"other = true",
		"hooks = true",
		`command = "` + binPath + ` codex-hook"`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("config missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, `old = "block"`) {
		t.Fatalf("stale managed codex block survived replacement:\n%s", got)
	}
	assertFileMode(t, configPath, 0o600)
}

func TestInstallHooksUpdatesCodexTomlWithCommentedFeaturesHeader(t *testing.T) {
	binPath := writeExecutable(t, filepath.Join(t.TempDir(), "agent-gate"))
	homeDir := t.TempDir()
	codexDir := filepath.Join(homeDir, ".codex")
	if err := os.MkdirAll(codexDir, 0o755); err != nil {
		t.Fatalf("MkdirAll codex dir: %v", err)
	}
	configPath := filepath.Join(codexDir, "config.toml")
	initialContent := `[features]  # keep comment
hooks = false
`
	if err := os.WriteFile(configPath, []byte(initialContent), 0o600); err != nil {
		t.Fatalf("WriteFile config.toml: %v", err)
	}

	options := DefaultHooksOptions(binPath)
	options.HomeDir = homeDir
	options.InstallClaude = false
	options.InstallCursor = false
	options.InstallGemini = false
	options.InstallCopilot = false
	if err := InstallHooks(options); err != nil {
		t.Fatalf("InstallHooks: %v", err)
	}

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

func TestInstallHooksReplacesNestedJSONCommands(t *testing.T) {
	binPath := writeExecutable(t, filepath.Join(t.TempDir(), "agent-gate"))
	homeDir := t.TempDir()
	options := DefaultHooksOptions(binPath)
	options.HomeDir = homeDir
	options.InstallClaude = false
	options.InstallCodex = false
	options.InstallCursor = false
	options.InstallCopilot = false
	if err := InstallHooks(options); err != nil {
		t.Fatalf("InstallHooks: %v", err)
	}

	content, err := os.ReadFile(filepath.Join(homeDir, ".gemini", "settings.json"))
	if err != nil {
		t.Fatalf("ReadFile gemini settings: %v", err)
	}
	got := string(content)
	if strings.Contains(got, agentGatePlaceholder) {
		t.Fatalf("placeholder survived JSON render:\n%s", got)
	}
	if !strings.Contains(got, binPath+" gemini-hook") {
		t.Fatalf("rendered command missing bin path:\n%s", got)
	}
}

func TestInstallFromScratchCreatesExpectedFiles(t *testing.T) {
	binPath := writeExecutable(t, filepath.Join(t.TempDir(), "agent-gate"))
	homeDir := t.TempDir()
	runner := &recordingRunner{}

	hookOptions := DefaultHooksOptions(binPath)
	hookOptions.HomeDir = homeDir
	if err := InstallHooks(hookOptions); err != nil {
		t.Fatalf("InstallHooks() error: %v", err)
	}

	serviceOptions := ServiceOptions{
		BinPath:    binPath,
		HomeDir:    homeDir,
		ConfigHome: filepath.Join(homeDir, ".config"),
		StateHome:  filepath.Join(homeDir, ".local", "state"),
		Runner:     runner,
	}
	if err := InstallService(serviceOptions); err != nil {
		t.Fatalf("InstallService() error: %v", err)
	}

	testCases := []struct {
		path           string
		wantSubstrings []string
	}{
		{
			path:           filepath.Join(homeDir, ".claude", "settings.json"),
			wantSubstrings: []string{binPath},
		},
		{
			path:           filepath.Join(homeDir, ".codex", "config.toml"),
			wantSubstrings: []string{"hooks = true", `command = "` + binPath + ` codex-hook"`},
		},
		{
			path:           filepath.Join(homeDir, ".cursor", "hooks.json"),
			wantSubstrings: []string{binPath},
		},
		{
			path:           filepath.Join(homeDir, ".gemini", "settings.json"),
			wantSubstrings: []string{binPath + " gemini-hook"},
		},
		{
			path:           filepath.Join(homeDir, ".copilot", "hooks", "agent-gate.json"),
			wantSubstrings: []string{binPath},
		},
	}
	for _, testCase := range testCases {
		content, err := os.ReadFile(testCase.path)
		if err != nil {
			t.Fatalf("ReadFile(%q) error: %v", testCase.path, err)
		}
		for _, want := range testCase.wantSubstrings {
			if !strings.Contains(string(content), want) {
				t.Fatalf("%s missing %q:\n%s", testCase.path, want, content)
			}
		}
		assertFileMode(t, testCase.path, privateFileMode)
	}

	switch runtime.GOOS {
	case "darwin":
		servicePath := filepath.Join(homeDir, "Library", "LaunchAgents", launchdLabel+".plist")
		content, err := os.ReadFile(servicePath)
		if err != nil {
			t.Fatalf("ReadFile(%q) error: %v", servicePath, err)
		}
		if !strings.Contains(string(content), binPath) {
			t.Fatalf("launchd plist missing bin path:\n%s", content)
		}
		assertFileMode(t, servicePath, privateFileMode)
	case "linux":
		servicePath := filepath.Join(homeDir, ".config", "systemd", "user", systemdServiceName)
		content, err := os.ReadFile(servicePath)
		if err != nil {
			t.Fatalf("ReadFile(%q) error: %v", servicePath, err)
		}
		if !strings.Contains(string(content), binPath) {
			t.Fatalf("systemd unit missing bin path:\n%s", content)
		}
		assertFileMode(t, servicePath, privateFileMode)
	default:
		t.Fatalf("unexpected runtime.GOOS = %s", runtime.GOOS)
	}
}

func TestInstallServiceUsesFakeRunner(t *testing.T) {
	binPath := writeExecutable(t, filepath.Join(t.TempDir(), "agent-gate"))
	homeDir := t.TempDir()
	runner := &recordingRunner{}
	options := ServiceOptions{
		BinPath:    binPath,
		HomeDir:    homeDir,
		ConfigHome: filepath.Join(homeDir, ".config"),
		StateHome:  filepath.Join(homeDir, ".local", "state"),
		Stdout:     nil,
		Runner:     runner,
	}
	if err := InstallService(options); err != nil {
		t.Fatalf("InstallService: %v", err)
	}

	switch runtime.GOOS {
	case "darwin":
		targetPath := filepath.Join(homeDir, "Library", "LaunchAgents", launchdLabel+".plist")
		content, err := os.ReadFile(targetPath)
		if err != nil {
			t.Fatalf("ReadFile launchd plist: %v", err)
		}
		if !strings.Contains(string(content), binPath) {
			t.Fatalf("launchd plist missing bin path:\n%s", content)
		}
		runner.assertCalls(t, []string{
			"launchctl bootout gui/",
			"launchctl print gui/",
			"pgrep -f ^",
			"launchctl enable gui/",
			"launchctl bootstrap gui/",
			"launchctl kickstart -k gui/",
		})
	case "linux":
		targetPath := filepath.Join(homeDir, ".config", "systemd", "user", systemdServiceName)
		content, err := os.ReadFile(targetPath)
		if err != nil {
			t.Fatalf("ReadFile systemd service: %v", err)
		}
		if !strings.Contains(string(content), binPath) {
			t.Fatalf("systemd service missing bin path:\n%s", content)
		}
		runner.assertCalls(t, []string{
			"systemctl --user daemon-reload",
			"systemctl --user stop agent-gate.service",
			"pgrep -f ^",
			"systemctl --user enable --now agent-gate.service",
			"systemctl --user restart agent-gate.service",
		})
	default:
		t.Fatalf("test did not expect runtime.GOOS = %s", runtime.GOOS)
	}
}

func TestRenderServiceTemplates(t *testing.T) {
	renderedLaunchd, err := renderServiceTemplate("", "macos", launchdTemplateName, map[string]string{
		"@@BIN_PATH@@": "/tmp/agent-gate",
		"@@HOME@@":     "/tmp/home",
		"@@LOG_PATH@@": "/tmp/log",
	})
	if err != nil {
		t.Fatalf("render launchd template: %v", err)
	}
	for _, unwanted := range []string{"@@BIN_PATH@@", "@@HOME@@", "@@LOG_PATH@@"} {
		if strings.Contains(renderedLaunchd, unwanted) {
			t.Fatalf("launchd template still contains %q:\n%s", unwanted, renderedLaunchd)
		}
	}

	renderedSystemd, err := renderServiceTemplate("", "systemd", systemdServiceTemplate, map[string]string{
		"@@BIN_PATH@@": "/tmp/agent-gate",
	})
	if err != nil {
		t.Fatalf("render systemd template: %v", err)
	}
	if strings.Contains(renderedSystemd, "@@BIN_PATH@@") {
		t.Fatalf("systemd template still contains placeholder:\n%s", renderedSystemd)
	}
}

type recordingRunner struct {
	calls []string
}

func (runner *recordingRunner) Run(name string, args ...string) error {
	runner.calls = append(runner.calls, strings.TrimSpace(name+" "+strings.Join(args, " ")))
	if name == "launchctl" && len(args) > 0 && args[0] == "print" {
		return errors.New("not loaded")
	}
	return nil
}

func (runner *recordingRunner) Output(name string, args ...string) ([]byte, error) {
	runner.calls = append(runner.calls, strings.TrimSpace(name+" "+strings.Join(args, " ")))
	return nil, errors.New("not found")
}

func (runner *recordingRunner) assertCalls(t *testing.T, prefixes []string) {
	t.Helper()
	callText := strings.Join(runner.calls, "\n")
	for _, prefix := range prefixes {
		found := false
		for _, call := range runner.calls {
			if strings.HasPrefix(call, prefix) {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("recorded calls missing prefix %q:\n%s", prefix, callText)
		}
	}
}

func writeExecutable(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll executable dir: %v", err)
	}
	if err := os.WriteFile(path, []byte("#!/usr/bin/env sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("WriteFile executable: %v", err)
	}
	return path
}

func assertFileMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat %s: %v", path, err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("%s mode = %#o, want %#o", path, got, want)
	}
}
