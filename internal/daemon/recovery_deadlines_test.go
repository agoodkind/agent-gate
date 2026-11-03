package daemon_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"goodkind.io/agent-gate/internal/auditstorage"
	"goodkind.io/agent-gate/internal/daemon"
	"goodkind.io/agent-gate/internal/intake"
)

func TestRecoveryRevisitsLateReceiptAndDeadlines(t *testing.T) {
	harness := newOwnershipHarness(t, 1, false, false)
	harness.server.Close()
	catalog := ownershipCatalog(t, harness.cfg)
	bucket, err := catalog.EnsureCurrent(t.Context(), harness.cfg.AuditStoragePolicy().Rotation(), time.Now().Add(-24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	handle, err := catalog.OpenWriter(t.Context(), bucket)
	if err != nil {
		t.Fatal(err)
	}
	store, err := intake.NewStore(t.Context(), handle.Database, harness.cfg.AuditStoragePolicy(), ownershipLogger())
	if err != nil {
		t.Fatal(err)
	}
	var receipts []intake.AppendResult
	for index := range 4 {
		identifier := fmt.Sprintf("deadline-%d", index)
		payload := ownershipPayload(identifier)
		receipt, err := store.Append(t.Context(), intake.Record{EventID: identifier, System: "codex", SessionID: identifier, EventName: "PreToolUse", RawPayload: payload, NormalizedJSON: payload})
		if err != nil {
			t.Fatal(err)
		}
		receipts = append(receipts, receipt)
		if index != 0 {
			if err := store.MarkDeferredPending(t.Context(), receipt.EventID, receipt.ReceiptID); err != nil {
				t.Fatal(err)
			}
		}
	}
	_, claim, err := store.ClaimDeferred(t.Context(), receipts[2].ReceiptID, "future", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ScheduleDeferredRetry(t.Context(), claim, time.Now().Add(500*time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.ClaimDeferred(t.Context(), receipts[3].ReceiptID, "live", 700*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if err := handle.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := daemon.New(t.Context(), ownershipLogger(), harness.cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	restarted.StartAuditScheduler(t.Context())
	first := harness.nextCall(t)
	if !strings.Contains(first.input, "deadline-1") {
		t.Fatalf("recovery ignored initial eligibility: %s", first.input)
	}
	handle, err = catalog.OpenWriter(t.Context(), bucket)
	if err != nil {
		t.Fatal(err)
	}
	store, err = intake.NewStore(t.Context(), handle.Database, harness.cfg.AuditStoragePolicy(), ownershipLogger())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.MarkDeferredPending(t.Context(), receipts[0].EventID, receipts[0].ReceiptID); err != nil {
		t.Fatal(err)
	}
	if err := handle.Close(); err != nil {
		t.Fatal(err)
	}
	close(first.release)
	for range 3 {
		call := harness.nextCall(t)
		close(call.release)
	}
	ownershipWait(t, func() bool { complete, err := auditstorage.Completed(bucket.Path); return err == nil && complete })
	if outcome := ownershipOutcomes(t, harness.cfg); outcome != [4]int{4, 4, 4, 4} {
		t.Fatalf("late and timed outcomes = %v", outcome)
	}
}
