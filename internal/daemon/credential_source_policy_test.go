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

func TestCredentialReadConfiguredSourceStopsInspectionAndExecution(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(root, "runtime"))
	fixtures := filepath.Join("..", "hook", "testdata", "credential_reads")
	policy, err := os.ReadFile(filepath.Join(fixtures, "source_policy.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(config.Path()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config.Path(), policy, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadExisting(config.Path())
	if err != nil {
		t.Fatal(err)
	}
	for name, fixture := range map[string]string{
		"controlled-source.sh": "controlled_source.sh", "ordinary.sh": "permitted_source.sh",
		"controlled-source.template.sh": "permitted_source.sh",
	} {
		body, err := os.ReadFile(filepath.Join(fixtures, fixture))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, name), []byte(strings.ReplaceAll(string(body), "{{ROOT}}", root)), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(root, "controlled-source.sh"), filepath.Join(root, "alias.sh")); err != nil {
		t.Fatal(err)
	}
	var diagnostics bytes.Buffer
	server, err := daemon.New(context.Background(), slog.New(slog.NewTextHandler(&diagnostics, nil)), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(server.Close)
	for _, name := range []string{"controlled-source.sh", "alias.sh", "ordinary.sh", "controlled-source.template.sh"} {
		t.Run(name, func(t *testing.T) {
			command := "bash " + filepath.Join(root, name)
			raw, err := json.Marshal(credentialDaemonPayload{
				Event: "PreToolUse", Session: "configured-source-policy", CWD: root, Tool: "Bash",
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
			var output bytes.Buffer
			if response.GetExitCode() == 0 {
				process := exec.CommandContext(t.Context(), "bash", filepath.Join(root, name))
				process.Stdout, process.Stderr = &output, &output
				if err := process.Run(); err != nil {
					t.Fatalf("the permitted helper failed: %v", err)
				}
			}
			blocked := name == "controlled-source.sh" || name == "alias.sh"
			if blocked {
				if response.GetExitCode() != 2 || !strings.Contains(string(response.GetStderrData()), "The configured source path") {
					t.Fatal("the daemon did not deny the configured source")
				}
				if output.Len() != 0 || strings.Contains(string(response.GetStderrData()), "Helper inspection observed") {
					t.Fatal("the protected source produced execution or inspection output")
				}
				if _, err := os.Stat(filepath.Join(root, "executed")); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("the daemon allowed execution of protected source")
				}
				return
			}
			if response.GetExitCode() != 0 || output.String() != "allowed\n" {
				t.Fatal("the daemon did not allow ordinary source or the template exception")
			}
			if err := os.Remove(filepath.Join(root, "executed")); err != nil {
				t.Fatal(err)
			}
		})
	}
}
