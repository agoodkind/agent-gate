package daemon

import (
	"context"
	"path/filepath"
	"testing"

	"goodkind.io/agent-gate/internal/evaluation"
	"goodkind.io/agent-gate/internal/intake"
)

func TestSQLiteEvaluationRecorderNilLoggerPreservesStoreErrors(t *testing.T) {
	ctx := context.Background()
	store, err := intake.OpenSQLite(ctx, filepath.Join(t.TempDir(), "audit.db"), nil)
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	})
	recorder := sqliteEvaluationRecorder{store: store, log: nil}

	tests := []struct {
		name string
		run  func() error
	}{
		{
			name: "record completed",
			run: func() error {
				return recorder.RecordCompleted(ctx, evaluation.Record{})
			},
		},
		{
			name: "commit hot evaluation",
			run: func() error {
				return recorder.CommitHotEvaluation(ctx, "event", 1, true, evaluation.Record{})
			},
		},
		{
			name: "commit deferred evaluation",
			run: func() error {
				return recorder.CommitDeferredEvaluation(
					ctx, intake.DeferredClaim{}, evaluation.Record{}, nil,
				)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.run(); err == nil {
				t.Fatal("error = nil, want wrapped store error")
			}
		})
	}
}
