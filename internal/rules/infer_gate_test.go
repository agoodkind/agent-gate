package rules_test

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/stats"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"goodkind.io/agent-gate/api/inferencepb"
	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/hotkv"
	"goodkind.io/agent-gate/internal/rules"
	"goodkind.io/clyde/api/contextpb"
)

type inferenceFake struct {
	inferencepb.UnimplementedInferenceServer
	mu       sync.Mutex
	requests []*inferencepb.InferRequest
	handler  func(context.Context, *inferencepb.InferRequest) (*inferencepb.InferReply, error)
}

func (server *inferenceFake) Infer(ctx context.Context, request *inferencepb.InferRequest) (*inferencepb.InferReply, error) {
	server.mu.Lock()
	server.requests = append(server.requests, request)
	server.mu.Unlock()
	return server.handler(ctx, request)
}

func (server *inferenceFake) count() int {
	server.mu.Lock()
	defer server.mu.Unlock()
	return len(server.requests)
}

type clydeFake struct {
	contextpb.UnimplementedConversationContextServer
	mu       sync.Mutex
	requests []*contextpb.GetRecentTurnsRequest
	err      error
	delay    time.Duration
}

func (server *clydeFake) GetRecentTurns(ctx context.Context, request *contextpb.GetRecentTurnsRequest) (*contextpb.GetRecentTurnsReply, error) {
	server.mu.Lock()
	server.requests = append(server.requests, request)
	server.mu.Unlock()
	if server.delay > 0 {
		select {
		case <-time.After(server.delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if server.err != nil {
		return nil, server.err
	}
	return &contextpb.GetRecentTurnsReply{Turns: []*contextpb.Turn{{Role: "user", Text: "hello", Ts: "now"}}}, nil
}

type connectionCounter struct{ count atomic.Int32 }

func (counter *connectionCounter) TagRPC(ctx context.Context, _ *stats.RPCTagInfo) context.Context {
	return ctx
}
func (counter *connectionCounter) HandleRPC(context.Context, stats.RPCStats) {}
func (counter *connectionCounter) TagConn(ctx context.Context, _ *stats.ConnTagInfo) context.Context {
	return ctx
}

func (counter *connectionCounter) HandleConn(_ context.Context, event stats.ConnStats) {
	if _, ok := event.(*stats.ConnBegin); ok {
		counter.count.Add(1)
	}
}

func startInferenceServer(t *testing.T, fake *inferenceFake) (string, *connectionCounter) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	counter := &connectionCounter{}
	server := grpc.NewServer(grpc.StatsHandler(counter))
	inferencepb.RegisterInferenceServer(server, fake)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	return listener.Addr().String(), counter
}

func startClydeServer(t *testing.T, fake *clydeFake) (string, *connectionCounter) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	counter := &connectionCounter{}
	server := grpc.NewServer(grpc.StatsHandler(counter))
	contextpb.RegisterConversationContextServer(server, fake)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	return listener.Addr().String(), counter
}

func loadInferRule(t *testing.T, endpoint string, extra string) config.Rule {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	body := `[[rules]]
name = "infer-rule"
events = ["PreToolUse"]
action = "block"
violation_message = "blocked"

[[rules.conditions]]
kind = "infer"
endpoint = "` + endpoint + `"
layer_name = "layer"
prompt = "Classify"
input_field = "tool_input.command"
output_schema = '{"type":"object"}'
response_json_field = "decision"
response_json_equals = "block"
`
	if strings.Contains(extra, "response_json_field =") {
		body = strings.Replace(body, "response_json_field = \"decision\"\n", "", 1)
	}
	if strings.Contains(extra, "response_json_equals =") {
		body = strings.Replace(body, "response_json_equals = \"block\"\n", "", 1)
	}
	body += extra + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadExisting(path)
	if err != nil {
		t.Fatal(err)
	}
	return cfg.Rules[0]
}

func loadInferChainRule(t *testing.T, endpoint string) config.Rule {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	body := `[[rules]]
name = "infer-chain"
events = ["PreToolUse"]
action = "block"
violation_message = "blocked"

[[rules.conditions]]
kind = "infer"
endpoint = "` + endpoint + `"
layer_name = "v4"
prompt = "Classify"
input_field = "tool_input.command"
output_schema = '{"type":"object"}'
response_json_field = "decision"
response_json_equals = "block"
model = "v4"

[[rules.conditions]]
kind = "infer"
endpoint = "` + endpoint + `"
layer_name = "mini-high"
prompt = "Classify"
input_field = "tool_input.command"
output_schema = '{"type":"object"}'
response_json_field = "decision"
response_json_equals = "block"
model = "gpt-5.4-mini"
reasoning_effort = "high"
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadExisting(path)
	if err != nil {
		t.Fatal(err)
	}
	return cfg.Rules[0]
}

func evaluateInfer(t *testing.T, ctx context.Context, rule config.Rule, command string) []rules.Violation {
	t.Helper()
	fields := rules.FieldSet{ToolInputCommand: command, CWD: "/workspace", SessionID: "session"}
	return evaluateInferFields(t, ctx, rule, fields)
}

func evaluateInferFields(t *testing.T, ctx context.Context, rule config.Rule, fields rules.FieldSet) []rules.Violation {
	t.Helper()
	return rules.EvaluateAll(ctx, "claude", "PreToolUse", fields, []config.Rule{rule}, nil)
}

type traceCollector struct {
	mu     sync.Mutex
	traces []rules.InferenceTrace
}

func (collector *traceCollector) CollectInferenceTrace(trace rules.InferenceTrace) {
	collector.mu.Lock()
	defer collector.mu.Unlock()
	collector.traces = append(collector.traces, trace)
}

func TestInferNestedPredicateArbitrarySchemaAndTrace(t *testing.T) {
	fake := &inferenceFake{handler: func(_ context.Context, request *inferencepb.InferRequest) (*inferencepb.InferReply, error) {
		if request.GetInput() != "SECRET_INPUT" || request.GetPrompt() != "Classify" || request.GetModel() != "v4" {
			t.Fatalf("request = %+v", request)
		}
		return &inferencepb.InferReply{OutputJson: `{"animal":{"breed":"poodle"}}`, Status: inferencepb.InferenceStatus_INFERENCE_STATUS_COMPLETE}, nil
	}}
	endpoint, _ := startInferenceServer(t, fake)
	rule := loadInferRule(t, endpoint, "model = \"v4\"\nresponse_json_field = \"animal.breed\"\nresponse_json_equals = \"poodle\"")
	runtime := rules.NewInferRuntimeWithCache(nil, nil)
	t.Cleanup(runtime.Close)
	collector := &traceCollector{}
	ctx := rules.WithInferenceTraceCollector(rules.WithInferRuntime(context.Background(), runtime), collector)
	if violations := evaluateInfer(t, ctx, rule, "SECRET_INPUT"); len(violations) != 1 {
		t.Fatalf("violations = %d, want 1", len(violations))
	}
	if len(collector.traces) != 1 || collector.traces[0].Outcome != "matched" || collector.traces[0].Status != "complete" {
		t.Fatalf("traces = %+v", collector.traces)
	}
	encoded, _ := json.Marshal(collector.traces[0])
	for _, secret := range []string{"SECRET_INPUT", "Classify", "poodle"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("trace leaked payload %q: %s", secret, encoded)
		}
	}
}

func TestInferSendsGenerationOptionsAndPreservesMetadataInCacheTraces(t *testing.T) {
	fake := &inferenceFake{handler: func(_ context.Context, request *inferencepb.InferRequest) (*inferencepb.InferReply, error) {
		options := request.GetGenerationOptions()
		if options == nil || options.GetReasoningEffort() != inferencepb.ReasoningEffort_REASONING_EFFORT_HIGH {
			t.Fatalf("generation options = %+v, want HIGH", options)
		}
		if options.MaxCompletionTokens == nil || options.GetMaxCompletionTokens() != 2048 {
			t.Fatalf("max completion tokens = %+v", options.MaxCompletionTokens)
		}
		if options.Temperature == nil || options.GetTemperature() != 0.25 {
			t.Fatalf("temperature = %+v", options.Temperature)
		}
		return &inferencepb.InferReply{
			OutputJson: `{"decision":"block"}`,
			Status:     inferencepb.InferenceStatus_INFERENCE_STATUS_COMPLETE,
			Metadata: &inferencepb.InvocationMetadata{
				RequestId: "request-1", ServiceVersion: "service-1",
				RequestedModel: "gpt-5.4-mini", ActualModel: "gpt-5.4-mini-2026-07-01",
				BackendFingerprint: "fp-1", BackendVersion: "backend-1",
				PromptSha256: "prompt-hash", SchemaSha256: "schema-hash",
				PromptTokens: int64Pointer(41), CompletionTokens: int64Pointer(7),
				TotalTokens: int64Pointer(48), FinishReason: "stop", LatencyMs: 12,
			},
		}, nil
	}}
	endpoint, _ := startInferenceServer(t, fake)
	rule := loadInferRule(t, endpoint, "model = \"gpt-5.4-mini\"\nreasoning_effort = \"high\"\nmax_completion_tokens = 2048\ntemperature = 0.25\ncache_ttl_ms = 1000")
	runtime := rules.NewInferRuntimeWithCache(nil, nil)
	t.Cleanup(runtime.Close)
	collector := &traceCollector{}
	ctx := rules.WithInferenceTraceCollector(rules.WithInferRuntime(context.Background(), runtime), collector)
	evaluateInfer(t, ctx, rule, "input")
	evaluateInfer(t, ctx, rule, "input")
	if fake.count() != 1 {
		t.Fatalf("calls = %d, want 1", fake.count())
	}
	if len(collector.traces) != 2 || !collector.traces[1].CacheHit {
		t.Fatalf("traces = %+v", collector.traces)
	}
	for _, trace := range collector.traces {
		if trace.Metadata == nil || trace.Metadata.GetRequestId() != "request-1" ||
			trace.Metadata.GetPromptTokens() != 41 || trace.Metadata.GetCompletionTokens() != 7 ||
			trace.Metadata.GetTotalTokens() != 48 || trace.Metadata.PromptTokens == nil ||
			trace.Metadata.CompletionTokens == nil || trace.Metadata.TotalTokens == nil {
			t.Fatalf("trace metadata = %+v", trace.Metadata)
		}
		if trace.Metadata.GetRequestedModel() != "gpt-5.4-mini" ||
			trace.Metadata.GetPromptSha256() != "prompt-hash" ||
			trace.Metadata.GetSchemaSha256() != "schema-hash" {
			t.Fatalf("trace provenance = %+v", trace.Metadata)
		}
	}
}

func int64Pointer(value int64) *int64 {
	return &value
}

func TestInferFailedRPCCapturesClientLatency(t *testing.T) {
	fake := &inferenceFake{handler: func(context.Context, *inferencepb.InferRequest) (*inferencepb.InferReply, error) {
		time.Sleep(15 * time.Millisecond)
		return nil, status.Error(codes.Unavailable, "backend unavailable")
	}}
	endpoint, _ := startInferenceServer(t, fake)
	runtime := rules.NewInferRuntimeWithCache(nil, nil)
	t.Cleanup(runtime.Close)
	collector := &traceCollector{}
	ctx := rules.WithInferenceTraceCollector(rules.WithInferRuntime(context.Background(), runtime), collector)
	evaluateInfer(t, ctx, loadInferRule(t, endpoint, "on_error = \"open\""), "input")
	if len(collector.traces) != 1 || collector.traces[0].ErrorClass != "unavailable" {
		t.Fatalf("traces = %+v", collector.traces)
	}
	if collector.traces[0].Latency < 10*time.Millisecond {
		t.Fatalf("client latency = %s, want failed RPC duration", collector.traces[0].Latency)
	}
	if collector.traces[0].Metadata != nil {
		t.Fatalf("failed RPC metadata = %+v, want nil", collector.traces[0].Metadata)
	}
}

func TestInferErrorRepliesPreserveMetadataAndClientLatency(t *testing.T) {
	metadata := &inferencepb.InvocationMetadata{
		RequestId: "upstream-request", ServiceVersion: "service-version",
		RequestedModel: "requested-model", ActualModel: "actual-model",
		BackendFingerprint: "backend-fingerprint", BackendVersion: "backend-version",
		PromptSha256: "prompt-sha256", SchemaSha256: "schema-sha256",
		PromptTokens: int64Pointer(11), CompletionTokens: int64Pointer(4),
		TotalTokens: int64Pointer(15), FinishReason: "upstream-finish", LatencyMs: 7,
	}
	tests := []struct {
		name       string
		outputJSON string
		status     inferencepb.InferenceStatus
		errorClass string
	}{
		{
			name: "non-complete reply", outputJSON: `{"decision":"block"}`,
			status:     inferencepb.InferenceStatus_INFERENCE_STATUS_UNSPECIFIED,
			errorClass: "non_complete",
		},
		{
			name: "complete reply with invalid output", outputJSON: `{`,
			status:     inferencepb.InferenceStatus_INFERENCE_STATUS_COMPLETE,
			errorClass: "invalid_response",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fake := &inferenceFake{handler: func(context.Context, *inferencepb.InferRequest) (*inferencepb.InferReply, error) {
				time.Sleep(15 * time.Millisecond)
				return &inferencepb.InferReply{
					OutputJson: test.outputJSON,
					Status:     test.status,
					Metadata:   metadata,
				}, nil
			}}
			endpoint, _ := startInferenceServer(t, fake)
			runtime := rules.NewInferRuntimeWithCache(nil, nil)
			t.Cleanup(runtime.Close)
			collector := &traceCollector{}
			ctx := rules.WithInferenceTraceCollector(
				rules.WithInferRuntime(context.Background(), runtime),
				collector,
			)

			evaluateInfer(t, ctx, loadInferRule(t, endpoint, "on_error = \"open\""), "input")

			if len(collector.traces) != 1 {
				t.Fatalf("traces = %+v", collector.traces)
			}
			trace := collector.traces[0]
			if trace.ErrorClass != test.errorClass {
				t.Fatalf("error class = %q, want %q", trace.ErrorClass, test.errorClass)
			}
			if trace.Latency < 10*time.Millisecond {
				t.Fatalf("client latency = %s, want reply duration", trace.Latency)
			}
			if !proto.Equal(trace.Metadata, metadata) {
				t.Fatalf("metadata = %+v, want exact upstream metadata %+v", trace.Metadata, metadata)
			}
		})
	}
}

func TestInferBooleanScalarPredicate(t *testing.T) {
	fake := &inferenceFake{handler: func(context.Context, *inferencepb.InferRequest) (*inferencepb.InferReply, error) {
		return &inferencepb.InferReply{OutputJson: `{"decision":true}`, Status: 1}, nil
	}}
	endpoint, _ := startInferenceServer(t, fake)
	runtime := rules.NewInferRuntimeWithCache(nil, nil)
	t.Cleanup(runtime.Close)
	rule := loadInferRule(t, endpoint, "response_json_equals = true")
	if got := evaluateInfer(t, rules.WithInferRuntime(context.Background(), runtime), rule, "input"); len(got) != 1 {
		t.Fatalf("violations = %d, want boolean match to block", len(got))
	}
}

func TestInferNumericScalarPredicatesUseExecSemantics(t *testing.T) {
	tests := []struct {
		name     string
		expected string
		actual   string
	}{
		{name: "integer matches integral float", expected: "1", actual: "1.0"},
		{name: "float matches integer", expected: "1.0", actual: "1"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fake := &inferenceFake{handler: func(context.Context, *inferencepb.InferRequest) (*inferencepb.InferReply, error) {
				return &inferencepb.InferReply{OutputJson: `{"decision":` + test.actual + `}`, Status: 1}, nil
			}}
			endpoint, _ := startInferenceServer(t, fake)
			runtime := rules.NewInferRuntimeWithCache(nil, nil)
			t.Cleanup(runtime.Close)
			rule := loadInferRule(t, endpoint, "response_json_equals = "+test.expected)
			if got := evaluateInfer(t, rules.WithInferRuntime(context.Background(), runtime), rule, "input"); len(got) != 1 {
				t.Fatalf("violations = %d, want numeric match to block", len(got))
			}
		})
	}
}

func TestInferMatchNonmatchAndErrors(t *testing.T) {
	tests := []struct {
		name  string
		extra string
		reply *inferencepb.InferReply
		err   error
		want  bool
		class string
	}{
		{name: "mismatch allows", reply: &inferencepb.InferReply{OutputJson: `{"decision":"allow"}`, Status: 1}, want: false},
		{name: "nonmatch blocks", extra: `block_on = "nonmatch"`, reply: &inferencepb.InferReply{OutputJson: `{"decision":"allow"}`, Status: 1}, want: true},
		{name: "malformed open", reply: &inferencepb.InferReply{OutputJson: `{`, Status: 1}, want: false, class: "invalid_response"},
		{name: "missing closed", extra: `on_error = "closed"`, reply: &inferencepb.InferReply{OutputJson: `{}`, Status: 1}, want: true, class: "invalid_response"},
		{name: "noncomplete closed", extra: `on_error = "closed"`, reply: &inferencepb.InferReply{OutputJson: `{"decision":"block"}`}, want: true, class: "non_complete"},
		{name: "grpc open", err: status.Error(codes.Unavailable, "secret backend response"), want: false, class: "unavailable"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fake := &inferenceFake{handler: func(context.Context, *inferencepb.InferRequest) (*inferencepb.InferReply, error) {
				return test.reply, test.err
			}}
			endpoint, _ := startInferenceServer(t, fake)
			runtime := rules.NewInferRuntimeWithCache(nil, nil)
			t.Cleanup(runtime.Close)
			collector := &traceCollector{}
			ctx := rules.WithInferenceTraceCollector(rules.WithInferRuntime(context.Background(), runtime), collector)
			blocked := len(evaluateInfer(t, ctx, loadInferRule(t, endpoint, test.extra), "input")) > 0
			if blocked != test.want {
				t.Fatalf("blocked = %v, want %v", blocked, test.want)
			}
			if test.class != "" && (len(collector.traces) != 1 || collector.traces[0].ErrorClass != test.class) {
				t.Fatalf("traces = %+v", collector.traces)
			}
		})
	}
}

func TestInferDeadlineAndStandaloneAbsence(t *testing.T) {
	fake := &inferenceFake{handler: func(ctx context.Context, _ *inferencepb.InferRequest) (*inferencepb.InferReply, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	endpoint, _ := startInferenceServer(t, fake)
	runtime := rules.NewInferRuntimeWithCache(nil, nil)
	t.Cleanup(runtime.Close)
	collector := &traceCollector{}
	ctx := rules.WithInferenceTraceCollector(rules.WithInferRuntime(context.Background(), runtime), collector)
	if got := evaluateInfer(t, ctx, loadInferRule(t, endpoint, "timeout_ms = 10"), "input"); len(got) != 0 {
		t.Fatal("deadline should fail open")
	}
	if len(collector.traces) != 1 || collector.traces[0].ErrorClass != "deadline_exceeded" {
		t.Fatalf("deadline traces = %+v", collector.traces)
	}
	missing := loadInferRule(t, "127.0.0.1:1", "on_error = \"open\"\ntimeout_ms = 10")
	if got := evaluateInfer(t, context.Background(), missing, "input"); len(got) != 0 {
		t.Fatal("standalone missing service should fail open")
	}
}

func TestInferPersistentChannelsEndpointSeparationCacheTTLAndIdentity(t *testing.T) {
	handler := func(context.Context, *inferencepb.InferRequest) (*inferencepb.InferReply, error) {
		return &inferencepb.InferReply{OutputJson: `{"decision":"block"}`, Status: 1}, nil
	}
	firstFake := &inferenceFake{handler: handler}
	secondFake := &inferenceFake{handler: handler}
	firstEndpoint, firstConnections := startInferenceServer(t, firstFake)
	secondEndpoint, secondConnections := startInferenceServer(t, secondFake)
	store := hotkv.New(hotkv.Options{PruneInterval: 0})
	t.Cleanup(store.Close)
	runtime := rules.NewInferRuntimeWithCache(nil, store)
	t.Cleanup(runtime.Close)
	collector := &traceCollector{}
	ctx := rules.WithInferenceTraceCollector(rules.WithInferRuntime(context.Background(), runtime), collector)
	firstRule := loadInferRule(t, firstEndpoint, "cache_ttl_ms = 20")
	evaluateInfer(t, ctx, firstRule, "same")
	evaluateInfer(t, ctx, firstRule, "same")
	if firstFake.count() != 1 || firstConnections.count.Load() != 1 {
		t.Fatalf("first calls/connections = %d/%d", firstFake.count(), firstConnections.count.Load())
	}
	if len(collector.traces) < 2 || !collector.traces[1].CacheHit {
		t.Fatalf("cache traces = %+v", collector.traces)
	}
	differentModel := loadInferRule(t, firstEndpoint, "cache_ttl_ms = 20\nmodel = \"other\"")
	evaluateInfer(t, ctx, differentModel, "same")
	if firstFake.count() != 2 {
		t.Fatal("model must participate in cache identity")
	}
	evaluateInfer(t, ctx, loadInferRule(t, secondEndpoint, ""), "same")
	if secondConnections.count.Load() != 1 {
		t.Fatal("separate endpoint must use separate channel")
	}
	time.Sleep(25 * time.Millisecond)
	evaluateInfer(t, ctx, firstRule, "same")
	if firstFake.count() != 3 {
		t.Fatal("expired TTL must call backend again")
	}
}

func TestInferCacheIdentityIncludesGenerationOptions(t *testing.T) {
	fake := &inferenceFake{handler: func(context.Context, *inferencepb.InferRequest) (*inferencepb.InferReply, error) {
		return &inferencepb.InferReply{OutputJson: `{"decision":"block"}`, Status: 1}, nil
	}}
	endpoint, _ := startInferenceServer(t, fake)
	runtime := rules.NewInferRuntimeWithCache(nil, nil)
	t.Cleanup(runtime.Close)
	ctx := rules.WithInferRuntime(context.Background(), runtime)
	declarations := []string{
		"reasoning_effort = \"high\"",
		"reasoning_effort = \"low\"",
		"reasoning_effort = \"high\"\nmax_completion_tokens = 100",
		"reasoning_effort = \"high\"\nmax_completion_tokens = 101",
		"reasoning_effort = \"high\"\ntemperature = 0.0",
		"reasoning_effort = \"high\"\ntemperature = 0.5",
	}
	for _, declaration := range declarations {
		evaluateInfer(t, ctx, loadInferRule(t, endpoint, declaration+"\ncache_ttl_ms = 1000"), "same")
	}
	if fake.count() != len(declarations) {
		t.Fatalf("calls = %d, want %d distinct generation identities", fake.count(), len(declarations))
	}
}

func TestInferCacheIdentityIncludesResolvedClydeFields(t *testing.T) {
	clyde := &clydeFake{}
	clydeEndpoint, _ := startClydeServer(t, clyde)
	inference := &inferenceFake{handler: func(context.Context, *inferencepb.InferRequest) (*inferencepb.InferReply, error) {
		return &inferencepb.InferReply{OutputJson: `{"decision":"block"}`, Status: 1}, nil
	}}
	endpoint, _ := startInferenceServer(t, inference)
	extra := "cache_ttl_ms = 1000\ncontext_source = \"clyde_recent_turns\"\ncontext_endpoint = \"" + clydeEndpoint + "\"\ncontext_workspace_field = \"cwd\"\ncontext_session_field = \"session_id\"\ncontext_on_error = \"error\""
	rule := loadInferRule(t, endpoint, extra)
	runtime := rules.NewInferRuntimeWithCache(nil, nil)
	t.Cleanup(runtime.Close)
	ctx := rules.WithInferRuntime(context.Background(), runtime)

	evaluateInferFields(t, ctx, rule, rules.FieldSet{ToolInputCommand: "same", CWD: "/first", SessionID: "session"})
	evaluateInferFields(t, ctx, rule, rules.FieldSet{ToolInputCommand: "same", CWD: "/second", SessionID: "session"})
	evaluateInferFields(t, ctx, rule, rules.FieldSet{ToolInputCommand: "same", CWD: "/second", SessionID: "other"})

	if inference.count() != 3 {
		t.Fatalf("inference calls = %d, want 3", inference.count())
	}
}

func TestInferCacheIdentitySeparatesEmbeddedNULContextValues(t *testing.T) {
	clyde := &clydeFake{}
	clydeEndpoint, _ := startClydeServer(t, clyde)
	inference := &inferenceFake{handler: func(context.Context, *inferencepb.InferRequest) (*inferencepb.InferReply, error) {
		return &inferencepb.InferReply{OutputJson: `{"decision":"block"}`, Status: 1}, nil
	}}
	endpoint, _ := startInferenceServer(t, inference)
	extra := "cache_ttl_ms = 1000\ncontext_source = \"clyde_recent_turns\"\ncontext_endpoint = \"" + clydeEndpoint + "\"\ncontext_workspace_field = \"cwd\"\ncontext_session_field = \"session_id\"\ncontext_on_error = \"error\""
	rule := loadInferRule(t, endpoint, extra)
	runtime := rules.NewInferRuntimeWithCache(nil, nil)
	t.Cleanup(runtime.Close)
	ctx := rules.WithInferRuntime(context.Background(), runtime)

	evaluateInferFields(t, ctx, rule, rules.FieldSet{
		ToolInputCommand: "same",
		CWD:              "a\x00b",
		SessionID:        "c",
	})
	evaluateInferFields(t, ctx, rule, rules.FieldSet{
		ToolInputCommand: "same",
		CWD:              "a",
		SessionID:        "b\x00c",
	})

	if inference.count() != 2 {
		t.Fatalf("inference calls = %d, want 2", inference.count())
	}
}

func TestInferSingleflightIdentitySeparatesEmbeddedNULContextValues(t *testing.T) {
	clyde := &clydeFake{delay: 20 * time.Millisecond}
	clydeEndpoint, _ := startClydeServer(t, clyde)
	inference := &inferenceFake{handler: func(context.Context, *inferencepb.InferRequest) (*inferencepb.InferReply, error) {
		time.Sleep(30 * time.Millisecond)
		return &inferencepb.InferReply{OutputJson: `{"decision":"block"}`, Status: 1}, nil
	}}
	endpoint, _ := startInferenceServer(t, inference)
	extra := "context_source = \"clyde_recent_turns\"\ncontext_endpoint = \"" + clydeEndpoint + "\"\ncontext_workspace_field = \"cwd\"\ncontext_session_field = \"session_id\"\ncontext_on_error = \"error\""
	rule := loadInferRule(t, endpoint, extra)
	runtime := rules.NewInferRuntimeWithCache(nil, nil)
	t.Cleanup(runtime.Close)
	ctx := rules.WithInferRuntime(context.Background(), runtime)
	fieldSets := []rules.FieldSet{
		{ToolInputCommand: "same", CWD: "a\x00b", SessionID: "c"},
		{ToolInputCommand: "same", CWD: "a", SessionID: "b\x00c"},
	}
	var waitGroup sync.WaitGroup
	for _, fields := range fieldSets {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			_ = rules.EvaluateAll(
				ctx,
				"claude",
				"PreToolUse",
				fields,
				[]config.Rule{rule},
				nil,
			)
		}()
	}
	waitGroup.Wait()

	if inference.count() != 2 {
		t.Fatalf("inference calls = %d, want 2", inference.count())
	}
}

func TestInferSingleflightIdentityIncludesResolvedClydeFields(t *testing.T) {
	clyde := &clydeFake{delay: 20 * time.Millisecond}
	clydeEndpoint, _ := startClydeServer(t, clyde)
	inference := &inferenceFake{handler: func(context.Context, *inferencepb.InferRequest) (*inferencepb.InferReply, error) {
		time.Sleep(30 * time.Millisecond)
		return &inferencepb.InferReply{OutputJson: `{"decision":"block"}`, Status: 1}, nil
	}}
	endpoint, _ := startInferenceServer(t, inference)
	extra := "context_source = \"clyde_recent_turns\"\ncontext_endpoint = \"" + clydeEndpoint + "\"\ncontext_workspace_field = \"cwd\"\ncontext_session_field = \"session_id\"\ncontext_on_error = \"error\""
	rule := loadInferRule(t, endpoint, extra)
	runtime := rules.NewInferRuntimeWithCache(nil, nil)
	t.Cleanup(runtime.Close)
	ctx := rules.WithInferRuntime(context.Background(), runtime)
	fields := []rules.FieldSet{
		{ToolInputCommand: "same", CWD: "/first", SessionID: "session"},
		{ToolInputCommand: "same", CWD: "/second", SessionID: "other"},
	}
	var waitGroup sync.WaitGroup
	for _, fieldSet := range fields {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			evaluateInferFields(t, ctx, rule, fieldSet)
		}()
	}
	waitGroup.Wait()

	if inference.count() != 2 {
		t.Fatalf("inference calls = %d, want 2", inference.count())
	}
}

func TestInferConcurrentIdenticalCallsSingleflight(t *testing.T) {
	fake := &inferenceFake{handler: func(context.Context, *inferencepb.InferRequest) (*inferencepb.InferReply, error) {
		time.Sleep(40 * time.Millisecond)
		return &inferencepb.InferReply{OutputJson: `{"decision":"block"}`, Status: 1}, nil
	}}
	endpoint, _ := startInferenceServer(t, fake)
	runtime := rules.NewInferRuntimeWithCache(nil, nil)
	t.Cleanup(runtime.Close)
	ctx := rules.WithInferRuntime(context.Background(), runtime)
	rule := loadInferRule(t, endpoint, "")
	var waitGroup sync.WaitGroup
	for range 12 {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			_ = evaluateInfer(t, ctx, rule, "same")
		}()
	}
	waitGroup.Wait()
	if fake.count() != 1 {
		t.Fatalf("calls = %d, want 1", fake.count())
	}
}

func TestInferClydeContextPoliciesSelectorsBoundsAndChannelReuse(t *testing.T) {
	clyde := &clydeFake{}
	clydeEndpoint, clydeConnections := startClydeServer(t, clyde)
	var inferenceCalls atomic.Int32
	inference := &inferenceFake{handler: func(_ context.Context, request *inferencepb.InferRequest) (*inferencepb.InferReply, error) {
		call := inferenceCalls.Add(1)
		if call <= 2 && request.GetContext() != `{"turns":[{"role":"user","text":"hello","ts":"now"}]}` {
			t.Fatalf("context = %q", request.GetContext())
		}
		return &inferencepb.InferReply{OutputJson: `{"decision":"block"}`, Status: 1}, nil
	}}
	endpoint, _ := startInferenceServer(t, inference)
	extra := "context_source = \"clyde_recent_turns\"\ncontext_endpoint = \"" + clydeEndpoint + "\"\ncontext_workspace_field = \"cwd\"\ncontext_session_field = \"session_id\"\ncontext_turn_budget = 3\ncontext_max_chars_per_turn = 120\ncontext_on_error = \"error\""
	runtime := rules.NewInferRuntimeWithCache(nil, nil)
	t.Cleanup(runtime.Close)
	ctx := rules.WithInferRuntime(context.Background(), runtime)
	rule := loadInferRule(t, endpoint, extra)
	evaluateInfer(t, ctx, rule, "one")
	spaceEndpointRule := loadInferRule(t, endpoint, strings.Replace(extra, clydeEndpoint, "  "+clydeEndpoint+"  ", 1))
	evaluateInfer(t, ctx, spaceEndpointRule, "two")
	if clydeConnections.count.Load() != 1 || len(clyde.requests) != 2 {
		t.Fatalf("clyde connections/requests = %d/%d", clydeConnections.count.Load(), len(clyde.requests))
	}
	request := clyde.requests[0]
	if request.GetWorkspaceRef() != "/workspace" || request.GetSessionRef() != "session" || request.GetTurnBudget() != 3 || request.GetMaxCharsPerTurn() != 120 {
		t.Fatalf("clyde request = %+v", request)
	}
	clyde.err = errors.New("context secret")
	emptyPolicy := loadInferRule(t, endpoint, strings.Replace(extra, `context_on_error = "error"`, `context_on_error = "empty"`, 1))
	evaluateInfer(t, ctx, emptyPolicy, "three")
	errorPolicy := loadInferRule(t, endpoint, extra+"\non_error = \"closed\"")
	if got := evaluateInfer(t, ctx, errorPolicy, "four"); len(got) != 1 {
		t.Fatal("context error policy should flow to closed inference error")
	}
}

func TestInferClydeContextAndInferenceShareConditionTimeout(t *testing.T) {
	clyde := &clydeFake{delay: 30 * time.Millisecond}
	clydeEndpoint, _ := startClydeServer(t, clyde)
	inference := &inferenceFake{handler: func(ctx context.Context, _ *inferencepb.InferRequest) (*inferencepb.InferReply, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	endpoint, _ := startInferenceServer(t, inference)
	extra := "timeout_ms = 50\ncontext_source = \"clyde_recent_turns\"\ncontext_endpoint = \"" + clydeEndpoint + "\"\ncontext_workspace_field = \"cwd\"\ncontext_session_field = \"session_id\"\ncontext_on_error = \"error\""
	runtime := rules.NewInferRuntimeWithCache(nil, nil)
	t.Cleanup(runtime.Close)
	ctx := rules.WithInferRuntime(context.Background(), runtime)
	rule := loadInferRule(t, endpoint, extra)

	started := time.Now()
	evaluateInfer(t, ctx, rule, "one")
	elapsed := time.Since(started)

	if elapsed >= 75*time.Millisecond {
		t.Fatalf("elapsed = %s, want one shared 50ms timeout budget", elapsed)
	}
}

func TestInferRejectsOutOfRangeContextBoundsBeforeClydeCall(t *testing.T) {
	clyde := &clydeFake{}
	clydeEndpoint, _ := startClydeServer(t, clyde)
	inference := &inferenceFake{handler: func(context.Context, *inferencepb.InferRequest) (*inferencepb.InferReply, error) {
		return &inferencepb.InferReply{OutputJson: `{"decision":"block"}`, Status: 1}, nil
	}}
	endpoint, _ := startInferenceServer(t, inference)
	extra := "context_source = \"clyde_recent_turns\"\ncontext_endpoint = \"" + clydeEndpoint + "\"\ncontext_workspace_field = \"cwd\"\ncontext_session_field = \"session_id\"\ncontext_on_error = \"error\"\non_error = \"closed\""
	rule := loadInferRule(t, endpoint, extra)
	rule.Conditions[0].ContextTurnBudget = math.MaxInt32 + 1
	runtime := rules.NewInferRuntimeWithCache(nil, nil)
	t.Cleanup(runtime.Close)

	violations := evaluateInfer(t, rules.WithInferRuntime(context.Background(), runtime), rule, "one")

	if len(violations) != 1 {
		t.Fatalf("violations = %d, want closed inference error", len(violations))
	}
	if len(clyde.requests) != 0 {
		t.Fatalf("clyde requests = %d, want 0", len(clyde.requests))
	}
}

func TestInferConditionsRunInDeclarationOrder(t *testing.T) {
	tests := []struct {
		name           string
		firstDecision  string
		secondDecision string
		secondError    error
		wantModels     []string
		wantBlocked    bool
	}{
		{name: "v4 allow", firstDecision: "allow", wantModels: []string{"v4"}},
		{name: "v4 block then mini high allow", firstDecision: "block", secondDecision: "allow", wantModels: []string{"v4", "gpt-5.4-mini"}},
		{name: "both block", firstDecision: "block", secondDecision: "block", wantModels: []string{"v4", "gpt-5.4-mini"}, wantBlocked: true},
		{name: "mini error fail open", firstDecision: "block", secondError: status.Error(codes.Unavailable, "mini unavailable"), wantModels: []string{"v4", "gpt-5.4-mini"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var modelsMu sync.Mutex
			var models []string
			fake := &inferenceFake{handler: func(_ context.Context, request *inferencepb.InferRequest) (*inferencepb.InferReply, error) {
				modelsMu.Lock()
				models = append(models, request.GetModel())
				modelsMu.Unlock()
				if request.GetModel() == "gpt-5.4-mini" {
					options := request.GetGenerationOptions()
					if options == nil || options.GetReasoningEffort() != inferencepb.ReasoningEffort_REASONING_EFFORT_HIGH {
						t.Fatalf("mini generation options = %+v, want HIGH", options)
					}
					if test.secondError != nil {
						return nil, test.secondError
					}
					return &inferencepb.InferReply{OutputJson: `{"decision":"` + test.secondDecision + `"}`, Status: 1}, nil
				}
				return &inferencepb.InferReply{OutputJson: `{"decision":"` + test.firstDecision + `"}`, Status: 1}, nil
			}}
			endpoint, _ := startInferenceServer(t, fake)
			rule := loadInferChainRule(t, endpoint)
			if len(rule.Conditions) != 2 || rule.Conditions[0].Model != "v4" ||
				rule.Conditions[1].Model != "gpt-5.4-mini" ||
				rule.Conditions[1].ReasoningEffort != config.ReasoningEffortHigh {
				t.Fatalf("compiled inference chain = %+v", rule.Conditions)
			}
			runtime := rules.NewInferRuntimeWithCache(nil, nil)
			t.Cleanup(runtime.Close)
			blocked := len(evaluateInfer(t, rules.WithInferRuntime(context.Background(), runtime), rule, "input")) > 0
			if blocked != test.wantBlocked {
				t.Fatalf("blocked = %v, want %v", blocked, test.wantBlocked)
			}
			if strings.Join(models, ",") != strings.Join(test.wantModels, ",") {
				t.Fatalf("models = %v, want %v", models, test.wantModels)
			}
		})
	}
}
