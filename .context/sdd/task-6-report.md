# Task 6 Report

## Result

The intake database now records one durable receipt for every successful append, including duplicate canonical events. The same database now owns a typed evaluation ledger for complete evaluations, ordered layers, and versioned labels.

## Schema and migration

- `intake_receipts` assigns a distinct integer receipt identity and references the canonical `intake_events` row.
- `gate_evaluations` references the matching receipt and event identities and stores final evaluation metadata.
- `gate_evaluation_layers` uses `(evaluation_id, layer_index)` identity and a self-reference for parent layers.
- `gate_evaluation_labels` uses `(evaluation_id, namespace, label_version)` identity.
- Evaluation and child writes use one transaction. Schema initialization also uses one transaction.
- Opening a populated current database preserves its existing intake rows and adds the new tables and indices.

## TDD evidence

Receipt RED:

```text
go test -count=1 ./internal/intake -run 'TestAppendRecordsEveryReceipt|TestOpenSQLiteMigratesPopulatedCurrentDatabase|TestAppendRollsBackEventWhenReceiptInsertFails|TestConcurrentAppendsKeepReceipts'
build failed because intake.AppendResult had no ReceiptID field
```

Receipt GREEN:

```text
ok goodkind.io/agent-gate/internal/intake 1.094s
```

Evaluation RED:

```text
go test -count=1 ./internal/evaluation
no non-test Go files in internal/evaluation
FAIL goodkind.io/agent-gate/internal/evaluation [build failed]
```

Evaluation GREEN:

```text
ok goodkind.io/agent-gate/internal/evaluation 0.360s
```

Configuration identity RED:

```text
go test -count=1 ./internal/config -run 'TestIdentity'
build failed because *Config had no Identity method
```

Configuration identity GREEN:

```text
ok goodkind.io/agent-gate/internal/config 0.319s
```

Review follow-up RED:

```text
invalid JSON was accepted and pragma foreign_keys returned 0
invalid label confidence -0.01 was accepted
```

Review follow-up GREEN:

```text
ok goodkind.io/agent-gate/internal/evaluation 0.277s
```

## Commits

- `52ad154` adds durable receipt identity and receipt transaction tests.
- `2626522` adds the typed evaluation models, schema, atomic store, reads, and tests.
- `6da0b43` enables foreign-key enforcement and validates ledger JSON and confidence.
- `a4caf6f` adds exact-byte and structural configuration identities.
- `8f98c6e` satisfies the durable ledger static checks.

## Verification

Focused storage verification passes:

```text
go test -count=1 -tags sqlite_fts5 ./internal/intake ./internal/evaluation
ok goodkind.io/agent-gate/internal/intake 0.354s
ok goodkind.io/agent-gate/internal/evaluation 0.500s
```

The required full test suite passes:

```text
make test
all packages passed
```

`make check` clears `lint-format`, `lint-gocyclo`, `lint-deadcode`, `staticcheck-extra`, and every Task 6 `golangci-lint` finding. The command remains nonzero because the concurrent Task 5b inference commit has 25 new `golangci-lint` findings outside Task 6 scope.

## Review

Read-only review confirmed transaction atomicity, receipt/event foreign keys, parent-layer integrity, and nullable field round trips. The review found missing active foreign-key enforcement and missing JSON and confidence validation; commit `6da0b43` fixes both with RED/GREEN tests.

The review also suggested uniqueness on `(receipt_id, attempt, mode)`. Task 6 defines `evaluation_id` as the evaluation identity and does not define that tuple as unique, so this task does not add that constraint.

## Concerns

The direct intake package test without the repository SQLite build tag cannot create its FTS5 virtual table. The focused verification uses `sqlite_fts5`, matching the repository harness.

Task 6 does not wire evaluation writes into inference or enforcement responses. That integration remains in the later task named by the brief.
