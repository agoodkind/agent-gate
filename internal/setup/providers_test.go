package setup

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	installer "goodkind.io/agent-gate/internal/install"
)

func TestDetectProvidersSeparatesClientsAndManagedRegistrations(t *testing.T) {
	homeDir := t.TempDir()
	binPath := filepath.Join(t.TempDir(), "old agent-gate")
	if err := os.WriteFile(binPath, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatalf("write executable: %v", err)
	}
	hooks := installer.DefaultHooksOptions(binPath)
	hooks.HomeDir = homeDir
	hooks.Providers = []installer.Provider{installer.ProviderCursor}
	plan, err := installer.PrepareHookInstallation(hooks)
	if err != nil {
		t.Fatalf("PrepareHookInstallation: %v", err)
	}
	if err := installer.ApplyHookInstallation(plan); err != nil {
		t.Fatalf("ApplyHookInstallation: %v", err)
	}
	if err := os.Remove(binPath); err != nil {
		t.Fatalf("remove old executable: %v", err)
	}

	claudePath := filepath.Join(t.TempDir(), "claude")
	geminiPath := filepath.Join(t.TempDir(), "gemini")
	states, err := DetectProviders(DetectOptions{
		HomeDir: homeDir,
		LookPath: func(name string) (string, error) {
			switch name {
			case "claude":
				return claudePath, nil
			case "gemini":
				return geminiPath, nil
			default:
				return "", errors.New("not found")
			}
		},
	})
	if err != nil {
		t.Fatalf("DetectProviders: %v", err)
	}

	want := []ProviderState{
		{Provider: installer.ProviderClaude, ClientPath: claudePath},
		{Provider: installer.ProviderCodex},
		{Provider: installer.ProviderCursor, ManagedRegistration: true},
		{Provider: installer.ProviderGemini, ClientPath: geminiPath},
		{Provider: installer.ProviderCopilot},
	}
	if !reflect.DeepEqual(states, want) {
		t.Fatalf("states = %#v, want %#v", states, want)
	}
}

func TestDetectProvidersDoesNotInferRegistrationFromUnrelatedFile(t *testing.T) {
	homeDir := t.TempDir()
	settingsPath := filepath.Join(homeDir, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o700); err != nil {
		t.Fatalf("create settings directory: %v", err)
	}
	if err := os.WriteFile(settingsPath, []byte(`{"hooks":{"FutureStart":[]}}`), 0o600); err != nil {
		t.Fatalf("write unrelated settings: %v", err)
	}
	states, err := DetectProviders(DetectOptions{
		HomeDir: homeDir,
		LookPath: func(string) (string, error) {
			return "", errors.New("not found")
		},
	})
	if err != nil {
		t.Fatalf("DetectProviders: %v", err)
	}
	if states[0].ManagedRegistration {
		t.Fatal("unrelated Claude hook was detected as managed")
	}
}
