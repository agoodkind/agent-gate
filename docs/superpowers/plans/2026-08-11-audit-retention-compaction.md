# Audit Retention and Compaction Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Bound the audit database by age or size and reclaim SQLite space without deleting live work or delaying daemon startup.

**Architecture:** A read-only planner selects eligible event graphs from one policy snapshot and clock value. Apply mode holds a process lock and a database lease, then commits bounded batches. The daemon starts a separate timer only after readiness and waits one full interval. Full compaction is an explicit offline workflow with verified rollback.

**Tech Stack:** Go, database/sql, mattn/go-sqlite3, Unix file locks, launchd, systemd, the existing command line interface, and Go integration tests.

The [approved design](../specs/2026-08-11-audit-lifecycle-design.md) defines the product contract.

## Global Constraints

- Implement AGATE-38 through AGATE-44 in order.
- Complete AGATE-33, AGATE-34, AGATE-35, AGATE-54, AGATE-55, AGATE-36, and AGATE-37 before AGATE-38.
- Use receipt time for age.
- Delete an event graph only after every related receipt and outbox is terminal.
- Never override protection to meet a size target.
- Never run retention, integrity scans, checkpoints, or page reclamation during daemon startup.
- Start the automatic timer only after daemon readiness. Wait one full interval.
- Give hook writes priority. Stop a maintenance run on database contention.
- Keep dry-run and status read-only.
- Never schedule full compaction.
- Reject unresolved full-compaction journals before any normal database open,
  migration, creation, or write.
- Use real SQLite files and public command boundaries in tests.
- Run make check before each ticket commit.
- Create one signed commit per ticket. Each commit can become one pull request.

## File Structure

AGATE-38:

~~~text
internal/auditmaintenance/types.go
internal/auditmaintenance/preview.go
internal/auditmaintenance/preview_test.go
internal/auditmaintenance/status.go
internal/auditmaintenance/status_test.go
cmd/agent-gate/audit.go
cmd/agent-gate/audit_test.go
cmd/agent-gate/main.go
cmd/agent-gate/main_test.go
~~~

AGATE-39:

~~~text
internal/auditmaintenance/lock.go
internal/auditmaintenance/lock_test.go
internal/auditmaintenance/lease.go
internal/auditmaintenance/lease_test.go
internal/auditmaintenance/apply.go
internal/auditmaintenance/apply_test.go
internal/auditstorage/maintenance_v5.go
internal/auditstorage/maintenance_v5_test.go
internal/intake/store.go
internal/intake/receipt_test.go
cmd/agent-gate/audit.go
cmd/agent-gate/audit_test.go
~~~

AGATE-40:

~~~text
internal/auditmaintenance/size.go
internal/auditmaintenance/size_test.go
internal/auditmaintenance/apply.go
internal/auditmaintenance/status.go
cmd/agent-gate/audit_test.go
~~~

AGATE-41:

~~~text
internal/auditmaintenance/compact.go
internal/auditmaintenance/compact_test.go
internal/auditmaintenance/status.go
cmd/agent-gate/audit.go
cmd/agent-gate/audit_test.go
~~~

AGATE-42:

~~~text
internal/auditmaintenance/schedule.go
internal/auditmaintenance/schedule_test.go
internal/auditstorage/schedule_v6.go
internal/auditstorage/schedule_v6_test.go
internal/intake/store.go
internal/intake/receipt_test.go
internal/daemon/maintenance_scheduler.go
internal/daemon/maintenance_scheduler_test.go
internal/daemon/run.go
internal/daemon/run_test.go
internal/daemon/server.go
internal/daemon/server_test.go
~~~

AGATE-43:

~~~text
internal/install/service_control.go
internal/install/service_control_test.go
internal/auditmaintenance/full_preflight.go
internal/auditmaintenance/full_preflight_test.go
cmd/agent-gate/audit.go
cmd/agent-gate/audit_test.go
~~~

AGATE-44:

~~~text
internal/auditmaintenance/full_compact.go
internal/auditmaintenance/full_compact_test.go
internal/auditstorage/cutover_journal.go
internal/auditstorage/cutover_journal_test.go
internal/install/service_control.go
internal/install/service_control_test.go
internal/daemon/compaction_gate.go
internal/daemon/compaction_gate_test.go
internal/daemon/run.go
internal/daemon/run_test.go
cmd/agent-gate/audit.go
cmd/agent-gate/audit_test.go
~~~

## Task 1: Preview maintenance and report status

**Ticket:** AGATE-38

**Files:** Use the AGATE-38 file set above.

- [ ] **Step 1: Write failing read-only planner tests**

Build a temporary version-four database with:

~~~text
old completed detail
recent completed detail
old completed summary
receipt awaiting hot evaluation
pending deferred evaluation
expired evaluation claim
pending audit outbox
expired audit claim
partly delivered outbox
~~~

Assert pending and expired claims remain protected. Open the database read-only and compare file bytes before and after preview.

~~~go
func TestPreviewSelectsOnlyEligibleGraphs(t *testing.T)
func TestPreviewProtectsReceiptUntilHotEvaluationCompletes(t *testing.T)
func TestPreviewProtectsRetryableWorkRegardlessOfAge(t *testing.T)
func TestPreviewUsesOneClockAndPolicySnapshot(t *testing.T)
func TestPreviewDoesNotWriteDatabase(t *testing.T)
~~~

Run:

~~~sh
go test ./internal/auditmaintenance -run Preview
~~~

Expected: FAIL because the package does not exist.

- [ ] **Step 2: Define plan and status types**

~~~go
type Plan struct {
    PlannedAt              time.Time  `json:"planned_at"`
    PolicyHash             string     `json:"policy_hash"`
    DetailCutoff           *time.Time `json:"detail_cutoff,omitempty"`
    SummaryCutoff          time.Time  `json:"summary_cutoff"`
    DetailCandidateGraphs  int64      `json:"detail_candidate_graphs"`
    SummaryCandidateGraphs int64      `json:"summary_candidate_graphs"`
    ProtectedGraphs        int64      `json:"protected_graphs"`
    ProtectedBytes         int64      `json:"protected_bytes"`
    EstimatedDeleteBytes   int64      `json:"estimated_delete_bytes"`
}

type Status struct {
    Policy             config.AuditStoragePolicy `json:"policy"`
    DatabaseBytes      int64                     `json:"database_bytes"`
    WALBytes           int64                     `json:"wal_bytes"`
    OldestDetailAt     *time.Time                `json:"oldest_detail_at,omitempty"`
    OldestSummaryAt    *time.Time                `json:"oldest_summary_at,omitempty"`
    ProtectedGraphs    int64                     `json:"protected_graphs"`
    ReclaimablePages   int64                     `json:"reclaimable_pages"`
    FullCompactNeeded  bool                      `json:"full_compact_needed"`
    IntegrityOK        bool                      `json:"integrity_ok"`
    IntegrityError     string                    `json:"integrity_error,omitempty"`
    LastRun            *RunSummary               `json:"last_run,omitempty"`
    MaintenanceDueAt   *time.Time                `json:"maintenance_due_at,omitempty"`
    NextAttemptAt      *time.Time                `json:"next_attempt_at,omitempty"`
    Overdue            bool                      `json:"overdue"`
    SizeState          SizeState                 `json:"size_state"`
}
~~~

Add:

~~~go
func Preview(
    ctx context.Context,
    path string,
    policy config.AuditStoragePolicy,
    now time.Time,
) (Plan, error)

func ReadStatus(
    ctx context.Context,
    path string,
    policy config.AuditStoragePolicy,
    now time.Time,
) (Status, error)
~~~

- [ ] **Step 3: Select whole event graphs**

Build one eligibility common table expression keyed by intake event ID.

An event is protected when any related receipt has:

~~~text
no completed hot evaluation
intake_deferred.state = pending
deferred_audit_outbox.state = pending
an undelivered deferred_audit_outbox entry
~~~

An expired claim remains pending and protected. A completed graph uses the latest durable receipt time for cutoffs.

Estimate bytes with SQLite payload lengths and page statistics. Label the value as an estimate.

- [ ] **Step 4: Add public read-only commands**

Route `audit` from the main command switch. Implement:

~~~text
agent-gate audit status
agent-gate audit status --json
agent-gate audit status --check
agent-gate audit maintain --dry-run
~~~

Use a read-only SQLite data source name for all four commands. `status --check` exits one for overdue maintenance, integrity failure, or an unmet unconstrained size target. Plain status and dry-run exit zero after reporting the condition.

Status derives maintenance due time from the last successful completion plus the active interval. Before any success, it uses the storage migration time. Overdue compares that due time with now.

Status also reads the scheduler's next attempt when present. Restart and reload may move the next attempt without clearing overdue work. Status never writes a missing run or schedule record.

Run:

~~~sh
go test ./internal/auditmaintenance ./cmd/agent-gate -run "Audit|Preview|Status"
~~~

Expected: PASS.

- [ ] **Step 5: Prove no hidden writes**

Test database, write-ahead log, and shared-memory file existence and bytes before and after every read-only command.

Run:

~~~sh
make test
make check
~~~

Expected: both commands exit zero.

- [ ] **Step 6: Commit**

~~~sh
git add internal/auditmaintenance cmd/agent-gate
git commit -S -m "Add audit maintenance preview and status" -m "Co-authored-by: Codex <noreply@openai.com>"
~~~

## Task 2: Apply bounded age retention

**Ticket:** AGATE-39

**Depends on:** AGATE-38

**Files:** Use the AGATE-39 file set above.

- [ ] **Step 1: Write failing apply tests**

Use the same graph fixture as preview. Assert:

~~~go
func TestApplyMatchesPreviewAtSameClockAndPolicy(t *testing.T)
func TestApplyDemotesDetailBeforeDeletingSummary(t *testing.T)
func TestApplyNeverDeletesProtectedGraph(t *testing.T)
func TestApplyCannotDeleteConcurrentHotEvaluationReceipt(t *testing.T)
func TestApplyRollsBackOnlyFailingBatch(t *testing.T)
func TestApplyIsIdempotent(t *testing.T)
func TestApplyStopsOnBusyWithoutBreakingConcurrentAppend(t *testing.T)
func TestApplyRejectsOverlappingLease(t *testing.T)
func TestApplyReleasesOwnedLeaseAfterSuccessAndFailure(t *testing.T)
func TestApplyReleasesOwnedLeaseAfterCancellation(t *testing.T)
func TestApplyKeepsSharedAuditEventReferencedByYoungerGraph(t *testing.T)
~~~

Run:

~~~sh
go test ./internal/auditmaintenance -run Apply
~~~

Expected: FAIL because apply and maintenance metadata do not exist.

- [ ] **Step 2: Add maintenance metadata and locks**

Register schema version five through the shared audit migration runner. Create:

~~~sql
create table audit_maintenance_lease (
    singleton integer primary key check(singleton = 1),
    owner text not null,
    run_id text not null,
    expires_at text not null
);

create table audit_maintenance_runs (
    run_id text primary key,
    planned_at text not null,
    started_at text not null,
    completed_at text,
    policy_hash text not null,
    plan_json text not null,
    detail_graphs integer not null default 0,
    summary_graphs integer not null default 0,
    reclaimed_bytes integer not null default 0,
    result text not null,
    error_class text not null default '',
    next_due_at text
);
~~~

Use an exclusive nonblocking file lock beside the database. Then acquire the database lease with compare-and-set SQL. Renew it before each batch. Every online and offline apply path must hold both locks.

Name the file lock by appending `.maintenance.lock` to the database path. Release
a lease only when both owner and run ID match. Every success, error, and
cancellation path releases its owned database lease before releasing the file
lock. Use a bounded cleanup context derived with `context.WithoutCancel`; never
reuse the cancelled apply context for lease cleanup.

~~~go
var ErrMaintenanceBusy = errors.New("audit maintenance is already running")

type ApplyOptions struct {
    Path     string
    Policy   config.AuditStoragePolicy
    Now      time.Time
    Owner    string
    LeaseTTL time.Duration
    Log      *slog.Logger
}
~~~

- [ ] **Step 3: Demote detail in bounded transactions**

Select at most `MaintenanceBatchRows` eligible event IDs in receipt order. In one transaction per batch:

1. Recheck protection for selected IDs.
2. Delete physical intake, evaluation, label, and completed outbox payload detail.
3. Set recorded manifests to expired.
4. Leave classes never recorded as not_recorded.
5. Commit.

Use a zero busy timeout on the maintenance connection. Return a deferred result when a write lock is unavailable.

- [ ] **Step 4: Delete old summary graphs**

Delete children before parents. Use retained outbox entry headers to locate derived audit event IDs. Delete:

~~~text
audit violations, decisions, operations, and events
completed outbox headers and entries
evaluation labels, layers, and evaluations
completed deferred rows and receipts
intake detail manifests and event summaries
~~~

Do not delete a graph when any receipt remains nonterminal. An audit event ID
can be shared by several intake graphs. Delete an audit event and its children
only when no outbox entry outside the selected graph set still references that
ID.

- [ ] **Step 5: Record results and expose apply**

Implement:

~~~text
agent-gate audit maintain --apply
~~~

Require exactly one of `--dry-run` or `--apply`. Record the immutable plan, counts, result, error class, and next due time. A busy result exits zero after saying maintenance deferred. Integrity or schema failures exit one and perform no deletion.

Run:

~~~sh
go test ./internal/auditmaintenance ./cmd/agent-gate -run "Apply|Maintain"
~~~

Expected: PASS.

- [ ] **Step 6: Run concurrency and repository verification**

Run a real intake append loop while maintenance commits one-row batches. Assert every append returns a receipt and every retained graph passes foreign-key checks.

~~~sh
go test -race ./internal/auditmaintenance ./internal/intake
make test
make check
~~~

Expected: all commands exit zero.

- [ ] **Step 7: Commit**

~~~sh
git add internal/auditstorage internal/auditmaintenance internal/intake cmd/agent-gate
git commit -S -m "Apply bounded audit age retention" -m "Co-authored-by: Codex <noreply@openai.com>"
~~~

## Task 3: Enforce the optional size target

**Ticket:** AGATE-40

**Depends on:** AGATE-39

**Files:** Use the AGATE-40 file set above.

- [ ] **Step 1: Write size-policy tests**

Create event graphs with distinct receipt times and payload sizes.

~~~go
func TestSizeRetentionDeletesOldestEligibleGraphFirst(t *testing.T)
func TestSizeRetentionRunsAfterAgeRetention(t *testing.T)
func TestSizeRetentionStopsAtProtectedData(t *testing.T)
func TestSizeRetentionStopsWhenCompactedUsageMeetsTarget(t *testing.T)
func TestSizeRetentionDoesNotDeleteForUnreclaimedFreePages(t *testing.T)
func TestSizeRetentionIgnoresCheckpointedWALAllocation(t *testing.T)
func TestZeroSizeTargetDisablesSizePruning(t *testing.T)
func TestStatusCheckAcceptsProtectedConstraint(t *testing.T)
~~~

Run:

~~~sh
go test ./internal/auditmaintenance ./cmd/agent-gate -run Size
~~~

Expected: FAIL because apply ignores MaxSizeBytes.

- [ ] **Step 2: Measure database and write-ahead log**

~~~go
type DatabaseSize struct {
    DatabaseBytes       int64
    WALBytes            int64
    PageSizeBytes       int64
    PageCount           int64
    FreePages           int64
    CompactedUsageBytes int64
}

func MeasureDatabaseSize(path string) (DatabaseSize, error)
~~~

After age retention, run a nonblocking passive checkpoint before size pruning.
Calculate compacted usage from allocated nonfree pages plus live write-ahead log
frames that the checkpoint could not backfill. Never use raw write-ahead log file
allocation as deletion pressure because checkpointed high-water space can remain
allocated. Re-run the passive checkpoint and remeasure after each committed
graph batch. Do not use estimated deleted bytes as proof that the target is met.

- [ ] **Step 3: Prune oldest eligible graphs**

After age work, select the oldest remaining eligible graphs by latest receipt time. Reuse the whole-graph delete transaction. Stop when compacted usage reaches the target, the database becomes busy, the context is cancelled, or no eligible graph remains.

Never delete more data only because free pages have not been reclaimed. Report `reclaim_pending` when compacted usage meets the target but physical bytes remain high.

Return `constrained` when protected graphs are the only remaining candidates. Include their estimated bytes.

- [ ] **Step 4: Update status exit behavior**

`status --check` succeeds for a protected constraint. It fails for `reclaim_pending` or when an enabled target remains unmet and eligible work remains after a completed run.

Run:

~~~sh
go test ./internal/auditmaintenance ./cmd/agent-gate -run Size
make check
~~~

Expected: both commands exit zero.

- [ ] **Step 5: Commit**

~~~sh
git add internal/auditmaintenance cmd/agent-gate/audit_test.go
git commit -S -m "Enforce the audit database size target" -m "Co-authored-by: Codex <noreply@openai.com>"
~~~

## Task 4: Reclaim pages incrementally

**Ticket:** AGATE-41

**Depends on:** AGATE-40

**Files:** Use the AGATE-41 file set above.

- [ ] **Step 1: Write compaction tests**

Create reclaimable pages in incremental and legacy databases.

~~~go
func TestCompactDryRunReportsFreePagesWithoutWriting(t *testing.T)
func TestCompactApplyCheckpointsAndReclaimsBoundedPages(t *testing.T)
func TestCompactApplyPreservesIntegrityAndRows(t *testing.T)
func TestCompactReportsFullCompactionNeededForLegacyDatabase(t *testing.T)
func TestMaintenanceCompactsOnlyWhenConfigured(t *testing.T)
func TestCompactApplyRejectsOverlappingMaintenance(t *testing.T)
~~~

Run:

~~~sh
go test ./internal/auditmaintenance ./cmd/agent-gate -run Compact
~~~

Expected: FAIL because compaction does not exist.

- [ ] **Step 2: Add an incremental plan**

~~~go
type CompactPlan struct {
    AutoVacuumMode   int   `json:"auto_vacuum_mode"`
    FreePages        int64 `json:"free_pages"`
    PagesToReclaim   int64 `json:"pages_to_reclaim"`
    FullModeRequired bool  `json:"full_mode_required"`
}
~~~

Cap normal reclaim to a named page count derived from the configured row batch. Keep it short and deterministic.

- [ ] **Step 3: Checkpoint and reclaim**

Apply uses:

~~~sql
pragma wal_checkpoint(PASSIVE);
pragma incremental_vacuum(N);
~~~

Treat a busy checkpoint as a deferred run. Measure file bytes after reclaim and store reclaimed bytes in the run record.

Public compact apply acquires the same file lock and database lease as maintenance. Normal maintenance passes its existing guard into compaction. Never acquire the guard twice in one run.

- [ ] **Step 4: Add public compact commands**

Implement:

~~~text
agent-gate audit compact --dry-run
agent-gate audit compact --apply
~~~

Normal `audit maintain --apply` invokes the same compact apply function only when `compact_after_maintenance` is true.

Run:

~~~sh
go test ./internal/auditmaintenance ./cmd/agent-gate -run Compact
make test
make check
~~~

Expected: all commands exit zero.

- [ ] **Step 5: Commit**

~~~sh
git add internal/auditmaintenance cmd/agent-gate
git commit -S -m "Add incremental audit database compaction" -m "Co-authored-by: Codex <noreply@openai.com>"
~~~

## Task 5: Schedule maintenance after readiness

**Ticket:** AGATE-42

**Depends on:** AGATE-41

**Files:** Use the AGATE-42 file set above.

- [ ] **Step 1: Write scheduler tests with a controlled clock**

~~~go
func TestMaintenanceSchedulerWaitsFullIntervalAfterReadiness(t *testing.T)
func TestDaemonStartupNeverCallsMaintenance(t *testing.T)
func TestOverdueRecordDoesNotTriggerStartupMaintenance(t *testing.T)
func TestMaintenanceReloadResetsFullInterval(t *testing.T)
func TestMaintenanceRestartResetsFullInterval(t *testing.T)
func TestMaintenanceRestartKeepsOverdueStatus(t *testing.T)
func TestMaintenanceReloadKeepsOverdueStatus(t *testing.T)
func TestMaintenanceFailureWaitsUntilNextInterval(t *testing.T)
func TestMaintenanceDeadlinePersistsOnlyAfterReadiness(t *testing.T)
func TestMaintenanceSchedulerStartsAfterServeAcceptLoop(t *testing.T)
~~~

Use an injected timer channel and runner. Do not sleep for real time.

Run:

~~~sh
go test ./internal/daemon -run Maintenance
~~~

Expected: FAIL because the scheduler does not exist.

- [ ] **Step 2: Add scheduler lifecycle**

Add a separate cancellation field. Do not reuse the update scheduler cancellation.

~~~go
type maintenanceRunner func(
    context.Context,
    string,
    config.AuditStoragePolicy,
    time.Time,
) (auditmaintenance.Result, error)

func (s *Server) StartMaintenanceScheduler(ctx context.Context)
func (s *Server) runMaintenanceScheduler(ctx context.Context)
~~~

The scheduler snapshots policy only when a timer fires. It calls the same apply engine as the command line.

Register schema version six for one scheduler next-attempt row. After readiness, calculate the first attempt and persist it from the scheduler goroutine. Do not make daemon readiness wait for that metadata write. A database write failure leaves the in-memory timer active and records an operational error. Never overwrite maintenance due time when resetting the attempt timer.

- [ ] **Step 3: Start only after gRPC can accept requests**

Wrap the listener with a one-time signal that closes when `Serve` enters its
accept loop. Run `Serve` in a goroutine, then wait for that signal or an early
serve error. Only the signal path reports readiness and starts the scheduler.

~~~go
readyListener := newReadinessListener(listener)
serveResult := serveAsync(grpcServer, readyListener)

select {
case <-readyListener.Serving():
    log.InfoContext(ctx, "daemon listening", "socket", socketPath)
    srv.StartMaintenanceScheduler(ctx)
case err := <-serveResult:
    return normalizeServeError(err)
}

return normalizeServeError(<-serveResult)
~~~

Starting the scheduler creates a full-interval timer and writes only its deadline
metadata after the accept loop is active. It must not inspect database integrity,
checkpoint, or call Preview. Readiness does not wait for that metadata write.

- [ ] **Step 4: Reset on reload**

After a valid runtime swap, cancel the old scheduler, start a new full interval, and replace the persisted next attempt asynchronously. Recalculate due status from the new policy without running maintenance. An invalid reload leaves policy and timing unchanged. Server Close cancels the scheduler.

Run:

~~~sh
go test ./internal/daemon -run "Maintenance|Reload"
go test -race ./internal/daemon -run Maintenance
make check
~~~

Expected: all commands exit zero.

- [ ] **Step 5: Commit**

~~~sh
git add internal/auditstorage internal/auditmaintenance internal/intake internal/daemon
git commit -S -m "Schedule audit maintenance after daemon readiness" -m "Co-authored-by: Codex <noreply@openai.com>"
~~~

## Task 6: Preflight full offline compaction

**Ticket:** AGATE-43

**Depends on:** AGATE-42

**Files:** Use the AGATE-43 file set above.

- [ ] **Step 1: Write full preflight tests**

Use real temporary files and a small command runner only for launchd or systemd.

~~~go
func TestFullCompactDryRunDoesNotStopServiceOrWriteDatabase(t *testing.T)
func TestFullCompactPreflightRequiresManagedServiceControl(t *testing.T)
func TestFullCompactPreflightRejectsInsufficientSpace(t *testing.T)
func TestFullCompactPreflightRejectsIntegrityFailure(t *testing.T)
func TestFullCompactPreflightReportsFailOpenWindow(t *testing.T)
func TestFullCompactPreflightRejectsSymlinkDatabasePath(t *testing.T)
~~~

Run:

~~~sh
go test ./internal/auditmaintenance ./internal/install ./cmd/agent-gate -run FullCompact
~~~

Expected: FAIL because no full mode exists.

- [ ] **Step 2: Add service status and control**

~~~go
type ServiceState struct {
    Platform   string
    Managed    bool
    Running    bool
    BinaryPath string
}

type ServiceController interface {
    Status(context.Context) (ServiceState, error)
    Stop(context.Context) error
    Start(context.Context) error
    WaitStopped(context.Context) error
    WaitReady(context.Context, string) error
}

func NewServiceController(options ServiceControlOptions) (ServiceController, error)
~~~

Use exact launchd label and systemd user unit already used by installation. Status must verify the configured service binary matches the running command.

- [ ] **Step 3: Measure free space and integrity**

Reject a database path whose final component is a symbolic link. Resolve the
parent directory to one canonical absolute path, require a regular database
file, and retain its device and inode for a second check immediately before
replacement. Use that canonical path for the database, sidecars, lock, copy,
and rollback files.

Use the database directory filesystem. Required free bytes equal database bytes plus write-ahead log bytes plus the larger of 64 MiB or ten percent.

~~~go
type FullCompactPlan struct {
    DatabasePath      string       `json:"database_path"`
    DatabaseSize      DatabaseSize `json:"database_size"`
    FreeBytes         uint64       `json:"free_bytes"`
    RequiredFreeBytes uint64       `json:"required_free_bytes"`
    Service           ServiceState `json:"service"`
    IntegrityOK       bool         `json:"integrity_ok"`
    HookImpact        string       `json:"hook_impact"`
}
~~~

Run `pragma integrity_check` on the source. This is an explicit command, not startup work.

- [ ] **Step 4: Expose full dry-run**

Implement:

~~~text
agent-gate audit compact --full --dry-run
~~~

Reject `--full` without exactly one of dry-run or apply. Print before confirmation:

~~~text
Hooks fail open while the managed daemon is stopped or gated for cutover verification.
~~~

Dry-run must never call Stop, acquire an apply lease, create a copy, or write metadata.

Run:

~~~sh
go test ./internal/auditmaintenance ./internal/install ./cmd/agent-gate -run FullCompact
make check
~~~

Expected: both commands exit zero.

- [ ] **Step 5: Commit**

~~~sh
git add internal/install internal/auditmaintenance cmd/agent-gate
git commit -S -m "Add full audit compaction preflight" -m "Co-authored-by: Codex <noreply@openai.com>"
~~~

## Task 7: Apply full compaction with rollback

**Ticket:** AGATE-44

**Depends on:** AGATE-43

**Files:** Use the AGATE-44 file set above.

- [ ] **Step 1: Write end-to-end success and failure tests**

~~~go
func TestFullCompactApplyReclaimsSpaceAndRestartsService(t *testing.T)
func TestFullCompactApplyEnablesIncrementalAutoVacuum(t *testing.T)
func TestFullCompactApplyLeavesSourceUnchangedBeforeReplacement(t *testing.T)
func TestFullCompactApplyRestoresDatabaseAfterCopyFailure(t *testing.T)
func TestFullCompactApplyRestoresDatabaseAfterIntegrityFailure(t *testing.T)
func TestFullCompactApplyRestoresDatabaseAfterGatedRestartFailure(t *testing.T)
func TestFullCompactApplyLeavesRecoveryPathsAfterRestoreFailure(t *testing.T)
func TestFullCompactApplyReleasesLeaseFromReplacementOrRestore(t *testing.T)
func TestFullCompactApplyReleasesLeaseAfterCancellation(t *testing.T)
func TestFullCompactApplyBlocksHooksUntilCutoverCommit(t *testing.T)
func TestFullCompactApplyRestoresOnlyBeforeCutoverCommit(t *testing.T)
func TestFullCompactApplyNeverRollsBackAcknowledgedReceipt(t *testing.T)
func TestFullCompactJournalPrecedesFirstRename(t *testing.T)
func TestFullCompactRecoversInterruptionAtEveryCutoverStep(t *testing.T)
func TestDaemonUnresolvedCutoverNeverCreatesEmptyDatabase(t *testing.T)
func TestDaemonCommittedCutoverUsesReplacement(t *testing.T)
func TestEveryAuditConstructorRejectsUnresolvedCutover(t *testing.T)
func TestPublicWriterCommandsRejectUnresolvedCutover(t *testing.T)
func TestFullCompactHoldsDaemonProcessLockThroughReplacement(t *testing.T)
func TestFullCompactTransfersProcessLockAfterJournalGateIsDurable(t *testing.T)
func TestFullCompactRecoveryReacquiresDaemonProcessLock(t *testing.T)
~~~

Use a real SQLite source. Use a test service process that owns and closes the database. Assert rows, integrity, file size, service state, and cleanup.

Run:

~~~sh
go test ./internal/auditmaintenance ./cmd/agent-gate -run FullCompactApply
~~~

Expected: FAIL because apply is not implemented.

- [ ] **Step 2: Hold exclusive maintenance and daemon control**

Acquire the maintenance file lock and database lease before stopping the
service. After the service stops, acquire the same daemon process lock that
`daemon.Run` uses. Failing to acquire it means another daemon won the race, so
abort before checkpoint or journal creation. Retain the daemon process lock
through copy creation, journal creation, replacement, and every recovery file
mutation. Retain the maintenance file lock through restart verification. Use a
unique run ID in every temporary and rollback name.

- [ ] **Step 3: Stop, checkpoint, and build the copy**

Perform this sequence:

1. Re-run preflight.
2. Stop the managed service.
3. Wait until the socket is unavailable, then acquire the daemon process lock.
4. Open the source, checkpoint and truncate the write-ahead log.
5. Set `pragma auto_vacuum = incremental` on that connection.
6. Run `VACUUM INTO` a unique working file in the same directory.
7. Close and reopen both databases.
8. Require the source mode to remain unchanged.
9. Require `pragma auto_vacuum = 2` on the working copy.
10. Run full integrity and foreign-key checks on the working copy.
11. Synchronize the working copy and its directory.

The `VACUUM INTO` test must prove the target receives incremental auto-vacuum without changing the source. Preserve source ownership and permissions on the verified working copy.

- [ ] **Step 4: Journal replacement before the first rename**

Recheck the canonical database device and inode. Before changing any database
path, create an owner-only cutover journal in the same directory. Record the run
ID, random token, canonical paths, source identity, copy identity, prior service
state, and phase. Synchronize the journal and directory before the first rename.

Use a write-ahead phase for every rename, sidecar move, and cleanup step. Update
and synchronize the journal before the filesystem mutation. Record completion
and synchronize it after the mutation. Recovery compares the journal with the
recorded device, inode, and content identities. It may resume only the adjacent
documented state. An unexpected identity stops recovery and preserves every
file.

Rename the original database to the rollback path. Rename the verified copy to
the original path. Move original sidecars beside the rollback database.
Synchronize the directory after every durable step.

Put journal parsing and access checks in the cycle-neutral `auditstorage`
package. Every database constructor checks it before `sql.Open`, migration, or
file creation. Every normal public command reaches the same guard. An unresolved
pre-commit journal rejects normal read-write and create access even when a crash
released the file lock. Only the full-compaction recovery path or gated daemon
may pass the matching run ID and token. A committed phase treats the replacement
as authoritative and permits normal access.

A phase before replacement installation reports recovery required and exits.
It never creates a database. A replacement-installed phase may start only in
the token-bound gate described below. Test every audit store constructor and
every public command that can create or write the database against an injected
interruption phase. No path may create a missing configured database, migrate a
file, or write a row while the journal remains unresolved.

- [ ] **Step 5: Start behind the journal gate**

After the replacement-installed phase and directory synchronization complete,
release the daemon process lock immediately before starting the managed service.
This is an explicit handoff from process exclusion to the durable journal gate.
Any process that acquires the daemon lock during the handoff must obey the same
journal token and cannot serve normally.

The cutover journal is also the gate. A daemon started in the
replacement-installed phase opens the replacement and enters the gRPC accept
loop, but it must not accept normal RPCs or start deferred work, update work, or
maintenance.
Normal RPCs return unavailable and write no receipt. The daemon writes a
token-bound ready acknowledgement only after the database opens and the accept
loop is active. Normal daemon startup without a journal never waits on this
gate.

While the gate remains closed, verify:

~~~text
expected binary path and build hash
token-bound gated readiness
database schema version
incremental auto-vacuum mode
integrity and foreign keys
one retained summary query
~~~

Commit the cutover by atomically changing and synchronizing the journal phase
to committed. The token-bound daemon observes that exact transition and opens
the gate. This is the point of no return because the daemon may then acknowledge
hook receipts. A restart with a committed journal also treats the replacement
as authoritative. Wait for the normal Status RPC. Record the successful full
compaction and release the owned lease from the replacement database with a
bounded non-cancelled cleanup context. Remove the rollback database and sidecars
only after normal readiness succeeds. Record cleanup completion, synchronize
the directory, then remove and synchronize the journal. Release the file lock
last.

- [ ] **Step 6: Recover every interruption and pre-commit failure**

Before cutover commit, every error after service stop enters one recovery
function:

1. Stop a partially restarted service.
2. Acquire or retain the daemon process lock before any file mutation.
3. Move the failed replacement aside with the run ID.
4. Restore the rollback database and sidecars atomically.
5. Synchronize the directory.
6. Record restoration in the journal and remove the acknowledgement.
7. Release the daemon process lock immediately before restoring the prior
   service state.
8. Verify the restored database and service.
9. Release the owned lease from the restored database with a bounded
   non-cancelled cleanup context.
10. Remove and synchronize the journal only after restored service verification.

If recovery cannot acquire the daemon process lock, it performs no filesystem
mutation. It retains the journal and every database path, then reports the
competing daemon as the recovery blocker.

After cutover commit, never restore the rollback database automatically. A
normal RPC may already have acknowledged a receipt in the replacement. If
normal readiness fails, stop automated cleanup, retain both databases, and
print exact recovery commands that preserve the replacement before any manual
rollback. If pre-commit restoration fails, retain every path and print exact
manual recovery commands, including owned lease cleanup. Never delete either
database on a failure path.

On command restart, read the journal before normal preflight. Exercise an
injected process interruption before and after every journal update, rename,
sidecar move, gate commit, and cleanup step. At each phase, invoke the daemon and
all public database-writing commands. Recovery must choose the verified source
or replacement without allowing another constructor to create or mutate the
database, accepting a request before commit, or rolling back after commit.

- [ ] **Step 7: Expose apply and verify no scheduler path**

Implement:

~~~text
agent-gate audit compact --full --apply
~~~

Require the user to type `compact audit.db` on an interactive terminal after the fail-open warning. Reject non-interactive apply. Search the daemon package and prove no code calls full compaction.

Run:

~~~sh
go test ./internal/auditmaintenance ./internal/install ./cmd/agent-gate -run FullCompact
make test
make lint
make check
~~~

Expected: all commands exit zero.

- [ ] **Step 8: Commit**

~~~sh
git add internal/auditstorage internal/auditmaintenance internal/install internal/daemon cmd/agent-gate
git commit -S -m "Apply full audit compaction with rollback" -m "Co-authored-by: Codex <noreply@openai.com>"
~~~

## Epic Acceptance

- [ ] Status and dry-run never write or trigger maintenance.
- [ ] Apply matches preview for one policy and clock.
- [ ] Age and size retention preserve every protected graph.
- [ ] Batches keep intake responsive and foreign keys valid.
- [ ] Automatic maintenance waits one full interval after readiness.
- [ ] Startup never runs maintenance.
- [ ] Restart and valid reload reset the full interval.
- [ ] Incremental compaction is bounded.
- [ ] Full compaction is explicit, offline, verified, and recoverable.
- [ ] Every ticket commit passes make check.
