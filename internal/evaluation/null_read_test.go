package evaluation_test

import (
	"path/filepath"
	"testing"

	"goodkind.io/agent-gate/internal/evaluation"
	"goodkind.io/agent-gate/internal/intake"
)

func TestPublicGetDistinguishesEmptyContentFromOmittedContent(t *testing.T) {
	for _, minimal := range []bool{false, true} {
		t.Run(map[bool]string{false: "full", true: "minimal"}[minimal], func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "audit.db")
			intakes, err := openFixtureIntake(t, t.Context(), path, nil)
			if err != nil {
				t.Fatal(err)
			}
			cfg := fixtureConfig(t, path)
			if err := cfg.PrepareAuditStorage(); err != nil {
				t.Fatal(err)
			}
			policy := cfg.AuditStoragePolicy()
			policy.Detail.EvaluationContent = !minimal
			store, err := evaluation.NewStoreWithPolicy(t.Context(), "", intakes.Handle(), policy)
			if err != nil {
				t.Fatal(err)
			}
			receipt, err := intakes.Append(t.Context(), intake.Record{EventID: "nullable", RawPayload: []byte{}})
			if err != nil {
				t.Fatal(err)
			}
			record := completeRecord(receipt)
			record.Evaluation.ErrorJSON = nil
			if err := store.RecordCompleted(t.Context(), record); err != nil {
				t.Fatal(err)
			}
			got, err := store.Get(t.Context(), record.Evaluation.EvaluationID)
			if err != nil {
				t.Fatal(err)
			}
			if (got.Evaluation.ErrorJSON == nil) != minimal || len(got.Evaluation.ErrorJSON) != 0 {
				t.Fatalf("error content = %#v", got.Evaluation.ErrorJSON)
			}
			for index, layer := range got.Layers {
				if minimal {
					if layer.InputJSON != nil || layer.OutputJSON != nil || layer.MetadataJSON != nil || layer.ErrorMessage != "" {
						t.Fatalf("minimal layer = %+v", layer)
					}
				} else if string(layer.InputJSON) != string(record.Layers[index].InputJSON) || string(layer.OutputJSON) != string(record.Layers[index].OutputJSON) || string(layer.MetadataJSON) != string(record.Layers[index].MetadataJSON) {
					t.Fatalf("full layer changed = %+v", layer)
				}
			}
			page, err := evaluation.Query(t.Context(), cfg, evaluation.QueryFilter{})
			if err != nil || len(page.Records) != 1 {
				t.Fatalf("public query = %+v, %v", page, err)
			}
			if minimal && page.Records[0].Layers[0].Output != nil {
				t.Fatal("query invented omitted output")
			}
		})
	}
}

func TestRetainedQueryFetchesOnlySelectedDetail(t *testing.T) {
	fixture := newRetainedFixture(t, 3, false)
	if _, err := fixture.handles[0].Database.ExecContext(t.Context(), readEvaluationSQLFixture(t, "corrupt_selected_metadata.sql"), "row-002"); err != nil {
		t.Fatal(err)
	}
	result, err := evaluation.Query(t.Context(), fixture.cfg, evaluation.QueryFilter{Offset: 1, Limit: 1})
	if err != nil || len(result.Records) != 1 || result.Records[0].BucketID != fixture.handles[1].Bucket.ID {
		t.Fatalf("selected detail = %+v, %v", result, err)
	}
	if _, err := evaluation.Query(t.Context(), fixture.cfg, evaluation.QueryFilter{Limit: 1}); err == nil {
		t.Fatal("selected corrupt detail was ignored")
	}
}
