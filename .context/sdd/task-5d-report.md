# Task 5d deterministic-first staged evaluation report

## Status

Commit `2031b29` evaluates deterministic-only rules before infer-bearing rules.
Any deterministic blocking verdict skips the inference phase, including on an
observe-only provider event. Provider capability still controls whether that
verdict becomes a blocking response or an audit-only result.

Only a deterministic allow enters the inference phase. Infer-bearing rules keep
declaration order, and conditions within each rule keep their existing ordered
AND semantics. The hot-path integration test proves v4 runs before
`gpt-5.4-mini` with HIGH reasoning.

The inference phase uses one shared context deadline configured by
`[performance.hook] inference_phase_timeout_ms`. The default and maximum are
4000ms. Each inference condition's `timeout_ms` remains an inner cap.

## RED evidence

`go test -count=1 ./internal/config -run '^TestHookInferencePhaseTimeout'`

Failed because `HookInferencePhaseTimeout` and its TOML field did not exist.
The later programmatic-config test failed with 5 seconds instead of the required
4-second cap.

`go test -count=1 ./internal/hook -run '^TestEvaluateHot(Deterministic|ObserveOnly|InferencePhase|Combines)'`

The initial run exposed four gaps:

1. A deterministic block still made the inference RPC.
2. An observe-only deterministic block ran inference before downgrading.
3. A 30ms phase budget consumed the full 1-second condition timeout.
4. Violations followed global declaration order instead of phase order.

## GREEN evidence

Before the separate evaluation-trace work appeared in the shared tree, these
commands passed:

`go test -count=1 ./internal/config ./internal/hook -run 'TestHookInferencePhaseTimeout|^TestEvaluateHot'`

`go test -race -count=1 ./internal/hook -run '^TestEvaluateHot(Deterministic|ObserveOnly|InferencePhase|Combines)'`

`go test -count=1 ./internal/daemon -run '^TestDeferredAuditOnlyInferenceUsesDaemonRuntimeAndAppendsTraces$'`

The deferred regression confirms audit-only inference still uses the daemon
runtime and appends inference traces.

## Outcome matrix

1. A deterministic block returns before inference with zero RPCs and traces.
2. A deterministic allow runs v4, then the mini HIGH confirmation layer.
3. An observe-only deterministic block skips inference, allows the provider,
   and records the deterministic violation as audit-only.
4. A 30ms shared deadline cancels the slow first inference and prevents the
   later infer-bearing rule from starting.
5. Violations combine as deterministic declaration order followed by inference
   declaration order.

## Repository check blocker

`make test`, `make check`, and a fresh combined config and hook test are blocked
by the separate uncommitted evaluation-trace work. The current compiler errors
are missing `context` references in `internal/rules/evaluation_trace.go` and
missing `collectPreSkippedInferenceLayers`, `deterministicRuleDecisions`, and
`deterministicOutputJSON` symbols in `internal/rules/engine.go`.

Task 5d does not modify `internal/rules`, intake, daemon receipt APIs,
evaluation storage, composer, oracle, or live configuration.

## Follow-up integration verification

Commit `9b2d4f5` resolved the evaluation-trace blocker described above. The Task
5d follow-up moves hook performance constants, accessors, and validation into
`internal/config/hook_performance.go`, which reduces `config.go` from 1024 to
929 lines without changing behavior.

The daemon's exhaustive zero-value `HookPerformance` literal now includes
`InferencePhaseTimeoutMS: 0`, preserving the 4000ms accessor default.

`go test -count=1 ./internal/config ./internal/daemon`

Passed both focused packages.

`make check`

Passed `lint-golangci`, formatting, cyclomatic complexity, dead-code analysis,
and extended static analysis.
