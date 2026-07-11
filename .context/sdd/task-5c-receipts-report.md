# Task 5c Receipt Identity Report

Status: DONE_WITH_CONCERNS

## Receipt identity

Deferred state is keyed by `receipt_id`, with the canonical `event_id` retained as
the validated event relationship. Duplicate canonical events therefore keep separate
pending, replay, and completion state in receipt order.

`Record` now exposes `ReceiptID` and `ReceivedAt`. `GetReceipt` loads the canonical
event for one receipt, and the daemon carries that receipt identity through enqueue,
replay, and completion.

Legacy event-keyed deferred rows migrate to the newest matching receipt. Rows without
a receipt move to `intake_deferred_repairs` with their existing state and the sanitized
`missing_receipt` classification. The migration does not fabricate a receipt link.

## TDD evidence

RED:

```text
go test -count=1 -tags sqlite_fts5 ./internal/intake -run 'TestDuplicateEventReceipts|TestMarkDeferredPendingRejects|TestOpenSQLiteMigratesLegacyDeferred'
```

The test package failed to compile because the old API lacked `Record.ReceiptID`,
`ErrReceiptEventMismatch`, and receipt parameters for deferred state transitions.

The added pre-deferred-state `GetReceipt` assertion also failed before the query fix:

```text
sql: Scan error on column index 24, name "replay_count": converting NULL to int is unsupported
```

GREEN:

```text
go test -count=1 -tags sqlite_fts5 ./internal/intake
ok goodkind.io/agent-gate/internal/intake

go test -count=1 ./internal/daemon
ok goodkind.io/agent-gate/internal/daemon

make test
PASS
```

## Concern

`make check` reaches the Go lint gate and reports one finding outside this slice:

```text
internal/evaluation/store.go:227:18: Close should use defer (sqlclosecheck)
```

The receipt identity files have no remaining reported check findings.
