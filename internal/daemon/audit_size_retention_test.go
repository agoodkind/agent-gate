package daemon_test

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"goodkind.io/agent-gate/api/daemonpb"
	"goodkind.io/agent-gate/internal/auditstorage"
	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/daemon"
	"goodkind.io/agent-gate/internal/intake"
)

func sizeRetentionConfig(t *testing.T, maxSize string, retained int) *config.Config {
	t.Helper()
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(root, "runtime"))
	if err := os.MkdirAll(filepath.Dir(config.Path()), 0o700); err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf("[audit.storage]\nmax_size = %q\nretention_buckets = %d\n", maxSize, retained)
	body += "[[rules]]\nname = \"size-audit\"\nevents = [\"PreToolUse\"]\naction = \"audit\"\nviolation_message = \"audit\"\n"
	body += "pattern = \"echo audit\"\nfield_paths = [\"tool_input.command\"]\n"
	if err := os.WriteFile(config.Path(), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadExisting(config.Path())
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func sizeRetentionLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func sizeRetentionRequest(paddingBytes int) *daemonpb.EvaluateHookRequest {
	return &daemonpb.EvaluateHookRequest{
		ProviderHint: "codex",
		RawJson:      []byte(fmt.Sprintf(`{"session_id":"size-rotation","hook_event_name":"PreToolUse","tool_name":"Shell","tool_input":{"command":"echo audit"},"padding":%q}`, strings.Repeat("x", paddingBytes))),
	}
}

func sizeRetentionCatalog(t *testing.T, cfg *config.Config) *auditstorage.Catalog {
	t.Helper()
	catalog, err := auditstorage.NewCatalog(cfg.AuditCatalogOptions())
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}

func sizeRetentionStatus(t *testing.T, cfg *config.Config) auditstorage.AuditStatus {
	t.Helper()
	status, err := sizeRetentionCatalog(t, cfg).Status(cfg.AuditStoragePolicy().Rotation(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return status
}

func waitSizeRetention(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.NewTimer(6 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for !condition() {
		select {
		case <-deadline.C:
			t.Fatal("audit storage did not produce the expected result")
		case <-ticker.C:
		}
	}
}

func TestAuditSchedulerRotatesCurrentDayOnSize(t *testing.T) {
	cfg := sizeRetentionConfig(t, "16MiB", 7)
	server, err := daemon.New(t.Context(), sizeRetentionLogger(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(server.Close)
	old := sizeRetentionStatus(t, cfg)
	server.StartAuditScheduler(t.Context())
	if _, err := server.EvaluateHook(t.Context(), sizeRetentionRequest(1024*1024)); err != nil {
		t.Fatal(err)
	}
	waitSizeRetention(t, func() bool { return sizeRetentionStatus(t, cfg).CurrentBucketID != old.CurrentBucketID })
	current := sizeRetentionStatus(t, cfg)
	if !current.NextBoundary.Equal(old.NextBoundary) {
		t.Fatalf("size rotation changed UTC window: %s, %s", old.NextBoundary, current.NextBoundary)
	}
	waitSizeRetention(t, func() bool {
		if _, err := server.EvaluateHook(t.Context(), sizeRetentionRequest(0)); err != nil {
			t.Fatal(err)
		}
		result, err := intake.Query(t.Context(), cfg, intake.QueryFilter{BucketID: current.CurrentBucketID})
		if err != nil {
			t.Fatal(err)
		}
		return len(result.Records) == 1
	})
}

func TestAuditStartupRotatesBeforeReadinessAndPrunesAfterReadiness(t *testing.T) {
	cfg := sizeRetentionConfig(t, "0B", 7)
	server, err := daemon.New(t.Context(), sizeRetentionLogger(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(server.Close)
	if _, err := server.EvaluateHook(t.Context(), sizeRetentionRequest(4*1024*1024)); err != nil {
		t.Fatal(err)
	}
	old := sizeRetentionStatus(t, cfg)
	server.Close()
	maxSize := "4MiB"
	cfg.Audit.Storage.MaxSize = &maxSize
	server, err = daemon.New(t.Context(), sizeRetentionLogger(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(server.Close)
	current := sizeRetentionStatus(t, cfg)
	if current.CurrentBucketID == old.CurrentBucketID || !current.NextBoundary.Equal(old.NextBoundary) {
		t.Fatalf("startup bucket = %s, previous = %s", current.CurrentBucketID, old.CurrentBucketID)
	}
	if _, err := os.Stat(old.CurrentBucketPath); err != nil {
		t.Fatalf("startup deleted history before readiness: %v", err)
	}
	server.StartAuditScheduler(t.Context())
	waitSizeRetention(t, func() bool { _, err := os.Stat(old.CurrentBucketPath); return errors.Is(err, os.ErrNotExist) })
}

func TestAuditRetentionReloadPreservesHistory(t *testing.T) {
	cfg := sizeRetentionConfig(t, "16MiB", 7)
	server, err := daemon.New(t.Context(), sizeRetentionLogger(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(server.Close)
	if _, err := server.EvaluateHook(t.Context(), sizeRetentionRequest(0)); err != nil {
		t.Fatal(err)
	}
	old := sizeRetentionStatus(t, cfg)
	body, err := os.ReadFile(config.Path())
	if err != nil {
		t.Fatal(err)
	}
	body = []byte(strings.Replace(string(body), "retention_buckets = 7", "retention_buckets = 0", 1))
	if err := os.WriteFile(config.Path(), body, 0o600); err != nil {
		t.Fatal(err)
	}
	statePath := cfg.AuditCatalogOptions().StatePath
	waitSizeRetention(t, func() bool {
		data, err := os.ReadFile(statePath)
		if err != nil {
			t.Fatal(err)
		}
		var state auditstorage.CatalogState
		if err := json.Unmarshal(data, &state); err != nil {
			t.Fatal(err)
		}
		return state.Retained == 0
	})
	if current := sizeRetentionStatus(t, cfg); current.CurrentBucketID != old.CurrentBucketID {
		t.Fatalf("retention change replaced bucket: %s", current.CurrentBucketID)
	}
	result, err := intake.Query(t.Context(), cfg, intake.QueryFilter{})
	if err != nil || len(result.Records) != 1 {
		t.Fatalf("receipts after retention reload = %d, want 1: %v", len(result.Records), err)
	}
}

func appendSizeRetentionReceipt(t *testing.T, cfg *config.Config, bucket auditstorage.Bucket, paddingBytes int) {
	t.Helper()
	handle, err := sizeRetentionCatalog(t, cfg).OpenWriter(t.Context(), bucket)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = handle.Close() }()
	store, err := intake.NewStore(t.Context(), handle.Database, cfg.AuditStoragePolicy(), sizeRetentionLogger())
	if err != nil {
		t.Fatal(err)
	}
	request := sizeRetentionRequest(paddingBytes)
	receipt, err := store.Append(t.Context(), intake.Record{
		System: "codex", SessionID: "size-rotation", EventName: "PreToolUse", ToolName: "Shell",
		RawPayload: request.RawJson, NormalizedJSON: request.RawJson,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.MarkDeferredPending(t.Context(), receipt.EventID, receipt.ReceiptID); err != nil {
		t.Fatal(err)
	}
}

func TestReplaySizeOnlyRetentionProcessesOldReceipts(t *testing.T) {
	cfg := sizeRetentionConfig(t, "16MiB", 0)
	catalog := sizeRetentionCatalog(t, cfg)
	bucket, err := catalog.EnsureCurrent(t.Context(), cfg.AuditStoragePolicy().Rotation(), time.Now().Add(-45*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	appendSizeRetentionReceipt(t, cfg, bucket, 0)
	server, err := daemon.New(t.Context(), sizeRetentionLogger(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(server.Close)
	server.StartAuditScheduler(t.Context())
	waitSizeRetention(t, func() bool {
		result, err := intake.Query(t.Context(), cfg, intake.QueryFilter{DeferredState: string(intake.DeferredStateComplete)})
		if err != nil {
			t.Fatal(err)
		}
		return len(result.Records) == 1
	})
}

func TestReplayProcessesSameDayReceiptIdentifiersFromSeparateSegments(t *testing.T) {
	cfg := sizeRetentionConfig(t, "16MiB", 7)
	server, err := daemon.New(t.Context(), sizeRetentionLogger(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(server.Close)
	if _, err := server.EvaluateHook(t.Context(), sizeRetentionRequest(1024*1024)); err != nil {
		t.Fatal(err)
	}
	first := sizeRetentionStatus(t, cfg)
	server.Close()
	server, err = daemon.New(t.Context(), sizeRetentionLogger(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(server.Close)
	second := sizeRetentionStatus(t, cfg)
	if first.CurrentBucketID == second.CurrentBucketID || !first.NextBoundary.Equal(second.NextBoundary) {
		t.Fatalf("same-day segments = %s, %s", first.CurrentBucketID, second.CurrentBucketID)
	}
	if _, err := server.EvaluateHook(t.Context(), sizeRetentionRequest(0)); err != nil {
		t.Fatal(err)
	}
	server.StartAuditScheduler(t.Context())
	waitSizeRetention(t, func() bool {
		result, err := intake.Query(t.Context(), cfg, intake.QueryFilter{BucketID: second.CurrentBucketID, DeferredState: string(intake.DeferredStateComplete)})
		if err != nil {
			t.Fatal(err)
		}
		return len(result.Records) == 1
	})
	result, err := intake.Query(t.Context(), cfg, intake.QueryFilter{DeferredState: string(intake.DeferredStateComplete)})
	if err != nil || len(result.Records) != 2 || result.Records[0].BucketID == result.Records[1].BucketID {
		t.Fatalf("completed same-day receipts = %+v: %v", result.Records, err)
	}
}

func TestAuditSizePrunePreservesExternalReader(t *testing.T) {
	cfg := sizeRetentionConfig(t, "4MiB", 7)
	server, err := daemon.New(t.Context(), sizeRetentionLogger(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(server.Close)
	if _, err := server.EvaluateHook(t.Context(), sizeRetentionRequest(4*1024*1024)); err != nil {
		t.Fatal(err)
	}
	old := sizeRetentionStatus(t, cfg)
	server.Close()
	server, err = daemon.New(t.Context(), sizeRetentionLogger(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(server.Close)
	readers, err := sizeRetentionCatalog(t, cfg).Read(t.Context(), cfg.AuditStoragePolicy().Rotation(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = readers.Close() })
	server.StartAuditScheduler(t.Context())
	waitSizeRetention(t, func() bool { return slices.Contains(sizeRetentionStatus(t, cfg).DeferredFiles, old.CurrentBucketPath) })
	if _, err := os.Stat(old.CurrentBucketPath); err != nil {
		t.Fatalf("size pruning deleted a database with an external reader: %v", err)
	}
	if err := readers.Close(); err != nil {
		t.Fatal(err)
	}
	waitSizeRetention(t, func() bool { _, err := os.Stat(old.CurrentBucketPath); return errors.Is(err, os.ErrNotExist) })
}

func TestReplaySizeOnlyRetentionSchedulesRetryAfterCurrentTime(t *testing.T) {
	cfg := sizeRetentionConfig(t, "16MiB", 0)
	server, err := daemon.New(t.Context(), sizeRetentionLogger(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(server.Close)
	status := sizeRetentionStatus(t, cfg)
	readers, err := sizeRetentionCatalog(t, cfg).Read(t.Context(), cfg.AuditStoragePolicy().Rotation(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	bucket := readers.Handles[0].Bucket
	if err := readers.Close(); err != nil {
		t.Fatal(err)
	}
	handle, err := sizeRetentionCatalog(t, cfg).OpenWriter(t.Context(), bucket)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = handle.Close() }()
	trigger, err := os.ReadFile(filepath.Join("testdata", "fail_deferred_audit.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := handle.Database.ExecContext(t.Context(), string(trigger)); err != nil {
		t.Fatal(err)
	}
	if _, err := server.EvaluateHook(t.Context(), sizeRetentionRequest(0)); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	server.StartAuditScheduler(t.Context())
	query, err := os.ReadFile(filepath.Join("testdata", "runtime_deferred_retry.sql"))
	if err != nil {
		t.Fatal(err)
	}
	var nextAttempt sql.NullString
	waitSizeRetention(t, func() bool {
		var attempt int
		var owner sql.NullString
		if err := handle.Database.QueryRowContext(t.Context(), string(query), 1).Scan(&attempt, &nextAttempt, &owner); err != nil {
			t.Fatal(err)
		}
		return attempt == 1 && nextAttempt.Valid && !owner.Valid
	})
	scheduled, err := time.Parse(time.RFC3339Nano, nextAttempt.String)
	if err != nil || !scheduled.After(started) {
		t.Fatalf("retry time in segment %s = %s, started = %s: %v", status.CurrentBucketID, scheduled, started, err)
	}
}
