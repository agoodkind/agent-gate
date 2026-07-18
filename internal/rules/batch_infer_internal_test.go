package rules

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"

	"goodkind.io/agent-gate/api/inferencepb"
	"goodkind.io/agent-gate/internal/config"
	clydev1 "goodkind.io/clyde/api/clyde/v1"
)

// fakeInferenceClient records every InferRequest and returns a fixed reply or
// error, so a batch call can be exercised without a live inference service.
type fakeInferenceClient struct {
	mu          sync.Mutex
	calls       int
	lastRequest *inferencepb.InferRequest
	reply       *inferencepb.InferReply
	err         error
}

func (client *fakeInferenceClient) Infer(
	_ context.Context,
	in *inferencepb.InferRequest,
	_ ...grpc.CallOption,
) (*inferencepb.InferReply, error) {
	client.mu.Lock()
	client.calls++
	client.lastRequest = in
	client.mu.Unlock()
	if client.err != nil {
		return nil, client.err
	}
	return client.reply, nil
}

func (client *fakeInferenceClient) count() int {
	client.mu.Lock()
	defer client.mu.Unlock()
	return client.calls
}

func (client *fakeInferenceClient) request() *inferencepb.InferRequest {
	client.mu.Lock()
	defer client.mu.Unlock()
	return client.lastRequest
}

// spyClydeClient counts transcript fetches and records whether the fetch context
// carried a deadline, so a test can prove the fetch runs once per command and
// under a bounded context. An optional stream error drives the fail-open path.
type spyClydeClient struct {
	clydev1.ClydeServiceClient
	mu          sync.Mutex
	calls       int
	hadDeadline bool
	body        string
	streamErr   error
}

func (client *spyClydeClient) StreamExportTranscript(
	ctx context.Context,
	_ *clydev1.ExportTranscriptRequest,
	_ ...grpc.CallOption,
) (grpc.ServerStreamingClient[clydev1.ExportChunk], error) {
	client.mu.Lock()
	client.calls++
	_, client.hadDeadline = ctx.Deadline()
	client.mu.Unlock()
	if client.streamErr != nil {
		return nil, client.streamErr
	}
	return &fakeExportStream{chunks: []*clydev1.ExportChunk{chunk(client.body)}}, nil
}

func (client *spyClydeClient) count() int {
	client.mu.Lock()
	defer client.mu.Unlock()
	return client.calls
}

func (client *spyClydeClient) sawDeadline() bool {
	client.mu.Lock()
	defer client.mu.Unlock()
	return client.hadDeadline
}

func completeReply(outputJSON string) *inferencepb.InferReply {
	return &inferencepb.InferReply{
		OutputJson: outputJSON,
		Status:     inferencepb.InferenceStatus_INFERENCE_STATUS_COMPLETE,
	}
}

func pointForTest() config.InferencePoint {
	return config.InferencePoint{Endpoint: "endpoint", Model: "model"}
}

// TestBuildBatchPromptListsRulesBlocksOnly confirms the prompt leads with the
// blocks-only instruction, enumerates each rule_id with its intent, and no longer
// demands one decision per rule, so it stays the stable cacheable prefix.
func TestBuildBatchPromptListsRulesBlocksOnly(t *testing.T) {
	participants := []batchParticipant{
		{ruleName: "native-worktree-parser-gap-consensus", intent: "block writes"},
		{ruleName: "native-search-parser-gap-consensus", intent: "block searches"},
	}
	prompt := buildBatchPrompt(participants)
	if !strings.Contains(prompt, `{"block":`) {
		t.Fatalf("prompt does not describe blocks-only output: %q", prompt)
	}
	if strings.Contains(prompt, "exactly") || strings.Contains(prompt, "decisions") {
		t.Fatalf("prompt still demands a decision per rule: %q", prompt)
	}
	for _, participant := range participants {
		if !strings.Contains(prompt, "rule_id: "+participant.ruleName) {
			t.Fatalf("prompt omits rule_id %q: %q", participant.ruleName, prompt)
		}
	}
}

// TestParseBlockList confirms the blocks-only reply parses into a rule_id set,
// that a present-but-empty list parses as an empty set (allow-all), that a
// single-rule list blocks only that rule, and that a non-JSON reply, a reply that
// omits the block key, or a reply carrying a wrong key all report failure so the
// caller errors every rule rather than reading a missing decision as allow-all.
func TestParseBlockList(t *testing.T) {
	blockSet, ok := parseBlockList(`{"block":["a","c"]}`)
	if !ok {
		t.Fatal("expected ok for well-formed reply")
	}
	if !blockSet["a"] || !blockSet["c"] || blockSet["b"] {
		t.Fatalf("blockSet = %+v, want a and c only", blockSet)
	}
	single, ok := parseBlockList(`{"block":["rule-a"]}`)
	if !ok || !single["rule-a"] || len(single) != 1 {
		t.Fatalf("single block list = %+v ok=%v, want {rule-a} ok=true", single, ok)
	}
	empty, ok := parseBlockList(`{"block":[]}`)
	if !ok || len(empty) != 0 {
		t.Fatalf("empty block list = %+v ok=%v, want empty set ok=true", empty, ok)
	}
	if _, ok := parseBlockList("not json"); ok {
		t.Fatal("expected failure for non-JSON reply")
	}
	if _, ok := parseBlockList(`{}`); ok {
		t.Fatal("expected failure for a reply that omits the block key")
	}
	if _, ok := parseBlockList(`{"blocked":["x"]}`); ok {
		t.Fatal("expected failure for a reply carrying a wrong key")
	}
}

// TestBatchDecisionsFromBlockList confirms a participant in the block set blocks,
// a participant absent from it allows without erroring, and a block-set entry that
// is not a participant is ignored.
func TestBatchDecisionsFromBlockList(t *testing.T) {
	participants := []batchParticipant{{ruleName: "a"}, {ruleName: "b"}}
	blockSet := map[string]bool{"a": true, "stray": true}
	decisions := batchDecisionsFromBlockList(participants, blockSet)
	if !decisions["a"].block || decisions["a"].errored {
		t.Fatalf("rule a = %+v, want block, not errored", decisions["a"])
	}
	if decisions["b"].block || decisions["b"].errored {
		t.Fatalf("rule b = %+v, want allow, not errored", decisions["b"])
	}
	if _, present := decisions["stray"]; present {
		t.Fatalf("non-participant block id leaked into decisions: %+v", decisions)
	}
}

// TestBatchVerdictForFallsBackWhenAbsent confirms verdictFor reports no verdict
// when the memo is nil or the point or rule is missing, so the read site falls back
// to an individual call.
func TestBatchVerdictForFallsBackWhenAbsent(t *testing.T) {
	var nilMemo *batchInferenceMemo
	if _, found := nilMemo.verdictFor(pointForTest(), "a"); found {
		t.Fatal("nil memo should report no verdict")
	}
	empty := &batchInferenceMemo{groups: map[config.InferencePoint]*batchGroupResult{}}
	if _, found := empty.verdictFor(pointForTest(), "a"); found {
		t.Fatal("empty memo should report no verdict")
	}
}

// TestEvaluateBatchGroupSendsJudgeInputBlocksOnly confirms the call sends the
// judge-input panel as Input, an empty Context, the blocks-only schema, and the
// rule intents as the stable Prompt, and that a block-list reply yields per-rule
// block and allow decisions.
func TestEvaluateBatchGroupSendsJudgeInputBlocksOnly(t *testing.T) {
	runtime := NewInferRuntimeWithCache(nil, nil)
	t.Cleanup(runtime.Close)
	fake := &fakeInferenceClient{reply: completeReply(`{"block":["a"]}`)}
	runtime.inferenceClients["infer"] = fake

	plan := &batchGroupPlan{
		point:        config.InferencePoint{Endpoint: "infer", Model: "m"},
		participants: []batchParticipant{{ruleName: "a", intent: "block a"}, {ruleName: "b", intent: "block b"}},
		seen:         map[string]bool{"a": true, "b": true},
	}
	judgeInput := "chat working directory: /repo\n\ntool call:\nrm -rf /\n\nstructure:\n..."

	result := runtime.evaluateBatchGroup(context.Background(), plan, judgeInput)

	req := fake.request()
	if req.GetInput() != judgeInput {
		t.Fatalf("Input = %q, want the judge-input panel %q", req.GetInput(), judgeInput)
	}
	if req.GetContext() != "" {
		t.Fatalf("Context = %q, want empty", req.GetContext())
	}
	if req.GetOutputSchema() != blocksOnlyOutputSchema {
		t.Fatalf("OutputSchema = %q, want blocks-only schema", req.GetOutputSchema())
	}
	if !strings.HasPrefix(req.GetPrompt(), "You are a security guard") || !strings.Contains(req.GetPrompt(), "rule_id: a") {
		t.Fatalf("Prompt does not lead with the rule intents: %q", req.GetPrompt())
	}
	if !result.decisions["a"].block || result.decisions["a"].errored {
		t.Fatalf("rule a = %+v, want block", result.decisions["a"])
	}
	if result.decisions["b"].block || result.decisions["b"].errored {
		t.Fatalf("rule b = %+v, want allow", result.decisions["b"])
	}
}

// TestEvaluateBatchGroupInvalidReplyErrorsAll confirms an unparseable reply and a
// valid-JSON reply that omits the block key both mark every participant errored so
// the read site applies each entry's on_error, while a present-but-empty block list
// allows every participant.
func TestEvaluateBatchGroupInvalidReplyErrorsAll(t *testing.T) {
	runtime := NewInferRuntimeWithCache(nil, nil)
	t.Cleanup(runtime.Close)
	fake := &fakeInferenceClient{reply: completeReply("not json")}
	runtime.inferenceClients["infer"] = fake
	plan := &batchGroupPlan{
		point:        config.InferencePoint{Endpoint: "infer", Model: "m"},
		participants: []batchParticipant{{ruleName: "a"}, {ruleName: "b"}},
		seen:         map[string]bool{"a": true, "b": true},
	}

	errored := runtime.evaluateBatchGroup(context.Background(), plan, "input")
	for _, name := range []string{"a", "b"} {
		if !errored.decisions[name].errored || errored.decisions[name].errorCode != "invalid_response" {
			t.Fatalf("rule %s = %+v, want errored invalid_response", name, errored.decisions[name])
		}
	}

	// A reply that is valid JSON but omits the block key states no decision, so it
	// must error every participant rather than read as a silent allow-all.
	fake.reply = completeReply(`{}`)
	missingKey := runtime.evaluateBatchGroup(context.Background(), plan, "input")
	for _, name := range []string{"a", "b"} {
		if !missingKey.decisions[name].errored || missingKey.decisions[name].errorCode != "invalid_response" {
			t.Fatalf("rule %s = %+v, want errored invalid_response for a missing block key", name, missingKey.decisions[name])
		}
	}

	fake.reply = completeReply(`{"block":[]}`)
	allowed := runtime.evaluateBatchGroup(context.Background(), plan, "input")
	for _, name := range []string{"a", "b"} {
		if allowed.decisions[name].block || allowed.decisions[name].errored {
			t.Fatalf("rule %s = %+v, want allow", name, allowed.decisions[name])
		}
	}
}

func batchRule(name, use string, point config.InferencePoint) config.Rule {
	return config.Rule{
		Name:   name,
		Intent: "block " + name,
		Eval: []config.RuleEval{
			{Kind: config.EvalKindInfer, Role: config.RoleEnforce, Use: use, Fanout: config.FanoutBatch},
		},
		EvalInference: map[string]config.InferencePoint{use: point},
	}
}

func batchRuntimeContext(t *testing.T, runtime *InferRuntime) context.Context {
	t.Helper()
	return WithInferRuntime(context.Background(), runtime)
}

// TestRunBatchInferenceFetchesTranscriptOnce confirms the planner fetches the
// conversation transcript once per command even when two inference points run, and
// that the fetch context carries a deadline (bounded context).
func TestRunBatchInferenceFetchesTranscriptOnce(t *testing.T) {
	runtime := NewInferRuntimeWithCache(nil, nil)
	t.Cleanup(runtime.Close)
	infer := &fakeInferenceClient{reply: completeReply(`{"block":[]}`)}
	runtime.inferenceClients["infer"] = infer
	clyde := &spyClydeClient{body: "user: do the thing"}
	runtime.clydeServiceClients["/clyde"] = clyde
	runtime.SetJudgeTranscript("/clyde", 2000, "", 1500*time.Millisecond, "")

	fields := &FieldSet{
		ConversationID:   "conv-1",
		CWD:              "/repo",
		ToolName:         "Bash",
		ToolInputCommand: "rm -rf /",
	}
	rules := []config.Rule{
		batchRule("a", "p1", config.InferencePoint{Endpoint: "infer", Model: "m1"}),
		batchRule("b", "p2", config.InferencePoint{Endpoint: "infer", Model: "m2"}),
	}

	memo := runBatchInference(batchRuntimeContext(t, runtime), fields, rules, "claude", "PreToolUse", nil)
	if memo == nil {
		t.Fatal("memo is nil, want a batch result")
	}
	if clyde.count() != 1 {
		t.Fatalf("transcript fetches = %d, want 1", clyde.count())
	}
	if infer.count() != 2 {
		t.Fatalf("inference calls = %d, want 2 (one per point)", infer.count())
	}
	if !clyde.sawDeadline() {
		t.Fatal("transcript fetch context had no deadline, want a bounded context")
	}
}

// TestRunBatchInferenceContextUnavailableSkipsJudge confirms that when a transcript
// endpoint is configured but the fetch fails, the judge does not enforce on the
// missing conversation. It issues no inference call and marks the rule errored with
// context_unavailable, so the eval matrix routes the rule to its deterministic
// fallback rather than letting the judge decide blind. This is the corrected
// behavior: the conversation is the judge's source of intent, so a configured-but-
// unavailable context must fall back, not run the judge on an empty panel.
func TestRunBatchInferenceContextUnavailableSkipsJudge(t *testing.T) {
	runtime := NewInferRuntimeWithCache(nil, nil)
	t.Cleanup(runtime.Close)
	infer := &fakeInferenceClient{reply: completeReply(`{"block":[]}`)}
	runtime.inferenceClients["infer"] = infer
	clyde := &spyClydeClient{streamErr: context.DeadlineExceeded}
	runtime.clydeServiceClients["/clyde"] = clyde
	runtime.SetJudgeTranscript("/clyde", 2000, "", 1500*time.Millisecond, "")

	fields := &FieldSet{
		ConversationID:   "conv-1",
		CWD:              "/repo",
		ToolName:         "Bash",
		ToolInputCommand: "ls",
	}
	rules := []config.Rule{batchRule("a", "p1", config.InferencePoint{Endpoint: "infer", Model: "m1"})}

	memo := runBatchInference(batchRuntimeContext(t, runtime), fields, rules, "claude", "PreToolUse", nil)
	if memo == nil {
		t.Fatal("memo is nil, want an errored memo that routes to the deterministic fallback")
	}
	if infer.count() != 0 {
		t.Fatalf("inference calls = %d, want 0 (the judge must not enforce without its conversation context)", infer.count())
	}
	verdict, found := memo.verdictFor(config.InferencePoint{Endpoint: "infer", Model: "m1"}, "a")
	if !found {
		t.Fatal("no verdict recorded for rule a")
	}
	if !verdict.errored || verdict.errorCode != "context_unavailable" {
		t.Fatalf("verdict = %+v, want errored context_unavailable so the eval matrix uses the deterministic fallback", verdict)
	}
}

// TestRunBatchInferenceNoEndpointJudgesContextless confirms that when no transcript
// endpoint is configured, contextless judging is intended: the judge still runs on
// the directory, command, and structural parse, and does not error. This keeps the
// operator's explicit choice to run the judge without a transcript distinct from a
// configured transcript that is merely unavailable.
func TestRunBatchInferenceNoEndpointJudgesContextless(t *testing.T) {
	runtime := NewInferRuntimeWithCache(nil, nil)
	t.Cleanup(runtime.Close)
	infer := &fakeInferenceClient{reply: completeReply(`{"block":[]}`)}
	runtime.inferenceClients["infer"] = infer
	// No SetJudgeTranscript, so the endpoint stays unset and contextless judging is
	// the configured intent.
	fields := &FieldSet{
		ConversationID:   "conv-1",
		CWD:              "/repo",
		ToolName:         "Bash",
		ToolInputCommand: "ls",
	}
	rules := []config.Rule{batchRule("a", "p1", config.InferencePoint{Endpoint: "infer", Model: "m1"})}

	memo := runBatchInference(batchRuntimeContext(t, runtime), fields, rules, "claude", "PreToolUse", nil)
	if memo == nil {
		t.Fatal("memo is nil, want the judge to run contextless when no endpoint is configured")
	}
	if infer.count() != 1 {
		t.Fatalf("inference calls = %d, want 1 (contextless judging is intended when no endpoint is set)", infer.count())
	}
	verdict, found := memo.verdictFor(config.InferencePoint{Endpoint: "infer", Model: "m1"}, "a")
	if !found || verdict.errored || verdict.block {
		t.Fatalf("verdict = %+v found=%v, want a clean non-errored allow", verdict, found)
	}
	if strings.Contains(infer.request().GetInput(), "recent conversation") {
		t.Fatalf("Input carries a conversation panel despite no configured endpoint: %q", infer.request().GetInput())
	}
}
