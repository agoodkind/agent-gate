package evaluation_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"goodkind.io/agent-gate/internal/intake"
)

func newPendingIntakeFixture(t *testing.T, count int) (retainedFixture, []*intake.Store) {
	t.Helper()
	fixture := newRetainedFixture(t, 0, false)
	var stores []*intake.Store
	for _, handle := range fixture.handles {
		store, err := intake.NewStore(t.Context(), handle.Database, fixture.cfg.AuditStoragePolicy(), nil)
		if err != nil {
			t.Fatal(err)
		}
		stores = append(stores, store)
		for index := range count {
			receipt, err := store.Append(t.Context(), intake.Record{
				EventID: fmt.Sprintf("pending-%03d", index), RecordedAt: fixture.base.Add(time.Duration(index) * time.Nanosecond),
				System: "codex", SessionID: "selected-session", EventName: "PreToolUse", ToolName: "Shell",
				RawPayload: []byte(`{}`), NormalizedJSON: []byte(`{"input":"selected"}`), EnvFingerprint: map[string]string{"AI_AGENT": "codex"},
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := store.MarkDeferredPending(t.Context(), receipt.EventID, receipt.ReceiptID); err != nil {
				t.Fatal(err)
			}
		}
	}
	return fixture, stores
}

func TestIntakeWalkReturnsSelectedRowAfterDeferredCompletion(t *testing.T) {
	fixture, stores := newPendingIntakeFixture(t, 1)
	var records []intake.QueryRecord
	err := intake.Walk(t.Context(), fixture.cfg, intake.QueryFilter{DeferredState: "pending"}, func(record intake.QueryRecord) error {
		records = append(records, record)
		if len(records) == 1 {
			return stores[1].MarkDeferredComplete(t.Context(), 1)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("valid completion failed the walk after %d rows: %v", len(records), err)
	}
	if len(records) != 2 || records[0].BucketID != fixture.handles[0].Bucket.ID || records[1].BucketID != fixture.handles[1].Bucket.ID {
		t.Fatalf("selected rows = %+v", records)
	}
	if records[0].Deferred.State != intake.DeferredStatePending || records[1].Deferred.State != intake.DeferredStateComplete || records[1].Deferred.CompletedAt == "" {
		t.Fatalf("fetch-time state = %+v", records)
	}
}

func TestIntakeWalkSelectionPreservesPaginationAndProjections(t *testing.T) {
	for _, normalized := range []bool{false, true} {
		for _, environment := range []bool{false, true} {
			t.Run(fmt.Sprintf("normalized=%t/environment=%t", normalized, environment), func(t *testing.T) {
				fixture, stores := newPendingIntakeFixture(t, 3)
				filter := intake.QueryFilter{
					DeferredState: "pending", System: "codex", SessionID: "selected-session", EventName: "PreToolUse", ToolName: "Shell",
					Since: fixture.base, Until: fixture.base.Add(2 * time.Nanosecond), Offset: 1, Limit: 3,
					IncludeNormalized: normalized, IncludeEnv: environment,
				}
				var records []intake.QueryRecord
				err := intake.Walk(t.Context(), fixture.cfg, filter, func(record intake.QueryRecord) error {
					records = append(records, record)
					if len(records) == 1 {
						for _, store := range stores {
							if err := store.MarkDeferredComplete(t.Context(), 2); err != nil {
								return err
							}
						}
					}
					return nil
				})
				if err != nil {
					t.Fatalf("valid completion failed paginated walk after %d rows: %v", len(records), err)
				}
				if len(records) != 3 {
					t.Fatalf("selected count = %d, want 3", len(records))
				}
				wantBuckets := []string{fixture.handles[1].Bucket.ID, fixture.handles[0].Bucket.ID, fixture.handles[1].Bucket.ID}
				wantIDs := []string{"pending-002", "pending-001", "pending-001"}
				for index, record := range records {
					if record.BucketID != wantBuckets[index] || record.EventID != wantIDs[index] {
						t.Fatalf("selected row %d was replaced: %+v", index, record)
					}
					if index > 0 && record.Deferred.State != intake.DeferredStateComplete {
						t.Fatalf("selected row %d state = %s", index, record.Deferred.State)
					}
					if normalized && string(record.NormalizedJSON) != `{"input":"selected"}` {
						t.Fatalf("normalized projection = %s", record.NormalizedJSON)
					}
					if !normalized && record.NormalizedJSON != nil {
						t.Fatal("unrequested normalized content returned")
					}
					if environment && record.EnvFingerprint["AI_AGENT"] != "codex" {
						t.Fatalf("environment projection = %v", record.EnvFingerprint)
					}
					if !environment && record.EnvFingerprint != nil {
						t.Fatal("unrequested environment returned")
					}
				}
			})
		}
	}
}

func TestIntakeWalkStillReportsMissingSelectedIdentity(t *testing.T) {
	fixture, stores := newPendingIntakeFixture(t, 1)
	count := 0
	err := intake.Walk(t.Context(), fixture.cfg, intake.QueryFilter{DeferredState: "pending"}, func(intake.QueryRecord) error {
		count++
		if count == 1 {
			_, err := fixture.handles[1].Database.ExecContext(t.Context(), readEvaluationSQLFixture(t, "delete_selected_intake.sql"), "pending-000")
			return err
		}
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), `intake event "pending-000" disappeared during read`) || count != 1 {
		t.Fatalf("missing identity result = %d, %v", count, err)
	}
	if _, err := stores[1].Get(t.Context(), "pending-000"); !errors.Is(err, intake.ErrEventNotFound) {
		t.Fatalf("deleted event lookup = %v", err)
	}
}
