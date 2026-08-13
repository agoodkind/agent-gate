package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDefaultsPlanCloseWaitsForApply(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	configDir := filepath.Join(configHome, "agent-gate")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatalf("MkdirAll config directory: %v", err)
	}
	configPath := filepath.Join(configDir, "config.toml")
	if err := os.WriteFile(configPath, []byte("[log]\nlevel = \"debug\"\n"), 0o600); err != nil {
		t.Fatalf("WriteFile config: %v", err)
	}
	plan, err := PrepareDefaults(EnsureDefaultsOptions{})
	if err != nil {
		t.Fatalf("PrepareDefaults: %v", err)
	}
	applyStarted := make(chan struct{})
	continueApply := make(chan struct{})
	plan.beforeRename = func() {
		close(applyStarted)
		<-continueApply
	}
	applyResult := make(chan error, 1)
	go func() {
		_, applyErr := ApplyDefaults(plan)
		applyResult <- applyErr
	}()
	<-applyStarted
	closeResult := make(chan error, 1)
	go func() {
		closeResult <- plan.Close()
	}()
	select {
	case closeErr := <-closeResult:
		t.Fatalf("Close returned before Apply completed: %v", closeErr)
	case <-time.After(20 * time.Millisecond):
	}
	close(continueApply)
	if applyErr := <-applyResult; applyErr != nil {
		t.Fatalf("ApplyDefaults: %v", applyErr)
	}
	if closeErr := <-closeResult; closeErr != nil {
		t.Fatalf("Close: %v", closeErr)
	}
}

func TestApplyDefaultsRejectsParentSwapAtRename(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	configDir := filepath.Join(configHome, "agent-gate")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatalf("MkdirAll config directory: %v", err)
	}
	configPath := filepath.Join(configDir, "config.toml")
	originalContent := []byte("[log]\nlevel = \"debug\"\n")
	if err := os.WriteFile(configPath, originalContent, 0o600); err != nil {
		t.Fatalf("WriteFile config: %v", err)
	}
	plan, err := PrepareDefaults(EnsureDefaultsOptions{})
	if err != nil {
		t.Fatalf("PrepareDefaults: %v", err)
	}
	displacedDir := filepath.Join(configHome, "displaced")
	unexpectedDir := t.TempDir()
	unexpectedConfig := filepath.Join(unexpectedDir, "config.toml")
	unexpectedContent := []byte("unexpected config\n")
	if err := os.WriteFile(unexpectedConfig, unexpectedContent, 0o600); err != nil {
		t.Fatalf("WriteFile unexpected config: %v", err)
	}
	redirectedTempPath := ""
	plan.beforeRename = func() {
		if err := os.Rename(configDir, displacedDir); err != nil {
			t.Fatalf("Rename config directory: %v", err)
		}
		if err := os.Symlink(unexpectedDir, configDir); err != nil {
			t.Fatalf("Symlink unexpected directory: %v", err)
		}
		entries, err := os.ReadDir(displacedDir)
		if err != nil {
			t.Fatalf("ReadDir displaced directory: %v", err)
		}
		for _, entry := range entries {
			if entry.Name() == "config.toml" {
				continue
			}
			redirectedTempPath = filepath.Join(unexpectedDir, entry.Name())
			if err := os.WriteFile(
				redirectedTempPath,
				plan.content,
				0o600,
			); err != nil {
				t.Fatalf("WriteFile redirected temp: %v", err)
			}
		}
	}

	if _, err := ApplyDefaults(plan); err == nil {
		t.Fatal("ApplyDefaults returned nil error after parent swap")
	}
	got, err := os.ReadFile(unexpectedConfig)
	if err != nil {
		t.Fatalf("ReadFile unexpected config: %v", err)
	}
	if string(got) != string(unexpectedContent) {
		t.Fatalf("unexpected config = %q, want %q", got, unexpectedContent)
	}
	redirectedTemp, err := os.ReadFile(redirectedTempPath)
	if err != nil {
		t.Fatalf("ReadFile redirected temp: %v", err)
	}
	if string(redirectedTemp) != string(plan.content) {
		t.Fatalf("redirected temp = %q, want %q", redirectedTemp, plan.content)
	}
	entries, err := os.ReadDir(displacedDir)
	if err != nil {
		t.Fatalf("ReadDir displaced directory: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "config.toml" {
		t.Fatalf("displaced directory entries = %v", entries)
	}
	got, err = os.ReadFile(filepath.Join(displacedDir, "config.toml"))
	if err != nil {
		t.Fatalf("ReadFile displaced config: %v", err)
	}
	if string(got) != string(originalContent) {
		t.Fatalf("displaced config = %q, want %q", got, originalContent)
	}
}

func TestApplyDefaultsRejectsTargetSwapAtRename(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	configDir := filepath.Join(configHome, "agent-gate")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatalf("MkdirAll config directory: %v", err)
	}
	configPath := filepath.Join(configDir, "config.toml")
	if err := os.WriteFile(configPath, []byte("[log]\nlevel = \"debug\"\n"), 0o600); err != nil {
		t.Fatalf("WriteFile config: %v", err)
	}
	plan, err := PrepareDefaults(EnsureDefaultsOptions{})
	if err != nil {
		t.Fatalf("PrepareDefaults: %v", err)
	}
	replacementContent := []byte("replacement config\n")
	replacementPath := filepath.Join(configDir, "replacement.toml")
	plan.beforeRename = func() {
		if err := os.WriteFile(replacementPath, replacementContent, 0o600); err != nil {
			t.Fatalf("WriteFile replacement: %v", err)
		}
		if err := os.Rename(replacementPath, configPath); err != nil {
			t.Fatalf("Rename replacement: %v", err)
		}
	}

	if _, err := ApplyDefaults(plan); err == nil {
		t.Fatal("ApplyDefaults returned nil error after target swap")
	}
	got, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("ReadFile replacement config: %v", err)
	}
	if string(got) != string(replacementContent) {
		t.Fatalf("replacement config = %q, want %q", got, replacementContent)
	}
	entries, err := os.ReadDir(configDir)
	if err != nil {
		t.Fatalf("ReadDir config directory: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "config.toml" {
		t.Fatalf("config directory entries = %v", entries)
	}
}

func TestApplyDefaultsRejectsMissingTargetCreationAtRename(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	plan, err := PrepareDefaults(EnsureDefaultsOptions{})
	if err != nil {
		t.Fatalf("PrepareDefaults: %v", err)
	}
	configPath := filepath.Join(configHome, "agent-gate", "config.toml")
	replacementContent := []byte("replacement config\n")
	plan.beforeRename = func() {
		if err := os.WriteFile(configPath, replacementContent, 0o600); err != nil {
			t.Fatalf("WriteFile replacement: %v", err)
		}
	}

	if _, err := ApplyDefaults(plan); err == nil {
		t.Fatal("ApplyDefaults returned nil error after missing target creation")
	}
	got, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("ReadFile replacement config: %v", err)
	}
	if string(got) != string(replacementContent) {
		t.Fatalf("replacement config = %q, want %q", got, replacementContent)
	}
}

func TestApplyDefaultsRestoresTargetAfterConfigSymlinkSwapAtRename(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	configDir := filepath.Join(configHome, "agent-gate")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatalf("MkdirAll config directory: %v", err)
	}
	originalContent := []byte("[log]\nlevel = \"debug\"\n")
	originalTarget := filepath.Join(t.TempDir(), "original.toml")
	if err := os.WriteFile(originalTarget, originalContent, 0o600); err != nil {
		t.Fatalf("WriteFile original target: %v", err)
	}
	replacementContent := []byte("replacement config\n")
	replacementTarget := filepath.Join(t.TempDir(), "replacement.toml")
	if err := os.WriteFile(replacementTarget, replacementContent, 0o600); err != nil {
		t.Fatalf("WriteFile replacement target: %v", err)
	}
	configPath := filepath.Join(configDir, "config.toml")
	if err := os.Symlink(originalTarget, configPath); err != nil {
		t.Fatalf("Symlink original config: %v", err)
	}
	plan, err := PrepareDefaults(EnsureDefaultsOptions{})
	if err != nil {
		t.Fatalf("PrepareDefaults: %v", err)
	}
	plan.beforeRename = func() {
		if err := os.Remove(configPath); err != nil {
			t.Fatalf("Remove original symlink: %v", err)
		}
		if err := os.Symlink(replacementTarget, configPath); err != nil {
			t.Fatalf("Symlink replacement config: %v", err)
		}
	}

	if _, err := ApplyDefaults(plan); err == nil {
		t.Fatal("ApplyDefaults returned nil error after config symlink swap")
	}
	gotOriginal, err := os.ReadFile(originalTarget)
	if err != nil {
		t.Fatalf("ReadFile original target: %v", err)
	}
	if string(gotOriginal) != string(originalContent) {
		t.Fatalf("original target = %q, want %q", gotOriginal, originalContent)
	}
	gotReplacement, err := os.ReadFile(replacementTarget)
	if err != nil {
		t.Fatalf("ReadFile replacement target: %v", err)
	}
	if string(gotReplacement) != string(replacementContent) {
		t.Fatalf("replacement target = %q, want %q", gotReplacement, replacementContent)
	}
}
