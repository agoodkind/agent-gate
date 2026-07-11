# Task 5c schema sub-slice A report

## Scope

This commit changes only evaluation layer metadata storage. It adds the typed
`MetadataJSON` field, the additive SQLite column migration, JSON validation,
and insert and read round-trip support. It does not change intake receipt
linkage or any daemon, hook, rules, inference, or live configuration path.

## RED evidence

`go test -count=1 -tags sqlite_fts5 ./internal/evaluation`

The first run failed to compile because `evaluation.Layer` had no
`MetadataJSON` field at the migration, invalid-metadata, and round-trip test
sites.

After adding only the typed field, the same command produced three behavioral
failures:

1. Round-trip reads returned nil metadata.
2. The populated migration test could not drop the absent `metadata_json`
   column.
3. `RecordCompleted` accepted invalid layer metadata JSON.

After adding the schema and store paths, the migration test exposed one final
boundary issue: SQLite returned the default `'{}'` as text, which could not scan
directly into `json.RawMessage`. Scanning through bytes fixed both migrated text
defaults and newly inserted blob values.

## GREEN evidence

`go test -count=1 -tags sqlite_fts5 ./internal/evaluation -run '^TestStoreMigratesPopulatedLayerMetadata$'`

Passed. A populated pre-column database gains
`metadata_json blob not null default '{}'`, and every old layer reads back as
`{}`.

`go test -count=1 -tags sqlite_fts5 ./internal/evaluation -run '^TestStoreRejectsInvalidJSONBeforeWriting/layer_metadata$'`

Passed. Invalid metadata is rejected before the transaction writes an
evaluation, and the evaluation remains absent.

`go test -count=1 -tags sqlite_fts5 ./internal/evaluation`

Passed the complete focused package, including nonempty metadata round-trip.

## Global verification

`make check` was attempted. The global gate stopped on concurrent out-of-scope
inference proto and rules test changes: pointer type mismatches in
`internal/rules/infer_gate_test.go` and formatting drift in
`api/inferencepb/inference_contract_test.go`. No evaluation finding was
reported. Those concurrent files were not modified by this sub-slice.

`make test` was not rerun because the same concurrent rules package type errors
make the repository-wide command non-buildable. The focused SQLite package is
green with a fresh uncached run.
