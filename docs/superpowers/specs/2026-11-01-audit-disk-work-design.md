# Bound audit disk work across the storage lifecycle

Agent Gate must stop reopening completed history during ordinary processing. Idle operation must stop writing after admitted work and due maintenance finish. Active processing must retain the existing audit guarantees without making its cost depend on the amount of completed history. Memory use must remain within explicit budgets as retained history and pending backlog grow.

The user selected an approach that keeps SQLite, corrects replay scheduling, history readers, and log trimming, and makes performance regressions fail validation. The user also requires preventing excessive memory use. This project includes storage ownership, scheduling, queries, logging, and qualification. It does not authorize deployment or a reset of the live installation.

## Keep persistence under the daemon

The current runtime already shares one SQLite connection among intake, evaluations, and audit logging. Concurrent chats do not require separate connections. The regression comes from opening historical files repeatedly after the daemon already knows which receipt changed.

Keep that shared current writer. The daemon submits short database operations through it and runs command evaluation outside database transactions. Chat count must not determine connection count, worker allocation, or a queue for each chat. This design adds neither a chat quota nor a change to current enforcement or overload behavior.

Use one temporary older-file context for bounded recovery or finalization work while the current writer continues accepting events. Rotate or reload through the existing coordination boundary, and reuse the current connection when storage does not change. Do not build a general connection pool or a cache of historical stores.

One persistence owner can manage a current file and an older file. A second temporary connection serves a different file. It does not represent another chat. Current-file reads within the daemon use short pages that release the shared connection between operations. Any additional reader requires evidence that this arrangement cannot preserve hook latency.

## Preserve the existing contract

The [September storage specification](2026-09-08-audit-rotation-design.md) already requires reducing repeated CPU and disk work and comparing identical traffic with empty and retained storage. This design extends that contract to size-created segments and enforces its performance requirements through automated checks.

Preserve current configuration behavior, audit detail selection, hook responses, atomic completion within each database, cancellation, replay ownership, and age and size retention. Retention may still delete expired or oversized files that contain unfinished work. Keep the existing durability policy for writable databases. Preserve existing history during this change. Do not introduce a schema migration, history copy, permanent replay database, or automatic reset.

## Explain the regression

The live daemon reopened all retained databases on a one-second timer, after successful hot evaluations, and after deferred completions. SQLite initialized shared-memory files when these connections became the first users of closed databases. Size rotation increased the retained database count and multiplied this work.

The November 1 measurements used daemon PID 954, running commit `2c19ce0`. The collector sampled macOS process counters over four five-second intervals. A separate five-second filesystem trace classified writes by file type.

Measured evidence:

| Measurement | Result |
|---|---:|
| Process writes across the four intervals | 5.46 to 9.44 million bytes per second |
| Retained database segments during the process sample | 79 |
| Shared-memory bytes written during the separate trace | 49,758,208 |
| WAL bytes written during that trace | 4,177,920 |
| Database bytes written during that trace | 1,916,928 |
| Shared-memory share of captured write bytes | 89% |

These measurements establish the current write source. They do not establish a controlled idle rate or the performance of the proposed replacement.

Source review also found that exact-segment history queries open every retained database before filtering. Supervisor log maintenance copies almost the full retained log after overflow and leaves little space for subsequent additions. The current supervisor log remained below its overflow threshold and did not explain the captured writes.

The existing qualification benchmark covers active scenarios with empty history or seven time windows. It lacks idle operation and a large count of size-created segments. Its summary prints performance verdicts without making a failed performance verdict fail the command. The [earlier issue verification](https://github.com/agoodkind/agent-gate/issues/159#issuecomment-5623483731) followed a reset to a small store and did not establish behavior with the current retained segment count.

## Approaches

The approaches have different ownership and consistency costs.

Approach assessment:

| Approach | Tradeoff | Decision |
|---|---|---|
| Increase the polling interval or change a SQLite connection option. | This retains repeated history discovery. A connection option alone can also prevent later writers from using shared memory correctly. | Reject as the complete correction. |
| Cache every retained connection. | This uses resources and delays deletion while connections retain file locks. It preserves queries over completed history after each event. | Reject as the complete correction. |
| Keep the shared writer, dispatch known pending receipt IDs, and recover older files in batches. | This reuses durable state and existing daemon ownership while preserving local atomic completion and whole-file retention. | Use this approach. |
| Add a permanent work database or replace SQLite. | This adds publication and recovery contracts between work and history stores. The existing completion transaction cannot atomically update a separate database. | Exclude from this change. |

The user selected the SQLite approach and rejected a design that might limit chats or add unnecessary resource-management machinery. The simpler ownership model and log headroom below are agent-selected design details for review.

## Schedule pending work directly

Keep one scheduler and the existing bounded identifier dispatch. Durable pending receipts remain the work queue and recovery source. Do not maintain another in-memory registry for every pending file or receipt.

Startup discovers retained files once in bounded background pages after readiness. It identifies receipts in the existing `pending` deferred state, unexpired claims, and scheduled retries. Stored lease expiration controls recovery of another process's claim. Discovery must not block synchronous hook capacity or open every retained database at once.

Preserve current replay eligibility. A receipt without a committed hot evaluation does not become eligible unless it already has committed deferred-pending state. Preserve that receipt and its existing query results. After admitted runtime users finish, a receipt in state `none` or `complete` does not prevent file finalization. This change must not synthesize evaluations for interrupted hot requests.

After a hot completion commits pending work, dispatch its known segment and receipt IDs directly. The daemon already has those values. Do not request a history scan to rediscover them. A full dispatch batch leaves the receipt in SQLite and records that work remains undispatched. Request cancellation must not discard that indication after the pending transaction commits.

Completion releases dispatch capacity. Refill a bounded batch from the known file only when pending work remains. Retry and lease deadlines determine the next timer. Older unfinished files can require further recovery passes when those deadlines arrive; recovery is not limited to exactly one startup pass. Advance past failing work fairly. A timer must not search completed files repeatedly, and a new event must not restart historical discovery.

Keep only the current dispatch batch, its refill indication, a recovery cursor, and the deadlines needed for actual pending work. Discover large backlogs incrementally. If a process stops after committing work but before dispatch, startup discovery must recover that work.

## Bound memory independently of history

Keep receipt payloads and undispatched backlog in SQLite. Scheduling entries contain only identifiers, cursors, deadlines, and accounting information. Load payloads only for active workers and release references after processing. Do not preload pending records or maintain an in-memory entry for every retained file or receipt.

Use the existing byte-bounded dispatch as the starting point. Account for identifiers, paths, current payloads, and query buffers in both bytes and entries. Reuse existing performance controls where they apply. Do not add a general resource-budget subsystem or a new control without an allocation that requires it.

Reserve capacity before allocating or reading a batch. When capacity fills, keep accepted work durable in SQLite and stop dispatching additional work until capacity becomes available. Do not discard accepted receipts, create another overflow queue, or allocate a goroutine for each waiting receipt.

A bounded scheduler may retain a cursor and one flag indicating that undispatched durable work remains. Capacity release resumes bounded discovery. Preserve notification correctness and retry deadlines without loading every pending record. Overflow must not restore an all-history scan after every hook event or repeatedly open completed databases.

Stream directory discovery and SQL pages. Limit aggregate query buffering across files; one bounded page per file still grows with file count. Stream unlimited query output while preserving ordering and result semantics. Explicit queries may perform the reads necessary for their results, but routine hook processing must not pay for those history reads.

The shared current writer and single older-file context make storage resources independent of chat count. Account for their SQLite page caches and prepared statements, including temporary overlap during rotation. Release query resources on cancellation. Do not keep every historical database mapped or open to avoid disk writes.

Before implementation, record the current batch's byte bound, maximum active payloads, SQLite cache costs, and aggregate working-memory calculation in the implementation plan. Select any necessary additional values from existing limits and measured workloads. Do not claim a fixed whole-process memory ceiling from queue limits or Go heap settings alone.

Qualification measures peak and settled Go heap, process resident memory, and macOS physical footprint, including SQLite's native allocations and mapped pages. With fixed request sizes, concurrency, and configured budgets, memory must plateau when those budgets fill. Further backlog or completed history must not create continuing memory growth. Repeated bursts, recovery, queries, rotation, and reload must release resources without an artificial garbage collection step that hides normal retention.

## Own each segment until its writes finish

The current segment accepts new receipts. A retired segment accepts only work already assigned to it. A completed retired segment cannot receive another ordinary write. An expired segment stops accepting work and awaits deletion after its users finish.

The daemon owns the current writer and the temporary older-file context. Process a batch in that context rather than creating a writer, logger, and processor for each receipt. Close it before examining another older file. A genuine retry may reopen an unfinished file. Completed history must not consume a permanent connection.

Rotation stops new assignments through the existing snapshot boundary. It then finishes or cancels admitted users under the existing lifecycle contract. A retired segment becomes complete only after its admitted writers and queued audit entries finish and its durable deferred table does not contain a receipt in state `pending`. This includes claims and retries scheduled for the future. An empty dispatch queue does not prove completion.

All writer entry points, including setup and audit-only writes, must honor this ownership. A later writer must not silently reopen completed retired history.

## Finish completed files once

After a retired segment becomes complete, obtain exclusive access through cross-process bucket coordination. Finish its SQLite checkpoint and convert that file from WAL mode to `journal_mode=DELETE`. SQLite can then read the completed file without creating a WAL or shared-memory file. Keep current and unfinished segments in WAL mode.

SQLite documents [read-only WAL behavior and journal conversion](https://www.sqlite.org/wal.html#read_only_databases). This operation changes a completed file's journaling state without copying the database or changing the audit schema.

Release competing users before conversion. An external reader can delay it. A failed conversion preserves the file under its actual journal mode and schedules a bounded retry. It must not create a busy loop, cancel synchronous requests, or delete sidecars manually.

The database header records a completed journal conversion. Startup resumes interruption using the actual header and pending receipt state. Never infer completion from a filename, file age, missing sidecars, or a failed lock attempt. Recover the database before deciding that it does not contain unfinished work.

Count the final checkpoint and journal conversion in qualification costs. They occur once per completed segment. Do not add a recurring checkpoint timer or row compaction.

## Select history before opening databases

Separate file discovery from SQLite opening. Apply an exact segment filter before opening databases. Apply a time filter before opening a file only when metadata proves that the file cannot contain a match. A filename's time window does not bound deferred evaluation completion times. Existing files do not provide those completion bounds; retain conservative file selection for such queries. Do not add per-event metadata writes to improve file selection. Use bounded pages and preserve global ordering when a query needs several segments. Cost totals must include every matching record.

Read completed files with ordinary read-only connections. These reads must not initialize schema, change journal mode, create sidecars, or acquire writers. Do not use `immutable=1` on a file that could change.

Current and unfinished segments retain normal SQLite concurrency. Readers need a defined connection lifetime with writer ownership. Do not create a read-only shared-memory node that a later writer in the same process would inherit. Any `readonly_shm` optimization needs separate qualification with the bundled SQLite version.

Offline queries and interrupted WAL recovery must return correct committed data. Test missing sidecars, readable leftover sidecars, and uncheckpointed WAL records. Recovery must visit only selected files. Report unreadable files directly rather than returning incomplete history as complete results.

Status continues to read metadata and file sizes. Reading status or counters must not write persistent metrics.

## Separate maintenance triggers

Replay, rotation, pruning, and log maintenance may share a coordinator, but each needs its own due condition. Completing a receipt must not trigger log processing or completed-history discovery. Rotation follows its absolute time boundary or current-file size threshold. Expiration follows the next applicable boundary. Failed deletion uses bounded retries.

Catalog writes require changed values. Under-limit logs must not cause rewrites. Failure reporting must preserve visible errors without generating an unbounded write loop.

## Give supervisor logs space for new output

Keep the 1 MiB supervisor log limit and preserve its inode for the supervisor's open descriptor. After overflow, retain at most the newest 512 KiB of complete lines. This leaves space for new output before another copy.

This proposal reduces recent supervisor output immediately after trimming. It does not change diagnostic archives or fail-open evidence. Design approval includes this explicit retention choice.

Count copied bytes across repeated small additions. After a successful trim, another copy requires approximately another 512 KiB of output. Account for line boundaries, oversized lines, concurrent appends, and failures. Do not raise the file limit to conceal amplification.

## Make regressions fail validation

Add tests through public daemon, query, and logging boundaries with real SQLite files. Include completed history with 0, 7, 80, and 160 segments. Use temporary size policies to create segments without copying production history.

Required outcomes:

| Workload | Required result |
|---|---|
| Idle operation after startup and pending work finish | The daemon stops reopening completed databases and rewriting audit or log files. Due rotation, expiration, and actual error handling remain explicit work. |
| Identical traffic with increasing completed history | Completed history does not multiply database opens, transactions, or replay discovery per event. |
| Backlog larger than scheduling and dispatch budgets | Accepted receipts remain in SQLite, memory stays within the accounted budgets, and processing resumes fairly after capacity becomes available. |
| Repeated bursts, queries, rotation, and reload | Peak memory stays within the qualified bound and settled memory does not keep growing after equivalent cycles. |
| Increasing concurrent chat count under a fixed event workload | Chats share the current writer without a connection or backlog queue for each chat. The change does not introduce a chat quota. |
| Exact-segment and bounded history queries | Queries open only necessary files and do not recreate sidecars for completed files. |
| Startup backlog and expired claims | Recovery processes retained eligible receipts with the existing ownership and atomic completion guarantees. |
| Rotation, size pruning, reload, sleep, cancellation, and shutdown | Work uses its original file until completion or permitted expiration. Users release access before deletion. |
| Interrupted checkpoint or journal conversion | Restart preserves committed records, resumes pending work, and does not mistake incomplete files for completed history. |
| Full supervisor logs with small subsequent additions | Retention respects the size limit and copies bytes in proportion to new output. |

Keep deterministic tests in normal `make test` and `make check` execution. Add a macOS CI qualification job that measures actual process writes and CPU through the real daemon entry point. Its command must fail when acceptance fails. Printing `FAIL` in a Markdown report does not satisfy this requirement.

Use independent processes and identical input traces for baseline and candidate. Record baseline variation before evaluating the candidate. Include idle periods after bursts, retained backlog, many size-created segments, exact queries, rotation, and both supported detail profiles. Keep fixture creation, reports, and measurement helpers outside the measured daemon process.

Measure accepted and completed receipts, hook responses, failures, CPU per event, physical writes per event, transaction counts, aggregate buffered bytes, open connections, peak and settled memory, and hook and completion latency. Include shutdown and final checkpoints in the accounting. Let startup and admitted work settle before measuring continuing idle writes.

For active workloads, CPU and physical writes per event must improve beyond baseline variation. Hook latency, error rates, completed-work throughput, and peak and settled process memory must not regress beyond that variation. Additional completed segments must not add per-event replay work or unbounded retained memory. Preserve the existing four persistence stages for normal deferred receipts and verify rollback through observable records.

Both physical measurements and deterministic ownership tests must pass. Run full repository checks and an independent concurrency review before implementation can qualify for merge. Live deployment requires separate authorization and verification.

## Delegate implementation details

The builder may choose internal types, fair recovery order, batch sizes within existing configured limits, any measured additional byte bound that an actual allocation needs, and the smallest meaningful fixtures. The builder must record aggregate memory accounting in the implementation plan, preserve public behavior, and prove connection ordering with the bundled SQLite version.

Changes to audit detail, persistence stages, durability, history preservation, file limits, or retention beyond the supervisor headroom proposal require an explicit decision. A failed qualification requires a correction or revised design. Do not relax thresholds, skip cases, lengthen timeouts, or reset storage to hide a failure.
