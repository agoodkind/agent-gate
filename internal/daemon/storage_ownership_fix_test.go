package daemon_test

import (
	"bytes"
	"fmt"
	"runtime"
	"runtime/pprof"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gofrs/flock"

	"goodkind.io/agent-gate/api/daemonpb"
	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/daemon"
	"goodkind.io/agent-gate/internal/intake"
)

func auditGoroutines(t *testing.T) [2]int {
	t.Helper()
	var output bytes.Buffer
	if err := pprof.Lookup("goroutine").WriteTo(&output, 2); err != nil {
		t.Fatal(err)
	}
	stacks := output.String()
	return [2]int{strings.Count(stacks, "/expirable.NewLRU["), strings.Count(stacks, "audit.(*EventLogger).worker(")}
}

func dumpOwnershipGoroutines(t *testing.T) {
	t.Helper()
	var output bytes.Buffer
	if err := pprof.Lookup("goroutine").WriteTo(&output, 2); err != nil {
		t.Fatal(err)
	}
	t.Log(output.String())
}

func TestStorageOwnershipLargeQueueLimitUsesByteBound(t *testing.T) {
	harness := newOwnershipHarness(t, 2, false, false)
	const configuredLimit = 1024 * 1024
	const maximumAllocation = 16 * 1024 * 1024
	harness.queueLimit = configuredLimit
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	harness.reload(t, 2, "large-queue")
	runtime.ReadMemStats(&after)
	reloadAllocation := after.TotalAlloc - before.TotalAlloc
	if reloadAllocation > maximumAllocation {
		t.Fatalf("queue reload allocated %d bytes", reloadAllocation)
	}
	harness.server.Close()
	cfg, err := config.LoadExisting(config.Path())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HookDeferredQueueLimit() != configuredLimit {
		t.Fatal("configuration changed the requested queue count")
	}
	runtime.ReadMemStats(&before)
	restarted, err := daemon.New(t.Context(), ownershipLogger(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(restarted.Close)
	runtime.ReadMemStats(&after)
	startupAllocation := after.TotalAlloc - before.TotalAlloc
	if startupAllocation > maximumAllocation {
		t.Fatalf("queue startup allocated %d bytes", startupAllocation)
	}
	harness.server = restarted
	restarted.StartAuditScheduler(t.Context())
	harness.accept(t, "large-queue-receipt")
	call := harness.nextCall(t)
	close(call.release)
	ownershipWait(t, func() bool { return ownershipOutcomes(t, cfg) == [4]int{1, 1, 1, 1} })
	t.Logf("configured_limit=%d reload_allocation_bytes=%d startup_allocation_bytes=%d", configuredLimit, reloadAllocation, startupAllocation)
}

func TestStorageOwnershipReceiptsAndReloadsReleaseAuditResources(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		t.Run(fmt.Sprintf("audit=%v", enabled), func(t *testing.T) {
			before := auditGoroutines(t)
			harness := newOwnershipHarness(t, 2, false, false)
			harness.auditEnabled = enabled
			harness.reload(t, 2, "resource-start")
			baseline := auditGoroutines(t)
			for batch := range 4 {
				for receipt := range 16 {
					harness.accept(t, fmt.Sprintf("resource-%d-%d", batch, receipt))
				}
				for range 16 {
					call := harness.nextCall(t)
					close(call.release)
				}
				total := (batch + 1) * 16
				violations := 0
				if enabled {
					violations = total
				}
				ownershipWait(t, func() bool { return ownershipOutcomes(t, harness.cfg) == [4]int{total, total, total, violations} })
				harness.reload(t, 2, fmt.Sprintf("resource-%d", batch))
				current := auditGoroutines(t)
				if current[0] > baseline[0] || current[1] > baseline[1] {
					t.Fatalf("audit goroutines grew from %v to %v after batch %d", baseline, current, batch)
				}
				t.Logf("batch=%d expiration_goroutines=%d audit_workers=%d", batch, current[0], current[1])
			}
			harness.server.Close()
			ownershipWait(t, func() bool { current := auditGoroutines(t); return current[0] <= before[0] && current[1] <= before[1] })
		})
	}
}

func TestStorageOwnershipQueueGrowthDuringConcurrentDispatch(t *testing.T) {
	harness := newOwnershipHarness(t, 2, false, false)
	stopConsumer := make(chan struct{})
	consumerDone := make(chan struct{})
	go func() {
		defer close(consumerDone)
		for {
			select {
			case call := <-harness.service.entered:
				close(call.release)
			case <-stopConsumer:
				return
			}
		}
	}()
	defer func() { close(stopConsumer); <-consumerDone }()
	stopProducer := make(chan struct{})
	producerDone := make(chan error, 1)
	var accepted atomic.Int64
	go func() {
		for identifier := 0; ; identifier++ {
			select {
			case <-stopProducer:
				producerDone <- nil
				return
			default:
			}
			response, err := harness.server.EvaluateHook(t.Context(), &daemonpb.EvaluateHookRequest{ProviderHint: "codex", RawJson: ownershipPayload(fmt.Sprintf("resizing-%d", identifier))})
			if err != nil {
				producerDone <- err
				return
			}
			if response.ExitCode != 0 || len(response.StderrData) != 0 {
				producerDone <- fmt.Errorf("hook did not accept receipt %d: %v", identifier, response)
				return
			}
			accepted.Add(1)
		}
	}()
	for _, limit := range []int{16, 32, 64, 128} {
		harness.queueLimit = limit
		harness.reload(t, 2, fmt.Sprintf("queue-%d", limit))
	}
	close(stopProducer)
	if err := <-producerDone; err != nil {
		t.Fatal(err)
	}
	total := int(accepted.Load())
	if total == 0 {
		t.Fatal("concurrent producer did not submit any receipts")
	}
	ownershipWait(t, func() bool { return ownershipOutcomes(t, harness.cfg) == [4]int{total, total, total, total} })
	closed := make(chan struct{})
	go func() { harness.server.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(3 * time.Second):
		t.Fatal("queue resizing prevented shutdown")
	}
	t.Logf("accepted_receipts=%d maximum_queue_limit=128", total)
}

func TestStorageOwnershipCurrentJobsContinueDuringHistoricalOpenContention(t *testing.T) {
	harness := newOwnershipHarness(t, 2, true, false)
	first := harness.nextCall(t)
	second := harness.nextCall(t)
	catalog := ownershipCatalog(t, harness.cfg)
	at := time.Now().Add(-3 * 24 * time.Hour)
	bucket, err := catalog.EnsureCurrent(t.Context(), harness.cfg.AuditStoragePolicy().Rotation(), at)
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
	payload := ownershipPayload("contended-history")
	record, err := store.Append(t.Context(), intake.Record{EventID: "contended-history", RecordedAt: at, System: "codex", SessionID: "contended-history", EventName: "PreToolUse", RawPayload: payload, NormalizedJSON: payload})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.MarkDeferredPending(t.Context(), record.EventID, record.ReceiptID); err != nil {
		t.Fatal(err)
	}
	if err := handle.Close(); err != nil {
		t.Fatal(err)
	}
	for identifier := range 4 {
		harness.accept(t, fmt.Sprintf("unrelated-current-%d", identifier))
	}
	// The public scheduler scans once per second while both workers remain occupied.
	time.Sleep(1200 * time.Millisecond)
	coordination := flock.New(harness.cfg.AuditCatalogOptions().CoordinationPath)
	if err := coordination.Lock(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = coordination.Close() }()
	close(first.release)
	close(second.release)
	for range 4 {
		call := harness.nextCall(t)
		if !strings.Contains(call.input, "unrelated-current-") {
			t.Fatalf("historical open ignored the coordination lock: %q", call.input)
		}
		close(call.release)
	}
	closed := make(chan struct{})
	go func() { harness.server.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(3 * time.Second):
		t.Fatal("historical opening reservation prevented cancellation")
	}
	if err := coordination.Close(); err != nil {
		t.Fatal(err)
	}
	unfinished := 7 - ownershipOutcomes(t, harness.cfg)[1]
	restarted, err := daemon.New(t.Context(), ownershipLogger(), harness.cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(restarted.Close)
	restarted.StartAuditScheduler(t.Context())
	for range unfinished {
		retry := harness.nextCall(t)
		close(retry.release)
	}
	var outcomes [4]int
	defer func() { t.Logf("durable_outcomes=%v", outcomes) }()
	ownershipWait(t, func() bool {
		outcomes = ownershipOutcomes(t, harness.cfg)
		return outcomes == [4]int{7, 7, 7, 7}
	})
}
