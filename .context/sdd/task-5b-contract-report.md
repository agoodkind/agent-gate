# Task 5b inference contract synchronization report

## Status

The agent-gate inference client now matches lm-review commit `3201ae5` on every
wire-significant field. The only proto difference is the required local
`go_package = "goodkind.io/agent-gate/api/inferencepb"`.

The generic infer condition accepts `reasoning_effort`, optional
`max_completion_tokens`, and optional `temperature`. Validation follows the
wire service bounds and adds no guard-specific or model-specific policy.

## RED evidence

`go test ./api/inferencepb -run '^TestInferenceWireContract$' -count=1`

Failed because `ReasoningEffort`, `GenerationOptions`, `InvocationMetadata`,
request field 6, and reply field 3 did not exist.

`go test ./internal/config -run '^TestInferCondition(CompilesGenericGenerationOptions|ValidatesGenericGenerationOptions)$' -count=1`

Failed because `Condition` had no reasoning effort, completion-token, or
temperature fields.

`go test ./internal/rules -run '^TestInfer(SendsGenerationOptionsAndPreservesMetadataInCacheTraces|FailedRPCCapturesClientLatency|CacheIdentityIncludesGenerationOptions|ConditionsRunInDeclarationOrder)$' -count=1`

Failed because requests had no generation options, replies and traces had no
invocation metadata, and ordered mini requests could not carry HIGH reasoning.

## GREEN evidence

`make proto`

Passed with `buf generate` and regenerated the local Go bindings.

`go test ./api/inferencepb ./internal/config ./internal/rules -run 'TestInferenceWireContract|^TestInfer' -count=1`

Passed all three focused packages after syncing to the pinned proto.

`go test -race ./internal/rules -run '^TestInfer' -count=1`

Passed before the final pinned-token-presence correction. The correction only
changes generated metadata token fields from optional pointers to plain
`int64`, matching commit `3201ae5`.

`make check`

Passed `lint-golangci`, formatting, cyclomatic complexity, deadcode, and
extended static analysis after the final proto generation.

`make test`

Passed every package before a concurrent edit appeared in
`internal/daemon/infer_deferred_test.go`. The final rerun reached all packages
but could not build `internal/daemon` because that uncommitted out-of-scope test
references a missing `deferredProcessor.inferRuntime` field. This task did not
modify daemon or hook code.

## Wire contract

The request retains fields `prompt = 1`, `input = 2`, `output_schema = 3`,
`context = 4`, and `model = 5`, and adds `generation_options = 6`.

Generation options contain the reasoning enum at field 1, optional completion
tokens at field 2, and optional temperature at field 3. The reasoning enum
preserves values 0 through 6 for unspecified, none, minimal, low, medium, high,
and xhigh.

The reply retains `output_json = 1` and `status = 2`, and adds metadata at field
3. Invocation metadata preserves request and service identity, requested and
actual model identity, backend identity, prompt and schema hashes, token counts,
finish reason, and upstream latency.

## Runtime evidence

Inference requests map the generic TOML options directly to the proto. Stable
cache identity includes each generation option and distinguishes unset values
from explicit values such as `temperature = 0.0`.

Successful cache entries preserve the match result and upstream invocation
metadata. Each payload-free trace records declaration index, layer, configured
model, endpoint, outcome, status, client-observed latency, cache-hit state,
sanitized error class, and cloned upstream metadata. Failed RPC traces preserve
client-observed latency and sanitized error provenance with no upstream
metadata.

The ordered-chain test proves these exact outcomes:

1. v4 allow performs one call and allows.
2. v4 block followed by gpt-5.4-mini HIGH allow performs two calls and allows.
3. Both models blocking performs two calls and blocks.
4. A mini RPC error with `on_error = "open"` performs two calls and allows.

Every mini attempt asserts `REASONING_EFFORT_HIGH` on the wire.
