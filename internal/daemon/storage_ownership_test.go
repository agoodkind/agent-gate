package daemon_test

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"

	"goodkind.io/agent-gate/api/daemonpb"
	"goodkind.io/agent-gate/api/inferencepb"
	"goodkind.io/agent-gate/internal/auditstorage"
	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/daemon"
	"goodkind.io/agent-gate/internal/intake"
)

type ownershipCall struct {
	input    string
	release  chan struct{}
	canceled chan struct{}
}

//testdouble:external This local gRPC service controls the external inference protocol.
type ownershipInference struct {
	inferencepb.UnimplementedInferenceServer
	entered  chan ownershipCall
	inflight atomic.Int64
	peak     atomic.Int64
}

func (service *ownershipInference) Infer(ctx context.Context, request *inferencepb.InferRequest) (*inferencepb.InferReply, error) {
	active := service.inflight.Add(1)
	defer service.inflight.Add(-1)
	for previous := service.peak.Load(); active > previous; previous = service.peak.Load() {
		if service.peak.CompareAndSwap(previous, active) {
			break
		}
	}
	call := ownershipCall{input: request.Input, release: make(chan struct{}), canceled: make(chan struct{})}
	service.entered <- call
	select {
	case <-call.release:
		return &inferencepb.InferReply{OutputJson: `{"decision":"block"}`, Status: inferencepb.InferenceStatus_INFERENCE_STATUS_COMPLETE}, nil
	case <-ctx.Done():
		close(call.canceled)
		return nil, ctx.Err()
	}
}

type ownershipLog struct {
	reloaded      chan struct{}
	cleanupFailed chan struct{}
}

func (writer ownershipLog) Write(body []byte) (int, error) {
	if strings.Contains(string(body), "config reloaded") {
		select {
		case writer.reloaded <- struct{}{}:
		default:
		}
	}
	if strings.Contains(string(body), "schedule deferred intake retry failed") {
		select {
		case writer.cleanupFailed <- struct{}{}:
		default:
		}
	}
	return len(body), nil
}

type ownershipHarness struct {
	server        *daemon.Server
	cfg           *config.Config
	service       *ownershipInference
	reloaded      chan struct{}
	cleanupFailed chan struct{}
	endpoint      string
	base          string
	interval      string
	maxSize       string
	profile       string
	detail        string
}

func newOwnershipHarness(t *testing.T, workers int, seedHistory bool, unusable bool) *ownershipHarness {
	t.Helper()
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(root, "runtime"))
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	service := &ownershipInference{entered: make(chan ownershipCall, 16)}
	protocol := grpc.NewServer()
	inferencepb.RegisterInferenceServer(protocol, service)
	go func() { _ = protocol.Serve(listener) }()
	t.Cleanup(protocol.Stop)
	harness := &ownershipHarness{service: service, endpoint: listener.Addr().String(), base: filepath.Join(root, "audit.db"), reloaded: make(chan struct{}, 8), cleanupFailed: make(chan struct{}, 1), interval: "24h", maxSize: "64MiB", profile: "full"}
	harness.writeConfig(t, workers, "original")
	harness.cfg, err = config.LoadExisting(config.Path())
	if err != nil {
		t.Fatal(err)
	}
	if seedHistory {
		catalog := ownershipCatalog(t, harness.cfg)
		for day := 2; day >= 1; day-- {
			at := time.Now().Add(-time.Duration(day) * 24 * time.Hour)
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
			identifier := fmt.Sprintf("historical-%d", day)
			payload := ownershipPayload(identifier)
			record, err := store.Append(t.Context(), intake.Record{EventID: identifier, RecordedAt: at, System: "codex", SessionID: identifier, EventName: "PreToolUse", RawPayload: payload, NormalizedJSON: payload})
			if err != nil {
				t.Fatal(err)
			}
			if err := store.MarkDeferredPending(t.Context(), record.EventID, record.ReceiptID); err != nil {
				t.Fatal(err)
			}
			if err := handle.Close(); err != nil {
				t.Fatal(err)
			}
		}
	}
	cfg := harness.cfg
	if unusable {
		if err := os.WriteFile(config.Path(), []byte("[audit.storage\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg, err = config.LoadDegradedPath(config.Path())
		if err != nil {
			t.Fatal(err)
		}
	}
	harness.server, err = daemon.New(t.Context(), slog.New(slog.NewTextHandler(ownershipLog{reloaded: harness.reloaded, cleanupFailed: harness.cleanupFailed}, nil)), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(harness.server.Close)
	harness.server.StartAuditScheduler(t.Context())
	return harness
}

func ownershipLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func ownershipCatalog(t *testing.T, cfg *config.Config) *auditstorage.Catalog {
	t.Helper()
	catalog, err := auditstorage.NewCatalog(cfg.AuditCatalogOptions())
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}

func ownershipFixture(t *testing.T, name string) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func (harness *ownershipHarness) writeConfig(t *testing.T, workers int, message string) {
	t.Helper()
	workersSetting := ""
	if workers > 0 {
		workersSetting = "deferred_workers = " + strconv.Itoa(workers)
	}
	body := strings.NewReplacer("{{PATH}}", strconv.Quote(harness.base), "{{ENDPOINT}}", strconv.Quote(harness.endpoint), "{{WORKERS}}", workersSetting, "{{MESSAGE}}", message, "{{INTERVAL}}", harness.interval, "{{MAX_SIZE}}", harness.maxSize, "{{PROFILE}}", harness.profile, "{{DETAIL}}", harness.detail).Replace(ownershipFixture(t, "ownership.toml"))
	if err := os.MkdirAll(filepath.Dir(config.Path()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config.Path(), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func (harness *ownershipHarness) reload(t *testing.T, workers int, message string) {
	t.Helper()
	harness.writeConfig(t, workers, message)
	select {
	case <-harness.reloaded:
	case <-time.After(5 * time.Second):
		t.Fatal("configuration did not reload")
	}
}

func ownershipPayload(identifier string) []byte {
	return fmt.Appendf(nil, `{"session_id":%q,"hook_event_name":"PreToolUse","tool_name":"Shell","tool_input":{"command":%q}}`, identifier, identifier)
}

func (harness *ownershipHarness) accept(t *testing.T, identifier string) {
	t.Helper()
	response, err := harness.server.EvaluateHook(t.Context(), &daemonpb.EvaluateHookRequest{ProviderHint: "codex", RawJson: ownershipPayload(identifier)})
	if err != nil {
		t.Fatal(err)
	}
	if response.ExitCode != 0 || len(response.StderrData) != 0 {
		t.Fatalf("accepted hook = %+v", response)
	}
}

func (harness *ownershipHarness) nextCall(t *testing.T) ownershipCall {
	t.Helper()
	select {
	case call := <-harness.service.entered:
		return call
	case <-time.After(5 * time.Second):
		t.Fatal("deferred inference did not start")
		return ownershipCall{}
	}
}

func ownershipWait(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for !condition() {
		select {
		case <-deadline.C:
			t.Fatal("durable outcome did not settle")
		case <-ticker.C:
		}
	}
}

func ownershipOutcomes(t *testing.T, cfg *config.Config) [4]int {
	t.Helper()
	readers, err := ownershipCatalog(t, cfg).Read(t.Context(), cfg.AuditStoragePolicy().Rotation(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := readers.Close(); err != nil {
			t.Error(err)
		}
	}()
	var total [4]int
	for _, handle := range readers.Handles {
		var counts [4]int
		if err := handle.Database.QueryRowContext(t.Context(), ownershipFixture(t, "ownership_outcome.sql")).Scan(&counts[0], &counts[1], &counts[2], &counts[3]); err != nil {
			t.Fatal(err)
		}
		for index := range total {
			total[index] += counts[index]
		}
	}
	return total
}

func TestStorageOwnershipReloadKeepsDefaultWorkerAndAcceptedEvents(t *testing.T) {
	harness := newOwnershipHarness(t, 0, false, false)
	if harness.cfg.HookDeferredWorkers() != 1 {
		t.Fatal("default worker count differs from one")
	}
	harness.accept(t, "first")
	first := harness.nextCall(t)
	harness.reload(t, 0, "replacement")
	harness.accept(t, "second")
	select {
	case <-first.canceled:
		t.Fatal("unchanged-storage reload canceled admitted work")
	case <-harness.service.entered:
		t.Fatal("reload created another default worker")
	case <-time.After(250 * time.Millisecond):
	}
	close(first.release)
	second := harness.nextCall(t)
	close(second.release)
	ownershipWait(t, func() bool { return ownershipOutcomes(t, harness.cfg) == [4]int{2, 2, 2, 2} })
	if harness.service.peak.Load() != 1 {
		t.Fatal("default concurrency exceeded one")
	}
	readers, err := ownershipCatalog(t, harness.cfg).Read(t.Context(), harness.cfg.AuditStoragePolicy().Rotation(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = readers.Close() }()
	rows, err := readers.Handles[0].Database.QueryContext(t.Context(), ownershipFixture(t, "ownership_config.sql"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var hashes []string
	for rows.Next() {
		var hash string
		if err := rows.Scan(&hash); err != nil {
			t.Fatal(err)
		}
		hashes = append(hashes, hash)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	original, err := harness.cfg.Identity()
	if err != nil {
		t.Fatal(err)
	}
	if len(hashes) != 2 || hashes[0] != original || hashes[1] == original {
		t.Fatalf("captured configuration hashes = %v", hashes)
	}
}

func TestStorageOwnershipConcurrentHistoricalJobsAndWorkerReduction(t *testing.T) {
	harness := newOwnershipHarness(t, 2, true, false)
	first := harness.nextCall(t)
	second := harness.nextCall(t)
	if first.input == second.input {
		t.Fatal("historical jobs used the same input")
	}
	harness.reload(t, 1, "reduced")
	harness.accept(t, "current")
	close(second.release)
	ownershipWait(t, func() bool { return ownershipOutcomes(t, harness.cfg)[1] == 1 })
	select {
	case <-harness.service.entered:
		t.Fatal("worker reduction admitted work while one job remained active")
	case <-first.canceled:
		t.Fatal("worker reduction canceled admitted work")
	case <-time.After(250 * time.Millisecond):
	}
	close(first.release)
	current := harness.nextCall(t)
	close(current.release)
	ownershipWait(t, func() bool { return ownershipOutcomes(t, harness.cfg) == [4]int{3, 3, 3, 3} })
	if harness.service.peak.Load() != 2 {
		t.Fatalf("historical concurrency = %d", harness.service.peak.Load())
	}
}

func TestStorageOwnershipRotationRenewsOriginalFileAndCancellationCleansClaim(t *testing.T) {
	harness := newOwnershipHarness(t, 2, false, false)
	harness.accept(t, "before-rotation")
	first := harness.nextCall(t)
	catalog := ownershipCatalog(t, harness.cfg)
	policy := harness.cfg.AuditStoragePolicy().Rotation()
	old, err := catalog.EnsureCurrent(t.Context(), policy, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	handle, err := catalog.OpenWriter(t.Context(), old)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = handle.Close() }()
	claimSQL := ownershipFixture(t, "ownership_claim.sql")
	var claimOwner, expires, next sql.NullString
	if err := handle.Database.QueryRowContext(t.Context(), claimSQL).Scan(&claimOwner, &expires, &next); err != nil {
		t.Fatal(err)
	}
	initialExpiration := expires.String
	if err := os.Truncate(old.Path, policy.SegmentBytes()); err != nil {
		t.Fatal(err)
	}
	ownershipWait(t, func() bool {
		status, err := catalog.Status(policy, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		return len(status.RetainedFiles) == 2
	})
	harness.reload(t, 2, "rotated")
	ownershipWait(t, func() bool {
		if err := handle.Database.QueryRowContext(t.Context(), claimSQL).Scan(&claimOwner, &expires, &next); err != nil {
			t.Fatal(err)
		}
		return expires.Valid && expires.String > initialExpiration
	})
	select {
	case <-first.canceled:
		t.Fatal("rotation canceled the original job")
	default:
	}
	harness.accept(t, "after-rotation")
	second := harness.nextCall(t)
	close(first.release)
	ownershipWait(t, func() bool { return ownershipOutcomes(t, harness.cfg)[1] == 1 })
	closed := make(chan struct{})
	go func() { harness.server.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown did not finish")
	}
	select {
	case <-second.canceled:
	case <-time.After(time.Second):
		t.Fatal("shutdown did not cancel inference")
	}
	readers, err := catalog.Read(t.Context(), policy, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, reader := range readers.Handles {
		if reader.Bucket.ID == old.ID {
			continue
		}
		if err := reader.Database.QueryRowContext(t.Context(), claimSQL).Scan(&claimOwner, &expires, &next); err != nil {
			t.Fatal(err)
		}
		if claimOwner.Valid || expires.Valid || !next.Valid {
			t.Fatalf("canceled claim = %v %v %v", claimOwner, expires, next)
		}
	}
	if err := readers.Close(); err != nil {
		t.Fatal(err)
	}
	if err := handle.Close(); err != nil {
		t.Fatal(err)
	}
	if err := catalog.Purge(t.Context(), func([]string) error { return nil }); err != nil {
		t.Fatalf("shutdown retained a file claim: %v", err)
	}
}

func TestStorageOwnershipUnusableStartupRecoversWorkersOnReload(t *testing.T) {
	harness := newOwnershipHarness(t, 2, false, true)
	response, err := harness.server.EvaluateHook(t.Context(), &daemonpb.EvaluateHookRequest{ProviderHint: "codex", RawJson: ownershipPayload("unusable")})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(response.StderrData)+string(response.StdoutData), "config_unusable") {
		t.Fatalf("unusable response = %+v", response)
	}
	harness.reload(t, 2, "recovered")
	harness.accept(t, "recovered-first")
	harness.accept(t, "recovered-second")
	first := harness.nextCall(t)
	second := harness.nextCall(t)
	close(first.release)
	close(second.release)
	ownershipWait(t, func() bool { return ownershipOutcomes(t, harness.cfg) == [4]int{2, 2, 2, 2} })
}

func TestStorageOwnershipPolicyResetCancelsJobsAndDiscardsQueuedIdentifiers(t *testing.T) {
	harness := newOwnershipHarness(t, 2, false, false)
	harness.accept(t, "old-first")
	harness.accept(t, "old-second")
	first := harness.nextCall(t)
	second := harness.nextCall(t)
	harness.accept(t, "old-queued")
	harness.interval = "12h"
	harness.reload(t, 2, "reset")
	for _, call := range []ownershipCall{first, second} {
		select {
		case <-call.canceled:
		case <-time.After(time.Second):
			t.Fatal("policy reset did not cancel the original job")
		}
	}
	cfg, err := config.LoadExisting(config.Path())
	if err != nil {
		t.Fatal(err)
	}
	harness.accept(t, "replacement")
	replacement := harness.nextCall(t)
	if !strings.Contains(replacement.input, "replacement") {
		t.Fatalf("reset admitted stale work: %q", replacement.input)
	}
	close(replacement.release)
	ownershipWait(t, func() bool { return ownershipOutcomes(t, cfg) == [4]int{1, 1, 1, 1} })
}

func TestStorageOwnershipRetentionCancelsOnlySelectedFile(t *testing.T) {
	harness := newOwnershipHarness(t, 2, true, false)
	first := harness.nextCall(t)
	second := harness.nextCall(t)
	oldestCall := first
	retainedCall := second
	if strings.Contains(second.input, "historical-2") {
		oldestCall = second
		retainedCall = first
	}
	catalog := ownershipCatalog(t, harness.cfg)
	policy := harness.cfg.AuditStoragePolicy().Rotation()
	readers, err := catalog.Read(t.Context(), policy, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	var oldest auditstorage.Bucket
	for _, reader := range readers.Handles {
		if oldest.Start.IsZero() || reader.Bucket.Start.Before(oldest.Start) {
			oldest = reader.Bucket
		}
	}
	if err := readers.Close(); err != nil {
		t.Fatal(err)
	}
	harness.maxSize = "4MiB"
	harness.reload(t, 2, "retention")
	cfg, err := config.LoadExisting(config.Path())
	if err != nil {
		t.Fatal(err)
	}
	handle, err := catalog.OpenWriter(t.Context(), oldest)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := handle.Database.ExecContext(t.Context(), ownershipFixture(t, "ownership_padding.sql"), cfg.AuditStoragePolicy().MaxBytes, "historical-2"); err != nil {
		t.Fatal(err)
	}
	if err := handle.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-oldestCall.canceled:
	case <-time.After(5 * time.Second):
		t.Fatal("retention did not cancel the selected file")
	}
	ownershipWait(t, func() bool {
		_, err := os.Stat(oldest.Path)
		return os.IsNotExist(err)
	})
	select {
	case <-retainedCall.canceled:
		t.Fatal("retention canceled an unrelated file")
	default:
	}
	close(retainedCall.release)
	ownershipWait(t, func() bool { return ownershipOutcomes(t, harness.cfg) == [4]int{1, 1, 1, 1} })
}

func TestStorageOwnershipContentSelectionReloadReplaysWithReplacementPolicy(t *testing.T) {
	for _, selection := range []string{"profile", "override"} {
		t.Run(selection, func(t *testing.T) {
			harness := newOwnershipHarness(t, 0, false, false)
			harness.accept(t, "before-selection")
			original := harness.nextCall(t)
			if selection == "profile" {
				harness.profile = "minimal"
			} else {
				harness.detail = "[audit.storage.detail]\nevaluation_content = false"
			}
			harness.reload(t, 0, "selection")
			select {
			case <-original.canceled:
			case <-time.After(time.Second):
				t.Fatal("content selection did not cancel the original serialization policy")
			}
			harness.accept(t, "after-selection")
			for range 2 {
				call := harness.nextCall(t)
				close(call.release)
			}
			ownershipWait(t, func() bool { return ownershipOutcomes(t, harness.cfg) == [4]int{2, 2, 2, 2} })
			readers, err := ownershipCatalog(t, harness.cfg).Read(t.Context(), harness.cfg.AuditStoragePolicy().Rotation(), time.Now())
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = readers.Close() }()
			var deferred, recorded, layers, originalHot, replacementHot int
			if err := readers.Handles[0].Database.QueryRowContext(t.Context(), ownershipFixture(t, "ownership_detail.sql")).Scan(&deferred, &recorded, &layers, &originalHot, &replacementHot); err != nil {
				t.Fatal(err)
			}
			if deferred != 2 || recorded != 0 || layers != 0 || originalHot != 1 || replacementHot != 0 {
				t.Fatalf("write-time content selection = %d %d %d %d %d", deferred, recorded, layers, originalHot, replacementHot)
			}
		})
	}
}

func TestStorageOwnershipReloadCloseReleasesTransitionAfterContendedCleanup(t *testing.T) {
	harness := newOwnershipHarness(t, 0, false, false)
	harness.accept(t, "contended")
	original := harness.nextCall(t)
	catalog := ownershipCatalog(t, harness.cfg)
	bucket, err := catalog.EnsureCurrent(t.Context(), harness.cfg.AuditStoragePolicy().Rotation(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	handle, err := catalog.OpenWriter(t.Context(), bucket)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = handle.Close() }()
	transaction, err := handle.Database.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = transaction.Rollback() }()
	if _, err := transaction.ExecContext(t.Context(), ownershipFixture(t, "ownership_hold.sql")); err != nil {
		t.Fatal(err)
	}
	harness.profile = "minimal"
	harness.writeConfig(t, 0, "contended-reload")
	select {
	case <-original.canceled:
	case <-time.After(5 * time.Second):
		t.Fatal("reload did not cancel external inference")
	}
	closed := make(chan struct{})
	go func() { harness.server.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(10 * time.Second):
		t.Fatal("shutdown did not finish after the cleanup deadline")
	}
	select {
	case <-harness.cleanupFailed:
	default:
		t.Fatal("SQLite contention did not exercise the cleanup error")
	}
	if err := transaction.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := handle.Close(); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadExisting(config.Path())
	if err != nil {
		t.Fatal(err)
	}
	restarted, err := daemon.New(t.Context(), ownershipLogger(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(restarted.Close)
	restarted.StartAuditScheduler(t.Context())
	replayed := harness.nextCall(t)
	close(replayed.release)
	ownershipWait(t, func() bool { return ownershipOutcomes(t, cfg) == [4]int{1, 1, 1, 1} })
	restarted.Close()
	if err := catalog.Purge(t.Context(), func([]string) error { return nil }); err != nil {
		t.Fatalf("contended shutdown retained a file claim: %v", err)
	}
}
