# Disposable audit storage and manual reset

Agent-gate stores audit history in daily SQLite files and retains the current day plus the previous six days by default. Changing rotation or retention settings discards the existing history and starts fresh.

This specification defines the replacement for [issue 159](https://github.com/agoodkind/agent-gate/issues/159). The repository is pre-alpha and has one consumer. Existing installations have undefined behavior until the user manually runs the new reset command. Implementation does not reset or deploy to the live installation.

## Define the approved behavior

- Create databases directly with the final schema. Delete every database migration, migration ledger, upgrade path, compatibility check, and legacy database fixture.
- Delete whole expired files, including any unfinished audit work they contain.
- Expire history by elapsed time. Time with the daemon stopped still counts.
- Make rotation and retention configurable. Default to a new file every day and seven retained daily windows.
- Use SQLite write-ahead logging with `synchronous=NORMAL`. Recent committed audit records may be lost after power loss or an operating-system crash. This tradeoff is approved.
- Provide a manual reset command that removes the installation, preserves hooks and `config.toml`, and calls the existing service installer.
- Keep enforcement, provider handling, and response formatting in the daemon. Hooks remain transport-only.

## Choose whole-file rotation

Each file contains the complete intake, receipt, evaluation, and audit-delivery graph for requests assigned to that file. Foreign keys and atomic completion remain local to one database. A receipt's deferred work continues in its original database while that database remains retained.

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

Keep existing content-selection overrides. Remove separate summary/detail retention and terminal detail demotion. Collapse `balanced` and `full` into the canonical `full` profile; retain `minimal`. Omitted content is represented as not recorded. Expiration removes the whole graph rather than rewriting its detail state.

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
- Exercise hook requests and deferred delivery across rotation, cancellation, reload, restart, and shutdown.
- Verify elapsed-time expiration after downtime, current-window retention, configuration increases and reductions, equivalent values, invalid edits, and changing policy back to a previous value.
- Inject interruption after invalidation, during deletion, and before fresh storage becomes ready. Verify retries cannot expose old history.
- Verify expired unfinished work disappears, retained unfinished work resumes, and receipt-ID collisions cannot cross database boundaries.
- Test global ordering, limits, detail lookup, cost aggregation, concurrent readers, and deletion failures through public boundaries.
- Verify actual filesystem reclamation without row-level expiration or compaction.
- Exercise reset with running and absent services, repeated invocation, custom paths, partially removed installations, executable restoration, preserved hooks/configuration, and service readiness. Keep service-control fakes limited to the operating-system boundary.
- Measure disk synchronization and hook latency under identical traffic before and after. Stack-sampling counts do not represent syscall counts.
- Run `make test`, `make lint`, and `make check`. Require adversarial review of concurrency and destructive-path handling before merge.

The implementation uses the harness-provided isolated checkout and logical signed commits. Merge, live deployment, and execution of the user's manual reset are separate actions.
