package daemon

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"goodkind.io/agent-gate/api/daemonpb"
	"goodkind.io/agent-gate/internal/hook"
	"goodkind.io/agent-gate/internal/rules"
	execconcern "goodkind.io/agent-gate/internal/rules/concerns/exec"
)

func TestTemporalResponseGateMissingJQIsExecutionError(t *testing.T) {
	scriptPath, err := filepath.Abs(
		filepath.Join("..", "..", "examples", "validators", "temporal-response-gate.sh"),
	)
	if err != nil {
		t.Fatalf("absolute temporal response gate path: %v", err)
	}
	_, err = (execconcern.OSRunner{}).Run(
		context.Background(),
		[]string{"/bin/bash", scriptPath},
		time.Second,
		[]byte(`{"matched":[]}`),
		[]string{"PATH=" + t.TempDir()},
	)
	if !errors.Is(err, execconcern.ErrSignaled) {
		t.Fatalf("missing jq error = %v, want ErrSignaled", err)
	}
}

func TestEvaluateHookCursorTemporalGateScriptSequence(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skipf("temporal-response-gate.sh requires jq: %v", err)
	}
	scriptPath, err := filepath.Abs(
		filepath.Join("..", "..", "examples", "validators", "temporal-response-gate.sh"),
	)
	if err != nil {
		t.Fatalf("absolute temporal response gate path: %v", err)
	}
	setDaemonTestDirs(t)
	cfg := temporalDaemonConfig(t, fmt.Sprintf(`
[[rules]]
name = "temporal-response-gate"
cursor_events = ["stop"]
action = "inject"
output = "Continue"

[[rules.conditions]]
kind = "exec"
command = [%q]
field_paths = [
    "last_user_message",
    "last_response_output",
    "response_output",
    "loop_count",
]
cache_ttl_ms = 0
`, scriptPath))
	server, err := New(newDiscardLogger(), cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer server.Close()
	server.runtime.Load().execRuntime = rules.NewExecRuntimeWithCache(
		execconcern.OSRunner{},
		newDiscardLogger(),
		server.hotKV,
	)

	evaluate := func(rawJSON string) *daemonpb.EvaluateHookResponse {
		t.Helper()
		response, evaluateErr := server.EvaluateHook(
			context.Background(),
			&daemonpb.EvaluateHookRequest{
				RawJson:      []byte(rawJSON),
				ProviderHint: hook.SystemCursor.String(),
			},
		)
		if evaluateErr != nil {
			t.Fatalf("EvaluateHook: %v", evaluateErr)
		}
		return response
	}

	evaluate(
		`{"hook_event_name":"beforeSubmitPrompt","conversation_id":"c1",` +
			`"prompt":"initial request","loop_count":0}`,
	)
	firstStop := evaluate(
		`{"hook_event_name":"stop","conversation_id":"c1",` +
			`"status":"completed","loop_count":0}`,
	)
	if !strings.Contains(string(firstStop.StdoutData), `"followup_message":"Continue"`) {
		t.Fatalf("first stop response = %q", firstStop.StdoutData)
	}

	evaluate(
		`{"hook_event_name":"beforeSubmitPrompt","conversation_id":"c1",` +
			`"prompt":"Continue","loop_count":1}`,
	)
	repeatedStop := evaluate(
		`{"hook_event_name":"stop","conversation_id":"c1",` +
			`"status":"completed","loop_count":1}`,
	)
	if strings.Contains(string(repeatedStop.StdoutData), "followup_message") {
		t.Fatalf("repeated stop was not suppressed: %q", repeatedStop.StdoutData)
	}

	evaluate(
		`{"hook_event_name":"beforeSubmitPrompt","conversation_id":"c1",` +
			`"prompt":"different request","loop_count":2}`,
	)
	differentStop := evaluate(
		`{"hook_event_name":"stop","conversation_id":"c1",` +
			`"status":"completed","loop_count":2}`,
	)
	if !strings.Contains(string(differentStop.StdoutData), `"followup_message":"Continue"`) {
		t.Fatalf("different stop response = %q", differentStop.StdoutData)
	}

	evaluate(
		`{"hook_event_name":"beforeSubmitPrompt","conversation_id":"c2",` +
			`"prompt":"Continue","loop_count":1}`,
	)
	missingPriorResponse := evaluate(
		`{"hook_event_name":"stop","conversation_id":"c2",` +
			`"status":"completed","loop_count":1}`,
	)
	if strings.Contains(string(missingPriorResponse.StdoutData), "followup_message") {
		t.Fatalf(
			"later loop without prior response was not suppressed: %q",
			missingPriorResponse.StdoutData,
		)
	}
}
