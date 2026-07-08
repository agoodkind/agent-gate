# Composer Overview

agent-gate resolves each guarded event with a deterministic oracle and, in parallel, an lm-review model verdict, then enforces one of them. The composer lives in `internal/composer/` and the oracles in `internal/oracle/`.

## Flow

`composer.Runtime.Decide(ruleSetID, command, cwd)` runs two goroutines. The oracle goroutine calls `oracle.Search` for the `search-guard` rule set and `oracle.Worktree` for `worktree-guard`. The model goroutine resolves the rule set's required context (`internal/composer/context.go`) and calls `judgepb.JudgeClient.Evaluate` against lm-review. It waits for both, combines them, and records disagreements. Both goroutines recover from panics so a failure on either side never crashes the gate.

## Authority

`[judge] authority` selects how the two verdicts combine, in `combineVerdicts`.

- `union` (default): the gate blocks when either the oracle or the model blocks, so the enforced blocks are the superset of both. The model catches launderings the oracle cannot parse, such as an MCP tool write, and the oracle catches what the model misses. The launderings are unbounded in shape, which is why the model is in the loop at all.
- `oracle`: the oracle verdict is enforced; the model decides only when the oracle returns Unknown.
- `llm`: the model verdict is enforced; the oracle is the safety net only when the model has no verdict.

Every mode fails closed when neither side decides. An unset or unknown value is treated as `union`. See `internal/config/judge.go`.

## Disagreement log

Every oracle-vs-model disagreement, and every oracle-Unknown case, is appended to `[judge] disagreement_log_path` (`internal/composer/disagreement.go`) with both verdicts, both reasons, both latencies, and the enforced verdict. This log is the evidence for moving `authority` from `oracle` to `llm`.

## Wiring

The daemon builds the runtime from config (`composer.NewRuntimeFromConfig`) and installs it per evaluation with `rules.WithComposerDecider` (`internal/daemon/server.go`). A rule reaches the composer through a `kind = "composer"` condition carrying a `rule_set_id` (`internal/rules/engine.go`). When the judge client address is unset the model path is skipped and the oracle decides alone.

## Tests

`internal/composer/composer_test.go`, `internal/oracle/*_test.go`, `internal/rules/composer_gate_test.go`, and `internal/config/composer_condition_test.go` hold the behavior.
