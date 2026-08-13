package installer

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
	"goodkind.io/agent-gate/internal/config"
)

func TestPrepareInstallationValidatesEveryLayerBeforeWrites(t *testing.T) {
	binPath := writeExecutable(t, filepath.Join(t.TempDir(), "agent-gate"))
	homeDir := t.TempDir()
	configHome := filepath.Join(homeDir, ".config")
	t.Setenv("XDG_CONFIG_HOME", configHome)
	configPath := filepath.Join(configHome, "agent-gate", "config.toml")
	servicePath := serviceTargetPathForTest(homeDir, configHome)
	files := map[string][]byte{
		configPath:  []byte("[log]\nlevel = \"debug\"\n"),
		servicePath: []byte("service-original\n"),
		filepath.Join(homeDir, ".claude", "settings.json"): []byte(`{"theme":"claude"}`),
		filepath.Join(homeDir, ".codex", "config.toml"):    []byte("model = \"original\"\n"),
		filepath.Join(homeDir, ".cursor", "hooks.json"):    []byte(`{"version":1}`),
		filepath.Join(homeDir, ".gemini", "settings.json"): []byte(`{"hooks":`),
	}
	for path, content := range files {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatalf("MkdirAll %s: %v", path, err)
		}
		if err := os.WriteFile(path, content, 0o600); err != nil {
			t.Fatalf("WriteFile %s: %v", path, err)
		}
	}
	hooks := DefaultHooksOptions(binPath)
	hooks.HomeDir = homeDir
	hooks.Providers = []Provider{ProviderClaude, ProviderCodex, ProviderCursor, ProviderGemini}
	service := ServiceOptions{
		BinPath:    binPath,
		HomeDir:    homeDir,
		ConfigHome: configHome,
		StateHome:  filepath.Join(homeDir, ".local", "state"),
	}

	_, err := PrepareInstallation(InstallationOptions{
		Config:  &config.EnsureDefaultsOptions{},
		Service: &service,
		Hooks:   &hooks,
	})
	if err == nil {
		t.Fatal("PrepareInstallation returned nil error for malformed final provider")
	}
	for path, want := range files {
		got, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatalf("ReadFile %s: %v", path, readErr)
		}
		if string(got) != string(want) {
			t.Fatalf("%s changed during preparation\nwant: %q\ngot: %q", path, want, got)
		}
	}
}

func TestPrepareInstallationClosesConfigPlanAfterHookFailure(t *testing.T) {
	binPath := writeExecutable(t, filepath.Join(t.TempDir(), "agent-gate"))
	homeDir := t.TempDir()
	configHome := filepath.Join(homeDir, ".config")
	t.Setenv("XDG_CONFIG_HOME", configHome)
	configPath := filepath.Join(configHome, "agent-gate", "config.toml")
	geminiPath := filepath.Join(homeDir, ".gemini", "settings.json")
	for path, content := range map[string][]byte{
		configPath: []byte("[log]\nlevel = \"debug\"\n"),
		geminiPath: []byte(`{"hooks":`),
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatalf("MkdirAll %s: %v", path, err)
		}
		if err := os.WriteFile(path, content, 0o600); err != nil {
			t.Fatalf("WriteFile %s: %v", path, err)
		}
	}
	hooks := DefaultHooksOptions(binPath)
	hooks.HomeDir = homeDir
	hooks.Providers = []Provider{ProviderGemini}
	baseline := openInstallerFileDescriptorCount(t)
	for range 20 {
		if _, err := PrepareInstallation(InstallationOptions{
			Config: &config.EnsureDefaultsOptions{},
			Hooks:  &hooks,
		}); err == nil {
			t.Fatal("PrepareInstallation succeeded with malformed Gemini config")
		}
	}
	if got := openInstallerFileDescriptorCount(t); got > baseline+4 {
		t.Fatalf("open file descriptors = %d, baseline = %d", got, baseline)
	}
}

func openInstallerFileDescriptorCount(t *testing.T) int {
	t.Helper()
	count := 0
	for descriptor := range 4096 {
		if _, err := unix.FcntlInt(uintptr(descriptor), unix.F_GETFD, 0); err == nil {
			count++
		} else if !errors.Is(err, unix.EBADF) {
			t.Fatalf("inspect file descriptor %d: %v", descriptor, err)
		}
	}
	return count
}

func TestApplyInstallationUsesPreparedBytes(t *testing.T) {
	binPath := writeExecutable(t, filepath.Join(t.TempDir(), "agent-gate"))
	homeDir := t.TempDir()
	configHome := filepath.Join(homeDir, ".config")
	t.Setenv("XDG_CONFIG_HOME", configHome)
	configPath := filepath.Join(configHome, "agent-gate", "config.toml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		t.Fatalf("MkdirAll config directory: %v", err)
	}
	if err := os.WriteFile(configPath, []byte("[log]\nlevel = \"debug\"\n"), 0o600); err != nil {
		t.Fatalf("WriteFile config: %v", err)
	}
	cursorPath := filepath.Join(homeDir, ".cursor", "hooks.json")
	if err := os.MkdirAll(filepath.Dir(cursorPath), 0o700); err != nil {
		t.Fatalf("MkdirAll cursor directory: %v", err)
	}
	if err := os.WriteFile(cursorPath, []byte(`{"version":1,"theme":"prepared"}`), 0o600); err != nil {
		t.Fatalf("WriteFile cursor config: %v", err)
	}
	runner := &recordingRunner{}
	serviceTemplatesDir := t.TempDir()
	servicePlatformDir, serviceTemplateName := serviceTemplateForTest()
	serviceTemplatePath := filepath.Join(
		serviceTemplatesDir,
		servicePlatformDir,
		serviceTemplateName,
	)
	if err := os.MkdirAll(filepath.Dir(serviceTemplatePath), 0o700); err != nil {
		t.Fatalf("MkdirAll service template directory: %v", err)
	}
	if err := os.WriteFile(serviceTemplatePath, []byte("prepared @@BIN_PATH@@\n"), 0o600); err != nil {
		t.Fatalf("WriteFile service template: %v", err)
	}
	hooks := DefaultHooksOptions(binPath)
	hooks.HomeDir = homeDir
	hooks.Providers = []Provider{ProviderCursor}
	service := ServiceOptions{
		BinPath:             binPath,
		ServiceTemplatesDir: serviceTemplatesDir,
		HomeDir:             homeDir,
		ConfigHome:          configHome,
		StateHome:           filepath.Join(homeDir, ".local", "state"),
		Runner:              runner,
	}
	plan, err := PrepareInstallation(InstallationOptions{
		Config:  &config.EnsureDefaultsOptions{AutoUpdateMode: config.UpdateModeCheck},
		Service: &service,
		Hooks:   &hooks,
	})
	if err != nil {
		t.Fatalf("PrepareInstallation: %v", err)
	}
	preparedConfig := append([]byte(nil), plan.Config.Content...)
	preparedService := append([]byte(nil), plan.Service.Content...)
	preparedHooks := append([]byte(nil), plan.Hooks.writes[0].content...)
	preparedServicePath := plan.Service.TargetPath
	substitutedServicePath := filepath.Join(t.TempDir(), "wrong-service")
	plan.Config.Content = []byte("invalid substituted config")
	plan.Service.Content = []byte("substituted service")
	plan.Service.TargetPath = substitutedServicePath
	if err := os.WriteFile(serviceTemplatePath, []byte("changed after prepare\n"), 0o600); err != nil {
		t.Fatalf("WriteFile intervening service template: %v", err)
	}
	for path, content := range map[string][]byte{
		configPath: []byte("invalid after prepare\n"),
		cursorPath: []byte(`{"hooks":`),
	} {
		if err := os.WriteFile(path, content, 0o600); err != nil {
			t.Fatalf("WriteFile intervening %s: %v", path, err)
		}
	}

	result, err := ApplyInstallation(plan)
	if err != nil {
		t.Fatalf("ApplyInstallation: %v", err)
	}
	wantCompleted := []Stage{StageConfig, StageService, StageHooks}
	if strings.TrimSpace(strings.Join(stagesToStrings(result.Completed), ",")) != strings.Join(stagesToStrings(wantCompleted), ",") {
		t.Fatalf("completed stages = %v, want %v", result.Completed, wantCompleted)
	}
	assertFileBytes(t, configPath, preparedConfig)
	assertFileBytes(t, preparedServicePath, preparedService)
	if _, err := os.Stat(substitutedServicePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Stat substituted service path error = %v, want not found", err)
	}
	assertFileBytes(t, cursorPath, preparedHooks)
}

func TestApplyInstallationReportsStageAndRepairCommand(t *testing.T) {
	parentFile := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(parentFile, []byte("occupied"), 0o600); err != nil {
		t.Fatalf("WriteFile parent: %v", err)
	}
	plan := &InstallationPlan{Hooks: &HookInstallationPlan{
		writes: []hookInstallationWrite{{
			targetPath: filepath.Join(parentFile, "hooks.json"),
			provider:   "cursor",
			content:    []byte("{}\n"),
		}},
	}}

	result, err := ApplyInstallation(plan)
	if err == nil {
		t.Fatal("ApplyInstallation returned nil error")
	}
	if len(result.Completed) != 0 {
		t.Fatalf("completed stages = %v, want none", result.Completed)
	}
	var applyErr *ApplyError
	if !errors.As(err, &applyErr) {
		t.Fatalf("error type = %T, want *ApplyError: %v", err, err)
	}
	if applyErr.Stage != StageHooks {
		t.Fatalf("stage = %q, want %q", applyErr.Stage, StageHooks)
	}
	if applyErr.RepairCommand != "agent-gate install hooks" {
		t.Fatalf("repair command = %q", applyErr.RepairCommand)
	}
}

func TestApplyInstallationRepairCommandsRetainPreparedChoices(t *testing.T) {
	binPath := filepath.Join(t.TempDir(), "agent gate")
	writeExecutable(t, binPath)
	serviceTemplatesDir := filepath.Join(t.TempDir(), "service templates")
	hookTemplatesDir := filepath.Join(t.TempDir(), "hook templates")
	options := InstallationOptions{
		Config: &config.EnsureDefaultsOptions{
			AutoUpdateMode: config.UpdateModeCheck,
			AuditProfile:   config.AuditStorageProfileMinimal,
		},
		Hooks: &HooksOptions{
			BinPath:      binPath,
			TemplatesDir: hookTemplatesDir,
			Providers:    []Provider{ProviderCursor},
		},
		Service: &ServiceOptions{
			BinPath:             binPath,
			ServiceTemplatesDir: serviceTemplatesDir,
		},
	}
	commands := installationRepairCommands(options)
	wants := map[Stage][]string{
		StageConfig: {
			"--auto-update check",
			"--audit-profile minimal",
			"--no-service",
			"--providers ''",
		},
		StageService: {
			"--bin-path '" + binPath + "'",
			"--service-templates '" + serviceTemplatesDir + "'",
		},
		StageHooks: {
			"--bin-path '" + binPath + "'",
			"--templates '" + hookTemplatesDir + "'",
			"--providers cursor",
		},
	}
	for stage, fragments := range wants {
		for _, fragment := range fragments {
			if !strings.Contains(commands[stage], fragment) {
				t.Errorf("%s repair command %q missing %q", stage, commands[stage], fragment)
			}
		}
	}
}

func serviceTargetPathForTest(homeDir string, configHome string) string {
	if runtime.GOOS == "darwin" {
		return filepath.Join(homeDir, "Library", "LaunchAgents", launchdLabel+".plist")
	}
	return filepath.Join(configHome, "systemd", "user", systemdServiceName)
}

func serviceTemplateForTest() (string, string) {
	if runtime.GOOS == "darwin" {
		return "macos", launchdTemplateName
	}
	return "systemd", systemdServiceTemplate
}

func assertFileBytes(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile %s: %v", path, err)
	}
	if string(got) != string(want) {
		t.Fatalf("%s content differs\nwant:\n%s\ngot:\n%s", path, want, got)
	}
}

func stagesToStrings(stages []Stage) []string {
	values := make([]string, 0, len(stages))
	for _, stage := range stages {
		values = append(values, string(stage))
	}
	return values
}
