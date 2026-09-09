# Audit CPU, storage rotation, and reset implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans to implement this plan task-by-task. Checkboxes track executable steps.

**Goal:** Remove repeated hashing and audit write amplification, replace maintenance with disposable daily databases, and provide the approved manual reset.

**Architecture:** One startup identity serves the process. Each database contains a complete event graph, and one catalog owns its lifetime. Deferred completion writes evaluation and audit rows in one transaction. The existing service installer completes reset.

**Tech stack:** Go, the existing mattn SQLite driver, embedded SQL, the existing filesystem-lock dependency, launchd on macOS, and user systemd on Linux.

**Spec:** Read the [approved design](../specs/2026-09-08-audit-rotation-design.md) at commit `2f6ba60` before implementing. That document owns product behavior, measurement results, and acceptance requirements. This plan defines the concrete changes and execution order.

## Global constraints

- Use the existing isolated checkout and branch. Do not create another worktree or change the live installation.
- Default to `bucket_interval = "24h"` and `retention_buckets = 7`. Retain the current UTC window and previous six windows.
- Changes to normalized rotation or retention values discard all existing history. Preserve this behavior across daemon restarts.
- Delete all database migrations and compatibility checks. Behavior with a pre-cut installation remains undefined until manual reset.
- Expiration discards unfinished work. No permanent operational database, archival transfer, rollback database, or preservation flag is added.
- Keep hooks and `config.toml` unchanged during manual reset. Call the existing `install service` implementation.
- Keep SQLite `NORMAL` explicit. It is already the current driver's default, so count no performance improvement from that setting alone.
- Keep each SQL script in a `.sql` file. Embed production SQL and read test fixtures from their own files.
- Use targeted package tests for development. Run `make test`, `make lint`, and `make check` for integration. Run `make check` before each commit.
- Create signed commits with the Codex trailer. Do not push, merge, deploy, or run reset without a separate request.

## Execution order and ownership

| Task | Responsibility | Depends on |
| --- | --- | --- |
| 1 | Reproducible baseline and immutable process identity | Approved spec |
| 2 | Flat schema, transactional audit writer, and removal of old maintenance | Task 1 |
| 3 | Rotation policy, catalog, locks, and destructive policy changes | Task 2 |
| 4 | Runtime rotation and one bounded replay scheduler | Task 3 |
| 5 | Retained-history queries, status, and setup integration | Task 4 |
| 6 | Manual reset through existing installation machinery | Task 5 |
| 7 | Documentation, performance qualification, and final review | Tasks 1 through 6 |

Tasks 2 through 5 form one coordinated storage replacement. Intermediate commits are development checkpoints, not release candidates. Do not add a second implementation or compatibility adapter to make an intermediate commit deployable. Keep the tree compiling and update directly affected tests with each task.

## Task 1: Measure the baseline and freeze process identity

**Files:** Modify [version.go](../../../internal/version/version.go), [main.go](../../../cmd/agent-gate/main.go), and [server.go](../../../internal/daemon/server.go). Add `internal/version/version_test.go` and `internal/daemon/audit_performance_test.go`. Extend [the existing daemon benchmarks](../../../internal/daemon/server_benchmark_test.go).

**Interfaces:** Keep `version.BuildHash() string`. Add `version.Initialize() error` and private `newBuildIdentity(resolve func() (string, error), open func(string) (io.ReadCloser, error)) *buildIdentity`. `buildIdentity` holds one `sync.Once`, a hash, and an error; its `initialize() error` and `value() string` methods both use the same once operation. Production supplies `os.Executable` and `os.Open` through a typed adapter. Cache failure as well as success; `value()` returns `"unknown"` after a failed read.

- [ ] **Capture the baseline before changing production code.** Add a fixed-trace benchmark that sends unique hook IDs through `Server.EvaluateHook`. Reuse `newBenchmarkServer` and `daemonTestConfig`, but enable auditing and use unique session/tool-use IDs so deduplication cannot turn the workload into repeated reads. Use the same trace for all comparisons: 1,000 valid allow requests, 100 valid block requests, and 100 deferred requests. Cycle through 1 KiB, 16 KiB, and 256 KiB normalized inputs within each category. External inference is a local deterministic test server with recorded responses.

```go
func benchmarkRequest(sequence uint64, payload string) *daemonpb.EvaluateHookRequest {
    return &daemonpb.EvaluateHookRequest{
        RawJson: []byte(fmt.Sprintf(
            `{"session_id":"performance","tool_use_id":"%d","hook_event_name":"PreToolUse","tool_name":"Shell","tool_input":{"command":%q}}`,
            sequence, payload,
        )),
        ProviderHint: "codex",
        EnvFingerprint: map[string]string{"CODEX_THREAD_ID": "performance"},
    }
}
```

Add the benchmark name `BenchmarkAuditTrace`. Run five fixed-count repetitions, save the output under the ignored build-output directory, and record the source commit and fixture digest. Flush and close the server after each trace before measuring final file bytes. Do not use the live database as benchmark input.

```sh
go test ./internal/daemon -run '^$' -bench '^BenchmarkAuditTrace$' -benchtime=1x -count=5
```

- [ ] **Add the identity regression.** This test uses real bytes on disk and a counted file boundary. The mutable executable path models replacement after startup; repeated calls must retain the first identity.

```go
func TestBuildIdentityReadsOnceAndSurvivesReplacement(t *testing.T) {
    path := filepath.Join(t.TempDir(), "agent-gate")
    original := []byte("first executable")
    if err := os.WriteFile(path, original, 0o700); err != nil { t.Fatal(err) }
    var reads atomic.Int64
    identity := newBuildIdentity(
        func() (string, error) { return path, nil },
        func(name string) (io.ReadCloser, error) {
            reads.Add(1)
            return os.Open(name)
        },
    )
    if err := identity.initialize(); err != nil { t.Fatal(err) }
    digest := sha256.Sum256(original)
    want := hex.EncodeToString(digest[:])[:12]
    if err := os.WriteFile(path, []byte("replacement"), 0o700); err != nil { t.Fatal(err) }
    var workers sync.WaitGroup
    for range 32 {
        workers.Go(func() {
            if got := identity.value(); got != want { t.Errorf("hash = %q, want %q", got, want) }
        })
    }
    workers.Wait()
    if got := reads.Load(); got != 1 { t.Fatalf("executable reads = %d, want 1", got) }
}
```

Also pass a reader that returns bytes followed by a sentinel error. Assert initialization returns that error, no partial hash is exposed, and later calls do not reopen the file.

- [ ] **Implement the once operation and eager initialization.** Hash with checked `io.Copy`, close the reader, and publish a value only after the complete read succeeds. Call `version.Initialize()` at daemon construction before requests can be served. The explicit version command uses `BuildHash` when needed. Do not initialize identity in unconditional CLI startup or the transport-hook path, since that would move repeated hashing into short-lived hook processes. Log an unavailable identity once. Leave existing evaluation and status callers using `BuildHash`; they become constant-time reads. Do not add another configuration-identity cache.

```go
func (identity *buildIdentity) value() string {
    if err := identity.initialize(); err != nil {
        return "unknown"
    }
    return identity.hash
}
```

- [ ] **Verify and commit.** Run the identity tests with the race detector, the existing status/evaluation tests, the unchanged trace again, and `make check`. Commit with subject `Cache agent-gate executable identity at startup`.

## Task 2: Flatten storage and remove redundant writes

**Files:** Replace [schema.go](../../../internal/auditstorage/schema.go) and [types.go](../../../internal/auditstorage/types.go); add `internal/auditstorage/schema.sql`. Add `internal/audit/sqlite_writer.go` and its embedded SQL. Update the current intake/evaluation stores, their single-file query helpers, the audit logger, and the daemon adapters together. Delete the maintenance subsystem and its CLI/setup integrations in this task.

### Final schema contract

Keep the existing business columns and their types in the following surviving tables. Apply exactly the additions and removals below when writing the new creation script. Copy current column definitions from the existing creation statements, not migration copy/rename statements. The resulting script contains creation statements only, with no schema-version ledger, `ALTER`, data-copy `INSERT ... SELECT`, or legacy detection.

| Table | Final change |
| --- | --- |
| `intake_events` | Keep existing event/search columns. Add nullable BLOB columns `wire_input`, `normalized_input`, `provider_evidence`, `environment_evidence`, plus `recorded_detail_mask integer not null`. |
| `intake_receipts` | Keep receipt identity, event ownership, and receive time. Keep the composite unique receipt/event key required by foreign keys; remove its duplicate explicitly named index. |
| `intake_deferred` | Keep state and fenced claim fields. Add `next_attempt_at text`, used by the bounded retry scheduler. |
| `gate_evaluations` | Keep current summaries and child counts. Replace `detail_state` with `content_recorded integer not null`; add nullable `error_json blob`. |
| `gate_evaluation_layers` | Keep existing summary fields and parent-layer key. Add nullable `input_json`, `output_json`, `metadata_json` BLOB columns and nullable `error_message text`. |
| `gate_evaluation_labels` | Keep label keys and summaries; add nullable `rationale text`. |
| `events` | Keep current event columns. |
| `operations`, `decisions`, `violations` | Keep current columns and add event foreign keys with `on delete cascade`. |

All other application tables in the old audit schema are deleted. In particular, no outbox, detail manifest, split detail, repair, maintenance, full-text, or migration table remains. SQLite's own sequence tables remain SQLite-owned.

Use a typed `DetailMask uint32` with fixed bits: wire 1, normalized 2, provider 4, environment 8, and evaluation content 16. Remove the deferred-audit-payload class. A nullable field distinguishes absent content from present empty bytes. The recorded mask describes retained audit content; temporary replay input does not set its bits. Remove expired-state projections and derive available/not-recorded results from the retained mask and actual columns.

Retain these secondary indexes, in addition to primary/unique keys:

```sql
create index intake_time_idx on intake_events(recorded_at, seq);
create index intake_system_time_idx on intake_events(system, recorded_at, seq);
create index intake_session_time_idx on intake_events(session_id, recorded_at, seq);
create index intake_event_time_idx on intake_events(event_name, recorded_at, seq);
create index intake_tool_time_idx on intake_events(tool_name, recorded_at, seq);
create index receipt_event_idx on intake_receipts(event_id, receipt_id);
create index receipt_time_idx on intake_receipts(received_at, receipt_id);
create index deferred_due_idx on intake_deferred(state, next_attempt_at, receipt_id);
create index deferred_event_idx on intake_deferred(event_id, state);
create index evaluation_event_idx on gate_evaluations(event_id);
create unique index evaluation_attempt_idx on gate_evaluations(receipt_id, mode, attempt);
create index evaluation_time_idx on gate_evaluations(completed_at, evaluation_id);
create index evaluation_verdict_time_idx on gate_evaluations(final_verdict, completed_at, evaluation_id);
create index audit_time_idx on events(time, event_id);
create index audit_system_time_idx on events(system, time, event_id);
create index audit_session_time_idx on events(session_id, time, event_id);
create index audit_event_time_idx on events(event_name, time, event_id);
create index audit_tool_time_idx on events(tool_name, time, event_id);
create index decision_kind_idx on decisions(kind, event_id);
create index violation_rule_idx on violations(rule, event_id);
```

The time/filter indexes serve the current public query filters. Event indexes serve ownership joins and terminal-input cleanup. The attempt key serves both uniqueness and receipt lookups. Layer and label primary-key prefixes serve their per-evaluation reads. Remove the other single-column indexes and full-text triggers. Record `EXPLAIN QUERY PLAN` output for surviving query families against representative data; a contrary result is a review finding to resolve before qualification, not permission to restore the old index inventory blindly.

**Interfaces:** Replace migration entrypoints with `auditstorage.Initialize(ctx context.Context, database *sql.DB) error`. Add `auditstorage.OpenWriter(ctx context.Context, path string) (*sql.DB, error)` for single-file opening with final-schema creation when the file is new. Existing-file opens do not inspect or repair old schemas. Every connection uses foreign keys, WAL, `NORMAL`, and the existing 5,000 ms busy timeout through URI parameters. Limit writer pools to one open and one idle connection.

```go
func WriteEventsInTx(ctx context.Context, transaction *sql.Tx, events []Event) error
func WriteEvents(ctx context.Context, database *sql.DB, events []Event) error
```

`WriteEventsInTx` belongs to package `audit` and inserts events and their child records without committing. `WriteEvents` owns one transaction around that operation. Keep event-ID deduplication and skip child insertion if the event already exists. Keep `intake.Store.CommitDeferredEvaluation`'s existing signature; extract `Event` from its `[]audit.NormalizedEntry` argument and write it in the existing completion transaction.

- [ ] **Add atomicity tests before replacing the writer.** Add `internal/intake/testdata/fail_audit_insert.sql`:

```sql
create trigger fail_audit_insert
before insert on events
when new.event_id = 'evt_blocked'
begin
    select raise(abort, 'forced audit failure');
end;
```

Use the existing `newTestStore`, `appendAtomicRecord`, `atomicEvaluationRecord`, and `atomicAuditEntries` helpers in `internal/intake/atomic_evaluation_test.go`:

```go
func TestDeferredAuditFailureRollsBackCompletion(t *testing.T) {
    store := newTestStore(t)
    receipt := appendAtomicRecord(t, store, "rollback")
    ctx := t.Context()
    if err := store.MarkDeferredPending(ctx, receipt.EventID, receipt.ReceiptID); err != nil { t.Fatal(err) }
    _, claim, err := store.ClaimDeferred(ctx, receipt.ReceiptID, "owner", time.Minute)
    if err != nil { t.Fatal(err) }
    fixture, err := os.ReadFile("testdata/fail_audit_insert.sql")
    if err != nil { t.Fatal(err) }
    if _, err := store.Handle().ExecContext(ctx, string(fixture)); err != nil { t.Fatal(err) }
    record := atomicEvaluationRecord(receipt, "evaluation", "deferred", claim.Attempt)
    err = store.CommitDeferredEvaluation(ctx, claim, record, atomicAuditEntries(receipt.EventID))
    if err == nil { t.Fatal("expected audit insertion failure") }
    if _, err := store.Evaluations().Get(ctx, "evaluation"); !errors.Is(err, evaluation.ErrNotFound) {
        t.Fatalf("evaluation survived rollback: %v", err)
    }
    pending, err := store.ListDeferredPending(ctx, 10)
    if err != nil || len(pending) != 1 { t.Fatalf("pending = %v, error = %v", pending, err) }
}
```

Add a read-only SQL fixture for the audit-row count and assert that the first event in the failed batch also rolled back. Add the successful counterpart: reopen the file, read the completed evaluation and both audit events, and assert the receipt is no longer pending. The current outbox implementation must fail this expectation because it has not published the audit rows at completion.

- [ ] **Replace the schema and all affected read/write projections.** Embed the new SQL; keep schema initialization transactional. Move each optional field into its owning insert/select. Remove all schema-presence branches from audit, intake, evaluation, completeness, and cost queries. Keep current validation of stored content and child counts.

- [ ] **Store canonical input once.** At append, write retained selected detail plus the input required to finish a receipt. For an existing event ID, repopulate missing replay-only columns from the matching incoming event and OR in newly recorded content bits. Clear excluded replay-only columns only after every receipt sharing the event has a hot result and none has pending deferred work. Do this in the terminal completion transaction. Test restart while pending, repeated receipts, truly empty input, and full/minimal policies.

- [ ] **Remove the outbox and batch unrelated audit events.** Use the transactional writer above from deferred completion. Replace the one-implementation sink/output slice in the logger with its concrete SQLite writer. Remove queued raw-payload copies. Extend `LoggerOptions` with `BatchMaxItems`, `BatchMaxBytes`, and `QueueMaxBytes`; use internal defaults of 64 items, 1 MiB per asynchronous batch, and 16 MiB queued bytes. Count serialized event bytes at admission. An event exceeding the asynchronous byte limit uses the existing visible drop path; direct callers receive an error. Never truncate or silently omit data. Atomic evaluation completion is its own transaction and does not pass through the asynchronous batch queue.

```go
func WriteEvents(ctx context.Context, database *sql.DB, events []Event) error {
    transaction, err := database.BeginTx(ctx, nil)
    if err != nil { return err }
    defer func() { _ = transaction.Rollback() }()
    if err := WriteEventsInTx(ctx, transaction, events); err != nil { return err }
    return transaction.Commit()
}
```

Test batch rollback, duplicate events, byte boundaries, queue overflow reporting, cancellation, and close/drain behavior. Register the driver's existing commit hook on the real test connection to assert one commit for a successful batch; row counts alone do not prove batching.

- [ ] **Delete obsolete code in the same compiling change.** Remove every versioned schema file, all `auditstorage/cutover*.go`, `evaluation/cutover.go`, legacy SQL fixtures, outbox implementation/tests/types/adapters, and `internal/auditmaintenance`. Remove maintain/compact CLI routing and setup previews. Remove the now-unused SQLite full-text build tag from build/test configuration along with its dedicated tests. Move the still-needed `Server.Close` out of the deleted maintenance scheduler file into `internal/daemon/lifecycle.go`. Delete maintenance timers and old tests rather than leaving no-op methods. Until Task 3 supplies the catalog, keep fresh single-file startup and a minimal file-size status implementation; no maintenance behavior remains.

- [ ] **Verify and commit.** Run narrow storage, logger, daemon, query, and setup tests, then `make test` and `make check`. Commit with subject `Flatten audit schema and commit audit records with evaluations`.

## Task 3: Add the policy catalog and destructive transitions

**Files:** Add `internal/auditstorage/catalog.go`, `catalog_state.go`, `catalog_lock.go`, and their tests. Update [audit_storage.go](../../../internal/config/audit_storage.go), [load.go](../../../internal/config/load.go), [default_config.toml](../../../internal/config/default_config.toml), and [config.toml.example](../../../config.toml.example).

The catalog owns physical file selection and locking. It does not import `audit`, `intake`, `evaluation`, or `daemon`. Keep config adaptation at callers so those packages cannot form an import cycle.

```go
type RotationPolicy struct {
    Interval time.Duration
    Retained int
}
type Bucket struct {
    ID string
    Path string
    Start time.Time
}
type CatalogOptions struct {
    BasePath string
    StatePath string
    CoordinationPath string
}
type Catalog struct { options CatalogOptions }
type BucketHandle struct {
    Bucket Bucket
    Database *sql.DB
    release func() error
}
type ReadSet struct { Handles []*BucketHandle }
type PruneResult struct {
    Removed []string
    Deferred []string
}
type CatalogState struct {
    BasePath string `json:"base_path"`
    IntervalSeconds int64 `json:"interval_seconds"`
    Retained int `json:"retention_buckets"`
    ResetPending bool `json:"reset_pending"`
    PendingBasePaths []string `json:"pending_base_paths,omitempty"`
}

func NewCatalog(options CatalogOptions) (*Catalog, error)
func (catalog *Catalog) EnsureCurrent(ctx context.Context, policy RotationPolicy, now time.Time) (Bucket, error)
func (catalog *Catalog) Reset(ctx context.Context, policy RotationPolicy, now time.Time) (Bucket, error)
func (catalog *Catalog) Prune(ctx context.Context, policy RotationPolicy, now time.Time) (PruneResult, error)
func (catalog *Catalog) OpenWriter(ctx context.Context, bucket Bucket) (*BucketHandle, error)
func (catalog *Catalog) Read(ctx context.Context, policy RotationPolicy, now time.Time) (*ReadSet, error)
func (catalog *Catalog) Purge(ctx context.Context) error
func (handle *BucketHandle) Close() error
func (set *ReadSet) Close() error
```

`EnsureCurrent` returns typed `ErrPolicyChanged` or `ErrResetPending` without opening old history when the persisted storage identity differs. `Reset` is destructive and requires the caller to have closed its writer/replay handles. `Purge` performs the deletion half for manual reset without creating a replacement database. Handle closure closes SQLite before releasing its filesystem lock.

The normalized storage identity includes canonical base path, interval seconds, and retained count. A changed base path uses the same hard cut against the recorded old family before opening the new family. State is one small JSON object at the stable per-user state path; it is not an upgrade ledger. Add pure config adapters `Config.AuditCatalogOptions() auditstorage.CatalogOptions` and `AuditStoragePolicy.Rotation() auditstorage.RotationPolicy`; config may import these types, while auditstorage must not import config. Use `filepath.Join(DefaultStateDir(), "audit-storage.json")` for state and `filepath.Join(filepath.Dir(RuntimeDir()), "agent-gate-audit.lock")` for the stable coordination file outside purged application roots.

- [ ] **Add the boundary regression.** Add the private pure function `bucketStart(now time.Time, interval time.Duration) time.Time` and table-driven tests around epoch, midnight, and daylight-saving dates. Use whole Unix seconds, including correct floor division for negative timestamps, instead of Go's year-one duration truncation.

```go
func bucketStart(now time.Time, interval time.Duration) time.Time {
    seconds := int64(interval / time.Second)
    value := now.Unix()
    remainder := value % seconds
    if remainder < 0 { remainder += seconds }
    return time.Unix(value-remainder, 0).UTC()
}

func TestBucketStartUsesEpochAlignedUTC(t *testing.T) {
    interval := 24 * time.Hour
    input := time.Date(2026, 9, 8, 23, 30, 0, 0, time.FixedZone("PDT", -7*3600))
    want := time.Date(2026, 9, 9, 0, 0, 0, 0, time.UTC)
    if got := bucketStart(input, interval); !got.Equal(want) {
        t.Fatalf("start = %v, want %v", got, want)
    }
}
```

- [ ] **Replace the config surface.** Keep `Profile` and `Detail` with canonical profiles `full` and `minimal`. Delete the old maintenance/size/retention fields. Add `BucketInterval time.Duration` and `RetentionBuckets int` to the resolved policy. Normalize raw TOML using the validated defaults. Reject zero, negative, fractional-second intervals, and arithmetic overflow. An invalid storage section must reject a destructive startup/reload transition rather than use a degraded default. Unknown old keys follow ordinary TOML decoding; add no translation or upgrade parser.

- [ ] **Implement discovery and read-only handles.** Name a bucket by inserting `-YYYYMMDDTHHMMSSZ` before the base extension. Match the exact base stem, timestamp syntax, and extension in the owning directory; do not use an unescaped glob over arbitrary user paths. Ignore unrelated entries and never follow a symlink as a deletion target. Retained starts satisfy `currentStart - (Retained-1)*Interval <= start <= currentStart`. Reject or exclude starts not aligned to the persisted interval. No file count compensates for missing time windows.

- [ ] **Implement the lock order.** Use the existing `gofrs/flock` dependency. Take the stable catalog coordination lock before per-bucket locks, and take multiple bucket locks in sorted path order. Metadata/discovery/open operations hold the catalog lock briefly. Readers and writers retain shared bucket locks for their handle lifetime. Destruction takes an exclusive bucket lock after users close. Never unlink a coordination-lock inode while another participant can acquire it. The reset operation's empty coordination lock is an active synchronization object, not retained audit or installation data; keep it outside purged data roots and reuse it through reinstall.

- [ ] **Implement durable invalidation.** Persist `ResetPending=true` and the desired normalized policy before deleting any family member. `PendingBasePaths` is the sorted unique union of the recorded active family, previously pending families, and the requested family. Write metadata through a temporary file, checked file sync, rename, and parent-directory sync. On restart, pending state always repeats deletion before opening data, even if configuration was changed back. After deletion, create and initialize the new current database, then publish `ResetPending=false` with the new base path and no pending paths. Do not publish ready while deletion or initialization is incomplete. Preserve error details and retry with a one-second minimum interval capped at 30 seconds.

The pending path list covers another configuration edit during interrupted deletion. It stores deletion targets only and is cleared after success; it contains no historical database copy or policy history.

- [ ] **Prove policy cuts using real files.** Create a catalog at a temporary base/state/coordination path, initialize two windows, and insert an intake event through the real store. Close handles, reset from seven to three retained windows, and reopen with the new policy. Assert the old event cannot be queried even when its filename would be the new current filename. Add interruption injection at pending-state write, first deletion, and new-database creation. The injected filesystem callback is the only fake; file creation, SQLite, and locks stay real.

```go
func TestExpiredFilesDoNotSurviveDowntime(t *testing.T) {
    directory := t.TempDir()
    catalog, err := NewCatalog(CatalogOptions{
        BasePath: filepath.Join(directory, "audit.db"),
        StatePath: filepath.Join(directory, "state.json"),
        CoordinationPath: filepath.Join(directory, "coordination.lock"),
    })
    if err != nil { t.Fatal(err) }
    policy := RotationPolicy{Interval: 24*time.Hour, Retained: 7}
    start := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
    old, err := catalog.EnsureCurrent(t.Context(), policy, start)
    if err != nil { t.Fatal(err) }
    later := start.Add(30*24*time.Hour)
    if _, err := catalog.EnsureCurrent(t.Context(), policy, later); err != nil { t.Fatal(err) }
    if _, err := catalog.Prune(t.Context(), policy, later); err != nil { t.Fatal(err) }
    if _, err := os.Stat(old.Path); !errors.Is(err, os.ErrNotExist) {
        t.Fatalf("expired database remains: %v", err)
    }
}
```

- [ ] **Verify and commit.** Run catalog tests with race detection, config tests, and `make check`. Commit with subject `Add disposable audit bucket catalog and policy reset`.

## Task 4: Integrate rotation and one replay scheduler

**Files:** Add `internal/daemon/audit_rotation.go`, `audit_rotation_test.go`, and `replay_scheduler.go`. Modify the daemon server from Task 1, [deferred_intake.go](../../../internal/daemon/deferred_intake.go), [intake_store.go](../../../internal/daemon/intake_store.go), and [run.go](../../../internal/daemon/run.go).

**Interfaces:** Add `bucket auditstorage.Bucket` and the owning `*auditstorage.BucketHandle` to each runtime snapshot. Add `newRuntimeSnapshotForBucket` with the existing constructor arguments plus that handle. Add `intake.NewStore(ctx context.Context, database *sql.DB, policy config.AuditStoragePolicy, log *slog.Logger) (*Store, error)` for a borrowed catalog database. Borrowed stores and sinks do not close the database; the snapshot closes the owning handle last.

Add daemon `StartAuditScheduler(ctx context.Context)` and private `reconcileAuditStorage(ctx context.Context, now time.Time) error`. Add `ReplayKey{BucketID string; ReceiptID int64}`. Add `Store.ListDueDeferred(ctx context.Context, now time.Time, afterReceiptID int64, limit int) ([]int64, error)` and `Store.ScheduleDeferredRetry(ctx context.Context, claim DeferredClaim, nextAttempt time.Time) error`. The latter clears only the matching claim and updates its next due time atomically.

- [ ] **Test the runtime boundary before wiring it.** Adapt `TestServerCloseWaitsForAdmittedEvaluation` to hold a real hook request while rotation is requested. Assert the old file remains usable until that request finishes, then send a second request and assert its bucket ID differs. Extend the fixture with an injected `now func() time.Time` and a manual timer channel; use no production sleeps to advance a day.

- [ ] **Wire the catalog into startup and snapshot creation.** Construct the catalog from config paths once. Initialize process identity before accepting requests. On ordinary startup, ensure the current bucket, open its writer handle, build the snapshot, then signal readiness. Start expiration/replay after readiness. For a pending or changed storage identity, complete the approved destructive transition before exposing a new audit store. No old history is served during this transition.

```text
ordinary rotation:
    prepare current bucket and replacement snapshot
    acquire runtime write lock
    wait for admitted old requests; swap snapshot
    release runtime write lock
    cancel and join old deferred users; drain logger; close old handle
    prune expired files; schedule replay for retained files

changed policy:
    validate candidate
    serialize lifecycle; stop old replay; wait for admitted requests
    close old audit handles
    catalog.Reset(candidate policy)
    build and publish fresh snapshot
    resume scheduler using the next absolute boundary
```

- [ ] **Replace worker-owned scans.** Keep existing bounded workers and configured claim lease/renewal intervals. One scheduler owns startup, due retries, and historical replay. Use a due-page limit of 100 receipt IDs, a 1 MiB dispatch budget, and an in-memory set of queued `ReplayKey` values. Enqueue notifications wake the scheduler; a one-second timer handles due retries and expired claims. No worker calls `ReplayPending` on a ticker. Use exponential retry delays of 1, 2, 4, 8, 16, then 30 seconds, bounded by bucket expiration. A lost claim is not a retryable evaluation failure.

Read the IDs first and queue only bucket/receipt identifiers. Load payloads inside workers after claiming work, so the dispatcher never holds a page of deserialized inputs. Its byte budget bounds queued identifiers and metadata; it does not introduce a new payload truncation rule. Release queue accounting after the worker finishes. Scan retained buckets sequentially and dispatch through the same bounded worker pool.

- [ ] **Make shutdown ownership explicit.** Derive worker contexts from the processor lifetime, track startup replay in its wait group, and check cancellation between records. Cancel active work before waiting for workers. Drain accepted audit-only messages using a bounded shutdown context, then close the store handle. Do not hold the runtime write lock while joining a scheduler that could be waiting for that same lock.

- [ ] **Handle reload, sleep, and failure.** Compare normalized storage identity before building a replacement. Unchanged policy keeps the current file and absolute deadline; rule/content changes do not delete history. Poll the wall clock at most once per second to detect overdue rotation after sleep. Ordinary rotation failure keeps the existing writer and reports/retries the error. A destructive policy change never restores the invalidated store. Expected missing files during a completed prune return empty history; corrupt retained files remain errors.

- [ ] **Verify and commit.** Test bounded replay under bursts, expired pending work, same receipt IDs in different buckets, stop/reload races, invalid config, interrupted reset, and writer-open failure. Run targeted daemon race tests, `make test`, and `make check`. Commit with subject `Rotate daemon audit storage and centralize deferred replay`.

## Task 5: Query retained history and replace status/setup behavior

**Files:** Update the audit, intake, and evaluation query modules and [cost.go](../../../internal/evaluation/cost.go). Update the CLI query/export routes from Task 1 and status routing in [audit.go](../../../cmd/agent-gate/audit.go), plus the setup coordinator and its tests.

**Interfaces:** Keep `audit.QueryReadOnly(ctx, cfg, filter)` as the sole audit query entrypoint and delete the write-capable `audit.Query`. Keep `intake.Query(ctx, cfg, filter)`. Change `evaluation.Query` and `evaluation.CostReport` to take `*config.Config` in place of a single path. Each entrypoint acquires a catalog `ReadSet` and closes it on every exit.

Add `Walk(ctx context.Context, cfg *config.Config, filter QueryFilter, yield func(QueryRecord) error) error` in each query package for streaming export. `Walk` treats zero limit as unbounded and honors cancellation and callback errors. Page-returning queries resolve zero to 100 records for audit/intake and the existing 50 for evaluations; reject limits above 1,000 and negative limits/offsets. Keep export on `Walk` rather than collecting unlimited rows in a slice.

Add `BucketID string` to public query records and to evaluation filters. The CLI exposes `--bucket` on receipt-specific queries; a positive `--receipt-id` requires it. A bucket ID is the UTC filename timestamp without a path. Reject a bucket outside the current retained catalog. Event/evaluation-ID searches may search all retained buckets; do not globally deduplicate equal IDs across buckets.

- [ ] **Add the global-limit regression.** Create two real buckets with interleaved completion timestamps and colliding local receipt IDs. Insert through the real stores. Query a limit of three and offset one; assert the result is the second through fourth records in global order, not three results per file. Use fixed UTC timestamps and explicit bucket filters for receipt lookups.

```text
bucket A evaluation times: 12:04, 12:01
bucket B evaluation times: 12:03, 12:02
query: descending completion time, offset 1, limit 3
result: B/12:03, B/12:02, A/12:01
```

- [ ] **Implement bounded merging.** Read keyset pages of 100 summary rows per bucket. Use a small heap ordered by timestamp descending, then existing local sequence/evaluation/event ID, then bucket ID. Advance only the bucket whose row was consumed. Apply offset and limit globally. Fetch detail only for selected records. Streaming export traverses all matching pages without collecting the full result in memory. Never attach every database to one SQLite connection.

Compute cost aggregates per bucket without a row limit, then merge by the existing day/model dimensions before calculating totals. Aggregate completeness over the full filtered selection, preserving its current meaning for omitted content. All reads use `mode=ro`; empty stores remain empty and do not initialize files.

- [ ] **Replace status with cheap metadata.** Define `AuditStatus` containing normalized interval/count, current bucket ID/path, retained file entries with database/WAL/SHM bytes, total bytes, next boundary, and any pending-reset or cleanup error. Use stat and the small policy metadata file only. Remove old graph counts, integrity scans, snapshots, and protected-size calculations. Add a test that calls status repeatedly while real writes continue and proves no history copy or extra database appears.

- [ ] **Update setup and public renderers.** Setup uses current-bucket resolution and the existing daemon readiness check. Remove maintenance previews and old profile choices. Update text/JSON/export rendering for bucket identity and the new status fields. Add new defaults to config examples without rewriting the user's live config. Remove obsolete schema/detail notes rather than displaying migration advice.

- [ ] **Verify and commit.** Test ordering ties, offset/limits, missing history, corrupt retained files, minimal detail, complete-detail filtering, cost totals, and a reader held open during prune. Run query/CLI/setup packages, `make test`, and `make check`. Commit with subject `Query retained audit buckets and replace maintenance status`.

## Task 6: Add manual reset through the existing installer

**Files:** Add `cmd/agent-gate/reset.go`, `reset_test.go`, `internal/install/reset.go`, and platform-specific process identity helpers. Extend [service_control.go](../../../internal/install/service_control.go). Reuse [install.go](../../../cmd/agent-gate/install.go) and [service_plan.go](../../../internal/install/service_plan.go).

**Interfaces:** Add `runReset(args []string) int`, `runResetWithDependencies(args []string, dependencies resetDependencies) int`, `installer.PrepareReset(options ResetOptions) (*ResetPlan, error)`, and `installer.ApplyReset(ctx context.Context, plan *ResetPlan) error`. `ResetError` carries `Stage string` and `Err error` and implements `Error` and `Unwrap`.

`ResetOptions` contains the canonical executable path, service options, state/cache/runtime/config roots, custom audit base and conversation paths, preserved hook/configuration paths, and a writer for diagnostics. `ResetPlan` freezes those resolved paths and verified process identity; `ApplyReset` owns executable staging and cleanup. Add `ServiceState.Absent bool` and `StopService(ctx, options) error`; absence is a typed platform result, not any command failure. Keep operating-system calls behind the existing runner boundary.

- [ ] **Add reset routing and a real-files fixture.** Reject extra arguments before mutation. Run reset only through the explicit `reset` command. Build `newResetInstallationFixture(t)` from the existing temporary-home installer fixture and Unix-socket readiness fixture. It uses real installer preparation/application, real files, a child daemon under temporary roots, and a fake launchctl/systemctl boundary that controls that child. It must never operate on the host's real service label.

The fixture methods are: `InstallUsingExistingInstaller()`, `SeedAuditAndInstallationState()`, `ReadPreservedFiles() map[string][]byte`, `RunReset() int`, `AssertPreservedFilesEqual(map[string][]byte)`, `AssertOldAuditAndStateAbsent()`, `AssertExecutableRestoredAndRunnable()`, and `AssertServiceReadyWithNewProcess()`. Assertions fail through the fixture's `testing.T`; helpers perform the real filesystem and socket observations described by their names.

```go
func TestResetReinstallsAndPreservesConfiguration(t *testing.T) {
    fixture := newResetInstallationFixture(t)
    fixture.InstallUsingExistingInstaller()
    fixture.SeedAuditAndInstallationState()
    before := fixture.ReadPreservedFiles()
    if code := fixture.RunReset(); code != 0 { t.Fatalf("reset exit = %d", code) }
    fixture.AssertPreservedFilesEqual(before)
    fixture.AssertOldAuditAndStateAbsent()
    fixture.AssertExecutableRestoredAndRunnable()
    fixture.AssertServiceReadyWithNewProcess()
}
```

- [ ] **Prepare without opening a database.** Parse configuration to resolve removal paths only. Use the existing canonical executable resolver and service-plan path construction. Capture the service's executable and daemon PID/start identity; refuse a different installation. Enumerate application-owned state/cache/runtime contents and non-preserved application config contents. For an external database family, remove only the configured base file, exact recognized bucket names, metadata, and sidecars. For a custom conversation directory, remove only the agent-gate-owned contents. Reject deletion overlap with preserved hooks/configuration or unrelated shared parent roots.

- [ ] **Stage and stop strictly.** Stage the executable outside deletion roots, preserving mode and signing attributes. Verify staged bytes and executability before unloading anything. On macOS boot out `gui/<uid>/io.goodkind.agent-gate`; on Linux stop and disable `agent-gate.service`. Inspect typed service state afterward. Wait up to ten seconds for the captured daemon; send TERM, wait up to ten seconds, then KILL only if PID, start identity, and executable still match. Wait for exit before deleting any database. Never signal by process basename or kill launchd itself.

- [ ] **Delete and reinstall.** Serialize the entire command with a separate operation lock at `filepath.Join(filepath.Dir(config.RuntimeDir()), "agent-gate-reset.lock")`. Take the catalog coordination lock only for storage teardown, and release it after purge and executable restoration, before starting the installer. The new daemon must be able to acquire the catalog lock during readiness. Keep the reset-operation lock until installation returns. Purge the audit family and application state, remove the service definition and installed executable, restore the staged executable at the same canonical path, then call the existing command implementation:

```go
return runInstall([]string{"service", "--bin-path", installedPath})
```

The dependency-aware test path calls `runInstallWithDependencies` with the real installer preparation/application functions and the controlled OS boundary. Do not call `install all`, because it selects configuration and hooks. Do not reproduce installer logic in reset.

Keep the lock layering non-recursive: the reset command holds its operation lock and calls `Catalog.Purge`, which owns and releases catalog coordination internally. Do not acquire that same catalog lock around `Purge`. Add a regression where installer readiness opens the fresh catalog; reset must complete without a lock timeout.

Already-absent targets succeed. Failed shutdown prevents database deletion. A failure after teardown reports its stage and restores a usable staged executable before returning when restoration is still possible; it does not restore data. Keep the staged executable until restoration has succeeded, then remove the staging directory. Repeated reset handles partial removal.

- [ ] **Verify and commit.** Test absent service, repeated reset, failed bootout, TERM-resistant child, PID reuse/identity mismatch, stale socket, custom paths next to unrelated files, symlinked executable, protected-path overlap, partial deletion, and reinstall/readiness failure. Verify preserved bytes and permissions. Run installer and CLI tests, `make test`, and `make check`. Commit with subject `Add manual agent-gate reset command`.

## Task 7: Qualify the full replacement and update documentation

**Files:** Update [audit-storage.md](../../audit-storage.md), command help, config examples, and [check-doc-commands.sh](../../../scripts/check-doc-commands.sh) where removed commands are exercised. Extend the baseline harness from Task 1. Keep this approved spec as the design source rather than copying it into user documentation.

- [ ] **Replace obsolete user instructions.** Keep the existing storage page as a how-to guide for configuring rotation, inspecting status, and manually resetting. State destructive consequences beside policy changes and reset. Remove maintenance/compaction/upgrade recipes and references to deleted commands. Read the surrounding docs and remove duplicate obsolete storage claims. Keep hook installation instructions and their ownership unchanged.

- [ ] **Run controlled before/after comparisons.** Use the exact baseline trace and fixture digest from Task 1. Measure an empty store and a seeded seven-window history, then idle pending backlog, steady traffic, bursts, repeated status, cancellation, rotation, and policy reset. Seed through real public storage boundaries in temporary roots; do not copy the user's live database. Replay the same trace five times per case and record baseline spread before judging the replacement.

Use the SQLite connection's real commit hook for transaction counts. Use operating-system process counters for CPU and physical disk bytes, and final closed-file sizes for retained bytes. Capture actual synchronization calls only through a validated instrument. Stack leaf observations remain attribution evidence, not call counts. Store no raw private hook payloads in benchmark reports.

```text
For each active-workload metric:
    baseline_spread = baseline_max - baseline_min
    improvement = baseline_median - replacement_median
    CPU and disk work per event pass when improvement > baseline_spread

For latency and error-rate non-regression:
    replacement_median <= baseline_median + baseline_spread

For completed-work throughput:
    replacement_median >= baseline_median - baseline_spread
```

Report p95/p99 latency, worst five-second CPU/disk intervals, queue peak, oldest pending age, retries per receipt, final bytes per event, and all failures. The deterministic trace must have zero unexplained persistence failures. If a comparison fails, investigate the measured path; do not change timeouts or thresholds to turn it green.

- [ ] **Prove the mechanical removals through behavior.** Verify one executable read per process, one replay scheduling owner, four necessary persistence stages for a normal deferred receipt without renewals/failures, atomic final audit results, no status-created copies, and expired-file reclamation. Search for migration/outbox/maintenance/FTS references to locate missed callers, then remove them; do not substitute static string tests for the behavioral checks.

- [ ] **Run final gates and review.** Run `make test`, `make lint`, and `make check`, plus focused race tests for catalog/replay/reset. Run the strongest-model adversarial review for input handling, concurrency, and destructive paths. Resolve findings and rerun only affected checks. Confirm the working tree contains only intended changes and verify signed commits before any later push request.

- [ ] **Commit the completed qualification and documentation.** Use subject `Document audit rotation and record performance qualification`. The deliverable includes the measured before/after report, current command examples, and explicit remaining failures if qualification did not pass. Do not describe the feature as complete while an acceptance case remains unproven.

## Coverage and handoff

| Approved concern | Tasks and proof |
| --- | --- |
| Repeated hashing and sampled CPU work | Task 1 identity regression; Task 7 CPU comparison |
| Transactions, duplicated detail, unused indexes, and SSD traffic | Task 2 atomic writer/schema; Task 7 disk and commit counters |
| Starvation and compounding replay | Tasks 3 and 4 absolute windows, single scheduler, bounded backlog tests |
| One-week configurable retention and hard cuts | Task 3 catalog tests; Task 4 live/offline reload cases |
| Query correctness and cheap inspection | Task 5 global merge, cost totals, no-copy status |
| Manual destructive reset preserving hooks/config | Task 6 real-files and child-daemon tests |
| No migrations or compatibility machinery | Task 2 deletions; Task 7 complete caller inventory |
| End-to-end qualification | Task 7 controlled workloads, canonical gates, and adversarial review |

Implementation starts only after the user chooses execution. The plan does not authorize live deployment or running reset on the user's installation.
