//go:build auditqualification

package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"goodkind.io/agent-gate/internal/audit"
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
