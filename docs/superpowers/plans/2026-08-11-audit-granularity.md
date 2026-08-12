# Audit Storage Granularity Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let operators choose retained audit detail while preserving durable summaries and live replay work.

**Architecture:** Configuration resolves one immutable storage policy. SQLite stores operational summaries separately from sensitive detail. Intake, evaluation, and outbox writes commit both projections together. Queries expose detail state instead of inventing empty content.

**Tech Stack:** Go, pelletier/go-toml/v2, database/sql, mattn/go-sqlite3 with FTS5, the existing command line interface, and Go tests.

The [approved design](../specs/2026-08-11-audit-lifecycle-design.md) defines the product contract.

## Global Constraints

- Implement AGATE-33, AGATE-34, AGATE-35, AGATE-54, AGATE-55, AGATE-36, and AGATE-37 in that order.
- Keep hook receipt writes durable before evaluation.
- Preserve every pending, claimed, retryable, or undelivered payload.
- Store cost inputs in summary rows.
- Treat a missing `[audit.storage]` configuration table as the balanced profile.
- Keep migrations transactional and idempotent.
- Use real temporary SQLite databases in tests.
- Run the focused test after each red and green step.
- Run make check before each ticket commit.
- Create one signed commit per ticket. Each commit can become one pull request.

## File Structure

AGATE-33:

~~~text
internal/config/audit_storage.go
internal/config/audit_storage_test.go
internal/config/config.go
internal/config/load.go
internal/config/default_config.toml
config.toml.example
cmd/agent-gate/main.go
cmd/agent-gate/main_test.go
~~~

AGATE-34:

~~~text
internal/auditstorage/types.go
internal/auditstorage/schema.go
internal/auditstorage/schema_test.go
internal/auditstorage/v1.go
internal/auditstorage/v1_test.go
internal/auditstorage/testdata/legacy_v1.sql
internal/intake/store.go
internal/intake/receipt_test.go
internal/intake/deferred_audit_outbox.go
internal/intake/deferred_audit_outbox_test.go
internal/evaluation/store.go
internal/evaluation/store_test.go
internal/audit/session.go
internal/audit/event_logger_test.go
~~~

AGATE-35:

~~~text
internal/auditstorage/types.go
internal/intake/store.go
internal/intake/schema_helpers.go
internal/intake/query.go
internal/intake/store_test.go
internal/intake/receipt_test.go
internal/daemon/server.go
internal/daemon/hot_ledger_test.go
internal/auditstorage/intake_v2.go
internal/auditstorage/intake_v2_test.go
~~~

AGATE-54:

~~~text
internal/evaluation/types.go
internal/evaluation/store.go
internal/evaluation/store_test.go
internal/evaluation/query.go
internal/evaluation/cost.go
internal/evaluation/cost_test.go
internal/evaluation/metadata.go
internal/intake/evaluation_commit.go
internal/intake/atomic_evaluation_test.go
internal/auditstorage/evaluation_v3.go
internal/auditstorage/evaluation_v3_test.go
~~~

AGATE-55:

~~~text
internal/intake/deferred_audit_outbox.go
internal/intake/deferred_audit_outbox_test.go
internal/intake/evaluation_commit.go
internal/intake/atomic_evaluation_test.go
internal/auditstorage/outbox_v4.go
internal/auditstorage/outbox_v4_test.go
~~~

AGATE-36:

~~~text
internal/auditstorage/types.go
internal/audit/query.go
internal/audit/event_logger_test.go
internal/intake/query.go
internal/intake/query_test.go
internal/evaluation/query.go
internal/evaluation/query_test.go
cmd/agent-gate/main.go
cmd/agent-gate/main_test.go
~~~

AGATE-37:

~~~text
internal/evaluation/query.go
internal/evaluation/query_test.go
cmd/agent-gate/main.go
cmd/agent-gate/main_test.go
~~~

## Task 1: Resolve audit storage profiles and overrides

**Ticket:** AGATE-33

**Files:** Use the AGATE-33 file set above.

- [ ] **Step 1: Write failing profile tests**

Cover the absent table, all three profiles, each explicit override, and explicit false values.

~~~go
func TestAuditStoragePolicyDefaultsToBalanced(t *testing.T) {
    cfg := loadConfigText(t, "[audit]\nenabled = true\n")

    policy := cfg.AuditStoragePolicy()

    if policy.Profile != config.AuditStorageProfileBalanced {
        t.Fatalf("profile = %q, want balanced", policy.Profile)
    }
    if policy.FullDetailRetention != 168*time.Hour {
        t.Fatalf("detail retention = %s, want 168h", policy.FullDetailRetention)
    }
    if policy.SummaryRetention != 720*time.Hour {
        t.Fatalf("summary retention = %s, want 720h", policy.SummaryRetention)
    }
}
~~~

Add table tests for these cases:

~~~text
balanced: detail 168h, summary 720h
full: detail 720h, summary 720h
minimal: detail 0, summary 720h
balanced and full: every configurable detail class enabled
minimal: every configurable detail class disabled after terminal completion
max_size_mb = 0: valid and disabled
max_size_mb < 0: rejected
compact_after_maintenance = false: preserved
wire_input = false: preserved
summary shorter than detail: rejected
zero or negative duration: rejected
unknown profile: rejected
maintenance_batch_rows <= 0: rejected
maintenance_interval <= 0: rejected
~~~

Run:

~~~sh
go test ./internal/config -run AuditStorage
~~~

Expected: FAIL because the policy types and accessor do not exist.

- [ ] **Step 2: Add raw configuration and effective policy types**

Use pointers for fields whose explicit zero or false value differs from omission.

~~~go
type AuditStorage struct {
    Profile                 string             `toml:"profile"`
    MaintenanceInterval     *string            `toml:"maintenance_interval"`
    MaxSizeMB               *int64             `toml:"max_size_mb"`
    MaintenanceBatchRows    *int               `toml:"maintenance_batch_rows"`
    CompactAfterMaintenance *bool              `toml:"compact_after_maintenance"`
    FullDetailRetention     *string            `toml:"full_detail_retention"`
    SummaryRetention        *string            `toml:"summary_retention"`
    Detail                  AuditStorageDetail `toml:"detail"`
}

type AuditStorageDetail struct {
    WireInput           *bool `toml:"wire_input"`
    NormalizedInput     *bool `toml:"normalized_input"`
    ProviderEvidence    *bool `toml:"provider_evidence"`
    EnvironmentEvidence *bool `toml:"environment_evidence"`
    EvaluationContent   *bool `toml:"evaluation_content"`
}

type AuditStorageDetailPolicy struct {
    WireInput           bool
    NormalizedInput     bool
    ProviderEvidence    bool
    EnvironmentEvidence bool
    EvaluationContent   bool
}

type AuditStoragePolicy struct {
    Profile                 AuditStorageProfile
    MaintenanceInterval     time.Duration
    MaxSizeBytes            int64
    MaintenanceBatchRows    int
    CompactAfterMaintenance bool
    FullDetailRetention     time.Duration
    SummaryRetention        time.Duration
    Detail                  AuditStorageDetailPolicy
}
~~~

Use zero full-detail retention only for minimal profile semantics. Reject an explicit zero duration.

- [ ] **Step 3: Resolve and validate one policy**

Add one resolver used by strict load, degraded load, config check, setup, daemon reload, and maintenance.

~~~go
func resolveAuditStorage(raw AuditStorage) (AuditStoragePolicy, error)

func (c *Config) AuditStoragePolicy() AuditStoragePolicy {
    return c.auditStoragePolicy
}
~~~

Resolve and retain the policy during load. Do not silently replace an invalid configured policy inside the accessor.

Add audit storage validation to the section validator. Strict load rejects invalid storage. A degraded initial load records the failure, retains all detail, and disables maintenance until a valid reload. A degraded reload retains the active runtime snapshot and deadline.

Run:

~~~sh
go test ./internal/config -run AuditStorage
~~~

Expected: PASS.

- [ ] **Step 4: Print the effective policy**

Extend config check output with stable values that setup and operators can inspect.

~~~text
agent-gate: config ok
audit storage: balanced
full detail: 168h0m0s
summary: 720h0m0s
size target: disabled
maintenance: every 24h0m0s, 1000 rows per batch
~~~

Test stdout and invalid configuration through the public command runner.

Run:

~~~sh
go test ./cmd/agent-gate -run ConfigCheck
~~~

Expected: PASS.

- [ ] **Step 5: Update configuration sources**

Add the balanced table to the embedded default. Add all supported keys and comments to the annotated configuration. Keep exact values only in configuration sources and command help.

Run:

~~~sh
make test
make check
~~~

Expected: both commands exit zero.

- [ ] **Step 6: Commit**

~~~sh
git add internal/config cmd/agent-gate/main.go cmd/agent-gate/main_test.go config.toml.example
git commit -S -m "Add audit storage policy resolution" -m "Co-authored-by: Codex <noreply@openai.com>"
~~~

## Task 2: Version SQLite audit schema migrations

**Ticket:** AGATE-34

**Depends on:** AGATE-33

**Files:** Use the AGATE-34 file set above.

- [ ] **Step 1: Write migration runner tests**

Open a populated legacy fixture through the public intake store. Assert the runner records the current schema as version one without changing application rows.

~~~go
func TestOpenSQLiteRecordsLegacyAuditSchemaVersion(t *testing.T) {
    path := installLegacyAuditFixture(t)

    store, err := intake.OpenSQLite(t.Context(), path, nil)
    if err != nil {
        t.Fatalf("OpenSQLite migration: %v", err)
    }
    t.Cleanup(func() { _ = store.Close() })

    assertLegacySummaryFidelity(t, store)
    assertSchemaVersion(t, store.Handle(), 1)
    assertForeignKeysClean(t, store.Handle())
}
~~~

Populate the fixture with intake, receipt, deferred, evaluation, label, audit, and outbox rows. Also reopen the database and compare results. Inject a failure before the version update and assert no version or partial metadata remains.

Run:

~~~sh
go test ./internal/auditstorage ./internal/intake -run Migration
~~~

Expected: FAIL because no shared migration runner exists.

- [ ] **Step 2: Add one ordered migration runner**

Use one package to own migration order without importing intake or evaluation.

~~~go
type Migration struct {
    Version int
    Apply   func(context.Context, *sql.Tx) error
}

func Migrate(ctx context.Context, database *sql.DB) error
func SchemaVersion(ctx context.Context, database *sql.DB) (int, error)
func MigrationAppliedAt(ctx context.Context, database *sql.DB, version int) (time.Time, error)
~~~

Keep the ordered migration registry inside auditstorage. Record each version and application time in `audit_schema_migrations`. Run every pending version in its own transaction. Execute `pragma foreign_key_check` inside that transaction before recording the version, setting `pragma user_version`, and committing. A failed version must roll back completely.

Move all current intake, evaluation, audit, and outbox schema creation into the version-one migration. Every database constructor calls the same migration entry point. Constructors may wrap the migrated handle but must not create application tables afterward.

- [ ] **Step 3: Enable incremental auto-vacuum for new databases**

Assert a new database reports incremental auto-vacuum. Assert a legacy database reports that full compaction is required without changing its mode.

Set `pragma auto_vacuum = incremental` before creating the first application table in a new file. Never run `VACUUM` while opening a legacy file.

Run:

~~~sh
go test ./internal/auditstorage ./internal/intake -run Migration
~~~

Expected: PASS.

- [ ] **Step 4: Run repository verification**

~~~sh
make test
make check
~~~

Expected: both commands exit zero.

- [ ] **Step 5: Commit**

~~~sh
git add internal/auditstorage internal/intake internal/evaluation internal/audit
git commit -S -m "Version SQLite audit schema migrations" -m "Co-authored-by: Codex <noreply@openai.com>"
~~~

## Task 3: Split intake summaries from replay detail

**Ticket:** AGATE-35

**Depends on:** AGATE-34

**Files:** Use the AGATE-35 file set above.

- [ ] **Step 1: Write populated intake migration tests**

Use the public store methods. Cover legacy fidelity, idempotence, a real append, deferred replay, a reopened database, and an injected migration failure.

~~~go
func TestAppendCommitsSummaryAndConfiguredDetailTogether(t *testing.T)
func TestOpenSQLiteMigratesLegacyIntakeDetail(t *testing.T)
func TestPendingReplayProtectsDisabledIntakeDetail(t *testing.T)
func TestAppendRollsBackSummaryWhenDetailWriteFails(t *testing.T)
~~~

Assert database rows and returned records. Do not assert collaborator call order.

Run:

~~~sh
go test ./internal/intake ./internal/daemon -run "Detail|Atomic|Replay"
~~~

Expected: FAIL because writes still use legacy mixed columns.

- [ ] **Step 2: Define cycle-neutral detail identity**

~~~go
type DetailState string

type DetailClass string

const (
    DetailStateAvailable   DetailState = "available"
    DetailStateExpired     DetailState = "expired"
    DetailStateNotRecorded DetailState = "not_recorded"
    DetailStateProtected   DetailState = "protected"
)
~~~

Define these shared types in `internal/auditstorage/types.go`. Intake, audit,
and evaluation may import `auditstorage`; none may import another store package
to obtain detail types.

Schema version two creates `intake_event_details` for content and `intake_event_detail_manifest` for recorded classes, available classes, state, and state change time.

- [ ] **Step 3: Pass one policy into the store**

Replace positional opening arguments with a typed option.

~~~go
type SQLiteOptions struct {
    Path   string
    Policy config.AuditStoragePolicy
    Log    *slog.Logger
}

func OpenSQLiteWithOptions(ctx context.Context, options SQLiteOptions) (*Store, error)
~~~

Add this typed replacement as `OpenSQLiteWithOptions` for one compatibility release. Keep the existing positional `OpenSQLite` as a balanced-policy wrapper. Update daemon construction and new tests to use the typed function. Remove the wrapper only in a separately announced application programming interface break. Never read live configuration from inside a transaction.

- [ ] **Step 4: Migrate and split intake writes**

Within the existing append transaction:

1. Insert identity, operation, hashes, and timestamps into summary storage.
2. Insert every replay-required class into protected detail storage.
3. Insert an event manifest.
4. Insert the durable receipt.
5. Commit once.

The profile controls terminal retention, not the durable receipt boundary. Keep disabled classes protected until AGATE-55 adds whole-graph terminal demotion.

Migration copies values before clearing compatibility columns. New code must not read or populate those columns.

- [ ] **Step 5: Prove daemon intake uses the effective policy**

Start a real server with minimal policy. Send a hook event that enters deferred work. Assert its payload remains readable until evaluation and outbox delivery complete.

Run:

~~~sh
go test ./internal/daemon -run "Ledger|Detail"
go test ./internal/intake -run "Detail|Atomic|Replay"
~~~

Expected: PASS.

- [ ] **Step 6: Run repository verification**

~~~sh
make test
make check
~~~

Expected: both commands exit zero.

- [ ] **Step 7: Commit**

~~~sh
git add internal/auditstorage internal/intake internal/daemon
git commit -S -m "Split intake summaries from replay detail" -m "Co-authored-by: Codex <noreply@openai.com>"
~~~

## Task 4: Split evaluation summaries from training detail

**Ticket:** AGATE-54

**Depends on:** AGATE-35

**Files:** Use the AGATE-54 file set above.

- [ ] **Step 1: Write evaluation migration and atomicity tests**

~~~go
func TestOpenSQLiteMigratesLegacyEvaluationDetail(t *testing.T)
func TestRecordCompletedCommitsEvaluationSummaryAndDetail(t *testing.T)
func TestCostReportSurvivesEvaluationDetailRemoval(t *testing.T)
func TestEvaluationDetailFailureRollsBackSummary(t *testing.T)
~~~

Run:

~~~sh
go test ./internal/evaluation ./internal/intake -run "Detail|Cost|Atomic"
~~~

Expected: FAIL because evaluation rows still mix summary and detail.

- [ ] **Step 2: Add schema version three**

Create `gate_evaluation_details`, `gate_evaluation_layer_details`, and `gate_evaluation_label_details`. Move exact input, output, full metadata, error text, and rationale.

Add summary columns for request identity, token totals, cost inputs, rule filters, and detail state.

- [ ] **Step 3: Project cost metadata**

~~~go
type CostMetadata struct {
    UpstreamMetadataStatus string
    RequestID              string
    RequestedModel         string
    PromptTokens           int64
    CachedTokens           int64
    CompletionTokens       int64
    CacheStatus            string
    CacheKeyHash           string
}

func ProjectCostMetadata(raw json.RawMessage) (CostMetadata, error)
~~~

Make CostReport read only summary columns.

- [ ] **Step 4: Write both halves through existing transactions**

Keep RecordCompletedInTx and its caller-owned transaction. Apply profile recording choices after validation. Protect evaluation content while its receipt can retry.

Run:

~~~sh
go test ./internal/evaluation ./internal/intake -run "Detail|Cost|Atomic"
make check
~~~

Expected: both commands exit zero.

- [ ] **Step 5: Commit**

~~~sh
git add internal/auditstorage internal/evaluation internal/intake
git commit -S -m "Split evaluation summaries from training detail" -m "Co-authored-by: Codex <noreply@openai.com>"
~~~

## Task 5: Split deferred audit headers from payload detail

**Ticket:** AGATE-55

**Depends on:** AGATE-54

**Files:** Use the AGATE-55 file set above.

- [ ] **Step 1: Write live delivery tests**

Test partial delivery, restart, expired claim retry, completed payload demotion, and corruption where a live payload is missing.

~~~go
func TestPendingDeferredAuditAlwaysRetainsPayload(t *testing.T)
func TestCompletedDeferredAuditCanDemotePayload(t *testing.T)
func TestClaimDeferredAuditRejectsMissingLivePayload(t *testing.T)
func TestHotCompletionWithoutOutboxDemotesDisabledDetail(t *testing.T)
func TestZeroEntryOutboxCompletionDemotesDisabledDetail(t *testing.T)
~~~

Run:

~~~sh
go test ./internal/intake -run DeferredAudit
~~~

Expected: FAIL because payload bytes remain in the header entry table.

- [ ] **Step 2: Add schema version four**

Create `deferred_audit_outbox_entry_details` keyed by receipt and entry index. Keep audit event identity, delivery time, recorded state, available state, and state change time in the header entry.

- [ ] **Step 3: Migrate and split outbox writes**

Insert header, detail, and evaluation rows through the existing deferred evaluation transaction. Claims read payload detail only after proving the entry remains pending. Missing live detail returns an integrity error without marking delivery complete.

Use one whole-graph terminal check after both completion paths. Call it in the
hot evaluation transaction even when no deferred outbox exists. Call it again
after deferred outbox creation, zero-entry completion, and final delivery.
When the last receipt and outbox entry is terminal, delete every class disabled
by policy and mark it not_recorded in that same transaction. Keep enabled
classes for age retention.

Run:

~~~sh
go test ./internal/intake -run "DeferredAudit|Atomic"
make check
~~~

Expected: both commands exit zero.

- [ ] **Step 4: Commit**

~~~sh
git add internal/auditstorage internal/intake
git commit -S -m "Split deferred audit payload detail" -m "Co-authored-by: Codex <noreply@openai.com>"
~~~

## Task 6: Report detail state in queries

**Ticket:** AGATE-36

**Depends on:** AGATE-35, AGATE-54, and AGATE-55

**Files:** Use the AGATE-36 file set above.

- [ ] **Step 1: Write query boundary tests**

Create current and migrated databases containing all four effective states. Query through the public command runner.

~~~json
{
  "event_id": "intake_123",
  "detail": {
    "state": "expired",
    "recorded_classes": ["wire_input", "normalized_input"],
    "available_classes": []
  }
}
~~~

Assert expired and not-recorded fields are absent. Assert protected payloads remain present. Preserve existing filters and default table columns.

Run:

~~~sh
go test ./internal/intake ./internal/audit ./internal/evaluation ./cmd/agent-gate -run Query
~~~

Expected: FAIL because query projections have no detail state.

- [ ] **Step 2: Add one public detail projection**

~~~go
type DetailProjection struct {
    State            DetailState   `json:"state"`
    RecordedClasses  []DetailClass `json:"recorded_classes"`
    AvailableClasses []DetailClass `json:"available_classes"`
    ExpiredAt        string        `json:"expired_at,omitempty"`
}
~~~

Add `DetailProjection` beside `DetailState` and `DetailClass` in the
cycle-neutral `auditstorage` package. Every query package uses that projection.

Compute protected state from pending and undelivered relationships at query time. Use the stored manifest for the other states.

Resolve state for the classes requested by that query. Return protected when live work retains a class beyond policy. Otherwise return expired when a recorded class is missing. Return not_recorded when no class expired and at least one requested class was never retained after terminal completion. Return available only when every requested class is present. Recorded and available class lists preserve mixed-state detail.

- [ ] **Step 3: Join detail only when requested**

Keep default seen queries content-safe. Load normalized input and environment evidence only when their existing flags request them. Load evaluation detail for JSON evaluation queries.

Never decode an empty compatibility column as genuine detail.

- [ ] **Step 4: Render missing detail plainly**

JSON omits unavailable content and includes detail state. Table output adds one detail column with the exact state.

Run:

~~~sh
go test ./internal/intake ./internal/audit ./internal/evaluation ./cmd/agent-gate -run Query
~~~

Expected: PASS.

- [ ] **Step 5: Run repository verification**

~~~sh
make test
make check
~~~

Expected: both commands exit zero.

- [ ] **Step 6: Commit**

~~~sh
git add internal/audit internal/intake internal/evaluation cmd/agent-gate
git commit -S -m "Report audit detail state in queries" -m "Co-authored-by: Codex <noreply@openai.com>"
~~~

## Task 7: Guard training exports

**Ticket:** AGATE-37

**Depends on:** AGATE-36

**Files:** Use the AGATE-37 file set above.

- [ ] **Step 1: Write public export tests**

Test complete, expired, not-recorded, and mixed selections.

~~~go
func TestExportEvaluationsRejectsIncompleteDetail(t *testing.T)
func TestExportEvaluationsSkipExpiredDetailReportsCount(t *testing.T)
func TestExportEvaluationsFiltersBeforeCheckingDetail(t *testing.T)
~~~

The default error must include the earliest selected record whose complete detail remains available. Print none when no selected complete record exists.

Run:

~~~sh
go test ./cmd/agent-gate ./internal/evaluation -run Export
~~~

Expected: FAIL because export does not enforce completeness.

- [ ] **Step 2: Add completeness to the query result**

~~~go
type DetailCompleteness struct {
    IncompleteCount          int
    EarliestCompleteDetailAt *time.Time
}
~~~

Calculate completeness after all user filters and before pagination truncates the selected export.

- [ ] **Step 3: Add the explicit skip flag**

Default behavior:

~~~text
agent-gate export evaluations: 3 selected evaluations lack complete detail; complete detail starts at 2026-08-04T12:00:00Z
~~~

Skip behavior writes only complete rows to stdout. It writes the omitted count to stderr.

Run:

~~~sh
go test ./cmd/agent-gate ./internal/evaluation -run Export
~~~

Expected: PASS.

- [ ] **Step 4: Run final epic verification**

~~~sh
make test
make lint
make check
~~~

Expected: all commands exit zero.

- [ ] **Step 5: Commit**

~~~sh
git add internal/evaluation cmd/agent-gate
git commit -S -m "Guard evaluation exports against missing detail" -m "Co-authored-by: Codex <noreply@openai.com>"
~~~

## Epic Acceptance

- [ ] A config without an audit storage table resolves to balanced.
- [ ] All profiles and overrides produce one effective policy.
- [ ] Legacy data migrates transactionally and remains queryable.
- [ ] New writes commit summaries and detail together.
- [ ] Live replay and outbox work retains required payloads.
- [ ] Cost queries work through the summary window.
- [ ] Queries expose accurate detail state.
- [ ] Training export fails safely unless skipping is explicit.
- [ ] Every ticket commit passes make check.
