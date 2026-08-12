# Agent Gate setup, documentation, and audit storage design

Agent Gate will guide a user from installation through verified operation, then
bound the audit database without risking replayable work.

This design covers AGATE-29, AGATE-30, AGATE-31, and AGATE-32.

## Define the outcome

The work has four user outcomes:

1. A new user can install Agent Gate and verify one durable decision.
2. A user can find supported features, limits, and failure behavior.
3. An operator can select how much audit detail SQLite retains.
4. An operator can bound database age or size and reclaim unused space safely.

The solution combines three access paths. An interactive setup command handles
first use. Configuration and daemon maintenance handle normal operation. The
same command line maintenance interface supports external schedulers.

The default changes from unlimited storage to the `balanced` profile. This is a
breaking change. The first due maintenance run after upgrade may delete old,
completed records.

## Keep scope focused

This design includes setup, user documentation, audit granularity, retention,
maintenance, and SQLite compaction.

It does not add a graphical interface, remote audit storage, cloud backups, or
content compression. Compaction reclaims unused SQLite pages. It does not
compress retained values.

The design does not change enforcement decisions. Every accepted hook event
still receives a durable receipt before evaluation.

## Guide users through setup

`agent-gate setup` becomes the canonical command after the release installer
places the binary.

Interactive setup performs this sequence:

1. Detect installed provider clients and existing managed registrations.
2. Select providers, with detected providers selected by default.
3. Select an audit profile, with `balanced` selected by default.
4. Show the effective retention policy and any immediate deletion estimate.
5. Prepare configuration, service, and hook changes without writing them.
6. Validate the complete plan.
7. Write the configuration atomically.
8. Install or repair the supervised user service.
9. Apply the prepared hook registrations without changing unrelated hooks.
10. Run one labeled, harmless lifecycle event through each selected hook.
11. Confirm the daemon stored each receipt and its derived decision.

The verification event uses a unique setup identifier. Setup succeeds only
after the installed hook command, daemon socket, intake store, evaluator, and
audit sink agree on that identifier.

Lifecycle probes use observe-only events. Each installed hook must exit zero
and persist an allow decision. Any other result fails verification.

Automation uses the same coordinator:

```sh
agent-gate setup \
    --non-interactive \
    --providers claude,codex,cursor,gemini,copilot \
    --audit-profile balanced \
    --auto-update apply
```

Existing `install hooks`, `install service`, and `install all` commands remain
available for repair and compatibility. They call the same planning and apply
components. They do not become separate implementations.

Setup preflights every selected provider before changing any provider file.
Malformed provider configuration stops the run before setup writes
configuration, service state, or hooks.

Atomic writes retain the previous bytes until replacement succeeds. A failure
after the service starts reports the failed layer and the exact repair command.
Setup does not hide a partial installation behind a success result.

## Give each document one purpose

The documentation uses task and specialist durable homes:

- Getting Started owns release installation, setup, the first rule, the first
  verified decision, upgrades, and setup recovery.
- Feature Guide explains actions, conditions, evaluators, providers, queries,
  exports, updates, limitations, and practical examples.
- Audit Storage explains profiles, retention, size limits, maintenance,
  deletion, compaction, status, and recovery.
- Hook Inventory remains limited to registered events, provider capabilities,
  and managed template behavior.
- Hook Contract and Judge Architecture retain their exact specialized
  contracts.
- Contributor Guide owns source prerequisites, build, test, deployment, and
  generated-file workflows.

The annotated configuration owns exact keys and configurable values. Command
help owns exact flags. User guides explain behavior and tasks without copying
those references.

The repository landing page keeps one release install path, one short product
description, and one link to each user guide. It removes setup, feature, and
storage procedures that move into their durable homes.

## Set audit profiles

Every profile preserves the summary needed to answer what Agent Gate received,
what it decided, and why a rule matched.

| Profile | Full detail | Summary | Intended use |
| --- | --- | --- | --- |
| `balanced` | 168 hours | 720 hours | Default local operation |
| `full` | 720 hours | 720 hours | Investigation and model evaluation |
| `minimal` | Until replay and audit delivery complete | 720 hours | Lower data exposure and disk use |

Full detail includes these classes:

- Exact hook input.
- Normalized evaluation input.
- Complete provider classification evidence.
- Environment evidence.
- Evaluator inputs, outputs, metadata, and label rationale.
- Deferred audit delivery payloads.

Summary data includes these classes:

- Receipt and event time.
- Resolved provider and classification result.
- Session, turn, event, tool, and operation identity.
- Rule, decision, enforcement result, and violation fields.
- Evaluation outcome, model identity, latency, token totals, and cost inputs.
- Content, configuration, prompt, schema, binary, and evaluation hashes.
- Deferred state, attempt count, completion time, and delivery result.

Token totals and cost inputs remain in summary storage so `query cost` keeps
working throughout the summary window. Exact evaluator inputs and outputs
expire with full detail.

## Configure storage policy

The default configuration adds this policy:

```toml
[audit.storage]
profile = "balanced"
maintenance_interval = "24h"
max_size_mb = 0
maintenance_batch_rows = 1000
compact_after_maintenance = true

# Optional profile overrides:
# full_detail_retention = "168h"
# summary_retention = "720h"

[audit.storage.detail]
# Optional profile overrides:
# wire_input = true
# normalized_input = true
# provider_evidence = true
# environment_evidence = true
# evaluation_content = true
```

The profile supplies every value that the user omits. An explicit field
overrides only that profile value. Configuration loading retains the distinction
between an omitted value and an explicit zero or false value.

`max_size_mb = 0` disables size pruning. A positive value sets a soft maximum
for the database and write-ahead log together. Age limits still apply when the
size limit is disabled.

When set, `summary_retention` must be at least `full_detail_retention`. Explicit
durations must be positive. `minimal` treats full detail as protected work
rather than a time window, so an explicit full detail duration changes its
effective policy.

Turning off a detail class does not remove data needed by live work. Agent Gate
stores that class until replay and audit delivery complete, then removes it.

`agent-gate config check` prints the effective storage profile and rejects
unknown profiles, invalid durations, unsafe ordering, nonpositive batch sizes,
and unsupported combinations.

A valid configuration reload changes the next maintenance plan. An invalid
reload leaves the active plan unchanged.

## Separate summary and detail storage

The storage schema separates durable summaries from large or sensitive detail.
This boundary allows one transaction to demote an event without deleting its
review history.

Intake summaries retain event identity, provider result, operation identity,
timestamps, hashes, and replay state. Intake details retain exact input,
normalized input, complete classification evidence, and environment evidence.

Evaluation summaries retain verdicts, model identity, timing, hashes, token
totals, cost inputs, cache status, and error codes. Evaluation details retain
input, output, metadata envelopes, error text, and label rationale.

Audit decisions and violations are summaries. Completed deferred audit headers
remain summaries. Their delivered payload bodies are detail.

Each summary exposes one detail state:

- `available` means the requested detail is present.
- `expired` means retention removed previously stored detail.
- `not_recorded` means the effective policy omitted that detail after work
  completed.
- `protected` means live work still retains detail despite the configured
  profile.

Queries never replace expired detail with an empty value that looks genuine.
JSON output reports the detail state and omits the missing field.

Training export requires complete evaluator detail. It fails with the earliest
available detail time when a selected record has expired content. The explicit
`--skip-expired-detail` flag omits those records and prints the omitted count.

## Protect live work

Retention protects an event while any related work can still run or replay.

An event remains protected when any receipt lacks a completed hot evaluation,
has a pending deferred evaluation, has an active or retryable evaluation claim,
has a pending audit outbox, has an active or retryable audit claim, or has an
undelivered outbox entry.

Protection follows relationships, not timestamps. An expired lease remains
retryable and therefore protected. A completed event becomes eligible only
after every related receipt and outbox reaches a terminal state.

Age and size policies never override protection. If protected data exceeds the
size target, maintenance deletes nothing from that protected set. Status reports
the target as constrained and names the protected byte estimate.

## Prune data predictably

The maintenance engine uses one policy snapshot and one clock value for each
run. This prevents a reload or clock change from splitting one run across two
policies.

The engine performs these steps:

1. Validate the schema and foreign key state.
2. Count protected, detailed, summary, expired, and reclaimable records.
3. Remove eligible detail older than the full detail cutoff.
4. Remove eligible event graphs older than the summary cutoff.
5. Checkpoint the write-ahead log without delaying current writers.
6. Apply an optional size target to the oldest remaining eligible graphs.
7. Checkpoint the write-ahead log again.
8. Reclaim free pages when configured.
9. Record the outcome and the next due time.

Each delete batch uses one transaction. The batch removes children before
parents or uses declared cascading relationships. A process interruption rolls
back the current batch and retains earlier committed batches.

Age uses the durable receipt time. Size pruning removes the oldest eligible
event graph first. It stops when nonfree pages and live write-ahead log frames
fit the target. Checkpointed write-ahead log allocation does not cause further
deletion. The engine rechecks live frames after each batch. It never deletes
more data only because free pages await reclaim. The size target is soft because
protected data, current writes, and SQLite allocation can keep the physical
files above the target.

The daemon gives hook writes priority. A busy database causes maintenance to
end the current run and retry later. It does not extend the hook path wait.

## Schedule maintenance

The daemon reports ready after the remote procedure call server enters its
accept loop. It then schedules its first automatic maintenance run one full
interval later. Startup never waits for retention, integrity scans, checkpoints,
or page reclamation. A missing or overdue maintenance record does not trigger
startup work.

The scheduler reads the active configuration at each trigger. After readiness,
it persists the next attempt asynchronously without delaying the daemon. A
reload updates that attempt without starting overlapping runs. Restarting the
daemon starts a new full interval. Maintenance due time remains based on the
last success, so overdue work stays visible in status. An operator can run
maintenance explicitly when immediate cleanup matters. A database-backed lease
allows only one daemon, command line, or external maintenance caller to apply a
plan at a time.

The database stores the last plan, start time, completion time, policy hash,
deleted counts, reclaimed bytes, result, error class, due time, and next attempt.
Operational logs report the same run identifier.

Maintenance failures do not disable enforcement. The scheduler records the
failure and retries at the next interval. A schema or integrity failure blocks
deletion until an operator repairs the database.

## Expose maintenance commands

The audit command group uses the same policy and engine as the daemon:

```sh
agent-gate audit status
agent-gate audit status --json
agent-gate audit status --check
agent-gate audit maintain --dry-run
agent-gate audit maintain --apply
agent-gate audit compact --dry-run
agent-gate audit compact --apply
agent-gate audit compact --full --apply
```

`audit status` reports the effective policy, database and write-ahead log size,
oldest full detail, oldest summary, protected records, expired records,
reclaimable pages, last result, and next maintenance.

`audit status --check` returns a failure when maintenance is overdue, integrity
checks fail, or a configured size target remains unmet for reasons other than
protected data. It reports the condition without starting maintenance. Plain
status remains informational.

`audit maintain --dry-run` uses the same candidate queries as apply. It reports
counts and estimated bytes for each deletion class. It never writes maintenance
metadata.

`audit maintain --apply` runs bounded deletion and normal incremental
compaction. Repeating it after success produces no further deletion for the
same clock and policy.

External schedulers call `audit maintain --apply`. They do not execute direct
SQLite statements.

## Reclaim SQLite space

New databases enable incremental auto-vacuum before creating application
tables. Normal maintenance checkpoints the write-ahead log and reclaims a
bounded number of free pages. This keeps automatic work short.

An existing database may require one full compaction before incremental reclaim
can reduce its file size. Status reports this state without changing the
database.

`audit compact --full --apply` performs an explicit offline compaction:

1. Confirm that the command controls the managed user service.
2. Confirm enough free space for the database copy, write-ahead log, and safety
   margin.
3. Check database integrity.
4. Stop the managed daemon and acquire its process lock.
5. Create a compact database copy in the same filesystem.
6. Check the copy's integrity and synchronize it to disk.
7. Synchronize a cutover journal before changing the original database path.
8. Replace the original database while retaining a rollback copy.
9. Start the managed daemon behind the journal's token-bound gate.
10. Verify its binary, accept loop, and database before the gate opens.
11. Commit the journal, open the gate, and verify the normal status request.
12. Remove the rollback copy and journal only after normal readiness succeeds.

The journal records every replacement phase before its filesystem mutation.
The shared storage boundary reads an unresolved journal before any constructor
opens, migrates, creates, or writes the database. Every normal daemon and public
command uses that boundary. No writer can fill a cutover gap after a crashed
command releases its file lock. Only token-bound gated startup and explicit
recovery can access a pre-commit replacement. Normal access either uses the
verified committed replacement or stops for recovery.

The compaction command holds the daemon process lock before it creates the
journal or changes a database path. It releases that lock only after the
replacement-installed journal phase is durable, immediately before gated
startup. Recovery reacquires the process lock before restoring any file. This
handoff prevents an unjournaled daemon from opening the database between service
shutdown and the durable gate.

The gated daemon returns unavailable without writing receipts. It also pauses
background and scheduled work. Normal startup without a journal never waits on
this gate. The command states before confirmation that hooks fail open while
the daemon is stopped or gated. The daemon never schedules full offline
compaction automatically.

Before the gate opens, a compaction or restart failure restores the original
database and service state. Opening the gate is the point of no return because
the daemon can then acknowledge new receipts. A later readiness failure keeps
both database copies and never rolls back acknowledged data automatically. The
command prints an exact recovery path for either failure phase.

## Handle upgrades as a breaking change

An installation without `[audit.storage]` resolves to `balanced`. The daemon
does not preserve the former unlimited behavior implicitly.

Pre-versioned databases can contain completed evaluations whose durable intake
parent is missing. Version one preserves each recognized orphan evaluation and
its layers, labels, deferred audit header, and deferred audit entries in typed
quarantine tables. It verifies each copied row before removing the invalid
canonical graph.

The repair runs inside the version-one transaction. A later failure restores
the original rows and removes the incomplete quarantine. A successful repair
records the missing parent reason, reports the retained evaluation count, and
keeps the preserved values available for direct SQLite inspection.

Only an evaluation missing its intake event or matching receipt qualifies for
this repair. The migration still checks every foreign key before commit. An
unrecognized violation fails startup and maintenance without deleting its
source row. Foreign key enforcement remains active for every new write.

The schema migration is transactional and idempotent. It creates summary and
detail storage, moves existing values, adds retention metadata, verifies foreign
keys, and records its schema version before maintenance can run.

The migration does not delete protected data. After migration, the first due
maintenance run removes completed detail older than 168 hours and completed
summaries older than 720 hours. The daemon schedules that run one full interval
after it reports ready. Operators may preview or apply it earlier with the
explicit maintenance command.

The release notes state this deletion before users upgrade. Users who need an
archive must export or copy the database before upgrading. Setting a different
explicit policy before the new daemon starts changes the first plan.

If migration cannot complete, the transaction leaves the old schema unchanged.
The daemon reports the storage migration failure and does not claim that durable
intake is active.

## Report failures clearly

Setup errors name the failed layer before the repair action. Maintenance errors
name the blocked operation and preserve the next safe action.

The main failure cases behave as follows:

- Invalid storage configuration leaves the active runtime and schedule intact.
- A busy database defers maintenance without delaying hook evaluation.
- A protected-data size breach reports `constrained` and retains the data.
- Missing detail reports its state instead of fabricated empty content.
- Integrity failures stop deletion and compaction.
- Insufficient temporary space stops full compaction before service shutdown.
- A pre-cutover full compaction failure restores the original database.
- A post-cutover failure preserves every acknowledged replacement receipt.
- A failed setup probe reports the provider and durable stage that failed.

## Verify real behavior

Tests enter through configuration, command line, hook, daemon, and SQLite
boundaries.

Configuration tests prove profile defaults, explicit overrides, invalid
ordering, reload behavior, and the breaking default for an older config.

Migration tests open populated legacy databases. They verify summary and detail
fidelity, foreign keys, idempotence, protected records, and rollback after an
injected failure.

Retention tests use a temporary database with old, recent, pending, claimed,
retryable, and completed event graphs. They verify detail demotion, summary
deletion, oldest-first size pruning, and protected-data constraints.

Concurrency tests append real intake while maintenance runs in bounded batches.
They verify receipts, deferred claims, audit delivery, and hook latency remain
correct.

Command tests run the public audit commands against temporary state. They
compare dry-run and apply plans, verify idempotence, and assert observable
status and exit behavior.

Daemon tests prove startup reports ready without running retention, integrity
scans, checkpoints, or page reclamation. They also prove an overdue record
appears in status without causing startup maintenance.

Compaction tests create reclaimable pages, run incremental and full modes, then
verify integrity, file size, retained rows, the cutover gate, pre-cutover
rollback, and post-cutover receipt preservation.

Setup tests use a temporary home with realistic provider configuration. They
run generated hook commands against a real local daemon and confirm durable
receipts and decisions for every selected provider.

Documentation verification checks links and command examples after behavior
tests establish the commands themselves.

## Deliver one coordinated solution

The four epics share this contract and decompose into small vertical issues:

1. AGATE-31 adds the policy, summary and detail boundary, migration, and query
   behavior. AGATE-57 repairs recognized pre-versioned evaluation orphans before
   later retention work can open the database.
2. AGATE-32 adds pruning, scheduling, status, compaction, and external command
   support.
3. AGATE-29 adds setup orchestration and end-to-end verification.
4. AGATE-30 establishes the documentation architecture and moves existing
   material into one durable home per fact.

No child issue may introduce a second storage policy or duplicate setup
workflow. All entry points use the same effective policy, plan, and maintenance
result.
