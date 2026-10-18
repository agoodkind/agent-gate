package daemon_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"goodkind.io/agent-gate/api/daemonpb"
	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/daemon"
)

func TestCredentialReadCombinedShellFlags(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(root, "runtime"))
	cfg, err := config.LoadExisting(filepath.Join("..", "..", "examples", "rules", "no-credential-file-reads.toml"))
	if err != nil {
		t.Fatal(err)
	}
	const canary = "synthetic-combined-flags-canary"
	if err := os.WriteFile(filepath.Join(root, ".env"), []byte(strings.Join([]string{"SERVICE_PASSWORD", canary}, "=")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, fixture := range map[string]string{"helper.sh": "guarded_helper.sh", "safe.sh": "permitted_source.sh"} {
		body, err := os.ReadFile(filepath.Join("..", "hook", "testdata", "credential_reads", fixture))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, name), []byte(strings.ReplaceAll(string(body), "{{ROOT}}", root)), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	var diagnostics bytes.Buffer
	server, err := daemon.New(context.Background(), slog.New(slog.NewTextHandler(&diagnostics, nil)), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(server.Close)
	for _, flag := range []string{"-exec", "-cxe"} {
		for _, helper := range []string{"helper.sh", "safe.sh", "combined_safe_program.sh"} {
			t.Run(flag+"/"+helper, func(t *testing.T) {
				program := "./" + helper
				expectedOutput := "allowed\n"
				if helper == "combined_safe_program.sh" {
					body, err := os.ReadFile(filepath.Join("..", "hook", "testdata", "credential_reads", helper))
					if err != nil {
						t.Fatal(err)
					}
					program = string(body)
					expectedOutput += expectedOutput
				}
				command := "sh " + flag + " '" + program + "'"
				if helper != "combined_safe_program.sh" {
					command = "sh " + flag + " " + program
				}
				raw, err := json.Marshal(credentialDaemonPayload{
					Event: "PreToolUse", Session: "combined-shell-flags", CWD: root, Tool: "Bash",
					Input: credentialDaemonToolInput{Command: command},
				})
				if err != nil {
					t.Fatal(err)
				}
				response, err := server.EvaluateHook(t.Context(), &daemonpb.EvaluateHookRequest{
					RawJson: raw, ProviderHint: "claude", Cwd: root,
				})
				if err != nil {
					t.Fatal(err)
				}
				if helper == "helper.sh" {
					if response.GetExitCode() != 2 || !strings.Contains(string(response.GetStderrData()), "Credential file reads") {
						t.Fatal("the combined shell flags bypassed credential helper denial")
					}
					if strings.Contains(string(response.GetStdoutData())+string(response.GetStderrData())+diagnostics.String(), canary) {
						t.Fatal("the denied helper disclosed synthetic contents")
					}
					if _, err := os.Stat(filepath.Join(root, "executed")); !errors.Is(err, os.ErrNotExist) {
						t.Fatal("the denied helper executed")
					}
					return
				}
				if response.GetExitCode() != 0 {
					t.Fatalf("the daemon rejected a permitted helper under valid combined flags: %s", response.GetStderrData())
				}
				process := exec.CommandContext(t.Context(), "sh", flag, program)
				process.Dir = root
				var stdout, stderr bytes.Buffer
				process.Stdout, process.Stderr = &stdout, &stderr
				if err := process.Run(); err != nil {
					t.Fatalf("the host shell rejected the permitted combined flags: %v", err)
				}
				if stdout.String() != expectedOutput {
					t.Fatal("the permitted helper did not produce its expected output")
				}
				if err := os.Remove(filepath.Join(root, "executed")); err != nil {
					t.Fatal("the permitted helper did not create its execution marker")
				}
			})
		}
	}
}
