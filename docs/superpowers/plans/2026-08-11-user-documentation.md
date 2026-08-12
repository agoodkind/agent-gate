# User Documentation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give new users one setup path, one feature guide, and one audit storage guide without duplicating specialist contracts.

**Architecture:** Three task guides own user workflows. Hook Inventory, Hook Contract, and Judge Architecture retain specialist behavior. The repository landing page routes readers and contributor instructions own source development. One verifier checks every first-party page and executes each public command example against the built command line interface.

**Tech Stack:** Markdown, Go documentation tests, the built Agent Gate command line interface, Make, and POSIX shell examples.

The [approved design](../specs/2026-08-11-audit-lifecycle-design.md) defines the documentation contract.

## Global Constraints

- Implement AGATE-51, AGATE-52, AGATE-56, AGATE-50, and AGATE-53 in that order.
- Complete AGATE-29 before AGATE-51.
- Complete AGATE-31 before AGATE-52.
- Complete AGATE-31 and AGATE-32 before AGATE-50.
- Give each fact, procedure, command, and recovery step one durable home.
- Keep exact configuration keys in the annotated configuration.
- Keep exact command flags in command help.
- Link one destination at most once per page.
- Use a filename only when the reader must open, edit, or run that artifact.
- Make every named repository path a relative Markdown link.
- Preserve current specialist contracts and remove stale historical measurements.
- Verify examples through public behavior. Do not assert prose strings as a substitute.
- Read every changed page before editing and again after editing.
- Run make check before each ticket commit.
- Create one signed commit per ticket. Each commit can become one pull request.

## Durable Homes

| Subject | Durable home |
| --- | --- |
| Install, setup, first rule, first verified decision, upgrade, setup recovery | Getting Started |
| Actions, conditions, evaluators, provider behavior, queries, exports, updates, limits | Feature Guide |
| Profiles, detail state, retention, maintenance, compaction, storage recovery | Audit Storage |
| Registered events, provider capabilities, managed template behavior | Hook Inventory |
| Payload preservation, classification, provider wire contracts | Hook Contract |
| Judge inputs, verdict folding, caching, cost method, limitations | Judge Architecture |
| Source prerequisites, build, test, lint, deploy, generated files | Contributor Guide |
| Product summary, release install, route to durable homes | Repository landing page |

## File Structure

AGATE-51:

~~~text
docs/getting-started.md
documentation_test.go
~~~

AGATE-52:

~~~text
docs/features.md
documentation_test.go
~~~

AGATE-56:

~~~text
HOOKS.md
docs/hook-schemas.md
docs/judge.md
AGENTS.md
documentation_test.go
~~~

AGATE-50:

~~~text
docs/audit-storage.md
documentation_test.go
~~~

AGATE-53:

~~~text
README.md
CONTRIBUTING.md
documentation_test.go
scripts/check-doc-commands.sh
scripts/testdata/doc-command-environment.sh
~~~

## Task 1: Create Getting Started

**Ticket:** AGATE-51

**Depends on:** AGATE-49

**Files:** Use the AGATE-51 file set above.

- [ ] **Step 1: Inventory overlapping setup procedures**

Read the repository landing page, Hook Inventory, annotated configuration, installer, setup help, and query help. Record every install, setup, first-rule, reload, verification, upgrade, and repair instruction.

Assign each item to Getting Started or delete it as implementation detail. Do not keep a duplicate with a link.

- [ ] **Step 2: Extend documentation discovery**

Add one helper that discovers first-party Markdown from explicit roots. Exclude vendored and planning documents.

~~~go
func firstPartyDocumentationPaths(t *testing.T) []string
~~~

Include the repository landing page, contributor guide when present, Hook Inventory, and every user or specialist page under the documentation directory.

Change local-link verification to use that set. The new guide must fail a broken relative link test.

Run:

~~~sh
go test . -run FirstPartyDocumentationLocalLinks
~~~

Expected: FAIL until the new guide and links exist.

- [ ] **Step 3: Write one complete first-use path**

Start with the successful outcome. Cover this sequence:

1. Check release prerequisites.
2. Run the single release installer.
3. Run interactive setup and interpret provider results.
4. Open the annotated configuration and add one deterministic rule.
5. Run configuration validation.
6. Save and verify the reload result.
7. Trigger one harmless matching event.
8. Query its durable receipt and decision.
9. Upgrade after reviewing the audit retention warning.
10. Recover each setup layer with its exact repair command.

Link the annotated configuration once for the edit. Link Audit Storage once for the breaking retention behavior. Do not copy profile values or flags. Until AGATE-50 creates Audit Storage, omit that link and keep the warning local to Getting Started.

- [ ] **Step 4: Verify commands and links**

Run every documented command against a test home and state directory. The setup example must enter through the public setup command with scripted terminal input.

Run:

~~~sh
go test . ./cmd/agent-gate -run "Documentation|Setup"
make check
~~~

Expected: both commands exit zero.

- [ ] **Step 5: Commit**

~~~sh
git add docs/getting-started.md documentation_test.go
git commit -S -m "Add Agent Gate Getting Started guide" -m "Co-authored-by: Codex <noreply@openai.com>"
~~~

## Task 2: Create Feature Guide

**Ticket:** AGATE-52

**Depends on:** AGATE-37

**Files:** Use the AGATE-52 file set above.

- [ ] **Step 1: Map feature prose to behavior sources**

Read the repository landing page, Hook Inventory, Hook Contract, Judge Architecture, annotated configuration, command help, and relevant behavior tests.

Map each retained claim to one public behavior source. Delete copied implementation values and historical measurements.

- [ ] **Step 2: Write the user feature model**

Explain behavior in this order:

1. The hook-to-decision path.
2. Rule targeting and all-match conditions.
3. Block, audit, inject, and mutate actions.
4. Deterministic, external, and inference evaluators.
5. Provider capability differences.
6. Fail-open transport and configured evaluator errors.
7. Intake, decision, evaluation, and cost queries.
8. Complete-detail exports and skipped expired detail.
9. Update behavior.
10. Current limitations.

Use two practical rules that exercise different actions. Link Hook Inventory, Hook Contract, Judge Architecture, and the annotated configuration at most once each.

- [ ] **Step 3: Verify public examples**

Extend command example verification to cover feature guide code blocks. Use temporary configuration and SQLite state. Assert exit status, structured output, or persisted results.

Keep validator example tests focused on executing the validator. Remove assertions that merely require prose fragments.

Run:

~~~sh
go test . ./cmd/agent-gate -run "Documentation|Query|Export|Update"
make check
~~~

Expected: both commands exit zero.

- [ ] **Step 4: Commit**

~~~sh
git add docs/features.md documentation_test.go
git commit -S -m "Add Agent Gate Feature Guide" -m "Co-authored-by: Codex <noreply@openai.com>"
~~~

## Task 3: Restore specialist documentation boundaries

**Ticket:** AGATE-56

**Depends on:** AGATE-51 and AGATE-52

**Files:** Use the AGATE-56 file set above.

- [ ] **Step 1: Compare every specialist claim**

Read each specialist page in full. Compare its workflows, commands, provider behavior, and failure claims with the two user guides.

For every overlap, keep the specialist contract or the user task. Delete the other copy. Preserve unique provider events, wire shapes, response fields, and judge failure semantics.

- [ ] **Step 2: Narrow Hook Inventory**

Retain registered events, capability mappings, managed ownership, and template behavior. Remove generic response-action explanations and repair command sequences already handled by the guides.

Keep one link to Hook Contract only when the reader needs exact payload or response behavior.

- [ ] **Step 3: Narrow Hook Contract and Judge Architecture**

Keep Hook Contract limited to input preservation, provider classification, payload normalization, responses, virtual fields, and evidence contracts.

Keep Judge Architecture limited to inputs, structural analysis, verdict folding, batching, caching, cost method, and current limitations. Remove the time-bound spend snapshot and generic query workflow.

- [ ] **Step 4: Correct operational agent guidance**

Delete the obsolete sentence that says direct SQLite inspection is required until a command line interface exists. Retain the historical cutover boundary only while it changes how agents query pre-cutover records.

- [ ] **Step 5: Verify fidelity and accuracy**

Compare the before and after pages. Account for every removed claim. Verify remaining provider event names and capabilities through shipped templates and behavior tests.

Run:

~~~sh
go test . ./internal/hook ./internal/install -run "Documentation|Provider|Schema|Install"
make check
~~~

Expected: both commands exit zero.

- [ ] **Step 6: Commit**

~~~sh
git add HOOKS.md docs/hook-schemas.md docs/judge.md AGENTS.md documentation_test.go
git commit -S -m "Restore Agent Gate specialist documentation scope" -m "Co-authored-by: Codex <noreply@openai.com>"
~~~

## Task 4: Create Audit Storage

**Ticket:** AGATE-50

**Depends on:** AGATE-44

**Files:** Use the AGATE-50 file set above.

- [ ] **Step 1: Verify the shipped storage contract**

Read the resolved policy tests, migration tests, maintenance command help, scheduler tests, compaction tests, and annotated configuration.

Record the behavior that operators must understand. Leave exact keys and flags in their source-owned references.

- [ ] **Step 2: Write the storage guide**

Cover this order:

1. State the bounded `balanced` default and destructive upgrade effect.
2. Compare `balanced`, `full`, and `minimal` by behavior.
3. Explain summary and detail classes.
4. Explain available, expired, not-recorded, and protected states.
5. Configure a profile and explicit overrides.
6. Preview age and size deletion.
7. Apply bounded maintenance.
8. Read status and constrained size results.
9. Use incremental compaction.
10. Run explicit offline full compaction.
11. Schedule the public maintenance command externally.
12. Recover failed maintenance or compaction.

State that startup never runs or waits for maintenance. State that the first automatic timer starts after readiness and waits one full interval.

Link the annotated configuration once for editing exact keys. Command help remains the only exact flag reference.

- [ ] **Step 3: Add the breaking release warning**

Use this warning in the guide and the release description:

~~~text
Audit storage now defaults to balanced retention. The first due maintenance run may delete completed detail older than 7 days and completed summaries older than 30 days. Export or copy the audit database before upgrading when you need an archive.
~~~

Replace the temporary warning in Getting Started with one link to this guide.

The pull request does not prove release publication. Keep the ticket open until the release description carries this warning.

- [ ] **Step 4: Verify all storage examples**

Run status, preview, apply, incremental compaction, and full dry-run examples against temporary databases. Do not run full apply from documentation verification.

Assert startup tests still prove no maintenance, integrity scan, checkpoint, or reclaim occurs before readiness.

Run:

~~~sh
go test . ./internal/auditmaintenance ./internal/daemon ./cmd/agent-gate -run "Documentation|Maintenance|Compact"
make check
~~~

Expected: both commands exit zero.

- [ ] **Step 5: Commit**

~~~sh
git add docs/audit-storage.md docs/getting-started.md documentation_test.go
git commit -S -m "Add Agent Gate Audit Storage guide" -m "Co-authored-by: Codex <noreply@openai.com>"
~~~

## Task 5: Reduce landing and contributor pages to durable scope

**Ticket:** AGATE-53

**Depends on:** AGATE-50, AGATE-51, AGATE-52, and AGATE-56

**Files:** Use the AGATE-53 file set above.

- [ ] **Step 1: Move contributor workflows**

Create Contributor Guide from current source prerequisites, clone, build, test, lint, check, deploy, status, and protocol generation material.

Verify every Make target against the Makefiles. Keep operational explanations only when a contributor needs them to run the target safely.

- [ ] **Step 2: Reduce the repository landing page**

Keep one short product description and one release install command. Link once to Getting Started, Feature Guide, Audit Storage, Hook Inventory, Hook Contract, Judge Architecture, and Contributor Guide.

Remove setup, configuration, feature, query, update, storage, repair, and source-build procedures now owned elsewhere.

- [ ] **Step 3: Replace fixed documentation tests**

Delete fixed three-page lists and static prose-presence assertions. Use the discovered first-party set for local links, duplicate destinations, installer flags, Make targets, provider specialist coverage, and command examples.

Add a shell verifier that:

1. Builds the current Agent Gate binary once.
2. Extracts command blocks tagged with `<!-- doc-test: run -->` or `<!-- doc-test: run fixture=query -->`.
3. Runs safe commands in a temporary home and state directory.
4. Supplies recorded fixtures for query and export examples.
5. Rejects destructive full compaction apply examples.
6. Reports the page, line, command, exit status, and stderr on failure.

Require a concrete skip tag, such as `<!-- doc-test: skip reason=requires-release-network -->`, before every intentionally unexecuted shell block. Reject an untagged shell block.

Start the script with strict mode and clean temporary files on exit, interrupt, or termination. Keep shell fixtures in their own shell file. Make the verifier executable and invoke it from one Go documentation test.

- [ ] **Step 4: Audit links and paths**

For every changed page, inventory Markdown links and path-like prose. Remove duplicate destinations. Replace every action path with one relative clickable link. Delete every filename used only to assert ownership.

Read the complete document set after the changes. Confirm one durable home per fact and no stale cross-document copy.

- [ ] **Step 5: Run final epic verification**

Run:

~~~sh
bash scripts/check-doc-commands.sh
go test . -run Documentation
make test
make lint
make check
~~~

Expected: all commands exit zero.

- [ ] **Step 6: Commit**

~~~sh
git add README.md CONTRIBUTING.md documentation_test.go scripts/check-doc-commands.sh scripts/testdata/doc-command-environment.sh
git commit -S -m "Consolidate Agent Gate documentation entry points" -m "Co-authored-by: Codex <noreply@openai.com>"
~~~

## Epic Acceptance

- [ ] A new user reaches a verified durable decision from one guide.
- [ ] Feature behavior and limitations have one user-facing home.
- [ ] Audit storage explains the breaking default before any maintenance action.
- [ ] Specialist pages contain exact contracts without task duplication.
- [ ] Source development has one contributor home.
- [ ] The landing page routes readers without copying procedures.
- [ ] Every local link resolves and every destination appears at most once per page.
- [ ] Public command examples execute against real behavior.
- [ ] Startup documentation and tests agree that maintenance waits one full interval after readiness.
- [ ] Every ticket commit passes make check.
