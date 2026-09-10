package auditstorage

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestStatusReportsPersistedCleanupErrorUntilSuccessfulRetry(t *testing.T) {
	catalog := testCatalog(t)
	policy := RotationPolicy{Interval: time.Hour, Retained: 1}
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	if _, err := catalog.EnsureCurrent(t.Context(), policy, now); err != nil {
		t.Fatal(err)
	}
	catalog.filesystemCheckpoint = func(operation, path string) error {
		if operation == "delete" {
			return errors.New("storage cleanup unavailable")
		}
		return nil
	}
	if _, err := catalog.Prune(t.Context(), policy, now.Add(time.Hour)); err == nil {
		t.Fatal("prune unexpectedly succeeded")
	}
	restarted, err := NewCatalog(catalog.options)
	if err != nil {
		t.Fatal(err)
	}
	status, err := restarted.Status(policy, now.Add(time.Hour))
	if err != nil || !strings.Contains(status.CleanupError, "storage cleanup unavailable") {
		t.Fatalf("persisted status = %+v, %v", status, err)
	}
	if _, err := restarted.Prune(t.Context(), policy, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	status, err = restarted.Status(policy, now.Add(time.Hour))
	if err != nil || status.CleanupError != "" {
		t.Fatalf("retry status = %+v, %v", status, err)
	}
}
