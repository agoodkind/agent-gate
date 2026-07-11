package daemon

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"google.golang.org/grpc"

	"goodkind.io/agent-gate/api/inferencepb"
	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/hook"
	"goodkind.io/agent-gate/internal/intake"
	"goodkind.io/agent-gate/internal/rules"
)

type deferredInferenceFake struct {
	inferencepb.UnimplementedInferenceServer
	mu    sync.Mutex
	calls int
}

func (server *deferredInferenceFake) Infer(
	context.Context,
	*inferencepb.InferRequest,
) (*inferencepb.InferReply, error) {
	server.mu.Lock()
	server.calls++
	server.mu.Unlock()
	return &inferencepb.InferReply{
		OutputJson: `{"decision":"block"}`,
		Status:     inferencepb.InferenceStatus_INFERENCE_STATUS_COMPLETE,
	}, nil
}

func (server *deferredInferenceFake) callCount() int {
	server.mu.Lock()
	defer server.mu.Unlock()
	return server.calls
}

func TestDeferredAuditReusesHotInferenceOutcomeAndTrace(t *testing.T) {
	fake := &deferredInferenceFake{}
	endpoint := startDeferredInferenceServer(t, fake)
	cfg := loadDeferredInferConfig(t, endpoint)
	runtime := rules.NewInferRuntimeWithCache(nil, nil)
	t.Cleanup(runtime.Close)
	collector := &inferenceTraceSink{traces: nil}
	ctx := rules.WithInferenceTraceCollector(
		rules.WithInferRuntime(context.Background(), runtime),
		collector,
	)
	rawPayload := []byte(`{"session_id":"s1","hook_event_name":"PreToolUse","tool_name":"Shell","tool_input":{"command":"echo audit"}}`)
	syncEvaluation := hook.EvaluateHotWithEventID(
		ctx,
		rawPayload,
		hook.SyncConfig(cfg),
		hook.SystemCodex,
		func(string) string { return "" },
		"evt-infer",
	)
	syncEvaluation.Deferred.InferenceTraces = collector.snapshot()
	processor := newDeferredProcessor(
		context.Background(),
		nil,
		nil,
		cfg,
		1,
		0,
		newDiscardLogger(),
	)
	t.Cleanup(processor.Close)
	record := intake.Record{
		EventID:        "evt-infer",
		System:         "codex",
		SessionID:      "s1",
		EventName:      "PreToolUse",
		RawPayload:     rawPayload,
		EnvFingerprint: map[string]string{},
	}

	deferredEvent, ok := processor.rebuildDeferredAudit(
		context.Background(),
		record,
		&syncEvaluation.Deferred,
	)

	if !ok {
		t.Fatal("rebuildDeferredAudit returned invalid event")
	}
	if fake.callCount() != 1 {
		t.Fatalf("inference calls = %d, want 1", fake.callCount())
	}
	if len(deferredEvent.InferenceTraces) != 1 || deferredEvent.InferenceTraces[0].LayerName != "classification" {
		t.Fatalf("inference traces = %+v", deferredEvent.InferenceTraces)
	}
	if len(deferredEvent.AuditOnlyViolations) != 1 {
		t.Fatalf("audit-only violations = %d, want 1", len(deferredEvent.AuditOnlyViolations))
	}
}

func TestDurableDeferredReplayExcludesSynchronousInference(t *testing.T) {
	fake := &deferredInferenceFake{}
	endpoint := startDeferredInferenceServer(t, fake)
	cfg := loadDeferredInferConfig(t, endpoint)
	processor := newDeferredProcessor(
		context.Background(),
		nil,
		nil,
		cfg,
		1,
		0,
		newDiscardLogger(),
	)
	t.Cleanup(processor.Close)
	record := intake.Record{
		EventID:        "evt-replay",
		System:         "codex",
		SessionID:      "s1",
		EventName:      "PreToolUse",
		RawPayload:     []byte(`{"session_id":"s1","hook_event_name":"PreToolUse","tool_name":"Shell","tool_input":{"command":"echo audit"}}`),
		EnvFingerprint: map[string]string{},
	}

	deferredEvent, ok := processor.rebuildDeferredAudit(context.Background(), record, nil)

	if !ok {
		t.Fatal("rebuildDeferredAudit returned invalid event")
	}
	if fake.callCount() != 0 {
		t.Fatalf("inference calls = %d, want 0", fake.callCount())
	}
	if len(deferredEvent.AuditOnlyViolations) != 1 {
		t.Fatalf("audit-only violations = %d, want 1", len(deferredEvent.AuditOnlyViolations))
	}
}

func startDeferredInferenceServer(t *testing.T, fake *deferredInferenceFake) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	inferencepb.RegisterInferenceServer(server, fake)
	go func() {
		_ = server.Serve(listener)
	}()
	t.Cleanup(server.Stop)
	return listener.Addr().String()
}

func loadDeferredInferConfig(t *testing.T, endpoint string) *config.Config {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	body := `
[[rules]]
name = "infer-block"
events = ["PreToolUse"]
action = "block"
violation_message = "blocked"
[[rules.conditions]]
kind = "infer"
endpoint = "` + endpoint + `"
layer_name = "classification"
prompt = "Classify"
input_field = "tool_input.command"
output_schema = '{"type":"object"}'
response_json_field = "decision"
response_json_equals = "block"
cache_ttl_ms = 0

[[rules]]
name = "audit-echo"
events = ["PreToolUse"]
action = "audit"
violation_message = "audit"
pattern = "echo audit"
field_paths = ["tool_input.command"]
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadExisting(path)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}
