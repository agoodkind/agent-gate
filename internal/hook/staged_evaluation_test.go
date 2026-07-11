package hook_test

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"

	"goodkind.io/agent-gate/api/inferencepb"
	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/hook"
	"goodkind.io/agent-gate/internal/rules"
)

type stagedInferenceFake struct {
	inferencepb.UnimplementedInferenceServer
	mu      sync.Mutex
	models  []string
	handler func(context.Context, *inferencepb.InferRequest) (*inferencepb.InferReply, error)
}

func (server *stagedInferenceFake) Infer(
	ctx context.Context,
	request *inferencepb.InferRequest,
) (*inferencepb.InferReply, error) {
	server.mu.Lock()
	server.models = append(server.models, request.GetModel())
	server.mu.Unlock()
	return server.handler(ctx, request)
}

func (server *stagedInferenceFake) calledModels() []string {
	server.mu.Lock()
	defer server.mu.Unlock()
	return append([]string(nil), server.models...)
}

type stagedTraceCollector struct {
	mu     sync.Mutex
	traces []rules.InferenceTrace
}

func (collector *stagedTraceCollector) CollectInferenceTrace(trace rules.InferenceTrace) {
	collector.mu.Lock()
	defer collector.mu.Unlock()
	collector.traces = append(collector.traces, trace)
}

func (collector *stagedTraceCollector) snapshot() []rules.InferenceTrace {
	collector.mu.Lock()
	defer collector.mu.Unlock()
	return append([]rules.InferenceTrace(nil), collector.traces...)
}

func startStagedInferenceServer(t *testing.T, fake *stagedInferenceFake) string {
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

func loadStagedHookConfig(t *testing.T, endpoint string, body string) *config.Config {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	body = strings.ReplaceAll(body, "{{ENDPOINT}}", endpoint)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadExisting(path)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func stagedContext(t *testing.T, collector *stagedTraceCollector) context.Context {
	t.Helper()
	runtime := rules.NewInferRuntimeWithCache(nil, nil)
	t.Cleanup(runtime.Close)
	ctx := rules.WithInferRuntime(context.Background(), runtime)
	return rules.WithInferenceTraceCollector(ctx, collector)
}

func completeStagedReply(decision string) *inferencepb.InferReply {
	return &inferencepb.InferReply{
		OutputJson: `{"decision":"` + decision + `"}`,
		Status:     inferencepb.InferenceStatus_INFERENCE_STATUS_COMPLETE,
	}
}

const stagedPreToolPayload = `{"hook_event_name":"PreToolUse","session_id":"s1","tool_name":"Shell","tool_input":{"command":"blocked command"}}`

func TestEvaluateHotDeterministicBlockSkipsInferencePhase(t *testing.T) {
	fake := &stagedInferenceFake{handler: func(context.Context, *inferencepb.InferRequest) (*inferencepb.InferReply, error) {
		return completeStagedReply("block"), nil
	}}
	endpoint := startStagedInferenceServer(t, fake)
	cfg := loadStagedHookConfig(t, endpoint, `
[[rules]]
name = "inference-first-in-config"
events = ["PreToolUse"]
action = "block"
violation_message = "inference blocked"
[[rules.conditions]]
kind = "infer"
endpoint = "{{ENDPOINT}}"
layer_name = "inference"
prompt = "Classify"
input_field = "tool_input.command"
output_schema = '{"type":"object"}'
response_json_field = "decision"
response_json_equals = "block"
model = "remote-model"

[[rules]]
name = "deterministic-second-in-config"
events = ["PreToolUse"]
action = "block"
violation_message = "deterministic blocked"
field_paths = ["tool_input.command"]
pattern = "blocked"
`)
	collector := &stagedTraceCollector{}

	evaluation := hook.EvaluateHot(
		stagedContext(t, collector),
		[]byte(stagedPreToolPayload),
		cfg,
		hook.SystemCodex,
		func(string) string { return "" },
	)

	if evaluation.Deferred.Decision != hook.ResponseDecisionBlock {
		t.Fatalf("decision = %q, want block", evaluation.Deferred.Decision)
	}
	if models := fake.calledModels(); len(models) != 0 {
		t.Fatalf("inference models = %v, want none", models)
	}
	if traces := collector.snapshot(); len(traces) != 0 {
		t.Fatalf("inference traces = %+v, want none", traces)
	}
}

func TestEvaluateHotDeterministicAllowRunsInferencePhase(t *testing.T) {
	fake := &stagedInferenceFake{handler: func(_ context.Context, request *inferencepb.InferRequest) (*inferencepb.InferReply, error) {
		if request.GetModel() == "gpt-5.4-mini" {
			options := request.GetGenerationOptions()
			if options == nil ||
				options.GetReasoningEffort() != inferencepb.ReasoningEffort_REASONING_EFFORT_HIGH {
				t.Fatalf("mini generation options = %+v, want HIGH", options)
			}
		}
		return completeStagedReply("block"), nil
	}}
	endpoint := startStagedInferenceServer(t, fake)
	cfg := loadStagedHookConfig(t, endpoint, `
[[rules]]
name = "deterministic-allow"
events = ["PreToolUse"]
action = "block"
violation_message = "deterministic blocked"
field_paths = ["tool_input.command"]
pattern = "does-not-match"

[[rules]]
name = "inference-block"
events = ["PreToolUse"]
action = "block"
violation_message = "inference blocked"
[[rules.conditions]]
kind = "infer"
endpoint = "{{ENDPOINT}}"
layer_name = "inference"
prompt = "Classify"
input_field = "tool_input.command"
output_schema = '{"type":"object"}'
response_json_field = "decision"
response_json_equals = "block"
model = "v4"

[[rules.conditions]]
kind = "infer"
endpoint = "{{ENDPOINT}}"
layer_name = "confirmation"
prompt = "Confirm"
input_field = "tool_input.command"
output_schema = '{"type":"object"}'
response_json_field = "decision"
response_json_equals = "block"
model = "gpt-5.4-mini"
reasoning_effort = "high"
`)
	collector := &stagedTraceCollector{}

	evaluation := hook.EvaluateHot(
		stagedContext(t, collector),
		[]byte(stagedPreToolPayload),
		cfg,
		hook.SystemCodex,
		func(string) string { return "" },
	)

	if evaluation.Deferred.Decision != hook.ResponseDecisionBlock {
		t.Fatalf("decision = %q, want block", evaluation.Deferred.Decision)
	}
	if models := fake.calledModels(); strings.Join(models, ",") != "v4,gpt-5.4-mini" {
		t.Fatalf("inference models = %v", models)
	}
	if traces := collector.snapshot(); len(traces) != 2 {
		t.Fatalf("inference traces = %+v, want two", traces)
	}
}

func TestEvaluateHotObserveOnlyDeterministicBlockSkipsInferenceAndAudits(t *testing.T) {
	fake := &stagedInferenceFake{handler: func(context.Context, *inferencepb.InferRequest) (*inferencepb.InferReply, error) {
		return completeStagedReply("block"), nil
	}}
	endpoint := startStagedInferenceServer(t, fake)
	cfg := loadStagedHookConfig(t, endpoint, `
[[rules]]
name = "inference-first-in-config"
events = ["Stop"]
action = "block"
violation_message = "inference blocked"
[[rules.conditions]]
kind = "infer"
endpoint = "{{ENDPOINT}}"
layer_name = "inference"
prompt = "Classify"
input_field = "last_assistant_message"
output_schema = '{"type":"object"}'
response_json_field = "decision"
response_json_equals = "block"
model = "remote-model"

[[rules]]
name = "deterministic-block"
events = ["Stop"]
action = "block"
violation_message = "deterministic blocked"
field_paths = ["last_assistant_message"]
pattern = "blocked"
`)
	collector := &stagedTraceCollector{}
	rawPayload := []byte(`{"hook_event_name":"Stop","session_id":"s1","turn_id":"t1","stop_hook_active":false,"last_assistant_message":"blocked"}`)

	evaluation := hook.EvaluateHot(
		stagedContext(t, collector),
		rawPayload,
		cfg,
		hook.SystemCodex,
		func(string) string { return "" },
	)

	if evaluation.Deferred.Decision != hook.ResponseDecisionAllow {
		t.Fatalf("decision = %q, want allow", evaluation.Deferred.Decision)
	}
	if len(evaluation.Deferred.BlockingViolations) != 0 ||
		len(evaluation.Deferred.AuditOnlyViolations) != 1 ||
		evaluation.Deferred.AuditOnlyViolations[0].RuleName != "deterministic-block" {
		t.Fatalf("blocking/audit violations = %+v/%+v", evaluation.Deferred.BlockingViolations, evaluation.Deferred.AuditOnlyViolations)
	}
	if models := fake.calledModels(); len(models) != 0 {
		t.Fatalf("inference models = %v, want none", models)
	}
	if traces := collector.snapshot(); len(traces) != 0 {
		t.Fatalf("inference traces = %+v, want none", traces)
	}
}

func TestEvaluateHotInferencePhaseUsesOneSharedBudget(t *testing.T) {
	fake := &stagedInferenceFake{handler: func(ctx context.Context, request *inferencepb.InferRequest) (*inferencepb.InferReply, error) {
		if request.GetModel() == "slow-model" {
			<-ctx.Done()
			return nil, ctx.Err()
		}
		return completeStagedReply("block"), nil
	}}
	endpoint := startStagedInferenceServer(t, fake)
	cfg := loadStagedHookConfig(t, endpoint, `
[performance.hook]
inference_phase_timeout_ms = 30

[[rules]]
name = "slow-inference"
events = ["PreToolUse"]
action = "block"
violation_message = "slow blocked"
[[rules.conditions]]
kind = "infer"
endpoint = "{{ENDPOINT}}"
layer_name = "slow"
prompt = "Classify"
input_field = "tool_input.command"
output_schema = '{"type":"object"}'
response_json_field = "decision"
response_json_equals = "block"
model = "slow-model"
timeout_ms = 1000
on_error = "open"

[[rules]]
name = "later-inference"
events = ["PreToolUse"]
action = "block"
violation_message = "later blocked"
[[rules.conditions]]
kind = "infer"
endpoint = "{{ENDPOINT}}"
layer_name = "later"
prompt = "Classify"
input_field = "tool_input.command"
output_schema = '{"type":"object"}'
response_json_field = "decision"
response_json_equals = "block"
model = "later-model"
`)
	collector := &stagedTraceCollector{}
	started := time.Now()

	evaluation := hook.EvaluateHot(
		stagedContext(t, collector),
		[]byte(stagedPreToolPayload),
		cfg,
		hook.SystemCodex,
		func(string) string { return "" },
	)

	if elapsed := time.Since(started); elapsed > 250*time.Millisecond {
		t.Fatalf("elapsed = %s, want shared 30ms budget", elapsed)
	}
	if evaluation.Deferred.Decision != hook.ResponseDecisionAllow {
		t.Fatalf("decision = %q, want fail-open allow", evaluation.Deferred.Decision)
	}
	if models := fake.calledModels(); strings.Join(models, ",") != "slow-model" {
		t.Fatalf("inference models = %v, want only slow-model", models)
	}
	traces := collector.snapshot()
	if len(traces) != 1 || traces[0].ErrorClass != "deadline_exceeded" {
		t.Fatalf("inference traces = %+v", traces)
	}
}

func TestEvaluateHotCombinesViolationsInPhaseOrder(t *testing.T) {
	fake := &stagedInferenceFake{handler: func(context.Context, *inferencepb.InferRequest) (*inferencepb.InferReply, error) {
		return completeStagedReply("block"), nil
	}}
	endpoint := startStagedInferenceServer(t, fake)
	cfg := loadStagedHookConfig(t, endpoint, `
[[rules]]
name = "inference-one"
events = ["PreToolUse"]
action = "audit"
violation_message = "inference one"
[[rules.conditions]]
kind = "infer"
endpoint = "{{ENDPOINT}}"
layer_name = "inference-one"
prompt = "Classify"
input_field = "tool_input.command"
output_schema = '{"type":"object"}'
response_json_field = "decision"
response_json_equals = "block"
model = "model-one"

[[rules]]
name = "deterministic-one"
events = ["PreToolUse"]
action = "audit"
violation_message = "deterministic one"
field_paths = ["tool_input.command"]
pattern = "blocked"

[[rules]]
name = "inference-two"
events = ["PreToolUse"]
action = "audit"
violation_message = "inference two"
[[rules.conditions]]
kind = "infer"
endpoint = "{{ENDPOINT}}"
layer_name = "inference-two"
prompt = "Classify"
input_field = "tool_input.command"
output_schema = '{"type":"object"}'
response_json_field = "decision"
response_json_equals = "block"
model = "model-two"

[[rules]]
name = "deterministic-two"
events = ["PreToolUse"]
action = "audit"
violation_message = "deterministic two"
field_paths = ["tool_input.command"]
pattern = "command"
`)
	collector := &stagedTraceCollector{}

	evaluation := hook.EvaluateHot(
		stagedContext(t, collector),
		[]byte(stagedPreToolPayload),
		cfg,
		hook.SystemCodex,
		func(string) string { return "" },
	)

	var names []string
	for _, violation := range evaluation.Deferred.AuditOnlyViolations {
		names = append(names, violation.RuleName)
	}
	if strings.Join(names, ",") != "deterministic-one,deterministic-two,inference-one,inference-two" {
		t.Fatalf("violation order = %v", names)
	}
	if models := fake.calledModels(); strings.Join(models, ",") != "model-one,model-two" {
		t.Fatalf("inference model order = %v", models)
	}
}
