# Audit CPU load, disk usage, rotation, and reset

Agent-gate removes repeated executable hashing, redundant database writes, and competing backlog scans. It stores audit history in daily SQLite files and retains the current day plus the previous six days by default. Changing rotation or retention settings discards the existing history and starts fresh.

This specification defines the replacement for [issue 159](https://github.com/agoodkind/agent-gate/issues/159). The repository is pre-alpha and has one consumer. Existing installations have undefined behavior until the user manually runs the new reset command. Implementation does not reset or deploy to the live installation.

## Cover the full reported problem

The goal includes processor usage, disk traffic, hook responsiveness, and bounded history. Rotation alone does not address repeated work on each event.

| Reported problem | Evidence and interpretation | Required result |
| --- | --- | --- |
| Automatic maintenance can starve across restarts and reloads. | The scheduler starts a fresh full interval instead of honoring elapsed time. The issue reports a restart cadence near its 24-hour interval. | Use absolute rotation boundaries and expire overdue files after readiness. |
| Manual cleanup contends with the live writer. | The issue reports one successful 1,000-graph batch in four attempts and an 80,010-graph backlog. The implementation already loops over batches; it exits on contention. | Delete the row-cleanup, lease, and compaction paths. Expire closed database files. |
| Disk synchronization and database operations add work during hook traffic. | The issue contains stack observations in SQLite, reads, writes, and `fsync`. Those observations are not syscall counts. The current driver's default is already `NORMAL`. | Reduce transactions, payload copies, and index work. Keep `NORMAL` explicit without claiming it is a new performance improvement. |
| Evaluation writes are canceled or exceed their deadlines. | The issue reports failures at transaction start, evaluation insertion, and layer insertion during active hook traffic. | Remove repeated work before changing timeouts. Propagate cancellation into storage and test hook outcomes under contention. |
| Stored history keeps growing. | The issue reports growth beyond 5.3 GB while cleanup stalls; this machine later measured about 18 GB. | Expire by age even across downtime. Verify filesystem reclamation. |
| CPU and solid-state drive (SSD) work can compound as activity and backlog grow. | Source shows repeated executable hashes, several persistence stages, duplicate payloads, per-worker replay scans, and large indexes. The temporary sample below measures the current load but does not prove runaway saturation. | Remove those mechanisms and test whether cost per event grows with backlog under fixed traffic. |
| Inspection can itself create substantial disk traffic. | Current status creates two database snapshots and performs graph and integrity scans. Its size-pressure path additionally deletes and vacuums a snapshot. | Make status read small metadata and file sizes. It must not copy, scan, or compact history. |

The feedback mechanism under investigation is additional work causing writer waits and deadlines, followed by fallback work and repeated replay attempts that create more work. Treat its existence in the code separately from the amount it contributes on the user's machine. The replacement must eliminate the identified redundant paths and measure the remaining contribution.

## Record the temporary computer sample

The daemon remained running throughout collection. No maintenance, reset, synthetic hook traffic, application build, or database scan ran during the capture. Ordinary interactive tool traffic continued. The temporary counter collector was compiled and calibrated before its own capture, during part of the separate stack-sampling window. All samplers exited after their bounded capture.

The sampled process was PID 5863, launched by launchd, running commit `29ea2ef` with build hash `6e9ca084079c`. The loaded image UUID matched the executable on disk. The executable measured 85,924,448 bytes. Its embedded build information confirmed SQLite driver version `v1.14.49`.

Process resource counters were recorded every five seconds from September 8 at 23:11:27 through 23:12:57 PDT, spanning 90.12 seconds. Mach clock units were converted using the host timebase. A 100 ms CPU control measured 99.8 ms of CPU time after conversion; the initial incorrect unit assumption was rejected before collecting the reported counters.

| Process measurement | Observed result |
| --- | ---: |
| CPU time | 6.42 seconds |
| Average CPU usage, relative to one core | 7.13% |
| Median / peak CPU usage across five-second intervals | 8.14% / 16.35% |
| Disk bytes read attributed to the process | 12.91 MB |
| Disk bytes written attributed to the process | 139.88 MB |
| Logical-write counter increase | 103.14 MB |
| Peak disk-write rate across five-second intervals | 4.50 MB/s |
| Physical memory footprint, start / end / sampled peak | 122.16 / 121.82 / 122.96 MB |
| Process page-ins | 0 |
| Intake receipts recorded during the timestamp window | 80 |
| Completed hot / deferred evaluations during that window | 77 / 77 |

The three consecutive 30-second periods used 9.02%, 5.03%, and 7.34% of one core and wrote 57.99, 35.36, and 46.53 MB. These intervals show variation within one observation; they are not independent controlled benchmark repetitions. Process disk counters include all writes charged to the process and do not identify SSD wear or internal flash write amplification.

A separate 90-second stack sample began at 23:10:37 PDT with a requested 10 ms sampling interval. Its window partly overlaps the resource-counter capture. It recorded 541 top-of-stack observations in SHA-256, 80 in `read`, 32 in `pread`, 24 in `fsync`, and 16 in `pwrite`. SQLite checkpoint stacks included synchronization calls. Most thread observations were waits. Truncated Go ancestry prevents assigning every hash observation to one caller.

System disk counters were collected as a control. Exclude their initial since-boot average. Their later intervals showed other computer activity as well as daemon traffic; they cannot attribute all disk activity to agent-gate. System swap-in, swap-out, and page-out counters did not increase between the surrounding memory snapshots. This capture demonstrates ongoing CPU and disk work, but did not reproduce memory thrashing or establish CPU/SSD saturation.

Raw samples and the temporary collection programs remain in the task's temporary output directory for inspection. The committed tables retain the measured baseline needed for the design. Improvements require a controlled before/after workload; they cannot be claimed from this capture alone.

## Define the approved behavior

- Create databases directly with the final schema. Delete every database migration, migration ledger, upgrade path, compatibility check, and legacy database fixture.
- Delete whole expired files, including any unfinished audit work they contain.
- Expire history by elapsed time. Time with the daemon stopped still counts.
- Make rotation and retention configurable. Default to a new file every day and seven retained daily windows.
- Use SQLite write-ahead logging with `synchronous=NORMAL`. Recent committed audit records may be lost after power loss or an operating-system crash. This tradeoff is approved.
- Provide a manual reset command that removes the installation, preserves hooks and `config.toml`, and calls the existing service installer.
- Keep enforcement, provider handling, and response formatting in the daemon. Hooks remain transport-only.

## Remove repeated CPU and disk work

### Compute process identity once

The current build-hash function opens and hashes the entire executable on each call. Hot evaluation, deferred evaluation, status, and the hot-write failure fallback each call it. A normal hook with deferred completion therefore reads the executable at least twice through this code path. File caching can avoid physical reads while the repeated hashing still consumes CPU.

Compute the executable hash once during process startup, before serving requests. Reuse that immutable value for every evaluation, status request, and failure record. Replacing the executable on disk must not change the running process's recorded identity. A failed read produces an explicit unavailable identity; never publish a partial-file hash. Loaded configuration identity is already cached and needs no additional cache.

### Commit audit results directly

SQLite is the only audit destination and shares the intake database handle. Insert normalized audit rows in the same transaction that commits deferred evaluation and receipt completion. Delete the deferred audit outbox, its payload copies, delivery claims, renewals, per-entry delivered markers, completion records, and replay path.

Keep the initial intake commit and hot completion boundary required for accepted work and the hook response. Keep deferred attempt ownership and fencing. Reduce a normal successful receipt with deferred work to those necessary stages: intake append, hot completion, deferred claim, and atomic deferred completion with audit rows. Long-running claim renewal and actual failures remain separately observable.

For audit-only events outside an evaluation transaction, write the bounded queued batch in one transaction instead of starting one transaction per event. Bound batches by serialized bytes as well as item count. Propagate the owning context through every SQL call and stop canceled work; the current sink's unconditional background context must go.

Do not retry writes on an already-canceled context or recompute expensive identity data to construct a failure record. Keep the existing externally visible fail-open behavior when required persistence fails, and expose the failure without creating an unbounded persistence retry loop.

### Delete unused indexing and retention scaffolding

Remove the full-text command index and its insert, delete, and update triggers. Production queries do not consume that index. For each other secondary index, retain it only when a surviving query or constraint requires it, using query plans and representative data to justify the choice.

Fold detail fields into their owning records where separate tables exist only to support independent expiration. Remove redundant availability manifests and detail-state rewrite paths. Keep repeated entities such as evaluation layers and labels.

Record optional audit detail according to the selected content policy at insertion time. Retain one canonical replay input for an unfinished receipt, including the original normalized input and provider/environment facts needed to evaluate it. Release replay-only content as part of terminal completion when the content policy excludes it. This bounded work-completion cleanup does not reinstate a background retention or demotion engine. The full-detail policy reuses retained canonical input rather than storing another serialized copy.

### Give replay one scheduling owner

Use one scheduler for both current and retained historical backlog. It reads bounded pages and dispatches each eligible receipt once to the existing bounded workers. Workers must not each scan the backlog on their own ticker. Claimed or already-queued work does not create additional evaluation attempts.

Apply bounded backoff to retryable failures and expose attempts, queue depth, and oldest pending age. Cancel owned work promptly during rotation and configuration cuts. Keep replay from delaying readiness or exhausting the synchronous hook's execution capacity.

### Keep inspection cheap

Status and rotation operate on storage metadata and file sizes. They do not create database snapshots, run whole-history integrity scans, estimate protected graphs, delete rows, or vacuum copies. Detail queries visit only the retained files and records needed by their filters. Historical file count alone must not multiply active-writer work per hook.

## Choose whole-file rotation

Each file contains the complete intake, receipt, evaluation, and final audit records for requests assigned to that file. Foreign keys and atomic completion remain local to one database. A receipt's deferred work continues in its original database while that database remains retained.

Whole-file deletion replaces row-level expiration and compaction. The alternatives considered were repairing the existing batch-deletion scheduler and separating unfinished work into a permanent operational database. The first retains writer contention and compaction machinery. The second adds transfer and publication bookkeeping to preserve work that the user explicitly permits expiration to discard.

The RAM cache remains outside this storage change.

## Configure the retained windows

```toml
[audit.storage]
bucket_interval = "24h"
retention_buckets = 7
```

`bucket_interval` is a positive duration containing a whole number of seconds. `retention_buckets` is a positive integer. Reject durations or counts whose combined retention arithmetic overflows.

Boundaries use UTC and align to the Unix epoch. The current boundary is the current time rounded down to a multiple of the interval. Retain the current window and the preceding `retention_buckets - 1` windows. Missing windows do not extend retention or cause empty historical files to be created.

For the default, a file written today remains alongside the previous six days. History therefore spans between six and seven days during normal operation. After a long shutdown, files older than that window expire when the daemon returns.

The configured SQLite base path identifies the storage family. Bucket names include their UTC start timestamp. Resolve one bucket path when opening a store; never resolve the clock separately for individual writes belonging to that store.

Keep existing content-selection overrides. Remove separate summary/detail retention and terminal audit-detail demotion. Collapse `balanced` and `full` into the canonical `full` profile; retain `minimal`. Omitted content is represented as not recorded. Expiration removes the whole graph rather than rewriting its detail state. Replay-only input follows the work-completion rule above.

### Record the measurement behind the default

Read-only measurements on September 8, 2026 identified the database held open by the live daemon. It contained about 18 GB and began on August 14, matching the user's manual reset. Older detail had already expired, so the recent complete periods informed the estimate.

| Period, UTC | Estimated database footprint |
| --- | ---: |
| August 24 through August 30 | 6.06 GB |
| August 31 through September 6 | 6.14 GB |
| September 2 through September 8 | 9.31 GB |
| August 29 through September 4, busiest measured rolling week | 10.69 GB |

The rolling periods overlap the calendar weeks. Content lengths were measured by day. SQLite table allocation and indexes were apportioned using content lengths and row counts, so these figures estimate disk footprint rather than measuring separate weekly files. One GB means 1,000,000,000 bytes.

Five GB represented roughly three to six days at these observed rates. The user selected one week after seeing the estimate. This is an age limit; it does not promise a 5 GB ceiling. The final simplified schema may use less storage.

## Apply a hard cut when settings change

Compare normalized rotation and retention values, not raw TOML text. Equivalent durations such as `24h` and `1440m` are unchanged. Comments, unrelated settings, and content-selection changes do not trigger this hard cut.

Persist the applied normalized policy in one small storage metadata file so the comparison also works after a restart. A changed policy invalidates every existing bucket in that storage family. Record the invalidation before deletion, and keep it recorded until fresh storage is ready. Interrupted deletion resumes as deletion; invalidated history never becomes readable again.

Perform these steps for a valid policy change:

1. Validate the replacement configuration and storage location before any deletion.
2. Serialize the transition with reload, rotation, replay, and reset. Stop admitting work to the old audit store, finish its admitted hook requests, cancel its deferred work, and close its readers and writers.
3. Delete the existing buckets and their SQLite sidecars, including unfinished work.
4. Initialize a fresh database with the final schema and new policy.
5. Mark the new storage ready and resume normal processing.

Invalid settings reject the transition without deleting data. Storage validation must not fall back to defaults and accidentally turn an invalid edit into an approved destructive change. If deletion or initialization fails after invalidation, report the failure and retry the fresh initialization path. Do not restore the old history or serve it through query commands.

A configuration change while the daemon is stopped follows the same transition at the next start. Reverting settings later is another hard cut; it never revives an earlier set of files.

This transition affects audit storage only. It does not uninstall the service, rewrite configuration, or alter hook registrations.

## Rotate without stranding work

Startup opens the current bucket and performs expiration after readiness without waiting a new full interval. The scheduler targets the next absolute boundary, so restart and reload cannot postpone rotation. Recheck the wall clock when the timer fires and when the machine resumes from sleep.

Use the existing runtime request lock and snapshot replacement boundary. Prepare the next bucket before switching ordinary rotation. Requests already admitted to the old snapshot finish against its database. New requests use the replacement after the swap. Rotate through a dedicated storage transition rather than recursively invoking a reload that restarts its own scheduler.

Give deferred processors an owned cancellation context. Track startup replay goroutines, check cancellation between records, and join all database users before closing their handles. Replay retained historical buckets through one serialized background worker with bounded pages, using each bucket's own intake, evaluation, and audit sink.

At expiration, cancel and join work for the expired bucket, close its handles, and delete its database and sidecars. Unfinished work never pins a bucket. If cleanup fails, report the actual failure and retry; exclude expired files from history queries while cleanup is pending.

Use a small filesystem lock to coordinate query readers, replay, rotation, and destructive deletion across processes. A query holds a read claim for the files it uses. Deletion obtains exclusive access after their users finish. Do not carry forward maintenance leases, graph-protection scans, compaction journals, or a historical policy ledger.

## Query retained history

Audit, intake, evaluation, detail lookup, and cost commands read all retained buckets. Open them read-only without creating missing files or initializing schema. Include only the active storage policy's retained history.

Apply filters within each bucket, then merge results in deterministic global order before applying the requested limit. Use bucket identity as an additional tie-breaker for identifiers that are database-local. Qualify receipt IDs with their bucket in results and exact receipt lookups; never join a receipt from one bucket to an evaluation in another. Keep stable event and evaluation identifiers where they already exist.

Use bounded per-bucket pages for bounded queries. Compute cost totals across all matching retained records, independent of a displayed result limit. Surface corrupt or unreadable retained files as query errors rather than silently reporting complete results with missing history.

Replace `audit status` with the active bucket, retained paths, total bytes including SQLite sidecars, normalized settings, next rotation boundary, and any outstanding storage transition or cleanup failure. Remove the overdue-maintenance, protected-graph, and compaction model.

Remove `audit maintain`, `audit compact`, and their flags. Automatic expiration needs no additional manual prune command.

## Reset the installation manually

`agent-gate reset` permanently removes installation state and audit history, then reinstalls the user service. Running the command is the user's explicit reset action. Never invoke it automatically during startup, configuration reload, update, or ordinary installation.

Preserve provider hook registrations, hook files, and `config.toml` byte-for-byte. Resolve the current executable, service identity, and deletion targets before changing anything. Keep the staged executable outside directories that reset removes.

1. Stage the running executable, retaining the permissions and signing attributes needed to restore it at its installed path.
2. Unload agent-gate's launchd job to prevent respawning. On Linux, stop and disable its user service.
3. Terminate any remaining daemon belonging to this installation and wait for exit. Escalate termination only for a verified remaining daemon process.
4. Remove its service definition, installed executable, databases, SQLite sidecars, logs, caches, sockets, update state, storage metadata, and other agent-gate installation state. Remove configured database files even when they are outside the default state directory. Never recursively remove an unrelated shared parent directory or a preserved hook/configuration target.
5. Restore the staged executable at the installed path, preserving paths referenced by the retained hooks.
6. Call the existing `install service` command implementation in-process. Reuse its service creation and readiness check. Do not copy the install implementation into reset or merely print a command for the user to run.

Already-absent artifacts are acceptable. A service-control error is not proof that a service is absent. Stop before database deletion if the daemon cannot be stopped. After deletion begins, failures report the completed stage and stop; repeated reset can finish from an already-partially-removed installation. Reinstallation failures return the existing install error. No database backup or rollback is created.

## Remove obsolete code and documentation

Delete the whole audit maintenance subsystem and all callers, schema migrations, legacy fixtures, upgrade tests, compaction recovery, and obsolete setup previews. Remove incremental auto-vacuum configuration and the schema/tables used only for maintenance, migration repair, and detail expiration. Keep indexes justified by surviving query behavior.

Update configuration defaults, examples, setup, command help, and storage documentation together. Replace the old storage maintenance instructions with the rotation and reset behavior. Keep the new specification as the design contract and the implementation plan as the executable work breakdown; do not duplicate the contract across both documents.

## Prove the replacement

Use temporary databases and isolated installation roots. Never mutate the live database or restart the user's live service during tests.

- Prove direct schema creation, reopening, foreign keys, atomic completion, and the effective `NORMAL` synchronization setting.
- Prove concurrent identity reads use the single startup hash, keep that identity after on-disk executable replacement, and reject partial-read hashes.
- Prove deferred completion persists evaluation and audit rows atomically without an outbox or per-entry delivery transactions. Exercise rollback through observable records, not call-order mocks.
- Verify optional detail is omitted, replay survives restart while pending, and terminal completion releases excluded replay-only input without losing retained audit content.
- Exercise hook requests and deferred delivery across rotation, cancellation, reload, restart, and shutdown.
- Verify elapsed-time expiration after downtime, current-window retention, configuration increases and reductions, equivalent values, invalid edits, and changing policy back to a previous value.
- Inject interruption after invalidation, during deletion, and before fresh storage becomes ready. Verify retries cannot expose old history.
- Verify expired unfinished work disappears, retained unfinished work resumes, and receipt-ID collisions cannot cross database boundaries.
- Test global ordering, limits, detail lookup, cost aggregation, concurrent readers, and deletion failures through public boundaries.
- Verify actual filesystem reclamation without row-level expiration or compaction.
- Exercise reset with running and absent services, repeated invocation, custom paths, partially removed installations, executable restoration, preserved hooks/configuration, and service readiness. Keep service-control fakes limited to the operating-system boundary.
- Compare CPU time, executable bytes hashed, transaction count, disk bytes written, final stored bytes, replay attempts, queue depth, and hook latency per input event under identical traffic before and after. Report median and tail across repeated runs.
- Test idle backlog, steady traffic, bursts, repeated status requests, cancellation, and rotation. Repeat the same input trace with an empty store and a representative retained backlog to expose costs that compound with history size.
- Require one executable hash per process, one replay scheduling owner, no per-entry outbox writes, no unused full-text-index maintenance, and no history copies or scans from status. A smaller database without these CPU and disk changes does not satisfy this specification.
- For identical representative active workloads, CPU and disk work per event must improve beyond baseline run-to-run variation. Hook latency, error rates, and completed-work throughput must not regress beyond that variation. Idle work must remain bounded. Record the baseline spread before evaluating the replacement. Keep failures visible instead of relaxing timeouts to make the comparison pass.
- Measure actual synchronization calls only with a validated syscall or SQLite instrumentation source. Stack-sampling observations do not represent syscall counts. Preserve the explicit `NORMAL` policy but credit no improvement to setting a value the current driver already uses.
- Run `make test`, `make lint`, and `make check`. Require adversarial review of concurrency and destructive-path handling before merge.

The implementation uses the harness-provided isolated checkout and logical signed commits. Merge, live deployment, and execution of the user's manual reset are separate actions.
