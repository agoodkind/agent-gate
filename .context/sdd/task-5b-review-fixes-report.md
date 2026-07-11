# Task 5b review fixes report

## Status

Commit `05b5406` preserves exact upstream `InvocationMetadata` on non-complete
replies and complete replies with invalid output. Error traces still use the
local sanitized error class and client-observed latency.

The ordered inference test now loads a deployable TOML rule with v4 first and
`gpt-5.4-mini` second. The mini layer compiles with
`reasoning_effort = "high"`.

The wire contract test now checks metadata kinds, optional cardinality, and
presence in addition to field numbers. It also pins reply metadata and
generation-options message presence.

## RED evidence

`go test -count=1 ./internal/rules -run 'TestInfer(ErrorRepliesPreserveMetadataAndClientLatency|ConditionsRunInDeclarationOrder)$'`

Both metadata cases failed with `metadata = <nil>`. The trace error classes
were `non_complete` and `invalid_response`, and the client latency assertions
passed. The TOML-loaded chain cases already passed, which showed that runtime
ordering was correct and the prior gap was the test's in-memory mutation.

## GREEN evidence

`go test -count=1 ./internal/rules -run 'TestInfer(ErrorRepliesPreserveMetadataAndClientLatency|ConditionsRunInDeclarationOrder)$'`

Passed.

`go test -count=1 ./api/inferencepb ./internal/rules -run 'TestInferenceWireContract|TestInvocationTokenUsagePresenceOnWireAndJSON|^TestInfer'`

Passed both focused packages.

`go test -race -count=1 ./internal/rules -run '^TestInfer'`

Passed the complete inference rule test surface under the race detector.

`make test`

Passed every repository package.

`make check`

The current shared tree fails on unrelated concurrent edits. The reported
findings are `internal/evaluation/store.go:227` requiring deferred close,
`internal/intake/store.go:1139` exceeding the file length limit, and
`internal/intake/store.go:791` using `[]any`. This task did not modify those
files.

## Ordered outcomes

1. v4 allow makes one v4 call and allows.
2. v4 block followed by mini HIGH allow makes two calls and allows.
3. Both layers block, so the rule makes two calls and blocks.
4. A mini unavailable error after v4 block makes two calls and allows under
   the default open error policy.

Every mini request asserts `REASONING_EFFORT_HIGH` on the wire.
