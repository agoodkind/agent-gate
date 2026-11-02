# Bound audit disk work and memory

Agent Gate must process incoming events without rediscovering completed history. SQLite remains the durable record of accepted work. The daemon owns active storage and background jobs. Chat count does not create a database connection, worker, or backlog queue for each chat.

The user requires a comprehensive disk correction, bounded memory, correct incoming-data reconciliation, and verification of interactions before implementation. The design preserves SQLite, existing history, audit detail selection, local atomic completion, and age and size retention. It does not authorize live deployment, reset, a database migration, or replacing SQLite.

## Establish the actual failure boundaries

The [September storage specification](2026-09-08-audit-rotation-design.md) already requires CPU and disk improvements under identical traffic with retained history. This design makes those requirements executable and includes memory ownership.

The November 1 measurements used daemon PID 954 at commit `2c19ce0`. Four five-second process samples measured writes. A separate five-second filesystem trace classified them.

Measured evidence:

| Measurement | Result |
|---|---:|
| Process write rate | 5.46 to 9.44 million bytes per second |
| Retained database segments during the process sample | 79 |
| Shared-memory writes in the separate trace | 49,758,208 bytes |
| WAL writes in that trace | 4,177,920 bytes |
| Database writes in that trace | 1,916,928 bytes |
| Shared-memory share of traced write bytes | 89% |

These observations identify the current write source. They do not establish a controlled idle baseline or qualify a replacement.

Source inspection identified additional cases that a connection-limit proposal does not resolve.

Design counterexamples:

| Case | Consequence |
|---|---|
| A refill clears a Boolean after a producer sets it. | Committed work can lose its only notification. |
| A receipt becomes due below a permanently advancing cursor. | Recovery can skip that receipt forever. |
| Two workers retain different files during inference and claim renewal. | One mutable historical context cannot safely serve both jobs. |
| An old snapshot closes a handle borrowed by its replacement. | The new snapshot can lose its database. |
| A new file starts receipt numbering at 1 after an old file assigned receipt 500. | The current temporal cache can reject a newer prompt as older. |
| Cache defaults permit 4,096 values of up to 1 MiB. | Retained values alone can total 4 GiB. |
| RPC admission occurs after decoding, and environment resolution has a separate parsing RPC. | An evaluation semaphore does not bound incoming decoded payloads. |
| A validator continues after its synchronous timeout. | Its input, output, and process can outlive the RPC admission slot. |
| A history query buffers a page and connection for every file. | Small individual pages do not establish a fixed aggregate memory bound. |

The cache calculation is a permitted capacity, not measured usage. The temporal-order defect and the other counterexamples follow from inspected source; they still require production-path regression tests.

The [earlier verification](https://github.com/agoodkind/agent-gate/issues/159#issuecomment-5623483731) followed a reset to a small store. Existing qualification lacks idle operation and many size-created segments, and its summary can print a failed performance verdict without failing the command.

## Approaches

The approaches differ in the state they retain and the contracts they change.

Approach assessment:

| Approach | Assessment |
|---|---|
| Poll less often or change one SQLite option. | This preserves repeated discovery and does not fix notification or ownership errors. |
| Keep every historical connection open. | This retains native memory and file claims in proportion to history. |
| Require exactly two daemon connections. | This unnecessarily serializes configured workers that process different files. |
| Keep one storage owner, durable pending rows, and owners only for active files. | This preserves concurrency and local transactions with an explicit bound derived from active workers. Use this approach. |
| Add a permanent replay database or replace SQLite. | This introduces cross-database publication and recovery contracts. Exclude it from this change. |

The user selected retaining SQLite and rejected chat quotas and unnecessary resource-management machinery. Connection ownership, scheduling, and the numerical limits below are agent-selected proposals for design approval.

## Separate the identities and lifetimes

A durable receipt identity remains the storage family, physical file identity, and local receipt ID. A receipt number alone cannot identify or order records across files.

Temporal prompt and response updates use a process-local order consisting of a current-file assignment generation and the receipt ID. Allocate a new generation under storage-transition coordination before publishing a different current-file assignment. An unchanged-file configuration reload preserves the generation. Selecting a previously used path again still requires a new assignment generation. Old requests retain their original order.

Compare generations before receipt IDs. Do not order on the wall clock or lexical filenames. The temporal cache resets on process restart. This order does not require a new durable column. Historical deferred replay must not promote old observations using the current generation.

Keep three lifetimes separate:

- A configuration snapshot retains immutable configuration and a reference to its storage owner.
- A storage owner controls one SQLite handle and its file claim.
- An active job retains its original owner, configuration, claim, and observation order until all of its work and cleanup finish.

Closing a snapshot releases its reference. It does not close a handle that another snapshot or active job still uses.

## Own active storage and workers

Keep the existing shared current-file connection for intake, evaluations, and audit rows. Network inference, external programs, and waits do not run inside its SQL transaction or storage-owner mutex.

H is the existing configured hot-event concurrency, with default max(4, 4 times GOMAXPROCS). W is the configured deferred-worker count, currently 1 by default. Use one daemon-owned set of W workers across snapshots. Configuration changes must not temporarily create two active worker sets. Reducing W stops new dispatch until existing jobs finish or complete the cancellation required by the configuration change.

Jobs in the same file share one serialized owner. Distinct historical files used by active jobs need at most W owners. Recovery and finalization probes consume an existing worker slot. Queued identifiers do not open a database or retain its handle.

Storage bounds:

| Resource | Bound |
|---|---:|
| Current writer | 1 connection |
| Historical owners for active work and probes | At most W connections |
| Preparation or draining during a serialized transition | At most 1 additional connection |
| Normal daemon database total | At most W + 1 connections |
| Daemon database total during a transition | At most W + 2 connections |
| Active deferred receipt graphs | At most W |
| Worker sets | 1 |

W defaults to the existing configured value, currently 1. These are daemon bounds. History commands currently run in separate CLI processes and need their own query bounds.

A worker releases its owner only after inference, renewal shutdown, completion or retry cleanup, and payload-reference release. Cancellation alone does not prove that the worker stopped using its data.

Historical audit normalization must reuse the existing normalization behavior without constructing another logger queue and processor for every receipt. Current and retiring asynchronous audit queues may overlap only during one serialized transition.

## Dispatch committed work directly

After hot completion successfully commits deferred-pending state, submit the known file and receipt IDs directly to the bounded dispatch set. This action must not open history, acquire lifecycle-transition locks, wait for a worker, or depend on the request remaining uncanceled.

A full dispatch set leaves the receipt in SQLite. It increments a generation counter for required discovery and signals the existing coalescing wake channel. The channel wakes the scheduler; the counter preserves the requirement to inspect durable work.

Keep these counters and recovery progress on the daemon rather than a replaceable snapshot. Storage-family replacement changes an epoch and invalidates old dispatch entries and cursors.

A refill captures a generation before reading. It acknowledges that generation only if the counter remains unchanged when the pass finishes. Concurrent changes require another pass without resetting the pass currently in progress.

An exhaustive synchronization model examined 210 interleavings of two commits and notifications with one refill. A clearable Boolean lost a notification in 24 schedules. A captured-generation acknowledgment did not lose one in that model. This checks the synchronization rule, not production implementation or performance.

Preserve current replay eligibility. Only committed deferred-pending state becomes eligible. An intake receipt without a completed hot evaluation does not automatically become replayable. Do not synthesize evaluations for those records.

## Recover with finite, fair passes

Startup begins background recovery after transport readiness. New events do not restart historical discovery.

A recovery pass captures its eligibility time and streams directory entries in bounded batches. For one unfinished file, capture its maximum receipt ID in a short read transaction. Use the existing deferred_due_idx index for pending metadata: first page NULL retry times by receipt ID up to that maximum, then page nonnull retry times by their complete time and receipt-ID tuple through the captured eligibility time. Return at most 100 raw index entries without filtering claim eligibility in SQL. Apply any additional receipt-ID exclusion after reading the page, and advance past every returned entry. Include state, retry time, claim owner, and claim expiration without payloads. Do not sort an unbounded pending result or scan all completed rows to produce a small page.

Eligibility uses the captured pass time. A failed attempt rescheduled after that time cannot run again in the same pass. An indexed lookup of the first pending retry after the pass time supplies the next future wake time without paging the whole future population. Claim-expiration deadlines also come from metadata pages already visited. Newly committed or moved records that the pass misses require another generation pass. A backward wall-clock change invalidates the eligibility fence and starts a new bounded clock generation. An isolated probe on the bundled SQLite 3.53.4 verified that both keyset query forms select deferred_due_idx without a temporary sort on the current schema. Representative-data cost and concurrent cursor correctness remain acceptance requirements.

Advance past ineligible records and admitted attempts. If dispatch lacks capacity, retain the position before the rejected receipt and stop reading until capacity returns. A failed attempt must not reset the receipt cursor. A file error reports a bounded diagnostic and advances to the next file.

Use the existing retry and claim conditions to derive the earliest possible eligibility time. Retain the earliest observed deadline as a scalar. Exclude receipts already queued or owned by a live local job from that deadline calculation; scheduling a deadline of now for queued work would create a rescan loop. A pass that excludes locally owned work records that fact in its bounded dispatch entry. Cleanup publishes any required dirty-generation change before removing that ownership indication, under the same synchronization used by pass acknowledgment. Current-file overflow marks current-file refill; genuine historical work marks historical recovery. Ordinary current-file completion only releases capacity and wakes existing work, without scheduling another history scan. ClaimDeferred remains the final authority for ownership and eligibility.

After the pass finishes, reset receipt cursors before a due-time revisit. This catches lower receipt IDs that become pending late or become eligible after a future retry or lease expiration.

When current work and historical recovery both need capacity, alternate worker admission opportunities. Recovery probes receive a turn even before a historical receipt has been discovered; otherwise continuous current work could prevent discovery forever. At most one probe advances the shared recovery cursor. A probe may become one active historical job without inserting another item into a full dispatch queue. Active-job deduplication and the W worker bound still apply. A direct producer cannot consume a reserved recovery turn, including when dispatch capacity is one. Leave its work durable for current-file refill instead.

A finite pass eventually examines its captured scope. Retry timestamps establish when work may run, not a fixed completion deadline independent of backlog. Recovery delay can grow with unfinished work. Retention may still delete unfinished files under the existing policy.

Fixed memory, arbitrarily many pending files, and the absence of a durable replay index require some rediscovery. Permit bounded directory and header inspection during recovery. Do not repeatedly open completed files through SQLite or inspect them after every event.

## Coordinate transitions without invalidating active jobs

Ordinary rotation stops assigning new receipts to the old file and publishes a prepared current owner. Existing jobs retain their original file until they finish. Rotation does not rebind a running job to another store.

Use the existing monotonic segment sequence to allocate a fresh physical file when a time or size boundary requires a new current assignment. Do not reactivate a completed physical file after a clock adjustment. Preserve existing elapsed-time retention rules.

An unchanged-storage reload reuses the current owner. Preparation must not change shared inference or validator settings that admitted old jobs still use. Capture configuration-dependent options per job or cancel and join the affected jobs before changing those settings. Retire old configuration work through explicit cancellation and cleanup, then reschedule its durable pending records under the replacement configuration. Scheduler generations survive this operation.

Pruning, storage-family replacement, and shutdown prevent new jobs for affected owners before cancellation. Join renewal goroutines, complete bounded retry cleanup, drain or retire queued audit work, close SQLite, then release file claims. A failed cleanup remains visible and does not justify an unbounded retry loop.

Lock order and waiting rules:

- Transitions acquire lifecycle coordination before snapshot publication.
- Direct dispatch uses only short dispatch synchronization.
- Owner synchronization changes references and admission state, then releases before SQL, inference, joins, or queue draining.
- Catalog coordination precedes bucket file locks.
- Release catalog coordination before waiting for workers or external readers.
- Do not hold the dispatch mutex while joining a worker that must call finishDispatch.
- Keep the bucket claim until SQLite closes.
- Close shared borrowers before acquiring exclusive finalization or deletion access. Do not upgrade their locks in place.
- An operation that already owns an exclusive file claim uses an internal SQLite opener that does not recursively acquire catalog and bucket locks.

## Finish completed files once

A retired file can finish only after admitted writers and queued audit entries drain and its durable deferred table does not contain a pending receipt. Future retries and live claims still count as pending. Queue emptiness or an empty eligible page does not establish completion.

Under exclusive file access, finish the checkpoint and convert the completed file from WAL to journal_mode=DELETE. Current and unfinished files retain WAL and synchronous=NORMAL. SQLite documents this [read-only journal conversion](https://www.sqlite.org/wal.html#read_only_databases).

Published unfinished files must always use WAL. Successful conversion becomes durable completion evidence in the file header. A restart with an incomplete conversion must recover and check durable pending state before attempting conversion again. Do not infer completion from age, a missing sidecar, or a failed lock.

Closed completed files do not retain a connection. Their explicit read-only queries must not recreate WAL or shared-memory files. Do not use immutable=1 on writable files or apply readonly_shm as a universal correction.

A reader can delay conversion or deletion. Report the actual condition and retry with bounded delay. Do not manually remove WAL files, copy history, add row compaction, or schedule recurring checkpoints. Include the final checkpoint and conversion in measured write costs.

A constant-cost clock and metadata check may remain for sleep, wall-clock changes, current-file size, and log thresholds. Full history discovery requires startup recovery, pending retry recovery, a real storage transition, or pressure that requires pruning. Idle ticks must not open historical SQLite files.

## Bound incoming and detached data

Apply one daemon-owned admission controller before application receive and decoding. It covers both ResolveHookEnvironment and EvaluateHook, as well as other payload-bearing daemon methods. Reuse the existing H concurrency setting rather than adding a chat limit.

Do not wait for capacity inside the server method wrapper. If it cannot acquire a slot immediately, return a dedicated structured busy reason before decoding or application execution. Its metadata supplies the daemon's resolved queue-wait allowance. The client fixes one admission deadline from the first busy response and the original RPC deadline; it does not read enforcement configuration or extend that deadline on later errors. Retry only that reason. Ordinary ResourceExhausted, send errors, and ambiguous completion are not retry signals. The original 25 ms default queue allowance becomes a client-side admission-retry budget, not an application queue of received request bodies.

In the pinned gRPC version, a wrapper around copied MethodDesc handlers can acquire before dec invokes RecvMsg. Generated files remain unchanged. stats.TagRPC creates a mutable lease record in the context before processRPC begins. The method wrapper updates that record rather than adding a value only to a child context that stats.End cannot inspect. A canceled request does not decode after failed admission. Once the wrapper owns a slot, cancellation does not release it early. Service methods recognize the owned lease instead of acquiring the same slot twice. Direct in-process calls use a scoped service-entry lease.

Keep the lease until actual RPC completion, including SendMsg execution. Release it from stats.End rather than method return or ctx.Done. Cancellation can precede actual buffer release or handler exit.

Make the existing 4 MiB message ceiling explicit. Bound managed-hook stdin before reading the entire input. Reject oversize input with a clear transport failure; never truncate a command or payload. Preserve the visible fail-open result for pre-acceptance overload. Do not retry EvaluateHook after an ambiguous send or response failure because its append may already have committed.

Share immutable wire input within an event. Allocate a separate normalized buffer only when normalization changes it. Functions that retain data beyond the event must take explicit ownership. Remove redundant full-payload copies only after verifying that no consumer mutates a shared buffer.

Detached validators need their own ownership because they can outlive an RPC. Admit a logical external job before allocating its serialized request or forking. Retain its reservation until subprocess completion, retries, result delivery, and cleanup finish. Use a daemon-owned cancellation context with explicit correlation facts rather than retaining the entire RPC context through WithoutCancel. Retain file identity for cancellation without borrowing its SQLite handle after the deferred worker finishes. Any later SQL work must submit a fenced operation rather than retain that connection through a background callback.

Apply the existing background timeout to the complete logical job, including expansion and retries. Per-attempt deadlines cannot extend that total deadline. Check it before each expansion and retry, and return the configured error outcome once after exhaustion rather than iterating through remaining targets. Expand commands incrementally. Bound collected stdout and streamed transcript bytes locally; token requests do not establish a receive-byte bound. An overflow returns an explicit resource error through the configured on_error policy without truncated content or a cached success.

Cancel and join owned external jobs during shutdown and relevant configuration cuts. Terminate only process groups created for those validators. Use bounded pipe cleanup and WaitDelay for descendants that retain output descriptors. Keep the reservation charged until the job actually stops; canceling a context alone must not admit replacement retained data.

Proposed default buffer limits:

| Component | Proposed default |
|---|---:|
| Encoded RPC message or individual external input/output | 4 MiB |
| Admitted payload-bearing RPCs | H; rejected RPCs do not wait in the handler |
| Hot-cache aggregate charge, including keys and values | 64 MiB |
| Outstanding encoded RPC response buffers | 16 MiB |
| Retained external-job input, output, and transcript buffers | 64 MiB |
| External logical jobs | H, independently counted from RPCs |
| Total external-job lifetime | Existing 30-second background setting |
| Pipe cleanup allowance after cancellation | 1 second |
| Deferred identifier charge | Existing 1 MiB |
| Async audit serialized queue | Existing 16 MiB |

All new values require explicit shipped performance configuration and validation. These numbers are proposed limits, not measured resident memory or a claim of performance improvement.

Existing cache entry count, per-value size, and worker controls still apply. Charge before allocating. Account separately for fixed channel storage, map metadata, active decoded graphs, native SQLite caches, retiring queues, and transport structures. SQLite's roughly 2,000 KiB cache target per connection is not a hard whole-process bound.

The daemon's data-memory contract is independent of stored history and undispatched backlog. It bounds retained application buffers and admitted work. gRPC can buffer HTTP/2 DATA before the method wrapper runs; this admission gate does not establish a hard aggregate transport-input limit. Open connections and rejected requests awaiting transport cleanup consume additional memory. Do not claim constant whole-process memory for unlimited simultaneously connected clients or a cancellation flood. Qualification measures the complete process with stated connection counts, payloads, and cancellation bursts, including this overhead. A hard bound across arbitrary transport clients would require connection or transport-buffer limits beyond this design. An operator-selected validator program controls its own internal allocations. Fixed memory and unchanged response latency cannot accompany unlimited instantaneous event input. This design does not assign quotas to chats.

## Retain response credits through transport ownership

SendMsg can enqueue a referenced buffer before the HTTP/2 transport frees it. stats.End therefore does not establish that all encoded response memory disappeared.

Reserve response bytes before protobuf marshaling. Use the standard protobuf encoding and a reference-counted gRPC buffer backed by a one-shot BufferPool. Return its credit only when the pool receives the buffer after the last transport reference releases. Account for capacity rather than payload length, including the minimum allocation needed for reference-counted pooling. Do not attempt to implement gRPC's sealed Buffer interface.

Keep the ordinary decoder. This adapter changes buffer ownership, not the wire format. Marshaling or admission failures must release their reservations exactly once. A stalled reader must not permit encoded response storage to exceed the byte budget.

KVList must calculate response size before cloning values. Metadata-only lists must not copy values. Reject a result that cannot fit the existing client message ceiling before materializing it; do not return silent partial success. Preserve successful requests that fit. Response failures after a handler runs follow the existing visible transport-failure behavior, not an automatic replay.

## Preserve temporal facts under cache pressure

Add an aggregate cache byte bound using the existing eviction policy. Include keys and values in accounting. A byte cap must not leave an older temporal value apparently current after rejecting a newer value.

Use one cache-atomic boundary for temporal registration, comparison, publication, and eviction observation. Remove the opposite temporal-mutex/cache-mutex acquisition order. An eviction observer must not call cache methods recursively.

Maintain two scalar orders: the highest registered hot observation order and a discarded-before order. Register a hot event before it can publish temporal observations.

Only removing a temporal entry, including an unavailable marker, raises discarded-before to the highest registered order. Ordinary writes and evictions that do not remove temporal state do not change this floor.

For a present key, accept only an order newer than its retained watermark. For a missing key, accept only an order newer than discarded-before. A successful atomic publication may then evict other entries and raise the floor for future publications. It does not invalidate the record just published.

A newer oversized temporal value replaces old content with a compact unavailable marker carrying its order. An absent key that cannot publish remains explicitly unavailable. A delayed older operation cannot recreate stale state after eviction.

This uses constant scalar fencing rather than an unbounded tombstone for every chat. Its availability tradeoff is explicit: temporal eviction can suppress a first publication for an unrelated missing key if that event registered before eviction. Existing keys retain their normal ordering. Existing availability fields must expose the missing context.

## Query with bounded working memory

History queries remain available offline through the CLI. They do not become daemon RPCs. Filter an exact file before opening databases. Use time-based exclusion only when metadata proves that a file cannot contain matching rows; a file's creation window does not bound deferred completion time.

A multi-file query acquires one shared family query claim under short catalog coordination. Capture the policy, query time, and highest published segment sequence. New files always consume a new sequence. This freezes the selected file family without a live connection or descriptor for every file. It does not freeze rows in writable files. Rotation and current-file writes continue. Destructive deletion and journal finalization need exclusive access to that family claim; a long query may delay those operations under the existing reader-retention contract.

Preserve the current live-query contract. Sequential per-file queries do not provide one atomic cross-file database snapshot. Concurrent commits may appear or be omitted according to the pages that observe them. Cost totals and displayed records can observe different commits. Do not claim snapshot consistency or use query start time as an invented visibility fence for older timestamps.

Exact-file queries use their existing file claim. Open one selected database at a time. The lock order is catalog coordination, family query claim when applicable, then bucket claim. Use nonblocking claim attempts under catalog coordination and release it before waiting or retrying. Keep source files protected until the query finishes, and release claims on cancellation or error.

Select a fixed-size global page by scanning selected files sequentially and retaining only the best matching summary keys after the global ordering cursor. Preserve the existing time, sequence or stable ID, and bucket tie-breakers. Fetch full records only for the selected keys, and revalidate their ordering keys before output. If a concurrent update changed a selected key, omit that stale selection and refill without emitting it out of order. Reset file iteration for the next global page. Offset consumes these pages without retaining skipped rows. Tests must verify live-query ordering without asserting cross-file snapshot completeness.

Unlimited output streams one global page at a time. Large detail fields must not cause all results to accumulate in memory. Cost aggregation streams scalar totals independently of the displayed limit. This trades additional explicit-query reads for bounded memory; it does not add per-event background work.

A corrupt file remains a query error. Streaming may already have emitted output when a later error occurs; the command must return failure and identify incomplete output. It must not claim complete results. Do not spool or copy history to conceal this tradeoff.

## Trigger maintenance only for actual changes

Keep catalog state writes conditional on changed values. Sample current and active-file sizes without opening completed SQLite databases. Maintain aggregate size estimates from observed file changes and reconcile actual file sizes before pruning. A boundary, changed directory contents, policy edit, or size pressure can require a streamed metadata pass.

Pruning preserves oldest-first behavior, active-reader protection, and the permitted removal of unfinished work. File identity and retention are rechecked after cancellation and before deletion. A blocked oldest file remains a visible cleanup condition.

Supervisor output keeps its 1 MiB cap and its inode. After overflow, retain at most the newest 512 KiB of complete lines. Omit an oversized incomplete line with a bounded diagnostic rather than copying a large suffix repeatedly. The retained supervisor window becomes smaller immediately after trimming. Diagnostic archives and fail-open evidence retain their current policies.

A second supervisor copy requires approximately another 512 KiB of new output. Under-limit files do not undergo writes. Compression and finalization run for actual new data rather than on every replay wake.

## Require proof before the architecture changes

The acceptance work must first exercise the contracts that invalidate a naive design. Use real temporary SQLite files, the real gRPC server and client, and owned local subprocesses. Do not test only private call order or static source text.

Required proofs:

| Boundary | Required observation |
|---|---|
| Full dispatch, canceled caller, and a concurrent refill | Every committed pending receipt remains discoverable without a lost wakeup. |
| Late lower receipt ID, future retry, live claim, and a failing file | Recovery revisits eligible work fairly and does not restart its progress after each failure. |
| W greater than 1 with long inference in different files | Renewals and completion use original owners across rotation and reload. |
| Rotation while a chat continues submitting prompts | The newest prompt wins despite the local receipt counter restarting. |
| Temporal eviction followed by a delayed older publication | Context becomes unavailable where required and does not revert to old data. |
| Both hook RPCs under saturated admission and cancellation | Decoded application payloads do not accumulate outside admitted work, leases release on actual completion, and measured transport overhead does not hide a memory regression. |
| Stalled response readers | Transport-held encoded buffers stay within their byte budget. |
| Detached validator timeout, retries, large output, and output descriptors inherited by descendants | Job reservations survive synchronous return and release only after cleanup. |
| Multi-file queries during writes and pruning | Ordering, offset, totals, file access, failure reporting, and bounded buffering remain correct. |

Isolated runtime probes already passed on the pinned dependencies: a unary RPC with a 1 MiB request entered its wrapper before decoding, its mutable lease remained visible in stats.End after response marshaling, and its reference-counted encoded response returned to the pool once. A separate buffer-view check delayed pool return until the final reference released. SQLite 3.53.4 selected the existing pending index and preserved a record through WAL conversion and three read-only reopenings without sidecars. These probes validate selected dependency primitives; they do not prove cancellation storms, process memory ceilings, concurrent recovery, or production performance.

The production-path receive, cancellation, and stalled-reader tests must pass before a broad lifecycle refactor depends on those mechanisms. SQLite finalization and interruption tests must establish the completed-header invariant under actual owner coordination before recovery skips those files.

Qualification covers empty history, seven windows, and 80 and 160 size-created segments; backlog beyond dispatch capacity; both detail profiles; repeated bursts and reloads; and concurrent chats at identical event workloads.

Measure whole-process peak and settled memory, Go heap, physical footprint, active and detached jobs, connections, buffered bytes, CPU, physical writes, hook and completion latency, accepted and completed receipts, retries, and errors. Include shutdown, final checkpoints, and journal conversion. Do not force garbage collection or reset storage to hide retained resources.

Use matched baseline and candidate workloads with independent repetitions. CPU and writes per event must improve beyond baseline variation. Hook latency, error rates, completed-work throughput, and peak and settled memory must not regress beyond that variation. More completed history must not add SQLite work to each incoming event. More undispatched backlog must remain durable rather than expand the working set.

Idle operation stops historical SQLite opens and writes after admitted work and due maintenance finish. Recovery may inspect directory entries and completed headers during a finite recovery pass. It does not retain a completed-file registry in memory or promise retry lateness independent of unfinished backlog.

Performance failures return a failing command status and fail CI. Keep deterministic regressions in normal repository checks and run physical CPU, disk, and memory qualification on macOS. Passing an ownership test alone does not qualify performance, and a performance report alone does not establish correct recovery.

No product implementation or deployment follows solely from this document. Design approval precedes the implementation plan. Deployment requires verification of the installed build and the original live symptom after authorization.
