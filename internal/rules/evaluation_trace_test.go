package rules_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"goodkind.io/agent-gate/api/inferencepb"
	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/hotkv"
	"goodkind.io/agent-gate/internal/rules"
)

func TestDetailedDeterministicTraceListsRulesInDeclarationOrder(t *testing.T) {
	matched := loadRule(t, "matched", "bad", []string{"PreToolUse"}, []string{"tool_input.command"}, "blocked")
	nonmatched := loadRule(t, "nonmatched", "never", []string{"PreToolUse"}, []string{"tool_input.command"}, "blocked")
	inapplicable := loadRule(t, "inapplicable", "bad", []string{"Stop"}, []string{"assistant_message"}, "blocked")
	disabled := loadRule(t, "disabled", "bad", []string{"PreToolUse"}, []string{"tool_input.command"}, "blocked")
	disabled.DisableIfEnv = []string{"TRACE_DISABLED"}
	inputJSON := json.RawMessage(`{"tool_input":{"command":"bad"}}`)

	detailed := rules.EvaluateAllDetailed(
		context.Background(),
		"claude",
		"PreToolUse",
		rules.FieldSet{ToolInputCommand: "bad"},
		[]config.Rule{matched, nonmatched, inapplicable, disabled},
		func(key string) string {
			if key == "TRACE_DISABLED" {
				return "1"
			}
			return ""
		},
		inputJSON,
		"v-test",
	)

	wantStatuses := []string{"matched", "nonmatched", "skipped", "skipped"}
	wantReasons := []string{"", "", "event_not_applicable", "disabled_by_env"}
	if len(detailed.Trace.Deterministic.CheckedRules) != len(wantStatuses) {
		t.Fatalf("checked rules = %+v", detailed.Trace.Deterministic.CheckedRules)
	}
	for i, decision := range detailed.Trace.Deterministic.CheckedRules {
		if decision.RuleName != []string{"matched", "nonmatched", "inapplicable", "disabled"}[i] ||
			decision.Status != wantStatuses[i] || decision.SkipReason != wantReasons[i] {
			t.Fatalf("decision %d = %+v", i, decision)
		}
	}
	if string(detailed.Trace.Deterministic.InputJSON) != string(inputJSON) ||
		detailed.Trace.Deterministic.InputHash != traceHash(inputJSON) ||
		detailed.Trace.Deterministic.ServiceVersion != "v-test" {
		t.Fatalf("deterministic trace = %+v", detailed.Trace.Deterministic)
	}
	if !json.Valid(detailed.Trace.Deterministic.OutputJSON) ||
		detailed.Trace.Deterministic.OutputHash != traceHash(detailed.Trace.Deterministic.OutputJSON) {
		t.Fatalf("deterministic output = %s", detailed.Trace.Deterministic.OutputJSON)
	}
}

func TestInferTraceRecordsAttemptThenSkippedDeclaration(t *testing.T) {
	var modelsMu sync.Mutex
	var models []string
	fake := &inferenceFake{handler: func(_ context.Context, request *inferencepb.InferRequest) (*inferencepb.InferReply, error) {
		modelsMu.Lock()
		models = append(models, request.GetModel())
		modelsMu.Unlock()
		decision := "allow"
		if request.GetModel() == "gpt-5.4-mini" {
			decision = "block"
		}
		return &inferencepb.InferReply{
			OutputJson: `{"decision":"` + decision + `"}`,
			Status:     inferencepb.InferenceStatus_INFERENCE_STATUS_COMPLETE,
			Metadata: &inferencepb.InvocationMetadata{
				RequestId:   "request-" + request.GetModel(),
				ActualModel: request.GetModel() + "-actual",
			},
		}, nil
	}}
	endpoint, _ := startInferenceServer(t, fake)
	runtime := rules.NewInferRuntimeWithCache(nil, nil)
	t.Cleanup(runtime.Close)
	rule := loadInferChainRule(t, endpoint)

	nonmatch := evaluateInferDetailed(t, runtime, rule, "input")
	if fake.count() != 1 || len(nonmatch.Trace.Layers) != 2 {
		t.Fatalf("calls/layers = %d/%+v", fake.count(), nonmatch.Trace.Layers)
	}
	if nonmatch.Trace.Layers[0].LayerName != "v4" || nonmatch.Trace.Layers[0].Status != "complete" ||
		nonmatch.Trace.Layers[1].LayerName != "mini-high" || nonmatch.Trace.Layers[1].Status != "skipped" ||
		nonmatch.Trace.Layers[1].SkipReason != "prior_condition_nonmatch" ||
		nonmatch.Trace.Layers[1].VerifiedProvenance.ReportedPromptHashStatus != "absent" ||
		nonmatch.Trace.Layers[1].VerifiedProvenance.ReportedSchemaHashStatus != "absent" {
		t.Fatalf("nonmatch layers = %+v", nonmatch.Trace.Layers)
	}

	modelsMu.Lock()
	models = nil
	modelsMu.Unlock()
	fake.requests = nil
	fake.handler = func(_ context.Context, request *inferencepb.InferRequest) (*inferencepb.InferReply, error) {
		modelsMu.Lock()
		models = append(models, request.GetModel())
		modelsMu.Unlock()
		return &inferencepb.InferReply{
			OutputJson: `{"decision":"block"}`,
			Status:     inferencepb.InferenceStatus_INFERENCE_STATUS_COMPLETE,
		}, nil
	}
	matched := evaluateInferDetailed(t, runtime, rule, "different")
	if len(matched.Trace.Layers) != 2 || matched.Trace.Layers[0].Status != "complete" ||
		matched.Trace.Layers[1].Status != "complete" || strings.Join(models, ",") != "v4,gpt-5.4-mini" {
		t.Fatalf("match models/layers = %v/%+v", models, matched.Trace.Layers)
	}
}

func TestInferContextTraceRecordsExactJSONAndEmptyPolicyError(t *testing.T) {
	clyde := &clydeFake{}
	clydeEndpoint, _ := startClydeServer(t, clyde)
	inference := &inferenceFake{handler: func(_ context.Context, request *inferencepb.InferRequest) (*inferencepb.InferReply, error) {
		return &inferencepb.InferReply{
			OutputJson: `{"decision":"block"}`,
			Status:     inferencepb.InferenceStatus_INFERENCE_STATUS_COMPLETE,
		}, nil
	}}
	endpoint, _ := startInferenceServer(t, inference)
	extra := "context_source = \"clyde_recent_turns\"\ncontext_endpoint = \"" + clydeEndpoint + "\"\ncontext_workspace_field = \"cwd\"\ncontext_session_field = \"session_id\"\ncontext_turn_budget = 3\ncontext_max_chars_per_turn = 120\ncontext_on_error = \"empty\""
	rule := loadInferRule(t, endpoint, extra)
	runtime := rules.NewInferRuntimeWithCache(nil, nil)
	t.Cleanup(runtime.Close)

	success := evaluateInferDetailed(t, runtime, rule, "first")
	if len(success.Trace.Layers) != 2 || success.Trace.Layers[0].Kind != "context" ||
		success.Trace.Layers[1].Kind != "inference" || success.Trace.Layers[1].ParentTraceIndex == nil ||
		*success.Trace.Layers[1].ParentTraceIndex != 1 {
		t.Fatalf("context layers = %+v", success.Trace.Layers)
	}
	if string(success.Trace.Layers[0].OutputJSON) != `{"turns":[{"role":"user","text":"hello","ts":"now"}]}` ||
		!strings.Contains(string(success.Trace.Layers[0].InputJSON), `"workspace":"/workspace"`) {
		t.Fatalf("context JSON = %s / %s", success.Trace.Layers[0].InputJSON, success.Trace.Layers[0].OutputJSON)
	}

	clyde.err = errors.New("context secret")
	failure := evaluateInferDetailed(t, runtime, rule, "second")
	if failure.Trace.Layers[0].Status != "error" || failure.Trace.Layers[0].ErrorCode == "" ||
		string(failure.Trace.Layers[0].OutputJSON) != `{}` || failure.Trace.Layers[1].Status != "complete" {
		t.Fatalf("empty-policy layers = %+v", failure.Trace.Layers)
	}
	if strings.Contains(failure.Trace.Layers[0].ErrorMessage, "context secret") {
		t.Fatalf("raw context error leaked: %+v", failure.Trace.Layers[0])
	}
}

func TestInferCacheTracePreservesOutputMetadataVersionAndExpiry(t *testing.T) {
	fake := &inferenceFake{handler: func(_ context.Context, _ *inferencepb.InferRequest) (*inferencepb.InferReply, error) {
		return &inferencepb.InferReply{
			OutputJson: `{"decision":"block","proof":"exact"}`,
			Status:     inferencepb.InferenceStatus_INFERENCE_STATUS_COMPLETE,
			Metadata: &inferencepb.InvocationMetadata{
				RequestId: "request-cache", ActualModel: "model-cache", BackendVersion: "backend-cache",
				PromptTokens: int64Pointer(0),
			},
		}, nil
	}}
	endpoint, _ := startInferenceServer(t, fake)
	cache := hotkv.New(hotkv.Options{})
	t.Cleanup(cache.Close)
	runtime := rules.NewInferRuntimeWithCache(nil, cache)
	t.Cleanup(runtime.Close)
	rule := loadInferRule(t, endpoint, "cache_ttl_ms = 1000")

	_ = evaluateInferDetailed(t, runtime, rule, "same")
	hit := evaluateInferDetailed(t, runtime, rule, "same")
	layer := hit.Trace.Layers[0]
	if fake.count() != 1 || layer.CacheStatus != "hit" || layer.CacheEntryVersion == nil ||
		*layer.CacheEntryVersion != 1 || layer.CacheExpiresAt == nil ||
		string(layer.OutputJSON) != `{"decision":"block","proof":"exact"}` ||
		layer.VerifiedProvenance.RequestedModel != "" ||
		layer.UpstreamMetadata.Status != "present" ||
		!strings.Contains(string(layer.UpstreamMetadata.Raw), `"request_id":"request-cache"`) ||
		!strings.Contains(string(layer.UpstreamMetadata.Raw), `"prompt_tokens":"0"`) ||
		strings.Contains(string(layer.UpstreamMetadata.Raw), `"completion_tokens"`) {
		t.Fatalf("cache layer = %+v", layer)
	}
}

func TestInferTraceBoundsRawMetadataAndRetainsHashTruth(t *testing.T) {
	fake := &inferenceFake{handler: func(context.Context, *inferencepb.InferRequest) (*inferencepb.InferReply, error) {
		return &inferencepb.InferReply{
			OutputJson: `{"decision":"block"}`,
			Status:     inferencepb.InferenceStatus_INFERENCE_STATUS_COMPLETE,
			Metadata: &inferencepb.InvocationMetadata{
				RequestId:    strings.Repeat("x", 5000),
				PromptSha256: traceHash([]byte("Classify")),
				SchemaSha256: strings.Repeat("spoofed", 1000),
			},
		}, nil
	}}
	endpoint, _ := startInferenceServer(t, fake)
	runtime := rules.NewInferRuntimeWithCache(nil, nil)
	t.Cleanup(runtime.Close)
	layer := evaluateInferDetailed(t, runtime, loadInferRule(t, endpoint, ""), "input").Trace.Layers[0]
	if layer.UpstreamMetadata.Status != "omitted_oversize" || len(layer.UpstreamMetadata.Raw) != 0 {
		t.Fatalf("upstream metadata = %+v", layer.UpstreamMetadata)
	}
	if layer.VerifiedProvenance.ReportedPromptHashStatus != "match" ||
		layer.VerifiedProvenance.ReportedSchemaHashStatus != "mismatch" ||
		layer.VerifiedProvenance.PromptSHA256 != traceHash([]byte("Classify")) ||
		layer.VerifiedProvenance.SchemaSHA256 != traceHash([]byte(`{"type":"object"}`)) {
		t.Fatalf("verified provenance = %+v", layer.VerifiedProvenance)
	}
}

func TestInferCacheTraceMarksSingleflightFollowerCoalesced(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{})
	fake := &inferenceFake{handler: func(_ context.Context, _ *inferencepb.InferRequest) (*inferencepb.InferReply, error) {
		close(started)
		<-release
		return &inferencepb.InferReply{OutputJson: `{"decision":"block"}`, Status: 1}, nil
	}}
	endpoint, _ := startInferenceServer(t, fake)
	runtime := rules.NewInferRuntimeWithCache(nil, nil)
	t.Cleanup(runtime.Close)
	rule := loadInferRule(t, endpoint, "")
	results := make(chan rules.DetailedEvaluation, 2)
	go func() { results <- evaluateInferDetailed(t, runtime, rule, "same") }()
	<-started
	go func() { results <- evaluateInferDetailed(t, runtime, rule, "same") }()
	time.Sleep(20 * time.Millisecond)
	close(release)
	first := <-results
	second := <-results
	statuses := first.Trace.Layers[0].CacheStatus + "," + second.Trace.Layers[0].CacheStatus
	if fake.count() != 1 || (statuses != "miss,coalesced" && statuses != "coalesced,miss") {
		t.Fatalf("calls/statuses = %d/%s", fake.count(), statuses)
	}
}

func TestInferTraceSanitizesGRPCErrorAndRejectsHashMismatch(t *testing.T) {
	t.Run("grpc", func(t *testing.T) {
		fake := &inferenceFake{handler: func(context.Context, *inferencepb.InferRequest) (*inferencepb.InferReply, error) {
			return nil, status.Error(codes.Unavailable, "secret backend response")
		}}
		endpoint, _ := startInferenceServer(t, fake)
		runtime := rules.NewInferRuntimeWithCache(nil, nil)
		t.Cleanup(runtime.Close)
		layer := evaluateInferDetailed(t, runtime, loadInferRule(t, endpoint, ""), "input").Trace.Layers[0]
		if layer.Status != "error" || layer.ErrorCode != "unavailable" ||
			strings.Contains(layer.ErrorMessage, "secret backend response") ||
			layer.UpstreamMetadata.Status != "absent" {
			t.Fatalf("grpc layer = %+v", layer)
		}
	})

	t.Run("hash mismatch", func(t *testing.T) {
		fake := &inferenceFake{handler: func(context.Context, *inferencepb.InferRequest) (*inferencepb.InferReply, error) {
			return &inferencepb.InferReply{
				OutputJson: `{"decision":"block"}`,
				Status:     inferencepb.InferenceStatus_INFERENCE_STATUS_COMPLETE,
				Metadata:   &inferencepb.InvocationMetadata{PromptSha256: "wrong", SchemaSha256: "also-wrong"},
			}, nil
		}}
		endpoint, _ := startInferenceServer(t, fake)
		runtime := rules.NewInferRuntimeWithCache(nil, nil)
		t.Cleanup(runtime.Close)
		layer := evaluateInferDetailed(t, runtime, loadInferRule(t, endpoint, ""), "input").Trace.Layers[0]
		if layer.Status != "error" || layer.ErrorCode != "hash_mismatch" ||
			string(layer.OutputJSON) != `{"decision":"block"}` {
			t.Fatalf("mismatch layer = %+v", layer)
		}
	})
}

func TestInferTracePreservesErroredReplyOutput(t *testing.T) {
	tests := []struct {
		name       string
		outputJSON string
		status     inferencepb.InferenceStatus
		errorCode  string
	}{
		{
			name: "non-complete", outputJSON: `{"decision":"partial"}`,
			status:    inferencepb.InferenceStatus_INFERENCE_STATUS_UNSPECIFIED,
			errorCode: "non_complete",
		},
		{
			name: "invalid response", outputJSON: `{`,
			status:    inferencepb.InferenceStatus_INFERENCE_STATUS_COMPLETE,
			errorCode: "invalid_response",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fake := &inferenceFake{handler: func(context.Context, *inferencepb.InferRequest) (*inferencepb.InferReply, error) {
				return &inferencepb.InferReply{
					OutputJson: test.outputJSON, Status: test.status,
					Metadata: &inferencepb.InvocationMetadata{RequestId: "errored-request"},
				}, nil
			}}
			endpoint, _ := startInferenceServer(t, fake)
			runtime := rules.NewInferRuntimeWithCache(nil, nil)
			t.Cleanup(runtime.Close)
			layer := evaluateInferDetailed(
				t,
				runtime,
				loadInferRule(t, endpoint, ""),
				"input",
			).Trace.Layers[0]
			if layer.Status != "error" || layer.ErrorCode != test.errorCode ||
				string(layer.OutputJSON) != test.outputJSON ||
				layer.UpstreamMetadata.Status != "present" ||
				!strings.Contains(string(layer.UpstreamMetadata.Raw), `"request_id":"errored-request"`) {
				t.Fatalf("errored layer = %+v", layer)
			}
		})
	}
}

func evaluateInferDetailed(
	t *testing.T,
	runtime *rules.InferRuntime,
	rule config.Rule,
	command string,
) rules.DetailedEvaluation {
	t.Helper()
	return rules.EvaluateAllDetailed(
		rules.WithInferRuntime(context.Background(), runtime),
		"claude",
		"PreToolUse",
		rules.FieldSet{ToolInputCommand: command, CWD: "/workspace", SessionID: "session"},
		[]config.Rule{rule},
		nil,
		json.RawMessage(`{"tool_input":{"command":`+quoteJSON(command)+`}}`),
		"v-test",
	)
}

func traceHash(value []byte) string {
	digest := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func quoteJSON(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}
