package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/grpc"

	"goodkind.io/agent-gate/api/daemonpb"
	"goodkind.io/agent-gate/internal/audit"
	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/daemon"
	"goodkind.io/agent-gate/internal/evaluation"
	installer "goodkind.io/agent-gate/internal/install"
	"goodkind.io/agent-gate/internal/intake"
	"goodkind.io/agent-gate/internal/setup"
)

func TestSetupEndToEndVerifiesInstalledProviderCommands(t *testing.T) {
	binaryPath := filepath.Join(t.TempDir(), "binary path with spaces", "agent-gate")
	if err := os.MkdirAll(filepath.Dir(binaryPath), 0o700); err != nil {
		t.Fatalf("MkdirAll binary directory: %v", err)
	}
	build := exec.CommandContext(t.Context(), "go", "build", "-o", binaryPath, ".")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build agent-gate: %v\n%s", err, output)
	}

	homeDir := t.TempDir()
	stateHome := filepath.Join(t.TempDir(), "state")
	runtimeBase, err := os.MkdirTemp("/tmp", "agent-gate-setup.")
	if err != nil {
		t.Fatalf("MkdirTemp runtime: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(runtimeBase) })
	t.Setenv("HOME", homeDir)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(homeDir, "config"))
	t.Setenv("XDG_STATE_HOME", stateHome)
	t.Setenv("XDG_RUNTIME_DIR", runtimeBase)

	options := installer.DefaultHooksOptions(binaryPath)
	options.HomeDir = homeDir
	plan, err := installer.PrepareHookInstallation(options)
	if err != nil {
		t.Fatalf("PrepareHookInstallation: %v", err)
	}
	if err := installer.ApplyHookInstallation(plan); err != nil {
		t.Fatalf("ApplyHookInstallation: %v", err)
	}

	enabled := true
	databasePath := filepath.Join(stateHome, "agent-gate", "sqlite", "audit.db")
	cfg := &config.Config{Audit: config.Audit{
		Enabled: &enabled,
		Outputs: config.AuditOutput{SQLite: config.AuditSQLiteOutput{Path: databasePath}},
	}}
	if err := config.EnsureRuntimeDir(); err != nil {
		t.Fatalf("EnsureRuntimeDir: %v", err)
	}
	listener, err := net.Listen("unix", config.DaemonSocketPath())
	if err != nil {
		t.Fatalf("Listen daemon socket: %v", err)
	}
	log := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	daemonServer, err := daemon.New(log, cfg)
	if err != nil {
		t.Fatalf("daemon.New: %v", err)
	}
	grpcServer := grpc.NewServer()
	daemonpb.RegisterAgentGateDServer(grpcServer, daemonServer)
	serveResult := make(chan error, 1)
	go func() { serveResult <- grpcServer.Serve(listener) }()
	closed := false
	t.Cleanup(func() {
		if !closed {
			grpcServer.Stop()
			daemonServer.Close()
		}
	})
	client, err := daemon.Connect(t.Context())
	if err != nil {
		t.Fatalf("connect daemon: %v", err)
	}
	if err := client.Close(); err != nil {
		t.Fatalf("close daemon client: %v", err)
	}

	setupID := "setup-e2e"
	results, err := setup.VerifyInstalledHooks(t.Context(), setup.ProbeRequest{
		SetupID:   setupID,
		Providers: installer.AllProviders(),
		HomeDir:   homeDir,
		BinPath:   binaryPath,
		Config:    cfg,
		Timeout:   5 * time.Second,
	})
	if err != nil {
		t.Fatalf("VerifyInstalledHooks: %v", err)
	}
	if len(results) != len(installer.AllProviders()) {
		t.Fatalf("results = %d, want %d", len(results), len(installer.AllProviders()))
	}
	for _, result := range results {
		if result.ExitCode != 0 || result.IntakeEventID == "" || result.ReceiptID <= 0 ||
			result.EvaluationID == "" || result.AuditEventID == "" || result.Decision != "allow" {
			t.Errorf("incomplete %s result: %#v", result.Provider, result)
		}
	}

	grpcServer.GracefulStop()
	daemonServer.Close()
	closed = true
	select {
	case err := <-serveResult:
		if err != nil && !errors.Is(err, grpc.ErrServerStopped) {
			t.Fatalf("serve daemon: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("daemon serve did not stop")
	}

	assertSetupDurableStateAfterClose(t, cfg, setupID)
}

func assertSetupDurableStateAfterClose(t *testing.T, cfg *config.Config, setupID string) {
	t.Helper()
	for _, provider := range installer.AllProviders() {
		system := string(provider)
		intakeResult, err := intake.Query(context.Background(), cfg, intake.QueryFilter{
			System: system, SessionID: setupID, Limit: 2,
		})
		if err != nil || len(intakeResult.Records) != 1 {
			t.Fatalf("%s intake after close = %d, %v", provider, len(intakeResult.Records), err)
		}
		evaluationResult, err := evaluation.Query(
			context.Background(),
			cfg.AuditSQLitePath(),
			evaluation.QueryFilter{
				Mode: "hot", System: system, SessionID: setupID,
				DetailMode: evaluation.QueryDetailSummary, Limit: 2,
			},
		)
		if err != nil || len(evaluationResult.Records) != 1 {
			t.Fatalf("%s evaluation after close = %#v, %v", provider, evaluationResult.Records, err)
		}
		auditResult, _, err := audit.QueryReadOnly(context.Background(), cfg, audit.QueryFilter{
			System: system, SessionID: setupID, Decision: "allow", Limit: 2,
		})
		if err != nil || len(auditResult) != 1 {
			t.Fatalf("%s audit after close = %d, %v", provider, len(auditResult), err)
		}
	}
}
