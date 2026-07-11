package daemon

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/stats"

	"goodkind.io/agent-gate/api/inferencepb"
	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/hook"
	"goodkind.io/agent-gate/internal/intake"
	"goodkind.io/agent-gate/internal/rules"
)

type deferredInferenceFake struct {
	inferencepb.UnimplementedInferenceServer
	mu         sync.Mutex
	calls      int
	outputJSON string
}

func newDeferredInferenceFake(outputJSON string) *deferredInferenceFake {
	return &deferredInferenceFake{
		UnimplementedInferenceServer: inferencepb.UnimplementedInferenceServer{},
		mu:                           sync.Mutex{}, calls: 0, outputJSON: outputJSON,
	}
}

func (server *deferredInferenceFake) Infer(
	context.Context,
	*inferencepb.InferRequest,
) (*inferencepb.InferReply, error) {
	server.mu.Lock()
	server.calls++
	outputJSON := server.outputJSON
	server.mu.Unlock()
	if outputJSON == "" {
		outputJSON = `{"decision":"block"}`
	}
	return &inferencepb.InferReply{
		OutputJson: outputJSON,
		Status:     inferencepb.InferenceStatus_INFERENCE_STATUS_COMPLETE,
	}, nil
}

func (server *deferredInferenceFake) callCount() int {
	server.mu.Lock()
	defer server.mu.Unlock()
	return server.calls
}

func TestDeferredAuditReusesHotInferenceOutcomeAndTrace(t *testing.T) {
	fake := newDeferredInferenceFake("")
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
		runtime,
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
	fake := newDeferredInferenceFake("")
	endpoint := startDeferredInferenceServer(t, fake)
	cfg := loadDeferredInferConfig(t, endpoint)
	processor := newDeferredProcessor(
		context.Background(),
		nil,
		nil,
		cfg,
		nil,
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

func TestDurableDeferredReplayExcludesAuditInferenceAndReportsEvaluatedRules(t *testing.T) {
	fake := newDeferredInferenceFake("")
	endpoint := startDeferredInferenceServer(t, fake)
	cfg := loadDeferredAuditInferConfig(t, endpoint)
	processor := newDeferredProcessor(
		context.Background(),
		nil,
		nil,
		cfg,
		nil,
		1,
		0,
		newDiscardLogger(),
	)
	t.Cleanup(processor.Close)
	record := intake.Record{
		EventID:        "evt-audit-replay",
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
	if len(deferredEvent.Rules) != 0 {
		t.Fatalf("reported evaluated rules = %+v, want none", deferredEvent.Rules)
	}
	if deferredEvent.Decision != hook.ResponseDecisionAllow {
		t.Fatalf("reconstructed decision = %q, want allow", deferredEvent.Decision)
	}
}

func TestDeferredAuditOnlyInferenceUsesDaemonRuntimeAndAppendsTraces(t *testing.T) {
	fake := newDeferredInferenceFake("")
	endpoint, connections := startCountedDeferredInferenceServer(t, fake)
	cfg := loadDeferredAuditInferConfig(t, endpoint)
	runtime := rules.NewInferRuntimeWithCache(nil, nil)
	t.Cleanup(runtime.Close)
	processor := newDeferredProcessor(
		context.Background(),
		nil,
		nil,
		cfg,
		runtime,
		1,
		0,
		newDiscardLogger(),
	)
	t.Cleanup(processor.Close)
	rawPayload := []byte(`{"session_id":"s1","hook_event_name":"PreToolUse","tool_name":"Shell","tool_input":{"command":"echo audit"}}`)

	for i := range 2 {
		eventID := "evt-audit-" + strconv.Itoa(i+1)
		hotEvent := hook.EvaluateHotWithEventID(
			context.Background(),
			rawPayload,
			hook.SyncConfig(cfg),
			hook.SystemCodex,
			func(string) string { return "" },
			eventID,
		).Deferred
		hotEvent.InferenceTraces = []rules.InferenceTrace{{LayerName: "hot-layer"}}
		record := intake.Record{
			EventID:        eventID,
			System:         "codex",
			SessionID:      "s1",
			EventName:      "PreToolUse",
			RawPayload:     rawPayload,
			EnvFingerprint: map[string]string{},
		}

		deferredEvent, ok := processor.rebuildDeferredAudit(
			context.Background(),
			record,
			&hotEvent,
		)
		if !ok {
			t.Fatal("rebuildDeferredAudit returned invalid event")
		}
		if len(deferredEvent.InferenceTraces) != 2 {
			t.Fatalf("inference traces = %+v", deferredEvent.InferenceTraces)
		}
		if deferredEvent.InferenceTraces[0].LayerName != "hot-layer" ||
			deferredEvent.InferenceTraces[1].LayerName != "audit-classification" {
			t.Fatalf("inference trace order = %+v", deferredEvent.InferenceTraces)
		}
	}

	if fake.callCount() != 2 {
		t.Fatalf("inference calls = %d, want 2", fake.callCount())
	}
	if connections.count.Load() != 1 {
		t.Fatalf("inference connections = %d, want 1", connections.count.Load())
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

type deferredConnectionCounter struct {
	count atomic.Int32
}

func (counter *deferredConnectionCounter) TagRPC(ctx context.Context, _ *stats.RPCTagInfo) context.Context {
	return ctx
}

func (counter *deferredConnectionCounter) HandleRPC(context.Context, stats.RPCStats) {}

func (counter *deferredConnectionCounter) TagConn(ctx context.Context, _ *stats.ConnTagInfo) context.Context {
	return ctx
}

func (counter *deferredConnectionCounter) HandleConn(_ context.Context, event stats.ConnStats) {
	if _, ok := event.(*stats.ConnBegin); ok {
		counter.count.Add(1)
	}
}

func startCountedDeferredInferenceServer(
	t *testing.T,
	fake *deferredInferenceFake,
) (string, *deferredConnectionCounter) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	counter := &deferredConnectionCounter{}
	server := grpc.NewServer(grpc.StatsHandler(counter))
	inferencepb.RegisterInferenceServer(server, fake)
	go func() {
		_ = server.Serve(listener)
	}()
	t.Cleanup(server.Stop)
	return listener.Addr().String(), counter
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

func loadDeferredAuditInferConfig(t *testing.T, endpoint string) *config.Config {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	body := `
[[rules]]
name = "infer-audit"
events = ["PreToolUse"]
action = "audit"
violation_message = "audit"
[[rules.conditions]]
kind = "infer"
endpoint = "` + endpoint + `"
layer_name = "audit-classification"
prompt = "Classify"
input_field = "tool_input.command"
output_schema = '{"type":"object"}'
response_json_field = "decision"
response_json_equals = "block"
cache_ttl_ms = 0
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
