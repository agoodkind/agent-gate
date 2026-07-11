# Task 5b implementation report

## Status

The generic `infer` condition is implemented in the commit that contains this report. The exact commit SHA is recorded in the task handoff because a commit cannot contain its own hash.

The initial implementation is `1d7a2dd82589576f261cb397c159a253854951a6`. Integrated lint and review fixes begin at `acb0207`, with deferred outcome reuse in the follow-up commit containing this report update.

## RED evidence

1. Config compiler:

   `go test ./internal/config -run 'TestInferCondition' -count=1`

   Failed with undefined `ConditionKindInfer`, infer fields, selector accessors, and defaults.

2. Local wire contract:

   `go test ./api/inferencepb -run TestInferenceWireContract -count=1`

   Failed because `api/inferencepb` had no generated non-test Go files.

3. Inference runtime:

   `go test ./internal/rules -run '^TestInfer' -count=1`

   Failed with undefined `InferenceTrace`, `NewInferRuntimeWithCache`, `WithInferenceTraceCollector`, and `WithInferRuntime`.

4. Daemon lifetime:

   `go test ./internal/daemon -run TestRuntimeSnapshotsShareInferenceRuntime -count=1`

   Failed because `Server` and `runtimeSnapshot` did not own an inference runtime and `newRuntimeSnapshot` could not receive one.

5. Numeric scalar parity:

   `go test ./internal/rules -run TestInferNumericScalarPredicatesUseExecSemantics -count=1`

   Failed because an integer TOML predicate did not match the integral JSON number `1.0`.

6. Shared condition deadline:

   `go test ./internal/rules -run '^TestInferClydeContextAndInferenceShareConditionTimeout$' -count=1`

   Failed at about 86ms because clyde context collection and inference each received a separate 50ms timeout budget.

7. Resolved clyde cache identity:

   `go test ./internal/rules -run '^TestInfer(CacheIdentity|SingleflightIdentity|ClydeContextPolicies)' -count=1`

   Failed because different workspace and session values shared one cache or flight, and whitespace around the same clyde endpoint opened a second channel.

8. Deferred inference reuse:

   `go test ./internal/daemon -run '^Test(DeferredAuditReusesHotInferenceOutcomeAndTrace|DurableDeferredReplayExcludesSynchronousInference)$' -count=1`

   Failed because deferred audit events had no inference trace field and reconstruction accepted no hot evaluation outcome.

## GREEN evidence

- `go test ./internal/config -run 'TestInferCondition' -count=1`: pass before unrelated `identity_test.go` appeared.
- `make proto && go test ./api/inferencepb -run TestInferenceWireContract -count=1`: pass.
- `go test ./internal/rules -run '^TestInfer' -count=1`: pass.
- `go test ./internal/daemon -run TestRuntimeSnapshotsShareInferenceRuntime -count=1`: pass.
- `make proto && go test ./api/inferencepb ./internal/rules ./internal/hook ./internal/daemon -count=1`: pass.
- `go test -race ./internal/rules -run '^TestInfer' -count=1`: pass.
- `go test ./internal/rules -run '^TestInferClydeContextAndInferenceShareConditionTimeout$' -count=1`: pass with one shared condition timeout.
- `make lint-deadcode`: pass with zero new findings.
- `make lint-format`: pass.
- `git diff --check`: pass.
- Resolved clyde cache and singleflight identity tests: pass with separate RPCs for different workspace or session values.
- Deferred inference reuse tests: pass with one inference RPC, preserved trace continuity, audit-only evaluation, and no inference during durable reconstruction.

## Full verification

- `make proto`: pass.
- The initial `make test` and `make check` runs were blocked by concurrent identity and evaluation work. Those external changes are now integrated, and the final follow-up verification below records the current repository result.
- Final `make test`: pass across all packages.
- Final `make check`: pass for `lint-golangci`, formatting, cyclomatic complexity, deadcode, and extended static analysis.
- Final `go test -race ./internal/rules -run '^TestInfer' -count=1`: pass.
- `git diff --check`: pass after final edits.

## Wire compatibility proof

The local `api/inferencepb/inference.proto` matches lm-review commit `54e8c88e922eeeb52b826e49c9e27e3bb989a453` on every wire-significant element:

- protobuf package `inference.v1`;
- service `Inference`, method `Infer`, and full identity `/inference.v1.Inference/Infer`;
- request strings `prompt = 1`, `input = 2`, `output_schema = 3`, `context = 4`, and `model = 5`;
- reply `output_json = 1` and `status = 2`;
- status values `UNSPECIFIED = 0` and `COMPLETE = 1`.

Only `go_package` differs, as allowed by the brief, so agent-gate builds without an unpublished lm-review version or local path replacement. The contract test verifies method identity, field numbers and types, and enum values.

## Persistent channel and cache proof

`InferRuntime` keeps separate endpoint-keyed inference and clyde `grpc.ClientConn` maps. In-process servers count physical `ConnBegin` events. Repeated calls use one connection, different endpoints use separate connections, and repeated clyde requests use one clyde connection.

The daemon owns one `InferRuntime` outside `runtimeSnapshot`, passes it to replacement snapshots, and closes it only when `Server.Close` runs. `TestRuntimeSnapshotsShareInferenceRuntime` verifies snapshot reuse. The daemon hot cache is shared with the runtime, and tests cover hit traces, TTL expiry, declaration identity separation, concurrent identical-call coalescing, and resolved clyde workspace and session separation.

## Standalone failure proof

`grpc.NewClient` remains lazy, so config loading and daemon startup do not require lm-review or clyde. A missing lm-review endpoint follows `on_error`; the focused test verifies fail-open standalone evaluation. Clyde failures either pass empty context or become an inference error according to `context_on_error`, and the latter then follows inference `on_error`.

## Self-review

- The runtime sends only the exact generic request fields and rejects every non-complete status.
- Cache identity hashes endpoint, layer declaration, resolved prompt, resolved schema, model, input and cache selectors and values, response predicate and scalar type, timeout, TTL, block and error behavior, and every context setting.
- Logs contain endpoint, rule, condition index, and sanitized status class only. Tests verify traces omit prompt, input, and output values.
- Conditions execute in declaration order. A nonmatching first layer prevents a later confirmation model call.
- Existing condition dispatch remains unchanged except for the new explicit gate-only kind.
- No user config, deployment state, composer code, lm-review files, or local Go replacement was changed.

## Concerns

1. The daemon carries inference traces from hot evaluation into immediate deferred audit reconstruction, but durable restart replay cannot recover those in-memory traces. The later persistence task must store them before the response boundary.
2. The brief does not prescribe public names or numeric maxima for clyde context bounds. This implementation uses `context_turn_budget` with a maximum of 32 and `context_max_chars_per_turn` with a maximum of 8000, defaulting to the published clyde values 4 and 280.
3. The current lm-review checkout has additive fields beyond pinned commit `54e8c88`, including generation options and reply metadata. This task intentionally preserves the pinned wire contract; a later integration task should reconcile those additions explicitly.
4. One infer condition now shares its timeout across clyde and inference. A shared deadline across several ordered inference conditions remains a later orchestration concern.
