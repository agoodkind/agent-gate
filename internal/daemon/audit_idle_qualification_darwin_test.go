//go:build auditqualification

package daemon_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"goodkind.io/agent-gate/internal/auditstorage"
	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/daemon"
	"goodkind.io/agent-gate/internal/evaluation"
	"goodkind.io/agent-gate/internal/intake"
	"goodkind.io/agent-gate/internal/testutil/processusage"
)

const (
	qualificationIdleVersion  = "idle-completed-history-v2-public-api"
	qualificationIdleSettling = time.Second
)

type qualificationMemory struct {
	ResidentBytes          uint64
	PhysicalFootprintBytes uint64
	GoHeapAllocBytes       uint64
	GoHeapInuseBytes       uint64
}

type qualificationIdleSample struct {
	AtNS      int64
	CPUNS     uint64
	DiskBytes uint64
	Memory    qualificationMemory
}

type qualificationIdlePhase struct {
	ElapsedNS int64
	CPUNS     uint64
	DiskBytes uint64
}

type qualificationIdleResult struct {
	Version               string
	Source                string
	WorkloadDigest        string
	Scenario              string
	History               string
	Repetition            string
	PID                   int
	MetricScope           string
	Limitations           []string
	StartedAt             time.Time
	SamplePeriodNS        int64
	RequestedIdleNS       int64
	SettlingNS            int64
	SegmentThresholdBytes int64
	RetentionMaxBytes     int64
	Profile               string
	CompletedHistoryFiles int
	BeforeStartup         qualificationHistory
	AfterStartup          qualificationHistory
	AfterShutdown         qualificationHistory
	Idle                  qualificationIdlePhase
	Shutdown              qualificationIdlePhase
	Samples               []qualificationIdleSample
	Peak                  qualificationMemory
	Settled               qualificationMemory
	TimebaseNumer         uint32
	TimebaseDenom         uint32
	Failures              []string
}

func BenchmarkAuditIdleQualification(b *testing.B) {
	b.StopTimer()
	if b.N != 1 {
		b.Fatal("qualification requires -benchtime=1x and one process per repetition")
	}
	result := qualificationIdleResult{
		Version:     qualificationIdleVersion,
		Source:      os.Getenv("QUALIFICATION_SOURCE"),
		Scenario:    "idle",
		History:     os.Getenv("QUALIFICATION_HISTORY"),
		Repetition:  os.Getenv("QUALIFICATION_REPETITION"),
		PID:         os.Getpid(),
		MetricScope: "test process with real in-process daemon and audit scheduler; process counters exclude child processes",
		Limitations: []string{
			"The process includes the Go test harness, fixture residue, and resource sampling overhead; it does not run the daemon RPC listener, supervisor, update scheduler, or production log sink.",
			"One-second memory samples report observed peaks rather than an exact lifetime maximum; settled memory is the final idle sample without forced garbage collection.",
			"The idle interval excludes preparation, verification queries, shutdown, and result serialization; shutdown counters appear separately.",
			"The public API does not expose scheduler readiness. Sampling starts after retained-history verification and fixed one-second settling; this interval does not establish that startup work completed.",
		},
		Failures: []string{},
	}
	if result.Source == "" || os.Getenv("QUALIFICATION_OUTPUT") == "" {
		b.Fatal("qualification source and output are required")
	}
	duration := 10 * time.Second
	if raw := os.Getenv("QUALIFICATION_IDLE_DURATION"); raw != "" {
		parsed, err := time.ParseDuration(raw)
		if err != nil {
			b.Fatal(err)
		}
		duration = parsed
	}
	if duration < time.Second || duration > time.Minute {
		b.Fatal("idle duration must be between 1s and 1m")
	}
	const samplePeriod = time.Second
	result.SamplePeriodNS = samplePeriod.Nanoseconds()
	result.RequestedIdleNS = duration.Nanoseconds()
	result.SettlingNS = qualificationIdleSettling.Nanoseconds()
	cfg := qualificationIdleConfig(b)
	policy := cfg.AuditStoragePolicy()
	result.SegmentThresholdBytes = policy.Rotation().SegmentBytes()
	result.RetentionMaxBytes = policy.MaxBytes
	result.Profile = string(policy.Profile)
	now := time.Now()
	result.CompletedHistoryFiles = qualificationSeedIdleHistory(b, cfg, result.History, now)
	count := result.CompletedHistoryFiles
	if result.History == "80" || result.History == "160" {
		result.Limitations = append(
			result.Limitations,
			"Historical files contain one real 1 KiB receipt and completed hot evaluation each, with sparse trailing bytes outside SQLite's populated pages. This fixture tests file cardinality and logical byte accounting rather than densely populated history.",
		)
	}
	result.BeforeStartup = qualificationInspectIdleHistory(b, cfg, now, count, count)
	server, err := daemon.New(b.Context(), qualificationIdleLogger(), cfg)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(server.Close)
	server.StartAuditScheduler(b.Context())
	result.AfterStartup = qualificationInspectIdleHistory(b, cfg, now, count+1, count)
	time.Sleep(qualificationIdleSettling)
	digestInput := fmt.Appendf(
		nil,
		"%s\nhistory=%s\nduration=%s\nsettle=%s\nconfig=%s",
		qualificationIdleVersion,
		result.History,
		duration,
		qualificationIdleSettling,
		qualificationReadFixture(b, "qualification_idle.toml"),
	)
	for index := range count {
		digestInput = append(digestInput, qualificationIdlePayload(uint64(index+1))...)
		digestInput = append(digestInput, '\n')
	}
	digest := sha256.Sum256(digestInput)
	result.WorkloadDigest = hex.EncodeToString(digest[:])
	begin := qualificationReadUsage(b)
	result.StartedAt = time.Now()
	result.Samples = append(
		result.Samples,
		qualificationIdleSample{Memory: qualificationReadMemory(begin)},
	)
	ticker := time.NewTicker(samplePeriod)
	defer ticker.Stop()
	deadline := time.NewTimer(duration)
	defer deadline.Stop()
	b.ResetTimer()
	b.StartTimer()
	measuring := true
	var finish processusage.Snapshot
	for measuring {
		select {
		case <-ticker.C:
		case <-deadline.C:
			measuring = false
		}
		finish = qualificationReadUsage(b)
		result.Samples = append(
			result.Samples,
			qualificationIdleSample{
				AtNS:      time.Since(result.StartedAt).Nanoseconds(),
				CPUNS:     finish.CPUNanoseconds - begin.CPUNanoseconds,
				DiskBytes: finish.DiskBytesWritten - begin.DiskBytesWritten,
				Memory:    qualificationReadMemory(finish),
			},
		)
	}
	b.StopTimer()
	result.Idle = qualificationIdlePhase{
		ElapsedNS: time.Since(result.StartedAt).Nanoseconds(),
		CPUNS:     finish.CPUNanoseconds - begin.CPUNanoseconds,
		DiskBytes: finish.DiskBytesWritten - begin.DiskBytesWritten,
	}
	result.TimebaseNumer, result.TimebaseDenom = finish.TimebaseNumer, finish.TimebaseDenom
	shutdownStarted := time.Now()
	shutdownBegin := qualificationReadUsage(b)
	server.Close()
	shutdownFinish := qualificationReadUsage(b)
	result.Shutdown = qualificationIdlePhase{
		ElapsedNS: time.Since(shutdownStarted).Nanoseconds(),
		CPUNS:     shutdownFinish.CPUNanoseconds - shutdownBegin.CPUNanoseconds,
		DiskBytes: shutdownFinish.DiskBytesWritten - shutdownBegin.DiskBytesWritten,
	}
	result.AfterShutdown = qualificationInspectIdleHistory(b, cfg, now, count+1, count)
	for _, sample := range result.Samples {
		result.Peak.ResidentBytes = max(result.Peak.ResidentBytes, sample.Memory.ResidentBytes)
		result.Peak.PhysicalFootprintBytes = max(
			result.Peak.PhysicalFootprintBytes,
			sample.Memory.PhysicalFootprintBytes,
		)
		result.Peak.GoHeapAllocBytes = max(
			result.Peak.GoHeapAllocBytes,
			sample.Memory.GoHeapAllocBytes,
		)
		result.Peak.GoHeapInuseBytes = max(
			result.Peak.GoHeapInuseBytes,
			sample.Memory.GoHeapInuseBytes,
		)
	}
	result.Settled = result.Samples[len(result.Samples)-1].Memory
	body, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		b.Fatal(err)
	}
	if err := os.WriteFile(
		os.Getenv("QUALIFICATION_OUTPUT"),
		append(body, '\n'),
		0o600,
	); err != nil {
		b.Fatal(err)
	}
	b.ReportMetric(float64(result.Idle.DiskBytes)/duration.Seconds(), "disk-bytes/s")
	b.ReportMetric(float64(result.Idle.CPUNS)/duration.Seconds(), "cpu-ns/s")
	b.ReportMetric(float64(result.Peak.ResidentBytes), "peak-resident-bytes")
}

func qualificationReadUsage(t testing.TB) processusage.Snapshot {
	t.Helper()
	usage, err := processusage.Read()
	if err != nil {
		t.Fatal(err)
	}
	return usage
}

func qualificationReadMemory(usage processusage.Snapshot) qualificationMemory {
	var memory runtime.MemStats
	runtime.ReadMemStats(&memory)
	return qualificationMemory{
		ResidentBytes:          usage.ResidentBytes,
		PhysicalFootprintBytes: usage.PhysicalFootprintBytes,
		GoHeapAllocBytes:       memory.HeapAlloc,
		GoHeapInuseBytes:       memory.HeapInuse,
	}
}

type qualificationFile struct {
	Name           string
	LogicalBytes   int64
	AllocatedBytes int64
}

type qualificationHistory struct {
	Files         []qualificationFile
	DatabaseFiles int
	CatalogBytes  int64
	Receipts      int
	Completed     int
	Evaluations   int
}

func qualificationIdleConfig(t testing.TB) *config.Config {
	t.Helper()
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(root, "runtime"))
	cfg, err := config.LoadExisting("testdata/qualification_idle.toml")
	if err != nil {
		t.Fatal(err)
	}
	cfg.Audit.Outputs.SQLite.Path = filepath.Join(root, "audit.db")
	if cfg.UpdateEnabled() {
		t.Fatal("qualification requires disabled updates")
	}
	return cfg
}

func qualificationIdleCatalog(t testing.TB, cfg *config.Config) *auditstorage.Catalog {
	t.Helper()
	catalog, err := auditstorage.NewCatalog(cfg.AuditCatalogOptions())
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}

func qualificationIdleHistoryCount(t testing.TB, history string) int {
	t.Helper()
	switch history {
	case "empty":
		return 0
	case "seven":
		return 6
	case "80":
		return 80
	case "160":
		return 160
	default:
		t.Fatalf("history must be empty, seven, 80, or 160: %q", history)
		return 0
	}
}

func qualificationSeedIdleHistory(
	t testing.TB,
	cfg *config.Config,
	history string,
	now time.Time,
) int {
	t.Helper()
	count := qualificationIdleHistoryCount(t, history)
	catalog := qualificationIdleCatalog(t, cfg)
	policy := cfg.AuditStoragePolicy().Rotation()
	for index := range count {
		recordedAt := now.Add(-24 * time.Hour)
		if history == "seven" {
			recordedAt = now.Add(-time.Duration(count-index) * 24 * time.Hour)
		}
		bucket, err := catalog.EnsureCurrent(context.Background(), policy, recordedAt)
		if err != nil {
			t.Fatal(err)
		}
		handle, err := catalog.OpenWriter(context.Background(), bucket)
		if err != nil {
			t.Fatal(err)
		}
		store, err := intake.NewStore(
			context.Background(),
			handle.Database,
			cfg.AuditStoragePolicy(),
			qualificationIdleLogger(),
		)
		if err != nil {
			t.Fatal(err)
		}
		payload := qualificationIdlePayload(uint64(index + 1))
		record := intake.Record{
			EventID:        fmt.Sprintf("idle-history-%d", index),
			RecordedAt:     recordedAt,
			System:         "codex",
			SessionID:      "idle-history",
			EventName:      "PreToolUse",
			RawPayload:     payload,
			NormalizedJSON: payload,
		}
		appended, err := store.Append(context.Background(), record)
		if err != nil {
			t.Fatal(err)
		}
		completed := evaluation.Record{
			Evaluation: evaluation.Evaluation{
				EvaluationID:      "hot-" + record.EventID,
				ReceiptID:         appended.ReceiptID,
				EventID:           appended.EventID,
				Attempt:           1,
				Mode:              "hot",
				StartedAt:         recordedAt,
				CompletedAt:       recordedAt,
				FinalVerdict:      "allow",
				FinalSource:       "deterministic",
				EnforcementAction: "allow",
			},
		}
		if err := store.Evaluations().RecordCompleted(context.Background(), completed); err != nil {
			t.Fatal(err)
		}
		if err := store.MarkDeferredComplete(context.Background(), appended.ReceiptID); err != nil {
			t.Fatal(err)
		}
		if err := handle.Close(); err != nil {
			t.Fatal(err)
		}
		if history != "seven" {
			// Sparse trailing bytes exercise size rotation without populating SQLite pages.
			if err := os.Truncate(bucket.Path, policy.SegmentBytes()); err != nil {
				t.Fatal(err)
			}
		}
	}
	return count
}

func qualificationInspectIdleHistory(
	t testing.TB,
	cfg *config.Config,
	now time.Time,
	wantFiles int,
	wantReceipts int,
) qualificationHistory {
	t.Helper()
	catalog := qualificationIdleCatalog(t, cfg)
	policy := cfg.AuditStoragePolicy().Rotation()
	readers, err := catalog.Read(context.Background(), policy, now)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := readers.Close(); err != nil {
			t.Fatal(err)
		}
	}()
	if len(readers.Handles) != wantFiles {
		t.Fatalf("retained files=%d, want=%d", len(readers.Handles), wantFiles)
	}
	integritySQL := qualificationReadFixture(t, "qualification_integrity.sql")
	completedSQL := qualificationReadFixture(t, "qualification_completed.sql")
	var result qualificationHistory
	for _, handle := range readers.Handles {
		var integrity string
		if err := handle.Database.QueryRowContext(context.Background(), integritySQL).Scan(
			&integrity,
		); err != nil {
			t.Fatal(err)
		}
		if integrity != "ok" {
			t.Fatalf("SQLite integrity for %s: %s", handle.Bucket.ID, integrity)
		}
		var receipts, completed, pending, evaluations int
		if err := handle.Database.QueryRowContext(context.Background(), completedSQL).Scan(
			&receipts,
			&completed,
			&pending,
			&evaluations,
		); err != nil {
			t.Fatal(err)
		}
		if pending != 0 || receipts != completed || receipts != evaluations {
			t.Fatalf(
				"incomplete fixture %s: receipts=%d completed=%d pending=%d evaluations=%d",
				handle.Bucket.ID,
				receipts,
				completed,
				pending,
				evaluations,
			)
		}
		result.Receipts += receipts
		result.Completed += completed
		result.Evaluations += evaluations
	}
	if result.Receipts != wantReceipts {
		t.Fatalf("retained receipts=%d, want=%d", result.Receipts, wantReceipts)
	}
	status, err := catalog.Status(policy, now)
	if err != nil {
		t.Fatal(err)
	}
	result.CatalogBytes = status.TotalBytes
	result.DatabaseFiles = len(status.RetainedFiles)
	var logicalBytes int64
	for _, bucket := range status.RetainedFiles {
		for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
			info, statError := os.Stat(bucket.Path + suffix)
			if os.IsNotExist(statError) {
				continue
			}
			if statError != nil {
				t.Fatal(statError)
			}
			stat, ok := info.Sys().(*syscall.Stat_t)
			if !ok {
				t.Fatal("Darwin file allocation statistics are unavailable")
			}
			const statBlockBytes = 512
			result.Files = append(
				result.Files,
				qualificationFile{
					Name:           filepath.Base(bucket.Path + suffix),
					LogicalBytes:   info.Size(),
					AllocatedBytes: stat.Blocks * statBlockBytes,
				},
			)
			logicalBytes += info.Size()
		}
	}
	if logicalBytes != status.TotalBytes || status.ExcessBytes != 0 {
		t.Fatalf(
			"catalog accounting=%d, file accounting=%d, excess=%d",
			status.TotalBytes,
			logicalBytes,
			status.ExcessBytes,
		)
	}
	return result
}

func qualificationReadFixture(t testing.TB, name string) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func TestQualificationIdleHistory(t *testing.T) {
	for _, history := range []string{"empty", "seven", "80", "160"} {
		t.Run(
			history,
			func(t *testing.T) {
				cfg := qualificationIdleConfig(t)
				now := time.Now()
				count := qualificationSeedIdleHistory(t, cfg, history, now)
				qualificationInspectIdleHistory(t, cfg, now, count, count)
				server, err := daemon.New(t.Context(), qualificationIdleLogger(), cfg)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(server.Close)
				server.StartAuditScheduler(t.Context())
				qualificationInspectIdleHistory(t, cfg, now, count+1, count)
				server.Close()
				qualificationInspectIdleHistory(t, cfg, now, count+1, count)
			},
		)
	}
}

func qualificationIdleLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func qualificationIdlePayload(sequence uint64) []byte {
	const payloadBytes = 1024
	const marker = "idle-history"
	format := func(command string) []byte {
		return fmt.Appendf(
			nil,
			`{"session_id":"performance","tool_use_id":"%d","hook_event_name":"PreToolUse","tool_name":"Shell","tool_input":{"command":%q}}`,
			sequence,
			command,
		)
	}
	envelopeBytes := len(format(marker))
	return format(marker + strings.Repeat("x", payloadBytes-envelopeBytes))
}
