package evaluation_test

import (
	"testing"

	"goodkind.io/agent-gate/internal/intake"
)

func TestIntakeSequenceTiesUseBucketBeforeEventID(t *testing.T) {
	fixture := newRetainedFixture(t, 0, false)
	for index, handle := range fixture.handles {
		store, err := intake.NewStore(t.Context(), handle.Database, fixture.cfg.AuditStoragePolicy(), nil)
		if err != nil {
			t.Fatal(err)
		}
		id := []string{"a", "z"}[index]
		if _, err := store.Append(t.Context(), intake.Record{EventID: id, RecordedAt: fixture.base, RawPayload: []byte(`{}`)}); err != nil {
			t.Fatal(err)
		}
	}
	result, err := intake.Query(t.Context(), fixture.cfg, intake.QueryFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Records) != 2 || result.Records[0].BucketID != fixture.handles[0].Bucket.ID || result.Records[0].EventID != "a" {
		t.Fatalf("sequence tie = %+v", result.Records)
	}
}
