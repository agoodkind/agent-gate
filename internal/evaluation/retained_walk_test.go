package evaluation_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"goodkind.io/agent-gate/internal/audit"
	"goodkind.io/agent-gate/internal/auditstorage"
	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/evaluation"
	"goodkind.io/agent-gate/internal/intake"
)

type retainedFixture struct {
	cfg     *config.Config
	catalog *auditstorage.Catalog
	handles []*auditstorage.BucketHandle
	want    []string
	base    time.Time
}

func newRetainedFixture(t *testing.T, count int, minimal bool) retainedFixture {
	t.Helper()
	root := t.TempDir()
	cfg := fixtureConfig(t, filepath.Join(root, "audit.db"))
	if err := cfg.PrepareAuditStorage(); err != nil {
		t.Fatal(err)
	}
	catalog, err := auditstorage.NewCatalog(cfg.AuditCatalogOptions())
	if err != nil {
		t.Fatal(err)
	}
	fixture := retainedFixture{cfg: cfg, catalog: catalog, base: time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)}
	for bucketIndex := range 2 {
		bucket, err := catalog.EnsureCurrent(t.Context(), cfg.AuditStoragePolicy().Rotation(), time.Now().Add(-time.Duration(bucketIndex)*24*time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		handle, err := catalog.OpenWriter(t.Context(), bucket)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = handle.Close() })
		fixture.handles = append(fixture.handles, handle)
		policy := cfg.AuditStoragePolicy()
		if minimal && bucketIndex == 1 {
			policy.Detail.EvaluationContent = false
		}
		store, err := intake.NewStore(t.Context(), handle.Database, policy, nil)
		if err != nil {
			t.Fatal(err)
		}
		for index := range count {
			at := fixture.base.Add(time.Duration(index) * time.Nanosecond)
			if index%2 == 1 {
				at = at.In(time.FixedZone("offset", -7*60*60))
			}
			id := fmt.Sprintf("row-%03d", index)
			receipt, err := store.Append(t.Context(), intake.Record{EventID: id, RecordedAt: at, System: "codex", RawPayload: []byte(`{}`)})
			if err != nil {
				t.Fatal(err)
			}
			record := completeRecord(receipt)
			record.Evaluation.EvaluationID = id
			record.Evaluation.StartedAt, record.Evaluation.CompletedAt = at, at
			record.Evaluation.ErrorJSON = nil
			record.Layers = record.Layers[1:]
			record.Layers[0].LayerIndex, record.Layers[0].ParentLayerIndex = 0, nil
			record.Layers[0].StartedAt, record.Layers[0].CompletedAt = at, at
			record.Layers[0].ModelName = "gpt-5.4-mini"
			record.Layers[0].MetadataJSON = costLayerMetadata("cost", id, "gpt-5.4-mini", 1000, 100, 10)
			if err := store.Evaluations().RecordCompleted(t.Context(), record); err != nil {
				t.Fatal(err)
			}
			if err := audit.WriteEvents(t.Context(), handle.Database, []audit.Event{{EventID: id, Time: at.Format(time.RFC3339Nano), System: "codex", Decision: audit.Decision{Kind: "allow"}, Violations: []audit.Violation{{Rule: "rule"}}}}); err != nil {
				t.Fatal(err)
			}
		}
	}
	for index := count - 1; index >= 0; index-- {
		for _, handle := range fixture.handles {
			fixture.want = append(fixture.want, handle.Bucket.ID+"/"+fmt.Sprintf("row-%03d", index))
		}
	}
	return fixture
}

func TestRetainedWalkUsesGlobalKeysetPagesAndNormalizedNanoseconds(t *testing.T) {
	fixture := newRetainedFixture(t, 103, false)
	var evaluations, seen, decisions []string
	if err := evaluation.Walk(t.Context(), fixture.cfg, evaluation.QueryFilter{Offset: 99}, func(record evaluation.QueryRecord) error {
		evaluations = append(evaluations, record.BucketID+"/"+record.EvaluationID)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := intake.Walk(t.Context(), fixture.cfg, intake.QueryFilter{Offset: 99}, func(record intake.QueryRecord) error {
		seen = append(seen, record.BucketID+"/"+record.EventID)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := audit.Walk(t.Context(), fixture.cfg, audit.QueryFilter{Offset: 99}, func(record audit.QueryRecord) error {
		decisions = append(decisions, record.BucketID+"/"+record.EventID)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for name, got := range map[string][]string{"evaluations": evaluations, "seen": seen, "decisions": decisions} {
		if !reflect.DeepEqual(got, fixture.want[99:]) {
			t.Fatalf("%s ordering = %v, want %v", name, got, fixture.want[99:])
		}
	}
	start, end := fixture.base.Add(10*time.Nanosecond), fixture.base.Add(20*time.Nanosecond)
	query, err := evaluation.Query(t.Context(), fixture.cfg, evaluation.QueryFilter{Since: start.In(time.FixedZone("east", 3600)), Until: end, Limit: 1000})
	if err != nil || len(query.Records) != 22 {
		t.Fatalf("evaluation bounds = %d, %v", len(query.Records), err)
	}
	intakes, err := intake.Query(t.Context(), fixture.cfg, intake.QueryFilter{Since: start, Until: end})
	if err != nil || len(intakes.Records) != 22 {
		t.Fatalf("intake bounds = %d, %v", len(intakes.Records), err)
	}
	audits, _, err := audit.QueryReadOnly(t.Context(), fixture.cfg, audit.QueryFilter{Since: start, Until: end})
	if err != nil || len(audits) != 22 {
		t.Fatalf("audit bounds = %d, %v", len(audits), err)
	}
	query, err = evaluation.Query(t.Context(), fixture.cfg, evaluation.QueryFilter{})
	if err != nil || len(query.Records) != 50 {
		t.Fatalf("evaluation default = %d, %v", len(query.Records), err)
	}
	intakes, err = intake.Query(t.Context(), fixture.cfg, intake.QueryFilter{})
	if err != nil || len(intakes.Records) != 100 {
		t.Fatalf("intake default = %d, %v", len(intakes.Records), err)
	}
	audits, _, err = audit.QueryReadOnly(t.Context(), fixture.cfg, audit.QueryFilter{})
	if err != nil || len(audits) != 100 {
		t.Fatalf("audit default = %d, %v", len(audits), err)
	}
}

func TestRetainedQueryCompletenessCostAndQualifiedReceipt(t *testing.T) {
	fixture := newRetainedFixture(t, 103, true)
	result, err := evaluation.Query(t.Context(), fixture.cfg, evaluation.QueryFilter{Limit: 1})
	if err != nil || result.Completeness.IncompleteCount != 103 {
		t.Fatalf("completeness = %+v, %v", result.Completeness, err)
	}
	complete, err := evaluation.Query(t.Context(), fixture.cfg, evaluation.QueryFilter{CompleteDetailOnly: true, Limit: 1000})
	if err != nil || len(complete.Records) != 103 || complete.Completeness.IncompleteCount != 103 {
		t.Fatalf("complete count = %d, completeness=%+v, %v", len(complete.Records), complete.Completeness, err)
	}
	for _, handle := range fixture.handles {
		result, err := evaluation.Query(t.Context(), fixture.cfg, evaluation.QueryFilter{ReceiptID: 1, BucketID: handle.Bucket.ID})
		if err != nil || len(result.Records) != 1 || result.Records[0].BucketID != handle.Bucket.ID {
			t.Fatalf("receipt query = %+v, %v", result, err)
		}
	}
	for _, filter := range []evaluation.QueryFilter{{ReceiptID: 1}, {BucketID: "../audit.db"}, {Limit: -1}, {Limit: 1001}, {Offset: -1}} {
		if _, err := evaluation.Query(t.Context(), fixture.cfg, filter); err == nil {
			t.Fatalf("accepted %+v", filter)
		}
	}
	report, err := evaluation.CostReport(t.Context(), fixture.cfg, costPricing(), evaluation.CostFilter{})
	if err != nil {
		t.Fatal(err)
	}
	model := findModelCost(t, report, "gpt-5.4-mini")
	if model.Calls != 206 || model.PromptTokens != 206000 || model.CachedTokens != 20600 || model.EstimatedCostMicros != 29355 || report.TotalBilledCostMicros != 29355 || len(report.Daily) != 1 {
		t.Fatalf("cross-bucket costs = %+v", report)
	}
	filtered, err := evaluation.CostReport(t.Context(), fixture.cfg, costPricing(), evaluation.CostFilter{Since: fixture.base.Add(10 * time.Nanosecond), Until: fixture.base.Add(20 * time.Nanosecond)})
	if err != nil || len(filtered.Models) != 1 || filtered.Models[0].Calls != 22 {
		t.Fatalf("filtered costs = %+v, %v", filtered, err)
	}
}

func TestWalkPinsBucketsDuringPruneAndClosesOnCallbackError(t *testing.T) {
	fixture := newRetainedFixture(t, 2, false)
	for _, handle := range fixture.handles {
		if err := handle.Close(); err != nil {
			t.Fatal(err)
		}
	}
	sentinel := errors.New("stop export")
	count := 0
	err := evaluation.Walk(t.Context(), fixture.cfg, evaluation.QueryFilter{}, func(evaluation.QueryRecord) error {
		count++
		result, err := fixture.catalog.Prune(t.Context(), fixture.cfg.AuditStoragePolicy().Rotation(), time.Now().Add(8*24*time.Hour))
		if err != nil || len(result.Deferred) != 2 || len(result.Removed) != 0 {
			t.Fatalf("prune during walk = %+v, %v", result, err)
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) || count != 1 {
		t.Fatalf("callback = %d, %v", count, err)
	}
	result, err := fixture.catalog.Prune(t.Context(), fixture.cfg.AuditStoragePolicy().Rotation(), time.Now().Add(8*24*time.Hour))
	if err != nil || len(result.Removed) != 2 {
		t.Fatalf("prune after walk = %+v, %v", result, err)
	}
}

func TestWalkCancellationAndCorruptRetainedHistory(t *testing.T) {
	fixture := newRetainedFixture(t, 1, false)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := evaluation.Walk(ctx, fixture.cfg, evaluation.QueryFilter{}, func(evaluation.QueryRecord) error { t.Fatal("yield after cancellation"); return nil }); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel = %v", err)
	}
	for _, handle := range fixture.handles {
		if err := handle.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(fixture.handles[1].Bucket.Path, []byte("corrupt database"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := evaluation.Query(t.Context(), fixture.cfg, evaluation.QueryFilter{}); err == nil {
		t.Fatal("corrupt retained bucket ignored")
	}
	if _, err := intake.Query(t.Context(), fixture.cfg, intake.QueryFilter{}); err == nil {
		t.Fatal("corrupt intake bucket ignored")
	}
	if _, _, err := audit.QueryReadOnly(t.Context(), fixture.cfg, audit.QueryFilter{}); err == nil {
		t.Fatal("corrupt audit bucket ignored")
	}
}

func TestAllWalkersPropagateCancellationAndCallbackErrors(t *testing.T) {
	fixture := newRetainedFixture(t, 2, false)
	for _, test := range []struct {
		name string
		walk func(context.Context, func() error) error
	}{
		{"audit", func(ctx context.Context, yield func() error) error {
			return audit.Walk(ctx, fixture.cfg, audit.QueryFilter{}, func(audit.QueryRecord) error { return yield() })
		}},
		{"intake", func(ctx context.Context, yield func() error) error {
			return intake.Walk(ctx, fixture.cfg, intake.QueryFilter{}, func(intake.QueryRecord) error { return yield() })
		}},
		{"evaluation", func(ctx context.Context, yield func() error) error {
			return evaluation.Walk(ctx, fixture.cfg, evaluation.QueryFilter{}, func(evaluation.QueryRecord) error { return yield() })
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			sentinel := errors.New("consumer stopped")
			calls := 0
			err := test.walk(t.Context(), func() error { calls++; return sentinel })
			if !errors.Is(err, sentinel) || calls != 1 {
				t.Fatalf("callback = %d, %v", calls, err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			calls = 0
			err = test.walk(ctx, func() error { calls++; cancel(); return nil })
			if !errors.Is(err, context.Canceled) || calls != 1 {
				t.Fatalf("cancellation = %d, %v", calls, err)
			}
		})
	}
}
