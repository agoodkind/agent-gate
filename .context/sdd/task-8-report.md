# Task 8: Evaluation query and training export report

## Result

The evaluation store now returns bounded, deterministic pages of complete
evaluations joined to durable intake metadata. Each record includes ordered safe
layer and label projections. The query projection excludes raw layer inputs,
free-form backend errors, label rationale, raw intake payloads, and authorization
data.

`agent-gate query evaluations` exposes the store through the existing shared time
flags and output conventions. Readable output is a summary table. `--json` emits
one nested evaluation per line as streaming JSONL, which is the training export
surface.

Implementation commit:
`e31f76841d8a53ed56893cbc11276606ec1c1605`

Starting commit:
`2f3f48498d56bc32d71d6195352b31cf871de73c`

## Query surface

The typed filter supports evaluation id, event id, receipt id, mode, completed
time range, system, session, event, tool, rule name, layer name, layer kind, layer
outcome, model, final verdict, limit, and offset.

Evaluation ordering is `completed_at DESC, evaluation_id DESC`. Layer ordering is
`layer_index`. Label ordering is `namespace, label_version`.

Examples:

```sh
agent-gate query evaluations --today --system codex --verdict block
agent-gate query evaluations --rule command-policy --outcome match --limit 100 --json
agent-gate query evaluations --receipt-id 42 --mode deferred --json
```

The JSON projection keeps locally verified provenance separate from untrusted
upstream metadata:

```json
{
  "metadata": {
    "schema_version": 2,
    "verified_provenance": {
      "requested_model": "model-id"
    },
    "upstream_metadata": {
      "source": "inference_reply",
      "trust": "untrusted",
      "status": "present",
      "raw": {
        "prompt_tokens": "0"
      }
    }
  }
}
```

The export preserves the presence of `prompt_tokens` and does not invent an
absent `completion_tokens` field. Raw upstream claims stay under
`upstream_metadata.raw`.

## Storage and migration

The layer table adds a trusted `outcome` column with `match`, `nonmatch`, or an
empty value for layers without a completed predicate. Inference outcomes come
from local rule evaluation state, not model response text.

The evaluation table adds `layer_count` and `label_count`. New writes store exact
counts in the same transaction as the parent and child rows. Queries reject
partial child deletion and malformed child JSON. Migrated rows use `-1` to mark
an unknown legacy count, and legacy rows still require at least one layer.

The read-only path checks for the evaluation table before querying. Missing
databases, old databases without evaluation tables, and empty evaluation tables
return a friendly empty result without creating or migrating the database.

## TDD evidence

Store/query RED:

```text
$ go test ./internal/evaluation
internal/evaluation/query_test.go:25:21: undefined: evaluation.QueryFilter
FAIL goodkind.io/agent-gate/internal/evaluation [build failed]
```

CLI RED:

```text
$ go test ./cmd/agent-gate -run 'TestRunQueryEvaluations|TestExistingQueryTableRenderers'
agent-gate query: unknown subcommand "evaluations"
FAIL goodkind.io/agent-gate/cmd/agent-gate
```

Child-integrity RED:

```text
$ go test ./internal/evaluation -run 'TestStoreListRejectsMissingAndCorruptChildRows/(missing_final_layer|missing_label)'
List accepted incomplete or corrupt child rows
FAIL goodkind.io/agent-gate/internal/evaluation
```

Focused GREEN:

```text
$ go test ./internal/evaluation ./cmd/agent-gate ./internal/rules ./internal/daemon ./internal/hook
ok goodkind.io/agent-gate/internal/evaluation
ok goodkind.io/agent-gate/cmd/agent-gate
ok goodkind.io/agent-gate/internal/rules
ok goodkind.io/agent-gate/internal/daemon
ok goodkind.io/agent-gate/internal/hook
```

Tests cover every filter, stable pagination, joined intake metadata, nested child
ordering, v2 provenance nesting, optional token presence, prohibited-field
omission, missing and corrupt children, additive migration, empty history, JSONL,
readable tables, and byte-compatible existing table renderers.

## Full verification

```text
$ make GO_MK_GENERATE= test
all repository packages passed

$ make GO_MK_GENERATE= check
lint-golangci      ok
lint-format        ok
lint-gocyclo       ok
lint-deadcode      ok
staticcheck-extra  ok
All checks passed.

$ git diff --check
no output
```

The isolated checkout used ignored `go.work` and `go.work.sum` files to route
`gksyntax` to Lahore's existing generated checkout. `GO_MK_GENERATE=` prevented
the verification gates from changing the local submodule. The submodule status
was clean before the implementation commit, and no `gksyntax` file is staged.

## Self-review

The implementation uses parameter-bound filter values, fixed schema inspection,
typed SQL scanning, bounded page sizes, and structured JSON decoding. It closes
the evaluation cursor before child queries because the intake store owns one
SQLite connection. Existing `query seen` and `query decisions` execution and
serialization paths are unchanged.

## Review follow-up

Review hardening commit:
`73624ae63e1d8f5c5169bba0ec8a9b69faf19a2d`

All layer-scoped filters now share one correlated `EXISTS` subquery. A query
cannot combine the rule identity from a deterministic layer with the model or
outcome from another inference layer.

V2 metadata now passes through a strict typed codec at write and read time. The
codec rejects unknown envelope, verified-provenance, generation-option, and
upstream invocation fields. It also rejects oversized metadata, control-bearing
known strings, invalid provenance source or trust, unknown statuses, and
inconsistent status/raw combinations. Upstream invocation claims use the shared
`inferencepb.InvocationMetadata` schema and the rules package's 4096-byte bound.
The JSONL export marshals a new allowlisted envelope instead of returning stored
metadata bytes.

Layer validation now requires `match` or `nonmatch` only for complete
`deterministic` and `inference` layers. Error, skipped, context, final,
validation, and other nonpredicate layers require an empty outcome. Every layer
also requires a canonical `sha256:` output hash whose digest exactly matches the
stored output JSON bytes. Both invariants run before writes and after reads.

Filter-correlation RED:

```text
$ go test ./internal/evaluation -run TestStoreListCorrelatesLayerScopedFilters
cross-layer disagreement returned evaluation eval-query-1
FAIL goodkind.io/agent-gate/internal/evaluation
```

Strict-metadata RED:

```text
$ go test ./internal/evaluation -run 'TestStoreRejectsUnsafeV2Metadata|TestStoreListRejectsUnknownV2MetadataAfterRead|TestStoreListReturnsOrderedSafeTrainingExport'
RecordCompleted accepted unsafe v2 metadata
List accepted unknown v2 metadata field
training export retained promptTokens instead of canonical prompt_tokens
FAIL goodkind.io/agent-gate/internal/evaluation
```

Layer-invariant RED:

```text
$ go test ./internal/evaluation -run 'TestStoreRejectsInvalidLayerOutcomeSemantics|TestStoreRejectsInvalidLayerOutputHash|TestStoreListRejectsMissingAndCorruptChildRows/(invalid_outcome_semantics|mismatched_output_hash)'
RecordCompleted accepted invalid layer outcome semantics
RecordCompleted accepted invalid output hash
List accepted incomplete or corrupt child rows
FAIL goodkind.io/agent-gate/internal/evaluation
```

Review GREEN:

```text
$ go test ./internal/evaluation ./internal/rules ./internal/daemon ./internal/hook ./cmd/agent-gate
ok goodkind.io/agent-gate/internal/evaluation
ok goodkind.io/agent-gate/internal/rules
ok goodkind.io/agent-gate/internal/daemon
ok goodkind.io/agent-gate/internal/hook
ok goodkind.io/agent-gate/cmd/agent-gate

$ make GO_MK_GENERATE= test
all repository packages passed

$ make GO_MK_GENERATE= check
lint-golangci      ok
lint-format        ok
lint-gocyclo       ok
lint-deadcode      ok
staticcheck-extra  ok
All checks passed.
```

## Legacy compatibility follow-up

Legacy compatibility commit:
`794eba378ae746b4c9244e60b07037690184dfb0`

Populated rows remain queryable when the outcome column is absent and after the
additive migration creates default-empty outcomes. The reader treats outcome
provenance as unknown only when the outcome column is absent or the row carries
the legacy `layer_count = -1` marker. It does not synthesize an outcome. New rows
and all legacy rows with known outcome provenance retain strict semantic checks.

Layer-scoped query arguments now stay local until the correlated `EXISTS` clause
is emitted. A legacy query that includes `outcome` and other layer filters emits
`1 = 0` with no unused SQLite bindings, so it returns an empty page instead of a
binding error.

Legacy RED:

```text
$ go test ./internal/evaluation -run 'TestQueryReadsPopulatedLegacyEvaluationsBeforeAndAfterMigration|TestStoreMigratesEvaluationQueryColumns'
Query before migration: complete predicate layer requires match or nonmatch outcome
FAIL goodkind.io/agent-gate/internal/evaluation
```

Legacy GREEN:

```text
$ go test ./internal/evaluation -run 'TestQueryReadsPopulatedLegacyEvaluationsBeforeAndAfterMigration|TestStoreMigratesEvaluationQueryColumns|TestStoreListRejectsMissingAndCorruptChildRows|TestStoreListCorrelatesLayerScopedFilters'
ok goodkind.io/agent-gate/internal/evaluation

$ make GO_MK_GENERATE= test
all repository packages passed

$ make GO_MK_GENERATE= check
lint-golangci      ok
lint-format        ok
lint-gocyclo       ok
lint-deadcode      ok
staticcheck-extra  ok
All checks passed.
```

The first full test run hit a transient `database is locked` result in
`TestReloadConfigValidSwap`. The exact daemon test passed 10 consecutive runs,
and the next full `make test` run passed every package.

## Concerns

Legacy evaluation rows created before child counts cannot prove the original
number of labels or the original final layer index. Their count remains `-1`, so
the query validates structure and requires at least one layer without inventing
history. New rows have exact count enforcement.

`--json` follows the existing query command convention and emits JSONL rather
than one JSON array. Each JSONL object contains its complete nested layers and
labels.
