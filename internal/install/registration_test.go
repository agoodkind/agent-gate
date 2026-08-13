package installer

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestReadManagedLifecycleCommandReadsInstalledProviders(t *testing.T) {
	binPath := writeExecutable(t, filepath.Join(t.TempDir(), "path with spaces", "agent-gate"))
	homeDir := t.TempDir()
	options := DefaultHooksOptions(binPath)
	options.HomeDir = homeDir
	plan, err := PrepareHookInstallation(options)
	if err != nil {
		t.Fatalf("PrepareHookInstallation: %v", err)
	}
	if err := ApplyHookInstallation(plan); err != nil {
		t.Fatalf("ApplyHookInstallation: %v", err)
	}

	tests := []struct {
		provider  Provider
		eventName string
		arguments []string
	}{
		{ProviderClaude, "SessionStart", []string{"managed-hook", "claude"}},
		{ProviderCodex, "SessionStart", []string{"managed-hook", "codex"}},
		{ProviderCursor, "sessionStart", []string{"managed-hook", "cursor"}},
		{ProviderGemini, "SessionStart", []string{"managed-hook", "gemini"}},
		{ProviderCopilot, "sessionStart", []string{"managed-hook", "copilot", "sessionStart"}},
	}
	for _, test := range tests {
		t.Run(string(test.provider), func(t *testing.T) {
			command, err := ReadManagedLifecycleCommand(options, test.provider)
			if err != nil {
				t.Fatalf("ReadManagedLifecycleCommand: %v", err)
			}
			if command.Provider != test.provider {
				t.Fatalf("provider = %q, want %q", command.Provider, test.provider)
			}
			if command.EventName != test.eventName {
				t.Fatalf("event name = %q, want %q", command.EventName, test.eventName)
			}
			if command.Executable != binPath {
				t.Fatalf("executable = %q, want %q", command.Executable, binPath)
			}
			if !reflect.DeepEqual(command.Arguments, test.arguments) {
				t.Fatalf("arguments = %#v, want %#v", command.Arguments, test.arguments)
			}
		})
	}
}

func TestReadManagedLifecycleCommandDerivesInstalledExecutable(t *testing.T) {
	binPath := writeExecutable(t, filepath.Join(t.TempDir(), "path with spaces", "agent-gate"))
	homeDir := t.TempDir()
	options := DefaultHooksOptions(binPath)
	options.HomeDir = homeDir
	options.Providers = []Provider{ProviderCursor}
	plan, err := PrepareHookInstallation(options)
	if err != nil {
		t.Fatalf("PrepareHookInstallation: %v", err)
	}
	if err := ApplyHookInstallation(plan); err != nil {
		t.Fatalf("ApplyHookInstallation: %v", err)
	}

	options.BinPath = ""
	command, err := ReadManagedLifecycleCommand(options, ProviderCursor)
	if err != nil {
		t.Fatalf("ReadManagedLifecycleCommand: %v", err)
	}
	if command.Executable != binPath {
		t.Fatalf("executable = %q, want %q", command.Executable, binPath)
	}
	if !reflect.DeepEqual(command.Arguments, []string{"managed-hook", "cursor"}) {
		t.Fatalf("arguments = %#v", command.Arguments)
	}
}

func TestHasManagedLifecycleRegistrationIgnoresUnrelatedExpectedEventCommands(t *testing.T) {
	for _, provider := range AllProviders() {
		t.Run(string(provider), func(t *testing.T) {
			binPath := writeExecutable(t, filepath.Join(t.TempDir(), "agent-gate"))
			homeDir := t.TempDir()
			options := DefaultHooksOptions(binPath)
			options.HomeDir = homeDir
			options.Providers = []Provider{provider}
			plan, err := PrepareHookInstallation(options)
			if err != nil {
				t.Fatalf("PrepareHookInstallation: %v", err)
			}
			if err := ApplyHookInstallation(plan); err != nil {
				t.Fatalf("ApplyHookInstallation: %v", err)
			}
			path := lifecycleConfigurationPath(homeDir, provider)
			content, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("ReadFile: %v", err)
			}
			content = []byte(strings.ReplaceAll(
				string(content),
				"managed-hook "+string(provider),
				"unrelated "+string(provider),
			))
			if err := os.WriteFile(path, content, 0o600); err != nil {
				t.Fatalf("WriteFile: %v", err)
			}

			options.BinPath = ""
			managed, err := HasManagedLifecycleRegistration(options, provider)
			if err != nil {
				t.Fatalf("HasManagedLifecycleRegistration: %v", err)
			}
			if managed {
				t.Fatal("unrelated expected-event command detected as managed")
			}
		})
	}
}

func TestReadManagedLifecycleCommandRejectsMissingInstalledExecutable(t *testing.T) {
	binPath := writeExecutable(t, filepath.Join(t.TempDir(), "agent-gate"))
	homeDir := t.TempDir()
	options := DefaultHooksOptions(binPath)
	options.HomeDir = homeDir
	options.Providers = []Provider{ProviderClaude}
	plan, err := PrepareHookInstallation(options)
	if err != nil {
		t.Fatalf("PrepareHookInstallation: %v", err)
	}
	if err := ApplyHookInstallation(plan); err != nil {
		t.Fatalf("ApplyHookInstallation: %v", err)
	}
	if err := os.Remove(binPath); err != nil {
		t.Fatalf("Remove executable: %v", err)
	}

	options.BinPath = ""
	_, err = ReadManagedLifecycleCommand(options, ProviderClaude)
	if err == nil || !strings.Contains(err.Error(), "binary not found") {
		t.Fatalf("error = %v, want missing binary", err)
	}
}

func TestReadManagedLifecycleCommandRejectsNonCommandNestedHook(t *testing.T) {
	binPath := writeExecutable(t, filepath.Join(t.TempDir(), "agent-gate"))
	homeDir := t.TempDir()
	options := DefaultHooksOptions(binPath)
	options.HomeDir = homeDir
	options.Providers = []Provider{ProviderClaude}
	plan, err := PrepareHookInstallation(options)
	if err != nil {
		t.Fatalf("PrepareHookInstallation: %v", err)
	}
	if err := ApplyHookInstallation(plan); err != nil {
		t.Fatalf("ApplyHookInstallation: %v", err)
	}

	path := filepath.Join(homeDir, ".claude", "settings.json")
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	var settings struct {
		Hooks map[string][]struct {
			Hooks []map[string]json.RawMessage `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(content, &settings); err != nil {
		t.Fatalf("Unmarshal settings: %v", err)
	}
	settings.Hooks["SessionStart"][0].Hooks[0]["type"] = json.RawMessage(`"prompt"`)
	content, err = json.Marshal(settings)
	if err != nil {
		t.Fatalf("Marshal settings: %v", err)
	}
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	_, err = ReadManagedLifecycleCommand(options, ProviderClaude)
	if err == nil || !strings.Contains(err.Error(), "lifecycle hook type") {
		t.Fatalf("error = %v, want non-command hook rejection", err)
	}
}

func TestReadManagedLifecycleCommandRejectsInvalidGeminiMatcherSet(t *testing.T) {
	tests := []struct {
		name   string
		mutate func([]json.RawMessage) []json.RawMessage
		want   string
	}{
		{
			name: "missing",
			mutate: func(groups []json.RawMessage) []json.RawMessage {
				return groups[:2]
			},
			want: "missing lifecycle matcher",
		},
		{
			name: "unrecognized",
			mutate: func(groups []json.RawMessage) []json.RawMessage {
				return append(groups, json.RawMessage(strings.Replace(
					string(groups[0]),
					"startup",
					"other",
					1,
				)))
			},
			want: "unrecognized lifecycle matcher",
		},
		{
			name: "conflicting command",
			mutate: func(groups []json.RawMessage) []json.RawMessage {
				groups[1] = json.RawMessage(strings.ReplaceAll(
					string(groups[1]),
					"managed-hook gemini",
					"managed-hook gemini conflict",
				))
				return groups
			},
			want: "conflicting lifecycle commands",
		},
		{
			name: "duplicate",
			mutate: func(groups []json.RawMessage) []json.RawMessage {
				return append(groups, groups[0])
			},
			want: "duplicate lifecycle matcher",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			binPath := writeExecutable(t, filepath.Join(t.TempDir(), "agent-gate"))
			homeDir := t.TempDir()
			options := DefaultHooksOptions(binPath)
			options.HomeDir = homeDir
			options.Providers = []Provider{ProviderGemini}
			plan, err := PrepareHookInstallation(options)
			if err != nil {
				t.Fatalf("PrepareHookInstallation: %v", err)
			}
			if err := ApplyHookInstallation(plan); err != nil {
				t.Fatalf("ApplyHookInstallation: %v", err)
			}

			path := filepath.Join(homeDir, ".gemini", "settings.json")
			content, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("ReadFile: %v", err)
			}
			var settings map[string]json.RawMessage
			if err := json.Unmarshal(content, &settings); err != nil {
				t.Fatalf("Unmarshal settings: %v", err)
			}
			var hooks map[string][]json.RawMessage
			if err := json.Unmarshal(settings["hooks"], &hooks); err != nil {
				t.Fatalf("Unmarshal hooks: %v", err)
			}
			hooks["SessionStart"] = test.mutate(hooks["SessionStart"])
			settings["hooks"], err = json.Marshal(hooks)
			if err != nil {
				t.Fatalf("Marshal hooks: %v", err)
			}
			content, err = json.Marshal(settings)
			if err != nil {
				t.Fatalf("Marshal settings: %v", err)
			}
			if err := os.WriteFile(path, content, 0o600); err != nil {
				t.Fatalf("WriteFile: %v", err)
			}

			_, err = ReadManagedLifecycleCommand(options, ProviderGemini)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}
