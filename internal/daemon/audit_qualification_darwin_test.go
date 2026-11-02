//go:build auditqualification

package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sync"
	"syscall"
	"testing"
	"time"

	"goodkind.io/agent-gate/internal/audit"
	"goodkind.io/agent-gate/internal/auditstorage"
	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/evaluation"
	"goodkind.io/agent-gate/internal/intake"
	"goodkind.io/agent-gate/internal/testutil/processusage"
)

const qualificationSeedsPerWindow = 200

type qualificationInterval struct {
	ElapsedNS int64
	CPUNS     uint64
	DiskBytes uint64
}

type qualificationResult struct {
	Source                 string
	FixtureDigest          string
	Scenario               string
	History                string
	Repetition             string
	ElapsedNS              int64
	CPUNS                  uint64
	DiskBytes              uint64
	TimebaseNumer          uint32
	TimebaseDenom          uint32
	CommitCallbacks        int64
	RequestLatencyNS       []int64
	CompletionLatencyNS    []int64
	StatusLatencyNS        []int64
	QueuePeak              int
	OldestPendingNS        int64
	Retries                map[int64]int
	Intervals              []qualificationInterval
	Files                  map[string]int64
	SeededReceipts         int
	CompletedTraceReceipts int
	Failures               []string
	SynchronizationCalls   *uint64
}

type qualificationRecorder struct {
	evaluationRecorder
	mu        sync.Mutex
	starts    map[int64]time.Time
	latencies []int64
	retries   map[int64]int
	failures  []string
	oldest    time.Duration
}

type qualificationIntake struct {
	intakeStore
	metrics *qualificationRecorder
}

func (store qualificationIntake) Append(ctx context.Context, record intake.Record) (intake.AppendResult, error) {
	started := time.Now()
	result, err := store.intakeStore.Append(ctx, record)
	if err == nil {
		store.metrics.mu.Lock()
		store.metrics.starts[result.ReceiptID] = started
		store.metrics.mu.Unlock()
	}
	return result, err
}

func (recorder *qualificationRecorder) CommitDeferredEvaluation(ctx context.Context, claim intake.DeferredClaim, record evaluation.Record, entries []audit.NormalizedEntry) error {
	err := recorder.evaluationRecorder.CommitDeferredEvaluation(ctx, claim, record, entries)
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	recorder.retries[claim.ReceiptID] = claim.Attempt - 1
	if err != nil {
		recorder.failures = append(recorder.failures, err.Error())
	} else if started, ok := recorder.starts[claim.ReceiptID]; ok {
		elapsed := time.Since(started)
		recorder.latencies = append(recorder.latencies, elapsed.Nanoseconds())
		recorder.oldest = max(recorder.oldest, elapsed)
		delete(recorder.starts, claim.ReceiptID)
	}
	return err
}

func BenchmarkAuditQualification(b *testing.B) {
	if os.Getenv("QUALIFICATION_SCENARIO") == "idle" {
		benchmarkAuditIdleQualification(b)
		return
	}
	if b.N != 1 {
		b.Fatal("qualification requires -benchtime=1x and one process per repetition")
	}
	result := qualificationResult{Source: os.Getenv("QUALIFICATION_SOURCE"), FixtureDigest: auditTraceDigest, Scenario: os.Getenv("QUALIFICATION_SCENARIO"), History: os.Getenv("QUALIFICATION_HISTORY"), Repetition: os.Getenv("QUALIFICATION_REPETITION"), Files: make(map[string]int64), Failures: []string{}}
	if result.Source == "" || os.Getenv("QUALIFICATION_OUTPUT") == "" {
		b.Fatal("qualification source and output are required")
	}
	switch result.Scenario {
	case "burst", "steady", "status", "cancellation", "backlog":
	default:
		b.Fatal("unknown qualification scenario")
	}
	if result.History != "empty" && result.History != "seven" {
		b.Fatal("history must be empty or seven")
	}
	requests := auditPerformanceTrace()
	if auditPerformanceTraceDigest(requests) != auditTraceDigest {
		b.Fatal("trace changed")
	}
	fake := newDeferredInferenceFake(`{"decision":"block"}`)
	cfg := auditTraceConfig(b, startAuditTraceInferenceServer(b, fake))
	server, start := newQualificationServer(b, cfg)
	if result.History == "seven" {
		result.SeededReceipts = qualificationSeedHistory(b, server, cfg, result.Scenario == "backlog")
	}
	if result.Scenario != "backlog" {
		start()
	}
	snapshot := server.runtime.Load()
	store, ok := snapshot.intakeStore.(*sqliteIntakeStore)
	if !ok {
		b.Fatal("qualification requires real intake")
	}
	counter := qualificationCommitCounter(b, store.Handle())
	metrics := &qualificationRecorder{evaluationRecorder: snapshot.evaluationRecorder, starts: make(map[int64]time.Time), retries: make(map[int64]int)}
	qualificationAttach(server, metrics)
	begin, err := processusage.Read()
	if err != nil {
		b.Fatal(err)
	}
	started := time.Now()
	done := make(chan struct{})
	intervals := make(chan []qualificationInterval, 1)
	go qualificationSample(begin, started, done, intervals)
	if result.Scenario == "backlog" {
		start()
	}
	b.ResetTimer()
	for index, request := range requests {
		ctx := context.Background()
		cancel := func() {}
		if result.Scenario == "cancellation" {
			ctx, cancel = context.WithCancel(ctx)
		}
		callStarted := time.Now()
		response, callError := server.EvaluateHook(ctx, request)
		result.RequestLatencyNS = append(result.RequestLatencyNS, time.Since(callStarted).Nanoseconds())
		cancel()
		if callError != nil {
			result.Failures = append(result.Failures, callError.Error())
		} else {
			assertAuditTraceResponse(b, index, response)
		}
		result.QueuePeak = max(result.QueuePeak, len(snapshot.deferredProcessor.events))
		if result.Scenario == "status" && (index+1)%100 == 0 {
			statusStarted := time.Now()
			qualificationStatus(b, server, cfg)
			result.StatusLatencyNS = append(result.StatusLatencyNS, time.Since(statusStarted).Nanoseconds())
		}
		if result.Scenario == "steady" {
			time.Sleep(10 * time.Millisecond)
		}
	}
	flushAuditTrace(b, server)
	qualificationFlushHistory(b, server, cfg)
	server.Close()
	b.StopTimer()
	result.ElapsedNS = time.Since(started).Nanoseconds()
	finish, err := processusage.Read()
	close(done)
	result.Intervals = <-intervals
	if err != nil {
		b.Fatal(err)
	}
	result.CPUNS = finish.CPUNanoseconds - begin.CPUNanoseconds
	result.DiskBytes = finish.DiskBytesWritten - begin.DiskBytesWritten
	result.TimebaseNumer, result.TimebaseDenom = finish.TimebaseNumer, finish.TimebaseDenom
	result.CommitCallbacks = counter.Load()
	metrics.mu.Lock()
	result.CompletionLatencyNS = slices.Clone(metrics.latencies)
	result.CompletedTraceReceipts = len(metrics.latencies)
	result.Retries = metrics.retries
	result.OldestPendingNS = metrics.oldest.Nanoseconds()
	result.Failures = append(result.Failures, metrics.failures...)
	metrics.mu.Unlock()
	if fake.callCount() != auditTraceDeferredRequests {
		result.Failures = append(result.Failures, fmt.Sprintf("inference calls=%d want=%d", fake.callCount(), auditTraceDeferredRequests))
	}
	if result.CompletedTraceReceipts != len(requests) {
		result.Failures = append(result.Failures, fmt.Sprintf("completed trace=%d want=%d", result.CompletedTraceReceipts, len(requests)))
	}
	files, err := filepath.Glob(filepath.Join(filepath.Dir(cfg.AuditSQLitePath()), "*.db*"))
	if err != nil {
		b.Fatal(err)
	}
	for _, path := range files {
		info, statError := os.Stat(path)
		if statError != nil {
			b.Fatal(statError)
		}
		result.Files[filepath.Base(path)] = info.Size()
	}
	body, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		b.Fatal(err)
	}
	if err := os.WriteFile(os.Getenv("QUALIFICATION_OUTPUT"), append(body, '\n'), 0o600); err != nil {
		b.Fatal(err)
	}
	if len(result.Failures) != 0 {
		b.Fatalf("qualification failures: %v", result.Failures)
	}
	b.ReportMetric(float64(result.CPUNS)/float64(len(requests)), "cpu-ns/event")
	b.ReportMetric(float64(result.DiskBytes)/float64(len(requests)), "disk-bytes/event")
}

func qualificationSample(begin processusage.Snapshot, started time.Time, done <-chan struct{}, output chan<- []qualificationInterval) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	var intervals []qualificationInterval
	previous, previousTime := begin, started
	for {
		select {
		case <-done:
			output <- intervals
			return
		case <-ticker.C:
			now := time.Now()
			next, err := processusage.Read()
			if err != nil {
				continue
			}
			intervals = append(intervals, qualificationInterval{ElapsedNS: now.Sub(previousTime).Nanoseconds(), CPUNS: next.CPUNanoseconds - previous.CPUNanoseconds, DiskBytes: next.DiskBytesWritten - previous.DiskBytesWritten})
			previous, previousTime = next, now
		}
	}
}

func qualificationSeedRecords(b testing.TB, store *intake.Store, day int, pending bool) {
	b.Helper()
	for index := range qualificationSeedsPerWindow {
		request := auditTraceRequest(uint64(index+1), "performance-allow", auditTraceInputSizes[index%len(auditTraceInputSizes)])
		record := intake.Record{EventID: fmt.Sprintf("seed-%d-%d", day, index), RecordedAt: time.Now().Add(-time.Duration(day) * 24 * time.Hour), System: "codex", SessionID: "seed", EventName: "PreToolUse", RawPayload: request.RawJson, NormalizedJSON: request.RawJson, EnvFingerprint: request.EnvFingerprint}
		appended, err := store.Append(context.Background(), record)
		if err != nil {
			b.Fatal(err)
		}
		completed := evaluation.Record{Evaluation: evaluation.Evaluation{EvaluationID: "hot-" + record.EventID, ReceiptID: appended.ReceiptID, EventID: appended.EventID, Attempt: 1, Mode: "hot", StartedAt: record.RecordedAt, CompletedAt: record.RecordedAt, FinalVerdict: "allow", FinalSource: "deterministic", EnforcementAction: "allow"}}
		if err := store.Evaluations().RecordCompleted(context.Background(), completed); err != nil {
			b.Fatal(err)
		}
		if pending {
			err = store.MarkDeferredPending(context.Background(), appended.EventID, appended.ReceiptID)
		} else {
			err = store.MarkDeferredComplete(context.Background(), appended.ReceiptID)
		}
		if err != nil {
			b.Fatal(err)
		}
	}
}

const qualificationIdleVersion = "idle-completed-history-v1"

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

func benchmarkAuditIdleQualification(b *testing.B) {
	b.StopTimer()
	if b.N != 1 {
		b.Fatal("qualification requires -benchtime=1x and one process per repetition")
	}
	result := qualificationIdleResult{
		Version: qualificationIdleVersion, Source: os.Getenv("QUALIFICATION_SOURCE"),
		Scenario: "idle", History: os.Getenv("QUALIFICATION_HISTORY"), Repetition: os.Getenv("QUALIFICATION_REPETITION"),
		PID: os.Getpid(), MetricScope: "test process with real in-process daemon and audit scheduler; process counters exclude child processes",
		Limitations: []string{
			"The process includes the Go test harness, fixture residue, and resource sampling overhead; it does not run the daemon RPC listener, supervisor, update scheduler, or production log sink.",
			"One-second memory samples report observed peaks rather than an exact lifetime maximum; settled memory is the final idle sample without forced garbage collection.",
			"The idle interval excludes preparation, verification queries, startup, shutdown, and result serialization; shutdown counters appear separately.",
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
	cfg := qualificationIdleConfig(b)
	policy := cfg.AuditStoragePolicy()
	result.SegmentThresholdBytes = policy.Rotation().SegmentBytes()
	result.RetentionMaxBytes = policy.MaxBytes
	result.Profile = string(policy.Profile)
	now := time.Now()
	result.CompletedHistoryFiles = qualificationSeedIdleHistory(b, cfg, result.History, now)
	count := result.CompletedHistoryFiles
	if result.History == "80" || result.History == "160" {
		result.Limitations = append(result.Limitations, "Historical files contain one real 1 KiB receipt and completed hot evaluation each, with sparse trailing bytes outside SQLite's populated pages. This fixture tests file cardinality and logical byte accounting rather than densely populated history.")
	}
	result.BeforeStartup = qualificationInspectIdleHistory(b, cfg, now, count, count)
	server, err := newServer(b.Context(), newDiscardLogger(), cfg, time.Now)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(server.Close)
	server.StartAuditScheduler(b.Context())
	<-server.auditStarted
	result.AfterStartup = qualificationInspectIdleHistory(b, cfg, now, count+1, count)
	time.Sleep(samplePeriod)
	digestInput := fmt.Appendf(nil, "%s\nhistory=%s\nduration=%s\nconfig=%s", qualificationIdleVersion, result.History, duration, qualificationReadFixture(b, "qualification_idle.toml"))
	for index := range count {
		digestInput = append(digestInput, auditTraceRequest(uint64(index+1), "idle-history", 1024).RawJson...)
		digestInput = append(digestInput, '\n')
	}
	digest := sha256.Sum256(digestInput)
	result.WorkloadDigest = hex.EncodeToString(digest[:])
	begin := qualificationReadUsage(b)
	result.StartedAt = time.Now()
	result.Samples = append(result.Samples, qualificationIdleSample{Memory: qualificationReadMemory(begin)})
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
		result.Samples = append(result.Samples, qualificationIdleSample{AtNS: time.Since(result.StartedAt).Nanoseconds(), CPUNS: finish.CPUNanoseconds - begin.CPUNanoseconds, DiskBytes: finish.DiskBytesWritten - begin.DiskBytesWritten, Memory: qualificationReadMemory(finish)})
	}
	b.StopTimer()
	result.Idle = qualificationIdlePhase{ElapsedNS: time.Since(result.StartedAt).Nanoseconds(), CPUNS: finish.CPUNanoseconds - begin.CPUNanoseconds, DiskBytes: finish.DiskBytesWritten - begin.DiskBytesWritten}
	result.TimebaseNumer, result.TimebaseDenom = finish.TimebaseNumer, finish.TimebaseDenom
	shutdownStarted := time.Now()
	shutdownBegin := qualificationReadUsage(b)
	server.Close()
	shutdownFinish := qualificationReadUsage(b)
	result.Shutdown = qualificationIdlePhase{ElapsedNS: time.Since(shutdownStarted).Nanoseconds(), CPUNS: shutdownFinish.CPUNanoseconds - shutdownBegin.CPUNanoseconds, DiskBytes: shutdownFinish.DiskBytesWritten - shutdownBegin.DiskBytesWritten}
	result.AfterShutdown = qualificationInspectIdleHistory(b, cfg, now, count+1, count)
	for _, sample := range result.Samples {
		result.Peak.ResidentBytes = max(result.Peak.ResidentBytes, sample.Memory.ResidentBytes)
		result.Peak.PhysicalFootprintBytes = max(result.Peak.PhysicalFootprintBytes, sample.Memory.PhysicalFootprintBytes)
		result.Peak.GoHeapAllocBytes = max(result.Peak.GoHeapAllocBytes, sample.Memory.GoHeapAllocBytes)
		result.Peak.GoHeapInuseBytes = max(result.Peak.GoHeapInuseBytes, sample.Memory.GoHeapInuseBytes)
	}
	result.Settled = result.Samples[len(result.Samples)-1].Memory
	body, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		b.Fatal(err)
	}
	if err := os.WriteFile(os.Getenv("QUALIFICATION_OUTPUT"), append(body, '\n'), 0o600); err != nil {
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
	return qualificationMemory{ResidentBytes: usage.ResidentBytes, PhysicalFootprintBytes: usage.PhysicalFootprintBytes, GoHeapAllocBytes: memory.HeapAlloc, GoHeapInuseBytes: memory.HeapInuse}
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

func qualificationSeedIdleHistory(t testing.TB, cfg *config.Config, history string, now time.Time) int {
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
		store, err := intake.NewStore(context.Background(), handle.Database, cfg.AuditStoragePolicy(), newDiscardLogger())
		if err != nil {
			t.Fatal(err)
		}
		request := auditTraceRequest(uint64(index+1), "idle-history", 1024)
		record := intake.Record{EventID: fmt.Sprintf("idle-history-%d", index), RecordedAt: recordedAt, System: "codex", SessionID: "idle-history", EventName: "PreToolUse", RawPayload: request.RawJson, NormalizedJSON: request.RawJson}
		appended, err := store.Append(context.Background(), record)
		if err != nil {
			t.Fatal(err)
		}
		completed := evaluation.Record{Evaluation: evaluation.Evaluation{EvaluationID: "hot-" + record.EventID, ReceiptID: appended.ReceiptID, EventID: appended.EventID, Attempt: 1, Mode: "hot", StartedAt: recordedAt, CompletedAt: recordedAt, FinalVerdict: "allow", FinalSource: "deterministic", EnforcementAction: "allow"}}
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

func qualificationInspectIdleHistory(t testing.TB, cfg *config.Config, now time.Time, wantFiles, wantReceipts int) qualificationHistory {
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
		if err := handle.Database.QueryRowContext(context.Background(), integritySQL).Scan(&integrity); err != nil {
			t.Fatal(err)
		}
		if integrity != "ok" {
			t.Fatalf("SQLite integrity for %s: %s", handle.Bucket.ID, integrity)
		}
		var receipts, completed, pending, evaluations int
		if err := handle.Database.QueryRowContext(context.Background(), completedSQL).Scan(&receipts, &completed, &pending, &evaluations); err != nil {
			t.Fatal(err)
		}
		if pending != 0 || receipts != completed || receipts != evaluations {
			t.Fatalf("incomplete fixture %s: receipts=%d completed=%d pending=%d evaluations=%d", handle.Bucket.ID, receipts, completed, pending, evaluations)
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
			result.Files = append(result.Files, qualificationFile{Name: filepath.Base(bucket.Path + suffix), LogicalBytes: info.Size(), AllocatedBytes: stat.Blocks * statBlockBytes})
			logicalBytes += info.Size()
		}
	}
	if logicalBytes != status.TotalBytes || status.ExcessBytes != 0 {
		t.Fatalf("catalog accounting=%d, file accounting=%d, excess=%d", status.TotalBytes, logicalBytes, status.ExcessBytes)
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
		t.Run(history, func(t *testing.T) {
			cfg := qualificationIdleConfig(t)
			now := time.Now()
			count := qualificationSeedIdleHistory(t, cfg, history, now)
			qualificationInspectIdleHistory(t, cfg, now, count, count)
			server, err := New(t.Context(), newDiscardLogger(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(server.Close)
			server.StartAuditScheduler(t.Context())
			<-server.auditStarted
			qualificationInspectIdleHistory(t, cfg, now, count+1, count)
			server.Close()
			qualificationInspectIdleHistory(t, cfg, now, count+1, count)
		})
	}
}
