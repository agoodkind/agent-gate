package installer

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func TestPrepareHookInstallationRejectsInvalidTemplateWithoutWrites(t *testing.T) {
	binPath := writeExecutable(t, filepath.Join(t.TempDir(), "agent-gate"))
	homeDir := t.TempDir()
	templatesDir := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(templatesDir, "cursor.json"),
		[]byte("not JSON"),
		0o600,
	); err != nil {
		t.Fatalf("WriteFile invalid template: %v", err)
	}
	options := DefaultHooksOptions(binPath)
	options.HomeDir = homeDir
	options.TemplatesDir = templatesDir
	options.InstallClaude = false
	options.InstallCodex = false
	options.InstallGemini = false
	options.InstallCopilot = false

	if _, err := PrepareHookInstallation(options); err == nil {
		t.Fatal("PrepareHookInstallation returned nil")
	}
	if _, err := os.Stat(filepath.Join(homeDir, ".cursor", "hooks.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cursor hook was written during validation: %v", err)
	}
}

func TestInstallHooksUpdatesCursorJSONIdempotently(t *testing.T) {
	binPath := writeExecutable(t, filepath.Join(t.TempDir(), "agent-gate"))
	homeDir := t.TempDir()
	cursorDir := filepath.Join(homeDir, ".cursor")
	if err := os.MkdirAll(cursorDir, 0o755); err != nil {
		t.Fatalf("MkdirAll cursor dir: %v", err)
	}
	cursorPath := filepath.Join(cursorDir, "hooks.json")
	initialContent := readInstallFixture(t, "cursor-existing.json")
	if err := os.WriteFile(cursorPath, []byte(initialContent), 0o600); err != nil {
		t.Fatalf("WriteFile hooks.json: %v", err)
	}

	options := DefaultHooksOptions(binPath)
	options.HomeDir = homeDir
	options.InstallClaude = false
	options.InstallCodex = false
	options.InstallGemini = false
	options.InstallCopilot = false
	if err := installHooks(options); err != nil {
		t.Fatalf("InstallHooks first run: %v", err)
	}
	firstContent, err := os.ReadFile(cursorPath)
	if err != nil {
		t.Fatalf("ReadFile first hooks.json: %v", err)
	}
	if err := installHooks(options); err != nil {
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
	if _, ok := payload.Hooks["removedEvent"]; ok {
		t.Fatalf("cursor removedEvent survived installation: %v", payload.Hooks)
	}
	oldEvent := payload.Hooks["oldEvent"]
	if len(oldEvent) != 1 || oldEvent[0].Command != "old" {
		t.Fatalf("cursor oldEvent = %v, want preserved external hook", oldEvent)
	}
	preToolUse := payload.Hooks["preToolUse"]
	if len(preToolUse) != 4 {
		t.Fatalf("preToolUse hooks = %d, want 4: %v", len(preToolUse), preToolUse)
	}
	wantCommands := []string{
		"external-hook --first",
		"wrapper --delegate agent-gate",
		"external-hook --last",
		binPath,
	}
	for i, wantCommand := range wantCommands {
		if preToolUse[i].Command != wantCommand {
			t.Fatalf("preToolUse[%d].command = %q, want %q", i, preToolUse[i].Command, wantCommand)
		}
	}
	if preToolUse[len(preToolUse)-1].FailClosed {
		t.Fatal("managed preToolUse failClosed = true, want false")
	}
	assertFileMode(t, cursorPath, 0o600)
	if _, err := os.Stat(filepath.Join(homeDir, ".claude", "settings.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("claude settings exists after opt-out: %v", err)
	}
}

func TestInstallHooksRemovesClaudeWorktreeFactoryHooks(t *testing.T) {
	binPath := writeExecutable(t, filepath.Join(t.TempDir(), "agent-gate"))
	homeDir := t.TempDir()
	claudeDir := filepath.Join(homeDir, ".claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatalf("MkdirAll Claude directory: %v", err)
	}
	settingsPath := filepath.Join(claudeDir, "settings.json")
	initialContent := readInstallFixture(t, "claude-existing.json")
	if err := os.WriteFile(settingsPath, []byte(initialContent), 0o600); err != nil {
		t.Fatalf("WriteFile Claude settings: %v", err)
	}

	options := DefaultHooksOptions(binPath)
	options.HomeDir = homeDir
	options.InstallCodex = false
	options.InstallCursor = false
	options.InstallGemini = false
	options.InstallCopilot = false
	if err := installHooks(options); err != nil {
		t.Fatalf("InstallHooks first run: %v", err)
	}
	firstContent, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("ReadFile Claude settings after first install: %v", err)
	}
	if err := installHooks(options); err != nil {
		t.Fatalf("InstallHooks second run: %v", err)
	}
	secondContent, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("ReadFile Claude settings after second install: %v", err)
	}
	if string(firstContent) != string(secondContent) {
		t.Fatalf("Claude hook install is not idempotent\nfirst:\n%s\nsecond:\n%s", firstContent, secondContent)
	}

	var settings struct {
		Theme       string                     `json:"theme"`
		Permissions map[string][]string        `json:"permissions"`
		Hooks       map[string]json.RawMessage `json:"hooks"`
	}
	if err := json.Unmarshal(secondContent, &settings); err != nil {
		t.Fatalf("Unmarshal Claude settings: %v\n%s", err, secondContent)
	}
	if settings.Theme != "kept" {
		t.Fatalf("theme = %q, want kept", settings.Theme)
	}
	if !reflect.DeepEqual(settings.Permissions["allow"], []string{"Read"}) {
		t.Fatalf("permissions = %v, want preserved allow list", settings.Permissions)
	}
	if _, ok := settings.Hooks["WorktreeCreate"]; ok {
		t.Fatalf("WorktreeCreate hook survived installation: %s", settings.Hooks["WorktreeCreate"])
	}
	preToolUse, ok := settings.Hooks["PreToolUse"]
	if !ok {
		t.Fatal("PreToolUse hook missing after installation")
	}
	if !strings.Contains(string(preToolUse), binPath) {
		t.Fatalf("PreToolUse hook missing agent-gate command: %s", preToolUse)
	}
	sessionStartCommands := eventCommands(t, settings.Hooks["SessionStart"])
	wantSessionStartCommands := []string{
		"/Users/example/.local/bin/clyde hook sessionstart",
		"external-hook --first",
		"wrapper --delegate /obsolete/bin/agent-gate",
		"external-hook --last",
		binPath,
	}
	if !reflect.DeepEqual(sessionStartCommands, wantSessionStartCommands) {
		t.Fatalf("SessionStart commands = %v, want %v", sessionStartCommands, wantSessionStartCommands)
	}
	if !strings.Contains(string(settings.Hooks["SessionStart"]), "empty-external") {
		t.Fatalf("preexisting empty external group was removed: %s", settings.Hooks["SessionStart"])
	}
	if emptyExternal, ok := settings.Hooks["EmptyExternal"]; !ok || string(emptyExternal) != "[]" {
		t.Fatalf("preexisting empty external event = %s, present = %t; want []", emptyExternal, ok)
	}
	assertFileMode(t, settingsPath, privateFileMode)
}

func TestInstallHooksMergesGeminiJSONIdempotently(t *testing.T) {
	binPath := writeExecutable(t, filepath.Join(t.TempDir(), "agent-gate"))
	homeDir := t.TempDir()
	settingsPath := filepath.Join(homeDir, ".gemini", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o755); err != nil {
		t.Fatalf("MkdirAll Gemini directory: %v", err)
	}
	if err := os.WriteFile(settingsPath, readInstallFixture(t, "gemini-existing.json"), 0o600); err != nil {
		t.Fatalf("WriteFile Gemini settings: %v", err)
	}

	options := DefaultHooksOptions(binPath)
	options.HomeDir = homeDir
	options.InstallClaude = false
	options.InstallCodex = false
	options.InstallCursor = false
	options.InstallCopilot = false
	if err := installHooks(options); err != nil {
		t.Fatalf("InstallHooks first run: %v", err)
	}
	firstContent, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("ReadFile first Gemini settings: %v", err)
	}
	if err := installHooks(options); err != nil {
		t.Fatalf("InstallHooks second run: %v", err)
	}
	secondContent, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("ReadFile second Gemini settings: %v", err)
	}
	if string(firstContent) != string(secondContent) {
		t.Fatalf("Gemini hook install is not idempotent\nfirst:\n%s\nsecond:\n%s", firstContent, secondContent)
	}

	var settings struct {
		Theme string                     `json:"theme"`
		Hooks map[string]json.RawMessage `json:"hooks"`
	}
	if err := json.Unmarshal(secondContent, &settings); err != nil {
		t.Fatalf("Unmarshal Gemini settings: %v", err)
	}
	if settings.Theme != "kept" {
		t.Fatalf("theme = %q, want kept", settings.Theme)
	}
	if _, ok := settings.Hooks["Legacy"]; ok {
		t.Fatalf("Legacy hook survived installation: %s", settings.Hooks["Legacy"])
	}
	wantCommands := []string{
		"external-hook --first",
		"wrapper --delegate /obsolete/bin/agent-gate",
		"external-hook --last",
		binPath + " gemini-hook",
	}
	gotCommands := eventCommands(t, settings.Hooks["BeforeTool"])
	if !reflect.DeepEqual(gotCommands, wantCommands) {
		t.Fatalf("BeforeTool commands = %v, want %v", gotCommands, wantCommands)
	}
}

func TestInstallHooksRejectsMalformedExistingJSONWithoutChangingIt(t *testing.T) {
	binPath := writeExecutable(t, filepath.Join(t.TempDir(), "agent-gate"))
	homeDir := t.TempDir()
	settingsPath := filepath.Join(homeDir, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o755); err != nil {
		t.Fatalf("MkdirAll Claude directory: %v", err)
	}
	initialContent := readInstallFixture(t, "malformed-existing.json")
	if err := os.WriteFile(settingsPath, initialContent, 0o600); err != nil {
		t.Fatalf("WriteFile malformed Claude settings: %v", err)
	}

	options := DefaultHooksOptions(binPath)
	options.HomeDir = homeDir
	options.InstallCodex = false
	options.InstallCursor = false
	options.InstallGemini = false
	options.InstallCopilot = false
	if err := installHooks(options); err == nil {
		t.Fatal("InstallHooks returned nil for malformed existing JSON")
	}
	got, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("ReadFile malformed Claude settings: %v", err)
	}
	if string(got) != string(initialContent) {
		t.Fatalf("malformed existing JSON changed\nwant: %s\ngot: %s", initialContent, got)
	}
}

func TestInstallHooksMalformedLaterProviderLeavesEveryTargetUntouched(t *testing.T) {
	binPath := writeExecutable(t, filepath.Join(t.TempDir(), "agent-gate"))
	homeDir := t.TempDir()
	initialFiles := map[string][]byte{
		filepath.Join(homeDir, ".claude", "settings.json"):             []byte(`{"theme":"claude"}`),
		filepath.Join(homeDir, ".codex", "config.toml"):                []byte("model = \"original\"\n"),
		filepath.Join(homeDir, ".cursor", "hooks.json"):                []byte(`{"hooks":`),
		filepath.Join(homeDir, ".gemini", "settings.json"):             []byte(`{"theme":"gemini"}`),
		filepath.Join(homeDir, ".copilot", "hooks", "agent-gate.json"): []byte(`{"owned":"copilot"}`),
	}
	for path, content := range initialFiles {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("MkdirAll %s: %v", filepath.Dir(path), err)
		}
		if err := os.WriteFile(path, content, 0o600); err != nil {
			t.Fatalf("WriteFile %s: %v", path, err)
		}
	}

	options := DefaultHooksOptions(binPath)
	options.HomeDir = homeDir
	if err := installHooks(options); err == nil {
		t.Fatal("InstallHooks returned nil for malformed Cursor JSON")
	}
	for path, want := range initialFiles {
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("ReadFile %s: %v", path, err)
		}
		if string(got) != string(want) {
			t.Errorf("%s changed after preflight failure\nwant: %s\ngot: %s", path, want, got)
		}
	}
}

func TestPrepareHookInstallationRejectsMalformedCodexManagedBlockWithoutWrites(t *testing.T) {
	binPath := writeExecutable(t, filepath.Join(t.TempDir(), "agent-gate"))
	homeDir := t.TempDir()
	configPath := filepath.Join(homeDir, ".codex", "config.toml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatalf("MkdirAll Codex directory: %v", err)
	}
	initialContent := []byte("model = \"original\"\n\n" + codexManagedBlockStart + "\nold = true\n")
	if err := os.WriteFile(configPath, initialContent, 0o600); err != nil {
		t.Fatalf("WriteFile Codex config: %v", err)
	}
	options := DefaultHooksOptions(binPath)
	options.HomeDir = homeDir
	options.InstallClaude = false
	options.InstallCursor = false
	options.InstallGemini = false
	options.InstallCopilot = false

	if _, err := PrepareHookInstallation(options); err == nil {
		t.Fatal("PrepareHookInstallation returned nil for malformed Codex managed block")
	}
	got, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("ReadFile Codex config: %v", err)
	}
	if string(got) != string(initialContent) {
		t.Fatalf("Codex config changed during preparation\nwant: %s\ngot: %s", initialContent, got)
	}
}

func TestInstallHooksFullyReplacesCopilotFile(t *testing.T) {
	binPath := writeExecutable(t, filepath.Join(t.TempDir(), "agent-gate"))
	homeDir := t.TempDir()
	hookPath := filepath.Join(homeDir, ".copilot", "hooks", "agent-gate.json")
	if err := os.MkdirAll(filepath.Dir(hookPath), 0o755); err != nil {
		t.Fatalf("MkdirAll Copilot hooks directory: %v", err)
	}
	initialContent := []byte(`{"external":"remove","hooks":{"Legacy":[{"command":"external-hook"}]}}`)
	if err := os.WriteFile(hookPath, initialContent, 0o600); err != nil {
		t.Fatalf("WriteFile Copilot hook: %v", err)
	}

	options := DefaultHooksOptions(binPath)
	options.HomeDir = homeDir
	options.InstallClaude = false
	options.InstallCodex = false
	options.InstallCursor = false
	options.InstallGemini = false
	if err := installHooks(options); err != nil {
		t.Fatalf("InstallHooks: %v", err)
	}
	content, err := os.ReadFile(hookPath)
	if err != nil {
		t.Fatalf("ReadFile Copilot hook: %v", err)
	}
	var hookFile map[string]json.RawMessage
	if err := json.Unmarshal(content, &hookFile); err != nil {
		t.Fatalf("Unmarshal Copilot hook: %v", err)
	}
	if _, ok := hookFile["external"]; ok {
		t.Fatalf("external top-level setting survived replacement: %s", content)
	}
	if strings.Contains(string(hookFile["hooks"]), "external-hook") {
		t.Fatalf("external hook survived replacement: %s", hookFile["hooks"])
	}
}

func TestInstallCopilotHooksRegistersResponseEventsWithHints(t *testing.T) {
	binPath := writeExecutable(t, filepath.Join(t.TempDir(), "agent-gate"))
	homeDir := t.TempDir()
	options := DefaultHooksOptions(binPath)
	options.HomeDir = homeDir
	options.InstallClaude = false
	options.InstallCodex = false
	options.InstallCursor = false
	options.InstallGemini = false
	if err := installHooks(options); err != nil {
		t.Fatalf("installHooks() error: %v", err)
	}
	path := filepath.Join(homeDir, ".copilot", "hooks", "agent-gate.json")
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile Copilot hooks: %v", err)
	}
	var envelope struct {
		Hooks map[string]json.RawMessage `json:"hooks"`
	}
	if err := json.Unmarshal(content, &envelope); err != nil {
		t.Fatalf("Unmarshal Copilot hooks: %v", err)
	}
	for _, eventName := range []string{
		"sessionStart", "subagentStart", "userPromptTransformed", "preToolUse", "postToolUse", "postToolUseFailure", "notification",
	} {
		event, ok := envelope.Hooks[eventName]
		if !ok {
			t.Fatalf("Copilot hooks missing %q", eventName)
		}
		commands := eventCommands(t, event)
		wantCommand := binPath + " copilot-hook " + eventName
		if len(commands) != 1 || commands[0] != wantCommand {
			t.Fatalf("%s commands = %#v, want %q", eventName, commands, wantCommand)
		}
	}
}

func readInstallFixture(t *testing.T, name string) []byte {
	t.Helper()
	content, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("ReadFile fixture %q: %v", name, err)
	}
	return content
}

func eventCommands(t *testing.T, event json.RawMessage) []string {
	t.Helper()
	var groups []struct {
		Command string `json:"command"`
		Hooks   []struct {
			Command string `json:"command"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(event, &groups); err != nil {
		t.Fatalf("Unmarshal hook event: %v\n%s", err, event)
	}
	var commands []string
	for _, group := range groups {
		if group.Command != "" {
			commands = append(commands, group.Command)
		}
		for _, hook := range group.Hooks {
			commands = append(commands, hook.Command)
		}
	}
	return commands
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
	if err := installHooks(options); err != nil {
		t.Fatalf("InstallHooks first run: %v", err)
	}
	if err := installHooks(options); err != nil {
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
		"[[hooks.SubagentStart]]",
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
	if err := installHooks(options); err != nil {
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
	if err := installHooks(options); err != nil {
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
	if err := installHooks(hookOptions); err != nil {
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

func TestInstallRendersCanonicalExecutablePathFromSymlink(t *testing.T) {
	targetPath := writeExecutable(t, filepath.Join(t.TempDir(), "agent-gate"))
	canonicalTargetPath, err := filepath.EvalSymlinks(targetPath)
	if err != nil {
		t.Fatalf("EvalSymlinks executable target: %v", err)
	}
	linkPath := filepath.Join(t.TempDir(), "agent-gate")
	if err := os.Symlink(targetPath, linkPath); err != nil {
		t.Fatalf("Symlink executable: %v", err)
	}
	homeDir := t.TempDir()
	hookOptions := DefaultHooksOptions(linkPath)
	hookOptions.HomeDir = homeDir
	hookOptions.InstallClaude = false
	hookOptions.InstallCodex = false
	hookOptions.InstallGemini = false
	hookOptions.InstallCopilot = false
	if err := installHooks(hookOptions); err != nil {
		t.Fatalf("InstallHooks: %v", err)
	}
	serviceOptions := ServiceOptions{
		BinPath:    linkPath,
		HomeDir:    homeDir,
		ConfigHome: filepath.Join(homeDir, ".config"),
		StateHome:  filepath.Join(homeDir, ".local", "state"),
		Runner:     &recordingRunner{},
	}
	if err := InstallService(serviceOptions); err != nil {
		t.Fatalf("InstallService: %v", err)
	}

	paths := []string{filepath.Join(homeDir, ".cursor", "hooks.json")}
	switch runtime.GOOS {
	case "darwin":
		paths = append(paths, filepath.Join(homeDir, "Library", "LaunchAgents", launchdLabel+".plist"))
	case "linux":
		paths = append(paths, filepath.Join(homeDir, ".config", "systemd", "user", systemdServiceName))
	default:
		t.Fatalf("unexpected runtime.GOOS = %s", runtime.GOOS)
	}
	for _, path := range paths {
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("ReadFile %s: %v", path, err)
		}
		if !strings.Contains(string(content), canonicalTargetPath) {
			t.Errorf("%s missing canonical executable %q:\n%s", path, canonicalTargetPath, content)
		}
		if strings.Contains(string(content), linkPath) {
			t.Errorf("%s retained symlink path %q:\n%s", path, linkPath, content)
		}
	}
}

func TestInstallLaunchdServiceUsesExactCommandSequence(t *testing.T) {
	binPath := writeExecutable(t, filepath.Join(t.TempDir(), "agent-gate"))
	homeDir := t.TempDir()
	runner := &recordingRunner{}
	readinessCalled := false
	options := ServiceOptions{
		BinPath:   binPath,
		HomeDir:   homeDir,
		StateHome: filepath.Join(homeDir, ".local", "state"),
		Ready: func() error {
			readinessCalled = true
			return nil
		},
	}
	if err := installLaunchdService(options, homeDir, io.Discard, runner); err != nil {
		t.Fatalf("installLaunchdService: %v", err)
	}

	domain := "gui/" + strconv.Itoa(os.Getuid())
	serviceTarget := domain + "/" + launchdLabel
	targetPath := filepath.Join(homeDir, "Library", "LaunchAgents", launchdLabel+".plist")
	want := []string{
		"launchctl bootout " + serviceTarget,
		"launchctl print " + serviceTarget,
		"pgrep -f ^" + regexp.QuoteMeta(binPath) + " daemon$",
		"launchctl enable " + serviceTarget,
		"launchctl bootstrap " + domain + " " + targetPath,
	}
	if !reflect.DeepEqual(runner.calls, want) {
		t.Fatalf("calls = %v, want %v", runner.calls, want)
	}
	if !readinessCalled {
		t.Fatal("readiness callback was not called")
	}
}

func TestInstallSystemdServiceUsesExactCommandSequence(t *testing.T) {
	binPath := writeExecutable(t, filepath.Join(t.TempDir(), "agent-gate"))
	homeDir := t.TempDir()
	runner := &recordingRunner{}
	readinessCalled := false
	options := ServiceOptions{
		BinPath:    binPath,
		HomeDir:    homeDir,
		ConfigHome: filepath.Join(homeDir, ".config"),
		Ready: func() error {
			readinessCalled = true
			return nil
		},
	}
	if err := installSystemdService(options, homeDir, io.Discard, runner); err != nil {
		t.Fatalf("installSystemdService: %v", err)
	}

	want := []string{
		"systemctl --user daemon-reload",
		"systemctl --user stop agent-gate.service",
		"pgrep -f ^" + regexp.QuoteMeta(binPath) + " daemon$",
		"systemctl --user enable --now agent-gate.service",
	}
	if !reflect.DeepEqual(runner.calls, want) {
		t.Fatalf("calls = %v, want %v", runner.calls, want)
	}
	if !readinessCalled {
		t.Fatal("readiness callback was not called")
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

func installHooks(options HooksOptions) error {
	plan, err := PrepareHookInstallation(options)
	if err != nil {
		return err
	}
	return ApplyHookInstallation(plan)
}

func writeExecutable(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll executable dir: %v", err)
	}
	if err := os.WriteFile(path, []byte("#!/usr/bin/env sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("WriteFile executable: %v", err)
	}
	canonicalPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatalf("EvalSymlinks executable: %v", err)
	}
	return canonicalPath
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
