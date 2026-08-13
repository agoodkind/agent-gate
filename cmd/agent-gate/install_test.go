package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"

	"goodkind.io/agent-gate/api/daemonpb"
	"goodkind.io/agent-gate/internal/config"
	agentinstall "goodkind.io/agent-gate/internal/install"
)

type readinessDaemonFake struct {
	daemonpb.UnimplementedAgentGateDServer
	callCount      atomic.Int32
	executablePath string
	buildHash      string
	exited         chan struct{}
	// served closes when the second Status call arrives, which proves the caller
	// received and recorded the mismatched identity from the first. A test can
	// then expire the wait at exactly that point rather than hoping a fixed
	// wall-clock budget covered the round trip.
	served    chan struct{}
	serveOnce sync.Once
}

func (server *readinessDaemonFake) Status(
	ctx context.Context,
	_ *daemonpb.StatusRequest,
) (*daemonpb.StatusResponse, error) {
	if server.callCount.Add(1) == 1 {
		return &daemonpb.StatusResponse{
			ExecutablePath: server.executablePath,
			BuildHash:      server.buildHash,
		}, nil
	}
	if server.served != nil {
		server.serveOnce.Do(func() { close(server.served) })
	}
	<-ctx.Done()
	close(server.exited)
	return nil, ctx.Err()
}

func TestRunInstallDefaultsBinPathThroughResolver(t *testing.T) {
	binPath := writeCommandExecutable(t)
	canonicalBinPath, err := filepath.EvalSymlinks(binPath)
	if err != nil {
		t.Fatalf("EvalSymlinks executable: %v", err)
	}
	for _, subcommand := range []string{"hooks", "service", "all"} {
		t.Run(subcommand, func(t *testing.T) {
			dependencies := successfulInstallDependencies(binPath)
			var hookBinPath string
			var serviceBinPath string
			dependencies.prepareHooks = func(options agentinstall.HooksOptions) (*agentinstall.HookInstallationPlan, error) {
				hookBinPath = options.BinPath
				return &agentinstall.HookInstallationPlan{}, nil
			}
			dependencies.installService = func(options agentinstall.ServiceOptions) error {
				serviceBinPath = options.BinPath
				if options.Ready != nil {
					return options.Ready()
				}
				return nil
			}

			exitCode := runInstallWithDependencies([]string{subcommand}, dependencies)
			if exitCode != 0 {
				t.Fatalf("exitCode = %d, want 0", exitCode)
			}
			if subcommand != "service" && hookBinPath != canonicalBinPath {
				t.Fatalf("hook bin path = %q, want %q", hookBinPath, canonicalBinPath)
			}
			if subcommand != "hooks" && serviceBinPath != canonicalBinPath {
				t.Fatalf("service bin path = %q, want %q", serviceBinPath, canonicalBinPath)
			}
		})
	}
}

func TestRunInstallCanonicalizesSymlinkedBinPathBeforeInstallation(t *testing.T) {
	targetPath := writeCommandExecutable(t)
	canonicalTargetPath, err := filepath.EvalSymlinks(targetPath)
	if err != nil {
		t.Fatalf("EvalSymlinks executable target: %v", err)
	}
	linkPath := filepath.Join(t.TempDir(), "agent-gate")
	if err := os.Symlink(targetPath, linkPath); err != nil {
		t.Fatalf("Symlink executable: %v", err)
	}

	for _, subcommand := range []string{"service", "all"} {
		t.Run(subcommand, func(t *testing.T) {
			dependencies := successfulInstallDependencies(targetPath)
			var hookBinPath string
			var serviceBinPath string
			var readinessBinPath string
			dependencies.prepareHooks = func(options agentinstall.HooksOptions) (*agentinstall.HookInstallationPlan, error) {
				hookBinPath = options.BinPath
				return &agentinstall.HookInstallationPlan{}, nil
			}
			dependencies.installService = func(options agentinstall.ServiceOptions) error {
				serviceBinPath = options.BinPath
				return options.Ready()
			}
			dependencies.waitForReady = func(binPath string) error {
				readinessBinPath = binPath
				return nil
			}

			exitCode := runInstallWithDependencies(
				[]string{subcommand, "--bin-path", linkPath},
				dependencies,
			)
			if exitCode != 0 {
				t.Fatalf("exitCode = %d, want 0", exitCode)
			}
			if serviceBinPath != canonicalTargetPath {
				t.Fatalf("service bin path = %q, want canonical %q", serviceBinPath, canonicalTargetPath)
			}
			if readinessBinPath != canonicalTargetPath {
				t.Fatalf("readiness bin path = %q, want canonical %q", readinessBinPath, canonicalTargetPath)
			}
			if subcommand == "all" && hookBinPath != canonicalTargetPath {
				t.Fatalf("hook bin path = %q, want canonical %q", hookBinPath, canonicalTargetPath)
			}
		})
	}
}

func TestRunInstallPrevalidatesBeforeMutations(t *testing.T) {
	binPath := writeCommandExecutable(t)
	for _, args := range [][]string{
		{"all", "--auto-update", "sometimes"},
		{"all", "--audit-profile", "sometimes"},
		{"all", "unexpected"},
		{"all", "--bin-path", filepath.Join(t.TempDir(), "missing")},
	} {
		dependencies := successfulInstallDependencies(binPath)
		mutationCount := 0
		dependencies.ensureConfig = func(config.EnsureDefaultsOptions) error {
			mutationCount++
			return nil
		}
		dependencies.installService = func(agentinstall.ServiceOptions) error {
			mutationCount++
			return nil
		}
		dependencies.applyHooks = func(*agentinstall.HookInstallationPlan) error {
			mutationCount++
			return nil
		}

		if exitCode := runInstallWithDependencies(args, dependencies); exitCode != 2 {
			t.Fatalf("runInstallWithDependencies(%v) = %d, want 2", args, exitCode)
		}
		if mutationCount != 0 {
			t.Fatalf("runInstallWithDependencies(%v) mutations = %d, want 0", args, mutationCount)
		}
	}
}

func TestRunInstallRejectsBrokenExecutableSymlinkBeforeMutations(t *testing.T) {
	binPath := writeCommandExecutable(t)
	brokenLinkPath := filepath.Join(t.TempDir(), "agent-gate")
	if err := os.Symlink(filepath.Join(t.TempDir(), "missing"), brokenLinkPath); err != nil {
		t.Fatalf("Symlink broken executable: %v", err)
	}
	dependencies := successfulInstallDependencies(binPath)
	mutationCount := 0
	dependencies.ensureConfig = func(config.EnsureDefaultsOptions) error {
		mutationCount++
		return nil
	}
	dependencies.installService = func(agentinstall.ServiceOptions) error {
		mutationCount++
		return nil
	}
	dependencies.applyHooks = func(*agentinstall.HookInstallationPlan) error {
		mutationCount++
		return nil
	}

	exitCode, stderr := captureInstallStderrWithDependencies(
		t,
		[]string{"all", "--bin-path", brokenLinkPath},
		dependencies,
	)
	if exitCode != 2 {
		t.Fatalf("exitCode = %d, want 2", exitCode)
	}
	if mutationCount != 0 {
		t.Fatalf("mutationCount = %d, want 0", mutationCount)
	}
	for _, want := range []string{brokenLinkPath, "resolve executable symlinks", "no such file"} {
		if !strings.Contains(stderr, want) {
			t.Fatalf("stderr = %q, want %q", stderr, want)
		}
	}
}

func TestRunInstallAllPrevalidatesInstallerOptionsBeforeMutations(t *testing.T) {
	binPath := writeCommandExecutable(t)
	for _, failingStage := range []string{"service options", "hook options"} {
		t.Run(failingStage, func(t *testing.T) {
			dependencies := successfulInstallDependencies(binPath)
			mutationCount := 0
			dependencies.ensureConfig = func(config.EnsureDefaultsOptions) error {
				mutationCount++
				return nil
			}
			dependencies.installService = func(agentinstall.ServiceOptions) error {
				mutationCount++
				return nil
			}
			dependencies.applyHooks = func(*agentinstall.HookInstallationPlan) error {
				mutationCount++
				return nil
			}
			if failingStage == "service options" {
				dependencies.validateService = func(agentinstall.ServiceOptions) error {
					return errors.New("invalid service template")
				}
			} else {
				dependencies.prepareHooks = func(agentinstall.HooksOptions) (*agentinstall.HookInstallationPlan, error) {
					return nil, errors.New("invalid hook template")
				}
			}

			if exitCode := runInstallWithDependencies([]string{"all"}, dependencies); exitCode != 2 {
				t.Fatalf("exitCode = %d, want 2", exitCode)
			}
			if mutationCount != 0 {
				t.Fatalf("mutationCount = %d, want 0", mutationCount)
			}
		})
	}
}

func TestRunInstallAllMalformedLaterHookLeavesConfigServiceAndHooksUntouched(t *testing.T) {
	binPath := writeCommandExecutable(t)
	homeDir := t.TempDir()
	initialFiles := map[string][]byte{
		filepath.Join(homeDir, "config-state"):                         []byte("config-original"),
		filepath.Join(homeDir, "service-state"):                        []byte("service-original"),
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
	dependencies := successfulInstallDependencies(binPath)
	dependencies.prepareHooks = func(options agentinstall.HooksOptions) (*agentinstall.HookInstallationPlan, error) {
		options.HomeDir = homeDir
		return agentinstall.PrepareHookInstallation(options)
	}
	dependencies.ensureConfig = func(config.EnsureDefaultsOptions) error {
		return os.WriteFile(filepath.Join(homeDir, "config-state"), []byte("config-mutated"), 0o600)
	}
	dependencies.installService = func(agentinstall.ServiceOptions) error {
		return os.WriteFile(filepath.Join(homeDir, "service-state"), []byte("service-mutated"), 0o600)
	}
	dependencies.applyHooks = agentinstall.ApplyHookInstallation

	exitCode := runInstallWithDependencies([]string{"all"}, dependencies)
	if exitCode != 2 {
		t.Fatalf("exitCode = %d, want preflight exit 2", exitCode)
	}
	for path, want := range initialFiles {
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("ReadFile %s: %v", path, err)
		}
		if string(got) != string(want) {
			t.Errorf("%s changed after hook preflight failure\nwant: %s\ngot: %s", path, want, got)
		}
	}
}

func TestRunInstallAllOrdersConfigServiceReadinessAndHooks(t *testing.T) {
	binPath := writeCommandExecutable(t)
	dependencies := successfulInstallDependencies(binPath)
	var calls []string
	dependencies.ensureConfig = func(options config.EnsureDefaultsOptions) error {
		calls = append(calls, "config:"+options.AutoUpdateMode)
		return nil
	}
	dependencies.validateConfig = func() error {
		calls = append(calls, "validate")
		return nil
	}
	dependencies.installService = func(options agentinstall.ServiceOptions) error {
		calls = append(calls, "service")
		return options.Ready()
	}
	dependencies.waitForReady = func(string) error {
		calls = append(calls, "ready")
		return nil
	}
	dependencies.applyHooks = func(*agentinstall.HookInstallationPlan) error {
		calls = append(calls, "hooks")
		return nil
	}

	exitCode := runInstallWithDependencies(
		[]string{"all", "--auto-update", "check"},
		dependencies,
	)
	if exitCode != 0 {
		t.Fatalf("exitCode = %d, want 0", exitCode)
	}
	want := []string{"config:check", "validate", "service", "ready", "hooks"}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %v, want %v", calls, want)
	}
}

func TestInstallAllUsesSharedInstallationPlan(t *testing.T) {
	binPath := writeCommandExecutable(t)
	dependencies := successfulInstallDependencies(binPath)
	preparedPlan := &agentinstall.InstallationPlan{}
	prepareCalled := false
	applyCalled := false
	dependencies.prepareInstallation = func(
		options agentinstall.InstallationOptions,
	) (*agentinstall.InstallationPlan, error) {
		prepareCalled = true
		if options.Config == nil || options.Config.AutoUpdateMode != config.UpdateModeCheck {
			t.Fatalf("config options = %+v, want check mode", options.Config)
		}
		if options.Config.AuditProfile != config.AuditStorageProfileMinimal {
			t.Fatalf("audit profile = %q, want minimal", options.Config.AuditProfile)
		}
		if options.Service == nil {
			t.Fatal("service options are nil")
		}
		if options.Hooks == nil {
			t.Fatal("hook options are nil")
		}
		return preparedPlan, nil
	}
	dependencies.applyInstallation = func(
		plan *agentinstall.InstallationPlan,
	) (agentinstall.ApplyResult, error) {
		applyCalled = true
		if plan != preparedPlan {
			t.Fatalf("applied plan = %p, want %p", plan, preparedPlan)
		}
		return agentinstall.ApplyResult{}, nil
	}
	dependencies.ensureConfig = func(config.EnsureDefaultsOptions) error {
		t.Fatal("legacy config apply was called")
		return nil
	}
	dependencies.installService = func(agentinstall.ServiceOptions) error {
		t.Fatal("legacy service apply was called")
		return nil
	}
	dependencies.applyHooks = func(*agentinstall.HookInstallationPlan) error {
		t.Fatal("legacy hook apply was called")
		return nil
	}

	exitCode := runInstallWithDependencies(
		[]string{
			"all",
			"--auto-update", config.UpdateModeCheck,
			"--audit-profile", string(config.AuditStorageProfileMinimal),
		},
		dependencies,
	)
	if exitCode != 0 {
		t.Fatalf("exitCode = %d, want 0", exitCode)
	}
	if !prepareCalled || !applyCalled {
		t.Fatalf("shared plan calls: prepare=%t apply=%t", prepareCalled, applyCalled)
	}
}

func TestInstallConfigRepairCommandAppliesPreparedChoices(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	configDir := filepath.Join(configHome, "agent-gate")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatalf("MkdirAll config directory: %v", err)
	}
	configPath := filepath.Join(configDir, "config.toml")
	initial := `[audit.storage]
profile = "full"

[update]
enabled = true
mode = "apply"
`
	if err := os.WriteFile(configPath, []byte(initial), 0o600); err != nil {
		t.Fatalf("WriteFile initial config: %v", err)
	}

	exitCode := runInstallWithDependencies([]string{
		"all",
		"--no-service",
		"--no-claude",
		"--no-codex",
		"--no-cursor",
		"--no-gemini",
		"--no-copilot",
		"--auto-update", config.UpdateModeCheck,
		"--audit-profile", string(config.AuditStorageProfileMinimal),
	}, defaultInstallDependencies())
	if exitCode != 0 {
		t.Fatalf("exitCode = %d, want 0", exitCode)
	}
	loaded, err := config.LoadExisting(configPath)
	if err != nil {
		t.Fatalf("LoadExisting repaired config: %v", err)
	}
	if loaded.UpdateMode() != config.UpdateModeCheck {
		t.Fatalf("update mode = %q, want check", loaded.UpdateMode())
	}
	if loaded.AuditStoragePolicy().Profile != config.AuditStorageProfileMinimal {
		t.Fatalf("audit profile = %q, want minimal", loaded.AuditStoragePolicy().Profile)
	}
}

func TestRunInstallAllAppliesPreparedHookBytesAfterServiceReadiness(t *testing.T) {
	binPath := writeCommandExecutable(t)
	homeDir := t.TempDir()
	cursorPath := filepath.Join(homeDir, ".cursor", "hooks.json")
	if err := os.MkdirAll(filepath.Dir(cursorPath), 0o755); err != nil {
		t.Fatalf("MkdirAll Cursor directory: %v", err)
	}
	if err := os.WriteFile(cursorPath, []byte(`{"version":1,"theme":"prepared"}`), 0o600); err != nil {
		t.Fatalf("WriteFile Cursor hooks: %v", err)
	}
	dependencies := successfulInstallDependencies(binPath)
	dependencies.prepareHooks = func(options agentinstall.HooksOptions) (*agentinstall.HookInstallationPlan, error) {
		options.HomeDir = homeDir
		return agentinstall.PrepareHookInstallation(options)
	}
	dependencies.installService = func(options agentinstall.ServiceOptions) error {
		if err := os.WriteFile(cursorPath, []byte(`{"hooks":`), 0o600); err != nil {
			return err
		}
		return options.Ready()
	}
	dependencies.applyHooks = agentinstall.ApplyHookInstallation

	exitCode := runInstallWithDependencies([]string{
		"all", "--no-config", "--no-claude", "--no-codex", "--no-gemini", "--no-copilot",
	}, dependencies)
	if exitCode != 0 {
		t.Fatalf("exitCode = %d, want 0", exitCode)
	}
	content, err := os.ReadFile(cursorPath)
	if err != nil {
		t.Fatalf("ReadFile Cursor hooks: %v", err)
	}
	if !json.Valid(content) {
		t.Fatalf("prepared Cursor hooks were not applied: %s", content)
	}
	if !strings.Contains(string(content), `"theme": "prepared"`) {
		t.Fatalf("prepared Cursor content missing preserved setting: %s", content)
	}
}

func TestRunInstallAllHonorsOptOutFlags(t *testing.T) {
	binPath := writeCommandExecutable(t)
	dependencies := successfulInstallDependencies(binPath)
	configCalled := false
	serviceCalled := false
	readinessCalled := false
	var hooks agentinstall.HooksOptions
	dependencies.ensureConfig = func(config.EnsureDefaultsOptions) error {
		configCalled = true
		return nil
	}
	dependencies.installService = func(agentinstall.ServiceOptions) error {
		serviceCalled = true
		return nil
	}
	dependencies.waitForReady = func(string) error {
		readinessCalled = true
		return nil
	}
	dependencies.prepareHooks = func(options agentinstall.HooksOptions) (*agentinstall.HookInstallationPlan, error) {
		hooks = options
		return &agentinstall.HookInstallationPlan{}, nil
	}

	exitCode := runInstallWithDependencies([]string{
		"all", "--no-config", "--no-service", "--no-claude", "--no-codex",
		"--no-cursor", "--no-gemini", "--no-copilot",
	}, dependencies)
	if exitCode != 0 {
		t.Fatalf("exitCode = %d, want 0", exitCode)
	}
	if configCalled || serviceCalled || readinessCalled {
		t.Fatalf(
			"skipped calls: config=%t service=%t readiness=%t",
			configCalled,
			serviceCalled,
			readinessCalled,
		)
	}
	if hooks.Providers == nil || len(hooks.Providers) != 0 {
		t.Fatalf("provider opt-outs = %#v, want nonnil empty selection", hooks.Providers)
	}
}

func TestRunInstallProvidersUseCanonicalOrder(t *testing.T) {
	binPath := writeCommandExecutable(t)
	dependencies := successfulInstallDependencies(binPath)
	var hooks agentinstall.HooksOptions
	dependencies.prepareHooks = func(options agentinstall.HooksOptions) (*agentinstall.HookInstallationPlan, error) {
		hooks = options
		return &agentinstall.HookInstallationPlan{}, nil
	}

	exitCode := runInstallWithDependencies(
		[]string{"hooks", "--providers", "gemini,claude,cursor"},
		dependencies,
	)
	if exitCode != 0 {
		t.Fatalf("exitCode = %d, want 0", exitCode)
	}
	want := []agentinstall.Provider{
		agentinstall.ProviderClaude,
		agentinstall.ProviderCursor,
		agentinstall.ProviderGemini,
	}
	if !reflect.DeepEqual(hooks.Providers, want) {
		t.Fatalf("providers = %v, want %v", hooks.Providers, want)
	}
}

func TestRunInstallRejectsMixedProviderSelectionForms(t *testing.T) {
	binPath := writeCommandExecutable(t)
	for _, flagName := range []string{
		"--no-claude",
		"--no-claude=false",
		"--no-codex",
		"--no-cursor",
		"--no-gemini",
		"--no-copilot",
	} {
		t.Run(flagName, func(t *testing.T) {
			dependencies := successfulInstallDependencies(binPath)
			prepareCalled := false
			dependencies.prepareHooks = func(agentinstall.HooksOptions) (*agentinstall.HookInstallationPlan, error) {
				prepareCalled = true
				return &agentinstall.HookInstallationPlan{}, nil
			}

			exitCode, stderr := captureInstallStderrWithDependencies(
				t,
				[]string{"hooks", "--providers", "cursor", flagName},
				dependencies,
			)
			if exitCode != 2 {
				t.Fatalf("exitCode = %d, want 2", exitCode)
			}
			if prepareCalled {
				t.Fatal("hooks were prepared after conflicting selection flags")
			}
			wantFlag := strings.TrimSuffix(flagName, "=false")
			if !strings.Contains(stderr, "--providers cannot be used with "+wantFlag) {
				t.Fatalf("stderr = %q, want mixed selection error", stderr)
			}
		})
	}
}

func TestRunInstallAllSharedPlanPreservesAllLegacyProviderOptOuts(t *testing.T) {
	binPath := writeCommandExecutable(t)
	dependencies := successfulInstallDependencies(binPath)
	dependencies.prepareInstallation = func(
		options agentinstall.InstallationOptions,
	) (*agentinstall.InstallationPlan, error) {
		if options.Hooks == nil {
			t.Fatal("hook options are nil")
		}
		if options.Hooks.Providers == nil || len(options.Hooks.Providers) != 0 {
			t.Fatalf("providers = %#v, want nonnil empty selection", options.Hooks.Providers)
		}
		return &agentinstall.InstallationPlan{}, nil
	}
	dependencies.applyInstallation = func(
		*agentinstall.InstallationPlan,
	) (agentinstall.ApplyResult, error) {
		return agentinstall.ApplyResult{}, nil
	}

	exitCode := runInstallWithDependencies([]string{
		"all", "--no-config", "--no-service", "--no-claude", "--no-codex",
		"--no-cursor", "--no-gemini", "--no-copilot",
	}, dependencies)
	if exitCode != 0 {
		t.Fatalf("exitCode = %d, want 0", exitCode)
	}
}

func TestRunInstallRejectsInvalidProvidersBeforePreparation(t *testing.T) {
	binPath := writeCommandExecutable(t)
	for _, value := range []string{"cursor,other", "cursor,cursor"} {
		t.Run(value, func(t *testing.T) {
			dependencies := successfulInstallDependencies(binPath)
			prepareCalled := false
			dependencies.prepareHooks = func(agentinstall.HooksOptions) (*agentinstall.HookInstallationPlan, error) {
				prepareCalled = true
				return &agentinstall.HookInstallationPlan{}, nil
			}
			exitCode := runInstallWithDependencies(
				[]string{"hooks", "--providers", value},
				dependencies,
			)
			if exitCode != 2 {
				t.Fatalf("exitCode = %d, want 2", exitCode)
			}
			if prepareCalled {
				t.Fatal("hooks were prepared after invalid provider selection")
			}
		})
	}
}

func TestRunInstallAllDoesNotWriteHooksAfterReadinessFailure(t *testing.T) {
	binPath := writeCommandExecutable(t)
	dependencies := successfulInstallDependencies(binPath)
	hooksCalled := false
	dependencies.installService = func(options agentinstall.ServiceOptions) error {
		if err := options.Ready(); err != nil {
			return fmt.Errorf("readiness: %w", err)
		}
		return nil
	}
	dependencies.waitForReady = func(string) error {
		return errors.New("identity mismatch")
	}
	dependencies.applyHooks = func(*agentinstall.HookInstallationPlan) error {
		hooksCalled = true
		return nil
	}

	exitCode, stderr := captureInstallStderrWithDependencies(
		t,
		[]string{"all"},
		dependencies,
	)
	if exitCode != 1 {
		t.Fatalf("exitCode = %d, want 1", exitCode)
	}
	if hooksCalled {
		t.Fatal("hooks were installed after readiness failure")
	}
	if !strings.Contains(stderr, "readiness: identity mismatch") {
		t.Fatalf("stderr = %q, want stage-specific readiness error", stderr)
	}
}

func TestWaitForDaemonReadyRetriesUntilIdentityMatches(t *testing.T) {
	oldPath := canonicalCommandExecutable(t)
	newPath := canonicalCommandExecutable(t)
	attemptCount := 0
	status := func(context.Context) (daemonIdentity, error) {
		attemptCount++
		if attemptCount == 1 {
			return daemonIdentity{}, errors.New("not accepting connections")
		}
		if attemptCount == 2 {
			return daemonIdentity{ExecutablePath: oldPath, BuildHash: "old"}, nil
		}
		return daemonIdentity{ExecutablePath: newPath, BuildHash: "new"}, nil
	}

	err := waitForDaemonReady(
		context.Background(),
		newPath,
		"new",
		time.Millisecond,
		status,
	)
	if err != nil {
		t.Fatalf("waitForDaemonReady: %v", err)
	}
	if attemptCount != 3 {
		t.Fatalf("attemptCount = %d, want 3", attemptCount)
	}
}

func TestWaitForDaemonReadyCanonicalizesReportedExecutablePath(t *testing.T) {
	targetPath := writeCommandExecutable(t)
	canonicalTargetPath, err := filepath.EvalSymlinks(targetPath)
	if err != nil {
		t.Fatalf("EvalSymlinks executable target: %v", err)
	}
	linkPath := filepath.Join(t.TempDir(), "agent-gate")
	if err := os.Symlink(targetPath, linkPath); err != nil {
		t.Fatalf("Symlink executable: %v", err)
	}
	status := func(context.Context) (daemonIdentity, error) {
		return daemonIdentity{ExecutablePath: linkPath, BuildHash: "same"}, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()

	if err := waitForDaemonReady(
		ctx,
		canonicalTargetPath,
		"same",
		time.Millisecond,
		status,
	); err != nil {
		t.Fatalf("waitForDaemonReady: %v", err)
	}
}

func TestWaitForDaemonReadyTimeoutIncludesLastMismatch(t *testing.T) {
	oldPath := canonicalCommandExecutable(t)
	newPath := canonicalCommandExecutable(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()
	status := func(context.Context) (daemonIdentity, error) {
		return daemonIdentity{ExecutablePath: oldPath, BuildHash: "old"}, nil
	}

	err := waitForDaemonReady(ctx, newPath, "new", time.Millisecond, status)
	if err == nil {
		t.Fatal("waitForDaemonReady returned nil")
	}
	for _, want := range []string{"timed out", oldPath, "old"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %q, want %q", err, want)
		}
	}
}

func TestWaitForDaemonReadyProductionLookupPreservesMismatchAtDeadline(t *testing.T) {
	oldPath := canonicalCommandExecutable(t)
	newPath := canonicalCommandExecutable(t)
	runtimeDir, err := os.MkdirTemp("/tmp", "agent-gate-ready.")
	if err != nil {
		t.Fatalf("MkdirTemp runtime dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(runtimeDir) })
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)
	if err := os.MkdirAll(config.RuntimeDir(), 0o700); err != nil {
		t.Fatalf("MkdirAll runtime dir: %v", err)
	}
	listener, err := net.Listen("unix", config.DaemonSocketPath())
	if err != nil {
		t.Fatalf("Listen daemon socket: %v", err)
	}
	server := grpc.NewServer()
	fake := &readinessDaemonFake{
		executablePath: oldPath,
		buildHash:      "old",
		exited:         make(chan struct{}),
		served:         make(chan struct{}),
	}
	daemonpb.RegisterAgentGateDServer(server, fake)
	go func() {
		_ = server.Serve(listener)
	}()
	t.Cleanup(server.Stop)

	// The assertion is that the deadline error still names the last identity the
	// daemon reported, so the first Status call has to land before the wait
	// expires. Expire it when that call has been served rather than after a
	// fixed 50ms: under CI contention the gRPC round trip did not fit in that
	// budget, the wait timed out having seen nothing, and the error carried a
	// bare deadline instead of the mismatch. The long timeout is only a backstop
	// so a hang fails the test rather than blocking the suite.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	go func() {
		<-fake.served
		cancel()
	}()
	err = waitForDaemonReady(
		ctx,
		newPath,
		"new",
		time.Millisecond,
		lookupDaemonIdentity,
	)
	if err == nil {
		t.Fatal("waitForDaemonReady returned nil")
	}
	for _, want := range []string{"timed out", oldPath, "old"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want %q", err, want)
		}
	}
	select {
	case <-fake.exited:
	case <-time.After(time.Second):
		t.Fatal("Status RPC outlived readiness cancellation")
	}
}

func successfulInstallDependencies(binPath string) installDependencies {
	return installDependencies{
		resolveExecutable:  func() (string, error) { return binPath, nil },
		validateExecutable: agentinstall.ValidateExecutable,
		prepareHooks: func(agentinstall.HooksOptions) (*agentinstall.HookInstallationPlan, error) {
			return &agentinstall.HookInstallationPlan{}, nil
		},
		applyHooks:      func(*agentinstall.HookInstallationPlan) error { return nil },
		validateService: func(agentinstall.ServiceOptions) error { return nil },
		ensureConfig:    func(config.EnsureDefaultsOptions) error { return nil },
		validateConfig:  func() error { return nil },
		installService: func(options agentinstall.ServiceOptions) error {
			if options.Ready != nil {
				return options.Ready()
			}
			return nil
		},
		waitForReady: func(string) error { return nil },
	}
}

func writeCommandExecutable(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "agent-gate")
	if err := os.WriteFile(path, []byte("executable"), 0o755); err != nil {
		t.Fatalf("WriteFile executable: %v", err)
	}
	return path
}

func canonicalCommandExecutable(t *testing.T) string {
	t.Helper()
	path, err := filepath.EvalSymlinks(writeCommandExecutable(t))
	if err != nil {
		t.Fatalf("EvalSymlinks executable: %v", err)
	}
	return path
}

func captureInstallStderrWithDependencies(
	t *testing.T,
	args []string,
	dependencies installDependencies,
) (int, string) {
	t.Helper()
	return captureStderr(t, func() int { return runInstallWithDependencies(args, dependencies) })
}

func captureStderr(t *testing.T, run func() int) (int, string) {
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
	exitCode := run()
	_ = writePipe.Close()
	os.Stderr = originalStderr

	output, err := io.ReadAll(readPipe)
	if err != nil {
		t.Fatalf("ReadAll stderr: %v", err)
	}
	return exitCode, string(output)
}
