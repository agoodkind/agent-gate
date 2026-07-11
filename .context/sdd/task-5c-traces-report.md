# Task 5c Complete Trace Report

Status: DONE_WITH_CONCERNS

## Boundary

`EvaluateAllDetailed` returns compatibility violations and a rich in-memory
`DecisionTrace`. The existing `EvaluateAll` API preserves its result contract.
The rich trace stays separate from payload-free `InferenceTrace`, so operational
audit data does not receive prompts, inputs, context, schemas, or model output.

The deterministic trace stores exact input JSON, canonical output JSON, hashes,
service identity, and every configured rule in declaration order. Rule decisions
distinguish matched, nonmatched, event-inapplicable, and environment-disabled
states.

Every configured inference condition produces an attempted or skipped layer.
Context layers precede their inference layer, cache hits retain exact output and
upstream metadata, singleflight followers are coalesced, client failures use
sanitized classifications, and upstream hash mismatches produce error traces.

## TDD evidence

Initial RED:

```text
undefined: rules.EvaluateAllDetailed
undefined: rules.DetailedEvaluation
```

After the detailed boundary compiled, inference tests remained RED because no rich
layers were collected:

```text
calls/layers = 1/[]
context layers = []
```

GREEN:

```text
go test -count=1 ./internal/rules -run 'TestDetailed|TestInferTrace|TestInferCacheTrace|TestInferContextTrace'
ok goodkind.io/agent-gate/internal/rules

go test -race -count=1 ./internal/rules -run 'TestInferTrace|TestInferCacheTrace'
ok goodkind.io/agent-gate/internal/rules

go test -count=1 ./internal/rules
ok goodkind.io/agent-gate/internal/rules
```

## Concern

`make check` has no trace-scope findings. It currently reports two findings in
concurrent out-of-scope work:

```text
internal/config/config.go:1024: file length exceeds 1000 lines
internal/daemon/server.go:99: config.HookPerformance is missing InferencePhaseTimeoutMS
```
