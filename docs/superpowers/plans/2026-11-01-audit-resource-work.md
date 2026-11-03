# Agent Gate audit resource implementation plan

> Use subagent-driven-development to implement and review each task before starting the next. Track completed steps with checkbox syntax.

Goal: Eliminate unnecessary disk writes and uncontrolled memory growth while preserving enforcement, audit correctness, recovery, and concurrent chats.

Architecture: Retain SQLite as durable intake and pending-work storage. Reuse active file owners and dispatch committed identifiers directly. Bound retained data through explicit lifetimes and validate the complete process under representative traffic.

Technology: Go, SQLite, gRPC, native process counters, temporary files, and local subprocesses.

## Current delivery scope

Deliver the reviewed storage ownership, direct dispatch, finite recovery, completed-file finalization, cache bounds, hook input limit, and KV list sizing changes. Preserve SQLite history, enforcement, audit outcomes, recovery, and concurrent chats.

RPC admission before decoding, transport response-buffer accounting, external-validator lifecycle changes, bounded history-query redesign, and supervisor-log redesign remain deferred. Their unchecked tasks below are not prerequisites for this delivery. Dependency probes do not establish that those designs work in production.

Use the existing analyzer and ordinary build pipeline. Do not change Makefiles, analyzer rules, or baselines, and do not use an analyzer override.

The retained-history idle tests and live process observations verify the reported write pattern. They do not establish matched-workload CPU, latency, throughput, or long-term memory qualification. Broader performance acceptance remains unfinished under [AGATE-65](https://tack.home.goodkind.io/browse/AGATE-65).

Read the [approved design](../specs/2026-11-01-audit-disk-work-design.md) before implementation. Its algorithms and proposed budgets are engineering decisions, not additions to the durable goal. Correct disproved assumptions, update affected tasks and interfaces, and record evidence. Preserve the user requirements when choosing a better implementation.

## Global constraints

- Preserve configured enforcement and error policies. Keep hooks transport-only.
- Preserve accepted pending work until completion or the existing permitted retention deletion.
- Preserve existing history without a migration, reset, history copy, or new permanent replay database.
- Do not introduce a chat quota or allocate a connection or backlog queue for each chat.
- Keep one configured deferred worker set across snapshots. Retain original file ownership through renewal and cleanup.
- Keep current and unfinished files in WAL with NORMAL synchronization. Completed-file conversion must not delete sidecars manually.
- Read the entire affected file before editing. Preserve unrelated work and use the current isolated worktree.
- Use ordinary repository checks. Targeted package tests are acceptable during debugging. New tests must exercise real behavior through an appropriate public boundary.
- Deploy and restart only the reviewed build under the user's authorization. Do not change installed configuration or reset live data.
- Commit logical changes with the Codex trailer. Publish ready-for-review pull requests after verification; do not merge without authorization.

## Review focus

- A cancellation after pending commit must not discard the notification needed to resume work.
- A lower receipt ID that becomes pending late or becomes due later must receive another finite recovery pass.
- Two historical jobs must retain different stores across renewal, rotation, and reload.
- Cache pressure must expose unavailable temporal context instead of restoring an older observation.
- An RPC or validator that returns synchronously must not release data credits while transport or background work still retains the data.

## Tracking and review boundaries

The local stack exists before product edits. Each branch follows the previous branch in this table. The existing design branch remains the first layer.

Review layers:

| Branch | Tasks | Observable result |
|---|---|---|
| codex/audit-disk-work-design | 1 | Comparable baseline measurements and failure fixtures accompany the design. |
| codex/audit-storage-runtime | 2 | Snapshots and jobs retain valid active storage without duplicate worker sets. |
| codex/audit-scheduling | 3 | Accepted work resumes fairly without repeated completed-history SQLite opens. |
| codex/audit-cache-state | 4 | Cache bytes and temporal order remain correct across rotation and eviction. |
| codex/audit-rpc-memory | 5 | RPC admission and response ownership prevent unaccounted application payload retention. |
| codex/audit-external-work | 6 | External work has bounded buffers, lifetime, and cleanup. |
| codex/audit-history | 7 and 8 | History and log operations use bounded work without recurring rewrites. |
| codex/audit-qualification | 9 | Combined CPU, disk, memory, correctness, and CI qualification establish the result. |

Update boundaries before further implementation when evidence changes dependencies. Rebase descendants after an accepted lower-layer change. Each task review compares against its recorded starting commit rather than HEAD~1.

## Responsibilities

The existing components establish placement. New files stay with the component that owns their behavior.

Implementation responsibilities:

| Tasks | Owning source | Responsibility |
|---|---|---|
| 1 and 9 | [process usage](../../../internal/testutil/processusage), [qualification benchmark](../../../internal/daemon/audit_qualification_darwin_test.go), [qualification adapter](../../../internal/daemon/audit_qualification_adapter_darwin_test.go), [qualification command](../../../scripts/qualify-audit.sh), [qualification summary](../../../scripts/testdata/audit-qualification-summary.go) | Measure the correct process and workload, preserve baseline evidence, and fail acceptance when results fail. |
| 2 | [daemon](../../../internal/daemon) | Separate snapshot, file-owner, and job lifetimes while retaining existing public daemon behavior. |
| 3 | [intake](../../../internal/intake), [audit storage](../../../internal/auditstorage) | Read bounded pending metadata, coordinate transitions and completed files, and preserve local transactions. |
| 4 | [hot cache](../../../internal/hotkv), [rules](../../../internal/rules), [configuration](../../../internal/config) | Bound aggregate cache bytes and maintain temporal facts under ordering and eviction. |
| 5 | [daemon commands](../../../cmd/agent-gate), daemon, configuration | Admit before application receive, preserve transport retry semantics, and account for response buffers. |
| 6 | rules, [exec condition](../../../internal/rules/concerns/exec) | Track external jobs and bounded I/O through actual completion. |
| 7 | audit storage, intake, [audit queries](../../../internal/audit), [evaluation queries](../../../internal/evaluation) | Stream live history with bounded buffering, correct ordering, and retained file access. |
| 8 | [log storage](../../../internal/logstorage) | Retain supervisor output with space for new data and bounded copy volume. |
| 9 | [CI](../../../.github/workflows/ci.yml) | Run appropriate regressions and physical resource qualification. |

## Task 1: Establish resource measurements and the baseline

Consume the existing processusage.Read and qualification fixtures. Preserve Read while adding ReadPID(pid int) and resident/physical-footprint fields to Snapshot when external measurement requires them. Read native units from the actual SDK and validate them before comparing results.

- [x] Add an idle case and retained-file cardinalities including 80 and 160. Fixture creation and result serialization must not count as daemon work. Do not poll history through SQLite during idle measurement.
- [x] Use actual storage and daemon behavior. Sparse fixtures may reduce physical fixture writes only if SQLite validity, logical file accounting, and the limitation of the fixture are verified. Do not substitute filename-only files or fabricate resource counters.
- [x] Capture an immutable baseline executable and results outside the repository before later runtime changes. Record the source commit, workload digest, fixture properties, metric scope, and limitations.
- [x] Run targeted qualification tests and inspect the artifact. It must reproduce retained-history write amplification or report the actual observation without claiming the cause disappeared.
- [x] Commit measurement and fixture changes. Report exact public entry points and commands for Task 9 to reuse.

## Task 2: Separate storage and job ownership

Consume the current Catalog.OpenWriter, newRuntimeSnapshotForBucket, runtimeSnapshot.close, and deferred processing interfaces. Keep New, Run, EvaluateHook, and StartAuditScheduler callable through their current public boundaries. New private types may change when the existing code makes another interface clearer.

- [ ] Make a file owner independent of configuration snapshots and preserve its SQLite handle until all borrowed references release.
- [ ] Maintain one W-worker service across snapshots. A job captures its file, configuration, recorder, claim, and assignment order. Do not rebind a running job to another file.
- [ ] Preserve unchanged-storage reload and define current-file assignment generation for Task 4. Record the exact internal interfaces produced before Task 3 begins.
- [ ] Add real-SQLite regression coverage for shared-owner reload, two historical jobs, rotation during renewal, and cancellation cleanup. Verify default W=1 and configured W greater than 1.
- [ ] Run the relevant daemon and storage tests, review connection accounting, and commit.

## Task 3: Replace rediscovery with direct dispatch and finite recovery

Consume the Task 2 owner and worker interfaces. Preserve the existing claim fencing and atomic completion methods. Add a metadata pager alongside ListDueDeferred rather than loading pending payloads.

- [ ] Dispatch known identifiers after pending commit, independently of request cancellation. Preserve overflow in SQLite and use generation acknowledgment rather than a clearable Boolean.
- [ ] Implement finite indexed metadata passes with a frozen eligibility time, cursor reset for due-time revisits, and the existing pending index. Bound returned raw entries and avoid an unbounded SQL sort or eligibility scan.
- [ ] Preserve progress after errors and reserve worker turns for discovery and historical work under continuous current traffic. Prevent queued or locally active work from creating due-now rescan loops.
- [ ] Coordinate retirement, pruning, and completed-file conversion. Persist completion through successful journal conversion after a durable pending check and exclusive access.
- [ ] Test queue capacity one, late pending commits, future retries, claims, cancellation after commit, failing-file fairness, restart, and interrupted conversion through real public operations.
- [ ] Run targeted daemon, intake, and storage verification. Commit and record any interface changes for later tasks.

## Task 4: Bound cache state without stale temporal facts

Consume the Task 2 assignment order. Keep database receipt identity separate from process-local temporal order.

- [ ] Add a validated aggregate cache byte limit with proposed default 64 MiB, preserving the existing entry and per-value controls. Account for keys and retained values before allocation.
- [ ] Compare temporal updates using assignment generation and receipt ID. Keep unchanged-file reload order and reject older updates from retired assignments.
- [ ] Implement atomic temporal publication and eviction fencing. Use bounded scalar state; expose unavailable context under pressure instead of restoring stale values or retaining misleading old values.
- [ ] Add public-boundary regressions for actual size rotation within one chat, eviction followed by a delayed older update, oversized values, and unrelated existing temporal keys.
- [ ] Preserve public KV semantics for successful requests. Expose a bounded list/read interface for Task 5 and record its exact signature and overflow error.
- [ ] Run targeted hotkv, rules, configuration, and temporal daemon tests. Commit.

## Task 5: Account for RPC input and response lifetimes

Consume the bounded cache/list interface from Task 4. The dependency probes demonstrate the pinned gRPC registration and BufferPool APIs; production behavior still needs tests.

- [ ] Admit before dec invokes RecvMsg using copied method descriptors and the existing H setting. Create a mutable lease carrier in stats.TagRPC and release owned slots at actual stats.End.
- [ ] Reject before execution when no slot is available. Retry only the dedicated no-execution busy result within one daemon-supplied admission budget and the original RPC deadline. Never retry ambiguous completion or generic ResourceExhausted.
- [ ] Make the existing 4 MiB message bound explicit and bound hook stdin without truncating input. Preserve transport-only hooks and visible failures.
- [ ] Charge encoded response capacity before marshaling and retain it through the last transport reference. Use the proposed aggregate response budget of 16 MiB. Do not implement the sealed gRPC Buffer interface.
- [ ] Expose the reservation behavior needed by Task 6 as a small reusable byte-budget primitive. Record its exact interface in the task report before a dependent task starts.
- [ ] Avoid copying KV values for metadata-only lists and reject replies that exceed the client ceiling before materializing them.
- [ ] Test both hook RPCs, cancellation, saturated admission, malformed input, slow readers, and successful replies using a real gRPC client and server. Measure transport overhead without asserting an unsupported hard bound across unlimited connections.
- [ ] Run targeted transport, daemon, KV, and hook tests. Commit.

## Task 6: Account for external jobs through actual completion

Consume the configuration and byte-reservation interface reported by Task 5. Correct that interface with evidence when necessary and update its consumers rather than copying accounting logic.

- [ ] Reserve logical job count and retained I/O before allocating or spawning. The proposed aggregate external buffer budget is 64 MiB; individual input/output defaults to 4 MiB and job count defaults to H.
- [ ] Keep owned job contexts independent of a returned RPC while retaining only explicit data and correlation facts. Do not retain SQLite handles or whole RPC contexts in background jobs.
- [ ] Apply the 30-second background setting to total logical job duration, not each retry or expanded command. Enforce local stdout and transcript byte bounds.
- [ ] Cancel owned process groups, stop renewals where applicable, and join cleanup. Use the proposed one-second pipe cleanup allowance. A canceled context does not release still-retained data credits.
- [ ] Preserve configured on_error results and reject truncated-success or cached-error outcomes.
- [ ] Test real local subprocesses with repeated attempts, expanded targets, oversized output, inherited descriptors, reload, and shutdown. Run targeted exec, rules, inference, and daemon tests. Commit.

## Task 7: Stream history and protect file lifetime

Consume Task 3 completed-file and catalog coordination behavior. Preserve offline CLI access and current live-query semantics.

- [ ] Select exact files before opening SQLite. Stream selected files using one family query claim for multi-file lifetime protection and the captured sequence boundary.
- [ ] Use bounded global keyset pages, revalidate selected ordering keys, and stream full results or scalar totals without retaining skipped offsets or a page for every file.
- [ ] Preserve deterministic ordering and receipt qualification. Do not infer completion-time bounds from a filename or promise an atomic cross-file snapshot.
- [ ] Keep cancellation, corrupt-file errors, and incomplete streamed output explicit. Do not copy or spool history.
- [ ] Test ordering, offsets, totals, large detail, concurrent pruning, late commits, completed files without sidecars, and offline queries. Run targeted query and catalog tests. Commit.

## Task 8: Prevent supervisor-tail rewrite amplification

- [ ] Retain the 1 MiB supervisor cap and inode. After overflow retain at most the newest 512 KiB of complete lines, with bounded handling of oversized incomplete output.
- [ ] Verify the open supervisor descriptor continues working, small subsequent appends do not trigger another full-tail copy, and diagnostic/fail-open policies retain their existing behavior.
- [ ] Run targeted logstorage tests and commit.

## Task 9: Qualify the combined correction

- [ ] Reuse the immutable Task 1 baseline and identical workload/fixture inputs. Extend the harness for the actual RPC entry points and resource lifetimes implemented above where initial evidence was narrower.
- [ ] Run independent paired repetitions for idle, steady, bursts, recovery, status/history queries, cancellation, rotation, reload, and both detail profiles. Include 0, 7, 80, and 160 retained files and backlog beyond dispatch capacity.
- [ ] Require CPU and disk improvement beyond baseline variation without latency, error-rate, throughput, or peak/settled-memory regression. Count final checkpoints and shutdown, and state connection/payload scope.
- [ ] Make failed or missing acceptance evidence return a failing status and fail the appropriate CI job. Replace or adapt the two legacy private-package qualification files through exported boundaries while preserving their active scenarios; do not suppress their tagged staticcheck findings. Do not print a successful command around a failed report.
- [ ] Run make test and the supported make build pipeline. The shared pipeline rejects the old make check and make lint aliases; Task 1 records those diagnostics. Complete a fresh whole-branch review of correctness, cancellation, resource ownership, and destructive paths.
- [ ] Publish the reviewed stack as ready for review, report qualification evidence and material limits, and preserve the verified candidate for any later authorized deployment.

## Coverage and adaptation

Tasks 2 and 3 implement storage ownership, direct dispatch, recovery, transitions, finalization, and maintenance triggers. Tasks 4 through 6 implement temporal identity, cache, incoming data, transport responses, and external job bounds. Tasks 7 and 8 implement history and logging. Tasks 1 and 9 establish evidence and regression enforcement for every workstream.

The execution method and implementation are already authorized. A task may correct an engineering assumption without another approval round when the user outcome and public contracts remain intact. Changes to public behavior or destructive scope require a concrete decision. Update dependent briefs before dispatch rather than freezing a disproved interface into later work.
