package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
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
	callCount atomic.Int32
	exited    chan struct{}
}

func (server *readinessDaemonFake) Status(
	ctx context.Context,
	_ *daemonpb.StatusRequest,
) (*daemonpb.StatusResponse, error) {
	if server.callCount.Add(1) == 1 {
		return &daemonpb.StatusResponse{
			ExecutablePath: "/old/agent-gate",
			BuildHash:      "old",
		}, nil
	}
	<-ctx.Done()
	close(server.exited)
	return nil, ctx.Err()
}

func TestRunInstallDefaultsBinPathThroughResolver(t *testing.T) {
	binPath := writeCommandExecutable(t)
	for _, subcommand := range []string{"hooks", "service", "all"} {
		t.Run(subcommand, func(t *testing.T) {
			dependencies := successfulInstallDependencies(binPath)
			var hookBinPath string
			var serviceBinPath string
			dependencies.installHooks = func(options agentinstall.HooksOptions) error {
				hookBinPath = options.BinPath
				return nil
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
			if subcommand != "service" && hookBinPath != binPath {
				t.Fatalf("hook bin path = %q, want %q", hookBinPath, binPath)
			}
			if subcommand != "hooks" && serviceBinPath != binPath {
				t.Fatalf("service bin path = %q, want %q", serviceBinPath, binPath)
			}
		})
	}
}

func TestRunInstallPrevalidatesBeforeMutations(t *testing.T) {
	binPath := writeCommandExecutable(t)
	for _, args := range [][]string{
		{"all", "--auto-update", "sometimes"},
		{"all", "unexpected"},
		{"all", "--bin-path", filepath.Join(t.TempDir(), "missing")},
	} {
		dependencies := successfulInstallDependencies(binPath)
		mutationCount := 0
		dependencies.ensureConfig = func(string) error {
			mutationCount++
			return nil
		}
		dependencies.installService = func(agentinstall.ServiceOptions) error {
			mutationCount++
			return nil
		}
		dependencies.installHooks = func(agentinstall.HooksOptions) error {
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

func TestRunInstallAllPrevalidatesInstallerOptionsBeforeMutations(t *testing.T) {
	binPath := writeCommandExecutable(t)
	for _, failingStage := range []string{"service options", "hook options"} {
		t.Run(failingStage, func(t *testing.T) {
			dependencies := successfulInstallDependencies(binPath)
			mutationCount := 0
			dependencies.ensureConfig = func(string) error {
				mutationCount++
				return nil
			}
			if failingStage == "service options" {
				dependencies.validateService = func(agentinstall.ServiceOptions) error {
					return errors.New("invalid service template")
				}
			} else {
				dependencies.validateHooks = func(agentinstall.HooksOptions) error {
					return errors.New("invalid hook template")
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

func TestRunInstallAllOrdersConfigServiceReadinessAndHooks(t *testing.T) {
	binPath := writeCommandExecutable(t)
	dependencies := successfulInstallDependencies(binPath)
	var calls []string
	dependencies.ensureConfig = func(mode string) error {
		calls = append(calls, "config:"+mode)
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
	dependencies.installHooks = func(agentinstall.HooksOptions) error {
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

func TestRunInstallAllHonorsOptOutFlags(t *testing.T) {
	binPath := writeCommandExecutable(t)
	dependencies := successfulInstallDependencies(binPath)
	configCalled := false
	serviceCalled := false
	readinessCalled := false
	var hooks agentinstall.HooksOptions
	dependencies.ensureConfig = func(string) error {
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
	dependencies.installHooks = func(options agentinstall.HooksOptions) error {
		hooks = options
		return nil
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
	if hooks.InstallClaude || hooks.InstallCodex || hooks.InstallCursor ||
		hooks.InstallGemini || hooks.InstallCopilot {
		t.Fatalf("provider opt-outs not preserved: %+v", hooks)
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
	dependencies.installHooks = func(agentinstall.HooksOptions) error {
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
	attemptCount := 0
	status := func(context.Context) (daemonIdentity, error) {
		attemptCount++
		if attemptCount == 1 {
			return daemonIdentity{}, errors.New("not accepting connections")
		}
		if attemptCount == 2 {
			return daemonIdentity{ExecutablePath: "/old/agent-gate", BuildHash: "old"}, nil
		}
		return daemonIdentity{ExecutablePath: "/new/agent-gate", BuildHash: "new"}, nil
	}

	err := waitForDaemonReady(
		context.Background(),
		"/new/agent-gate",
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

func TestWaitForDaemonReadyTimeoutIncludesLastMismatch(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()
	status := func(context.Context) (daemonIdentity, error) {
		return daemonIdentity{ExecutablePath: "/old/agent-gate", BuildHash: "old"}, nil
	}

	err := waitForDaemonReady(ctx, "/new/agent-gate", "new", time.Millisecond, status)
	if err == nil {
		t.Fatal("waitForDaemonReady returned nil")
	}
	for _, want := range []string{"timed out", "/old/agent-gate", "old"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %q, want %q", err, want)
		}
	}
}

func TestWaitForDaemonReadyProductionLookupPreservesMismatchAtDeadline(t *testing.T) {
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
	fake := &readinessDaemonFake{exited: make(chan struct{})}
	daemonpb.RegisterAgentGateDServer(server, fake)
	go func() {
		_ = server.Serve(listener)
	}()
	t.Cleanup(server.Stop)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err = waitForDaemonReady(
		ctx,
		"/new/agent-gate",
		"new",
		time.Millisecond,
		lookupDaemonIdentity,
	)
	if err == nil {
		t.Fatal("waitForDaemonReady returned nil")
	}
	for _, want := range []string{"timed out", "/old/agent-gate", "old"} {
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
		validateHooks:      func(agentinstall.HooksOptions) error { return nil },
		validateService:    func(agentinstall.ServiceOptions) error { return nil },
		ensureConfig:       func(string) error { return nil },
		validateConfig:     func() error { return nil },
		installService: func(options agentinstall.ServiceOptions) error {
			if options.Ready != nil {
				return options.Ready()
			}
			return nil
		},
		waitForReady: func(string) error { return nil },
		installHooks: func(agentinstall.HooksOptions) error { return nil },
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
