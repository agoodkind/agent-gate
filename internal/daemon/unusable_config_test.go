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

func unusableServer(t *testing.T) *Server {
	t.Helper()
	setDaemonTestDirs(t)
	// Missing a closing bracket, so nothing in the file decodes.
	writeConfig(t, config.Path(), "\n[[rules]]\nname = \"broken\"\nevents = [\"PreToolUse\"\n")

	cfg, err := config.LoadDegraded()
	if err != nil {
		t.Fatalf("degraded load refused to start: %v", err)
	}
	if !cfg.Unusable() {
		t.Fatal("test fixture did not produce an unusable config")
	}
	server, err := New(newDiscardLogger(), cfg)
	if err != nil {
		t.Fatalf("New refused an unusable config: %v", err)
	}
	t.Cleanup(server.Close)
	return server
}

// TestUnusableConfigAllowsButSaysSo is the load-bearing half of starting on a
// config that does not decode.
//
// A daemon holding no rules answers every call, and a clean allow from it would
// be worse than being down: a hook that reaches no daemon at least emits the
// fail-open notice, while a daemon returning an empty allow looks exactly like
// a call that passed every rule.
func TestUnusableConfigAllowsButSaysSo(t *testing.T) {
	server := unusableServer(t)

	response, err := server.EvaluateHook(context.Background(), &daemonpb.EvaluateHookRequest{
		RawJson: []byte(`{"hook_event_name":"PreToolUse","tool_name":"Bash",` +
			`"tool_input":{"command":"grep -rn x /repo"}}`),
		ProviderHint: hook.SystemClaude.String(),
	})
	if err != nil {
		t.Fatalf("EvaluateHook: %v", err)
	}
	if response.ExitCode != 0 {
		t.Fatalf("exit = %d, want 0; enforcing nothing must not block the call",
			response.ExitCode)
	}
	body := string(response.StdoutData) + string(response.StderrData)
	if !strings.Contains(body, "no rule was enforced") {
		t.Fatalf("response = %q, want it to say the call went unevaluated", body)
	}
	if !strings.Contains(body, string(hook.FailOpenReasonConfigUnusable)) {
		t.Fatalf("response = %q, want it to name config_unusable", body)
	}
}

// TestUnusableConfigIsReportedInStatus covers the operator's view. A daemon
// that is up and enforcing nothing is otherwise indistinguishable from a
// healthy one, which is how the outage went unnoticed for ten hours.
func TestUnusableConfigIsReportedInStatus(t *testing.T) {
	server := unusableServer(t)

	response, err := server.Status(context.Background(), &daemonpb.StatusRequest{})
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if response.RulesLoaded != 0 {
		t.Fatalf("RulesLoaded = %d, want 0", response.RulesLoaded)
	}
	if response.ConfigError == "" {
		t.Fatal("ConfigError is empty, so status looks healthy while nothing is enforced")
	}
	if !strings.Contains(response.ConfigError, "did not decode") {
		t.Fatalf("ConfigError = %q, want it to name the decode failure", response.ConfigError)
	}
}

// TestUsableConfigReportsItsRuleCount keeps the status signal meaningful.
func TestUsableConfigReportsItsRuleCount(t *testing.T) {
	setDaemonTestDirs(t)
	writeConfig(t, config.Path(), `
[audit]
enabled = false

[[rules]]
name = "block-alpha"
claude_events = ["PreToolUse"]
field_paths = ["tool_input.command"]
pattern = "alpha"
action = "block"
violation_message = "blocked"
`)
	cfg, err := config.LoadDegraded()
	if err != nil {
		t.Fatalf("LoadDegraded: %v", err)
	}
	server, err := New(newDiscardLogger(), cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer server.Close()

	response, err := server.Status(context.Background(), &daemonpb.StatusRequest{})
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if response.RulesLoaded != 1 {
		t.Fatalf("RulesLoaded = %d, want 1", response.RulesLoaded)
	}
	if response.ConfigError != "" {
		t.Fatalf("ConfigError = %q, want empty for a config that loaded",
			response.ConfigError)
	}
}

// TestUnusableConfigRecordsTheOutage confirms the evidence survives the daemon,
// so a later question of the form "was anything unenforced" has an answer.
func TestUnusableConfigRecordsTheOutage(t *testing.T) {
	// setDaemonTestDirs inside the helper owns XDG_STATE_HOME, so the record
	// path is read back from the same place the daemon writes it.
	server := unusableServer(t)

	_, err := server.EvaluateHook(context.Background(), &daemonpb.EvaluateHookRequest{
		RawJson:      []byte(`{"hook_event_name":"PreToolUse","tool_name":"Bash"}`),
		ProviderHint: hook.SystemClaude.String(),
	})
	if err != nil {
		t.Fatalf("EvaluateHook: %v", err)
	}

	record := FailOpenRecordPath()
	data, readErr := os.ReadFile(record) // #nosec G304 -- test-owned temp path.
	if readErr != nil {
		t.Fatalf("no durable record of the outage at %s: %v", record, readErr)
	}
	if !strings.Contains(string(data), "config_unusable") {
		t.Fatalf("record = %q, want it to name config_unusable", data)
	}
}

// assertSaysUnevaluated checks that a fail-open response names why no rule was
// applied. Every daemon path that allows a call without a verdict has to be
// distinguishable from one that passed every rule.
func assertSaysUnevaluated(
	t *testing.T,
	response *daemonpb.EvaluateHookResponse,
	reason hook.FailOpenReason,
) {
	t.Helper()
	body := string(response.StdoutData) + string(response.StderrData)
	if !strings.Contains(body, "no rule was enforced") {
		t.Fatalf("response = %q, want it to say the call went unevaluated", body)
	}
	if !strings.Contains(body, string(reason)) {
		t.Fatalf("response = %q, want it to name %q", body, reason)
	}
}

// TestNoDaemonPathReturnsASilentAllow is the structural guard behind the six
// per-path tests. Every place the daemon allows a call without a verdict has to
// name why, and an empty response literal is how that silently regresses.
//
// It reads the daemon's own source rather than exercising each path, because
// the point is to catch a path added later that no test covers yet. A response
// carrying real evaluated output is untouched; only the empty shape is refused.
func TestNoDaemonPathReturnsASilentAllow(t *testing.T) {
	sources, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	found := 0
	for _, name := range sources {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		data, readErr := os.ReadFile(name) // #nosec G304 -- package-local source.
		if readErr != nil {
			t.Fatalf("read %s: %v", name, readErr)
		}
		for number, line := range strings.Split(string(data), "\n") {
			if !strings.Contains(line, "StdoutData: nil") {
				continue
			}
			found++
			t.Errorf("%s:%d returns an allow with no body. An agent cannot tell "+
				"it from a call that passed every rule, so route it through "+
				"failOpenEvaluateHookResponseFor with the reason no rule was applied",
				name, number+1)
		}
	}
	if found == 0 {
		t.Log("no silent allow remains in the daemon")
	}
}
