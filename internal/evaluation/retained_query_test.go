package evaluation_test

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"goodkind.io/agent-gate/internal/auditstorage"
	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/evaluation"
	"goodkind.io/agent-gate/internal/intake"
)

func TestQueryRetainedBucketsGlobalLimit(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(root, "runtime"))
	cfg := &config.Config{}
	cfg.Audit.Outputs.SQLite.Path = filepath.Join(root, "audit.db")
	if err := cfg.PrepareAuditStorage(); err != nil {
		t.Fatal(err)
	}
	catalog, err := auditstorage.NewCatalog(cfg.AuditCatalogOptions())
	if err != nil {
		t.Fatal(err)
	}
	var bucketIDs []string
	for bucketIndex, minutes := range [][]int{{4, 1}, {3, 2}} {
		bucket, err := catalog.EnsureCurrent(t.Context(), cfg.AuditStoragePolicy().Rotation(), time.Now().Add(-time.Duration(bucketIndex)*24*time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		handle, err := catalog.OpenWriter(t.Context(), bucket)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = handle.Close() })
		bucketIDs = append(bucketIDs, bucket.ID)
		store, err := intake.NewStore(t.Context(), handle.Database, cfg.AuditStoragePolicy(), nil)
		if err != nil {
			t.Fatal(err)
		}
		for index, minute := range minutes {
			receipt, err := store.Append(t.Context(), intake.Record{EventID: fmt.Sprintf("event-%d", index), System: "codex", EventName: "PreToolUse", RawPayload: []byte(`{}`)})
			if err != nil {
				t.Fatal(err)
			}
			if receipt.ReceiptID != int64(index+1) {
				t.Fatalf("receipt = %d", receipt.ReceiptID)
			}
			record := completeRecord(receipt)
			record.Evaluation.EvaluationID = fmt.Sprintf("evaluation-%d", index)
			record.Evaluation.CompletedAt = time.Date(2026, 9, 8, 12, minute, 0, 0, time.UTC)
			if err := store.Evaluations().RecordCompleted(t.Context(), record); err != nil {
				t.Fatal(err)
			}
		}
	}
	result, err := evaluation.Query(t.Context(), cfg, evaluation.QueryFilter{Offset: 1, Limit: 3})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Records) != 3 {
		t.Fatalf("records = %d, want 3 globally: %+v", len(result.Records), result)
	}
	expectedBuckets := []string{bucketIDs[1], bucketIDs[1], bucketIDs[0]}
	for index, minute := range []int{3, 2, 1} {
		if result.Records[index].BucketID != expectedBuckets[index] {
			t.Fatalf("record %d bucket = %s, want %s", index, result.Records[index].BucketID, expectedBuckets[index])
		}
		if result.Records[index].CompletedAt.Minute() != minute {
			t.Fatalf("record %d = %s, want minute %d", index, result.Records[index].CompletedAt, minute)
		}
	}
}
