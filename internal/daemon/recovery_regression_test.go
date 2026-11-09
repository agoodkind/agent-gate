package daemon_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
	"goodkind.io/agent-gate/api/daemonpb"
	"goodkind.io/agent-gate/internal/auditstorage"
	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/daemon"
	"goodkind.io/agent-gate/internal/intake"
)

func seedRecoveryBacklog(t *testing.T, harness *ownershipHarness, count int) auditstorage.Bucket {
	t.Helper()
	catalog := ownershipCatalog(t, harness.cfg)
	bucket, err := catalog.EnsureCurrent(
		t.Context(),
		harness.cfg.AuditStoragePolicy().Rotation(),
		time.Now().Add(-24*time.Hour),
	)
	if err != nil {
		t.Fatal(err)
	}

	handle, err := catalog.OpenWriter(t.Context(), bucket)
	if err != nil {
		t.Fatal(err)
	}

	store, err := intake.NewStore(
		t.Context(),
		handle.Database,
		harness.cfg.AuditStoragePolicy(),
		ownershipLogger(),
	)
	if err != nil {
		t.Fatal(err)
	}

	for index := range count {
		identifier := fmt.Sprintf("backlog-%d", index)
		payload := ownershipPayload(identifier)
		receipt, err := store.Append(
			t.Context(),
			intake.Record{
				EventID:        identifier,
				System:         "codex",
				SessionID:      identifier,
				EventName:      "PreToolUse",
				RawPayload:     payload,
				NormalizedJSON: payload,
			},
		)
		if err != nil {
			t.Fatal(err)
		}

		if err := store.MarkDeferredPending(
			t.Context(),
			receipt.EventID,
			receipt.ReceiptID,
		); err != nil {
			t.Fatal(err)
		}
	}

	if err := handle.Close(); err != nil {
		t.Fatal(err)
	}

	return bucket
}

func TestRecoveryFairWithContinuousCurrentTraffic(t *testing.T) {
	harness := newOwnershipHarness(t, 1, false, false)
	harness.queueLimit = 1
	harness.reload(t, 1, "capacity-one")
	cfg, err := config.LoadExisting(config.Path())
	if err != nil {
		t.Fatal(err)
	}

	harness.cfg = cfg
	harness.server.Close()
	const historical = 12
	bucket := seedRecoveryBacklog(t, harness, historical)
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = watcher.Close() }()
	if err := watcher.Add(filepath.Dir(bucket.Path)); err != nil {
		t.Fatal(err)
	}

	var creations atomic.Int64
	watchDone := make(chan struct{})
	go func() {
		defer close(watchDone)

		for event := range watcher.Events {
			if event.Name == bucket.Path+"-shm" && event.Has(fsnotify.Create) {
				creations.Add(1)
			}
		}
	}()
	restarted, err := daemon.New(t.Context(), ownershipLogger(), harness.cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	harness.server = restarted
	accepted := 0
	accept := func() {
		ctx, cancel := context.WithCancel(t.Context())
		response, err := restarted.EvaluateHook(
			ctx,
			&daemonpb.EvaluateHookRequest{
				ProviderHint: "codex",
				RawJson:      ownershipPayload(fmt.Sprintf("current-%d", accepted)),
			},
		)
		cancel()
		if err != nil || response.ExitCode != 0 || len(response.StderrData) != 0 {
			t.Fatalf("accepted current receipt = %+v: %v", response, err)
		}

		accepted++
	}
	accept()
	accept()
	restarted.StartAuditScheduler(t.Context())
	seenHistorical := 0
	seenCurrent := 0

	for turns := 0; seenHistorical < historical; turns++ {
		if turns > historical*4+20 {
			t.Fatalf("historical recovery starved after %d current calls", seenCurrent)
		}

		call := harness.nextCall(t)
		if strings.Contains(call.input, "backlog-") {
			seenHistorical++
		} else {
			seenCurrent++
		}

		accept()
		accept()
		close(call.release)
	}

	for remaining := accepted - seenCurrent; remaining > 0; remaining-- {
		call := harness.nextCall(t)
		if !strings.Contains(call.input, "current-") {
			t.Fatalf("historical receipt repeated: %s", call.input)
		}

		close(call.release)
	}

	ownershipWait(
		t,
		func() bool {
			complete, err := auditstorage.Completed(bucket.Path)
			return err == nil && complete
		},
	)
	if err := watcher.Close(); err != nil {
		t.Fatal(err)
	}

	<-watchDone
	if count := creations.Load(); count < 1 || count > 2 {
		t.Fatalf("historical shared memory recreated %d times for %d receipts", count, historical)
	}

	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(bucket.Path + suffix); !os.IsNotExist(err) {
			t.Fatalf("completed file retained %s: %v", suffix, err)
		}
	}

	total := accepted + historical
	ownershipWait(
		t,
		func() bool {
			return ownershipOutcomes(t, harness.cfg) == [4]int{total, total, total, total}
		},
	)
	t.Logf(
		"historical_receipts=%d current_receipts=%d historical_shm_creations=%d",
		historical,
		accepted,
		creations.Load(),
	)
}
