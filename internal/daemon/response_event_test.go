package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"goodkind.io/agent-gate/api/daemonpb"
	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/hook"
)

func TestEvaluateHookBlocksNeutralResponseText(t *testing.T) {
	setDaemonTestDirs(t)
	configPath := filepath.Join(t.TempDir(), "config.toml")
	prohibitedVerb := strings.Join([]string{"car", "ries"}, "")
	configBody := `
[[rules]]
name = "no-response-metaphor"
events = ["Response"]
action = "block"
violation_message = "Use a concrete operation."
field_paths = ["assistant_message"]
pattern = '''\b` + prohibitedVerb + `\b'''
`
	if err := os.WriteFile(configPath, []byte(configBody), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := config.LoadExisting(configPath)
	if err != nil {
		t.Fatalf("LoadExisting: %v", err)
	}
	if validationErrors := hook.ValidateConfig(cfg); len(validationErrors) > 0 {
		t.Fatalf("ValidateConfig: %v", validationErrors[0])
	}
	server, err := newReadyTestServer(newDiscardLogger(), cfg)
	if err != nil {
		t.Fatalf("newReadyTestServer: %v", err)
	}
	defer server.Close()

	payload := `{"hook_event_name":"Response","session_id":"session-1","assistant_message":"The response ` + prohibitedVerb + ` the result."}`
	response, err := server.EvaluateHook(context.Background(), &daemonpb.EvaluateHookRequest{
		RawJson:      []byte(payload),
		ProviderHint: hook.SystemResponse.String(),
	})
	if err != nil {
		t.Fatalf("EvaluateHook: %v", err)
	}
	if response.ExitCode != 2 {
		t.Fatalf("ExitCode = %d, want 2", response.ExitCode)
	}
	stderr := string(response.StderrData)
	if !strings.Contains(stderr, "no-response-metaphor") || !strings.Contains(stderr, "Use a concrete operation.") {
		t.Fatalf("Stderr = %q, want rule diagnostic", stderr)
	}
}
