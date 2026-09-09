package daemon

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"goodkind.io/agent-gate/api/daemonpb"
	"goodkind.io/agent-gate/api/inferencepb"
	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/evaluation"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type replayInferenceServer struct {
	inferencepb.UnimplementedInferenceServer
	calls   atomic.Int64
	entered chan struct{}
	release <-chan struct{}
	fail    bool
}

func (s *replayInferenceServer) Infer(ctx context.Context, _ *inferencepb.InferRequest) (*inferencepb.InferReply, error) {
	call := s.calls.Add(1)
	if s.fail {
		return nil, status.Error(codes.Unavailable, "injected inference failure")
	}
	if call == 1 && s.entered != nil {
		close(s.entered)
		select {
		case <-s.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	promptTokens, completionTokens := int64(10), int64(2)
	return &inferencepb.InferReply{
		OutputJson: `{"decision":"block"}`, Status: inferencepb.InferenceStatus_INFERENCE_STATUS_COMPLETE,
		Metadata: &inferencepb.InvocationMetadata{RequestId: fmt.Sprintf("measured-%d", call), RequestedModel: "measured-model", PromptTokens: &promptTokens, CompletionTokens: &completionTokens},
	}, nil
}

func startReplayInference(t *testing.T, server *replayInferenceServer) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	grpcServer := grpc.NewServer()
	inferencepb.RegisterInferenceServer(grpcServer, server)
	go func() { _ = grpcServer.Serve(listener) }()
	t.Cleanup(grpcServer.Stop)
	return listener.Addr().String()
}

func replayTestConfig(t *testing.T, endpoint, profile string) *config.Config {
	t.Helper()
	cfg := loadDeferredAuditInferConfig(t, endpoint)
	cfg.Audit.Storage.Profile = profile
	cfg.Performance.Hook.DeferredQueueLimit = 1
	cfg.Performance.Hook.DeferredWorkers = 1
	return cfg
}

func replayRequest() *daemonpb.EvaluateHookRequest {
	return &daemonpb.EvaluateHookRequest{ProviderHint: "codex", RawJson: []byte(`{"session_id":"replay","hook_event_name":"PreToolUse","tool_name":"Shell","tool_input":{"command":"echo audit"}}`)}
}

func TestReplaySchedulerDrainsBurstWithoutLostInference(t *testing.T) {
	for _, profile := range []string{"full", "minimal"} {
		t.Run(profile, func(t *testing.T) {
			setDaemonTestDirs(t)
			release := make(chan struct{})
			fake := &replayInferenceServer{entered: make(chan struct{}), release: release}
			cfg := replayTestConfig(t, startReplayInference(t, fake), profile)
			server, err := newReadyTestServer(newDiscardLogger(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(server.Close)
			if _, err := server.EvaluateHook(t.Context(), replayRequest()); err != nil {
				t.Fatal(err)
			}
			select {
			case <-fake.entered:
			case <-time.After(time.Second):
				t.Fatal("worker did not enter inference")
			}
			const receipts = 130
			for range receipts - 1 {
				if _, err := server.EvaluateHook(t.Context(), replayRequest()); err != nil {
					t.Fatal(err)
				}
			}
			processor := server.runtime.Load().deferredProcessor
			processor.queueMu.Lock()
			queued, queuedBytes := len(processor.queued), processor.queuedBytes
			processor.queueMu.Unlock()
			if queued > 2 || queuedBytes > replayDispatchBytes {
				t.Fatalf("unbounded queue: %d entries, %d bytes", queued, queuedBytes)
			}
			if fake.calls.Load() != 1 {
				t.Fatalf("worker limit exceeded: %d", fake.calls.Load())
			}
			close(release)
			waitRuntimeCondition(t, func() bool { return completedReplayCount(t, server) == receipts })
			if calls := fake.calls.Load(); calls != receipts {
				t.Fatalf("calls = %d, want %d", calls, receipts)
			}
			server.Close()
			restarted, err := newReadyTestServer(newDiscardLogger(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer restarted.Close()
			if err := restarted.dispatchRetainedReplay(t.Context(), restarted.now()); err != nil {
				t.Fatal(err)
			}
			if completedReplayCount(t, restarted) != receipts || fake.calls.Load() != receipts {
				t.Fatal("completed receipts repeated after restart")
			}
		})
	}
}

func TestReplaySchedulerRetainsBucketKeysAndDiscardsExpiredPending(t *testing.T) {
	for _, profile := range []string{"full", "minimal"} {
		t.Run(profile, func(t *testing.T) {
			setDaemonTestDirs(t)
			fake := &replayInferenceServer{}
			cfg := replayTestConfig(t, startReplayInference(t, fake), profile)
			now := time.Now()
			var expiredPath string
			for _, days := range []int{8, 1, 0} {
				at := now.Add(-time.Duration(days) * 24 * time.Hour)
				server, err := newServer(t.Context(), newDiscardLogger(), cfg, func() time.Time { return at })
				if err != nil {
					t.Fatal(err)
				}
				if days == 8 {
					expiredPath = server.runtime.Load().bucket.Path
				}
				if _, err := server.EvaluateHook(t.Context(), replayRequest()); err != nil {
					t.Fatal(err)
				}
				if days == 1 {
					if _, _, err := server.runtime.Load().intakeStore.ClaimDeferred(t.Context(), 1, "interrupted-owner", time.Nanosecond); err != nil {
						t.Fatal(err)
					}
				}
				server.Close()
			}
			server, err := newReadyTestServer(newDiscardLogger(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer server.Close()
			waitRuntimeCondition(t, func() bool {
				processor := server.runtime.Load().deferredProcessor
				processor.queueMu.Lock()
				defer processor.queueMu.Unlock()
				return fake.calls.Load() == 2 && len(processor.queued) == 0
			})
			if completed := retainedReplayCount(t, server); completed != 2 {
				t.Fatalf("retained completions = %d", completed)
			}
			if fake.calls.Load() != 2 {
				t.Fatalf("retained calls = %d, want 2", fake.calls.Load())
			}
			if _, err := os.Stat(expiredPath); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("expired pending bucket remains: %v", err)
			}
		})
	}
}

func TestReplaySchedulerRunsOnlyItsPhaseAndPreservesCost(t *testing.T) {
	for _, profile := range []string{"full", "minimal"} {
		for _, withDeferred := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/deferred=%v", profile, withDeferred), func(t *testing.T) {
				setDaemonTestDirs(t)
				fake := &replayInferenceServer{}
				endpoint := startReplayInference(t, fake)
				cfg := loadHotAuditInferConfig(t, endpoint, "PreToolUse")
				cfg.Rules = cfg.Rules[:1]
				if withDeferred {
					cfg.Rules = append(cfg.Rules, loadDeferredAuditInferConfig(t, endpoint).Rules...)
				}
				cfg.Audit.Storage.Profile = profile
				server, err := newServer(t.Context(), newDiscardLogger(), cfg, time.Now)
				if err != nil {
					t.Fatal(err)
				}
				response, err := server.EvaluateHook(t.Context(), replayRequest())
				if err != nil || !strings.Contains(string(response.StdoutData), `"permissionDecision":"deny"`) {
					t.Fatalf("hot response = %+v, %v", response, err)
				}
				if fake.calls.Load() != 1 {
					t.Fatalf("hot calls = %d", fake.calls.Load())
				}
				server.Close()
				server, err = newReadyTestServer(newDiscardLogger(), cfg)
				if err != nil {
					t.Fatal(err)
				}
				defer server.Close()
				waitRuntimeCondition(t, func() bool { return completedReplayCount(t, server) == 1 })
				wantCalls := int64(1)
				wantMessages := []string{"hook.received", "hook.blocked"}
				if withDeferred {
					wantCalls++
					wantMessages = append(wantMessages, "hook.audit_violation")
				}
				if fake.calls.Load() != wantCalls {
					t.Fatalf("phase calls = %d, want %d", fake.calls.Load(), wantCalls)
				}
				assertHotAuditMessages(t, readHotAuditEvents(t, server), wantMessages)
				report, err := evaluation.CostReport(t.Context(), server.runtime.Load().bucket.Path, map[string]evaluation.ModelPricing{"measured-model": {InputPerMillion: 1, OutputPerMillion: 2}}, evaluation.CostFilter{})
				if err != nil || len(report.Models) != 1 || report.Models[0].Calls != wantCalls || report.Models[0].PromptTokens != 10*wantCalls || report.TotalBilledCostMicros != 14*wantCalls {
					t.Fatalf("cost report = %+v, %v", report, err)
				}
			})
		}
	}
}

func TestReplaySchedulerRetriesAfterUncertainExternalSuccess(t *testing.T) {
	setDaemonTestDirs(t)
	fake := &replayInferenceServer{}
	cfg := replayTestConfig(t, startReplayInference(t, fake), "minimal")
	server, err := newServer(t.Context(), newDiscardLogger(), cfg, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	database := server.runtime.Load().bucketHandle.Database
	if _, err := database.ExecContext(t.Context(), readDaemonSQLFixture(t, "fail_deferred_audit.sql")); err != nil {
		t.Fatal(err)
	}
	if _, err := server.EvaluateHook(t.Context(), replayRequest()); err != nil {
		t.Fatal(err)
	}
	server.StartAuditScheduler(t.Context())
	var nextAttempt sql.NullString
	waitRuntimeCondition(t, func() bool {
		var attempt int
		var owner sql.NullString
		if err := database.QueryRowContext(t.Context(), readDaemonSQLFixture(t, "runtime_deferred_retry.sql"), 1).Scan(&attempt, &nextAttempt, &owner); err != nil {
			t.Fatal(err)
		}
		return attempt == 1 && nextAttempt.Valid && !owner.Valid
	})
	if completedReplayCount(t, server) != 0 || fake.calls.Load() != 1 {
		t.Fatal("failed audit completion did not roll back")
	}
	if err := server.dispatchRetainedReplay(t.Context(), time.Now()); err != nil {
		t.Fatal(err)
	}
	if fake.calls.Load() != 1 {
		t.Fatal("retry ran before its due time")
	}
	if _, err := database.ExecContext(t.Context(), readDaemonSQLFixture(t, "drop_fail_deferred_audit.sql")); err != nil {
		t.Fatal(err)
	}
	waitRuntimeCondition(t, func() bool { return completedReplayCount(t, server) == 1 })
	if fake.calls.Load() != 2 {
		t.Fatalf("uncertain success attempts = %d, want 2", fake.calls.Load())
	}
	if err := server.dispatchRetainedReplay(t.Context(), time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if fake.calls.Load() != 2 {
		t.Fatal("committed completion repeated")
	}
	assertHotAuditMessages(t, readHotAuditEvents(t, server), []string{"hook.allowed", "hook.audit_violation"})
}

func completedReplayCount(t *testing.T, server *Server) int {
	t.Helper()
	var pending, completed int
	if err := server.runtime.Load().bucketHandle.Database.QueryRowContext(t.Context(), readDaemonSQLFixture(t, "runtime_deferred_summary.sql")).Scan(&pending, &completed); err != nil {
		t.Fatal(err)
	}
	return completed
}

func TestReplayInferenceErrorPolicyCommitsWithoutRetry(t *testing.T) {
	for _, policy := range []string{"open", "closed"} {
		t.Run(policy, func(t *testing.T) {
			setDaemonTestDirs(t)
			fake := &replayInferenceServer{fail: true}
			cfg := replayTestConfig(t, startReplayInference(t, fake), "full")
			cfg.Rules[0].Conditions[0].OnError = policy
			var clock atomic.Int64
			clock.Store(time.Now().Unix())
			now := func() time.Time { return time.Unix(clock.Load(), 0).UTC() }
			server, err := newServer(t.Context(), newDiscardLogger(), cfg, now)
			if err != nil {
				t.Fatal(err)
			}
			defer server.Close()
			ticks := make(chan time.Time)
			server.auditTicks = ticks
			server.StartAuditScheduler(t.Context())
			if _, err := server.EvaluateHook(t.Context(), replayRequest()); err != nil {
				t.Fatal(err)
			}
			waitRuntimeCondition(t, func() bool { return completedReplayCount(t, server) == 1 })
			var layerStatus, errorCode string
			if err := server.runtime.Load().bucketHandle.Database.QueryRowContext(t.Context(), readDaemonSQLFixture(t, "runtime_deferred_error.sql")).Scan(&layerStatus, &errorCode); err != nil || layerStatus != "error" || errorCode != "unavailable" {
				t.Fatalf("error layer = %s/%s, %v", layerStatus, errorCode, err)
			}
			clock.Add(31)
			select {
			case ticks <- now():
			case <-time.After(time.Second):
				t.Fatal("retry timer was not observed")
			}
			if err := server.dispatchRetainedReplay(t.Context(), now()); err != nil {
				t.Fatal(err)
			}
			if fake.calls.Load() != 1 {
				t.Fatalf("terminal inference error repeated: %d", fake.calls.Load())
			}
			want := []string{"hook.allowed"}
			if policy == "closed" {
				want = append(want, "hook.audit_violation")
			}
			assertHotAuditMessages(t, readHotAuditEvents(t, server), want)
		})
	}
}

func TestReplaySchedulerEnforcesIdentifierByteBudget(t *testing.T) {
	setDaemonTestDirs(t)
	cfg := daemonTestConfig(t)
	cfg.Performance.Hook.DeferredWorkers = 0
	cfg.Performance.Hook.DeferredQueueLimit = 10000
	server, err := newServer(t.Context(), newDiscardLogger(), cfg, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	replaceDeferredProcessorForTest(t, server, 10000, 0)
	const receipts = 4000
	for range receipts {
		if _, err := server.EvaluateHook(t.Context(), replayRequest()); err != nil {
			t.Fatal(err)
		}
	}
	if err := server.dispatchRetainedReplay(t.Context(), server.now()); err != nil {
		t.Fatal(err)
	}
	processor := server.runtime.Load().deferredProcessor
	processor.queueMu.Lock()
	defer processor.queueMu.Unlock()
	if len(processor.queued) == 0 || len(processor.queued) >= receipts || processor.queuedBytes > replayDispatchBytes {
		t.Fatalf("identifier budget failed: %d entries, %d bytes", len(processor.queued), processor.queuedBytes)
	}
	if processor.queuedBytes < replayDispatchBytes-1024 {
		t.Fatalf("dispatcher stopped before its byte budget: %d", processor.queuedBytes)
	}
}

func retainedReplayCount(t *testing.T, server *Server) int {
	t.Helper()
	readers, err := server.catalog.Read(t.Context(), server.runtime.Load().cfg.AuditStoragePolicy().Rotation(), server.now())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = readers.Close() }()
	total := 0
	for _, handle := range readers.Handles {
		var pending, completed int
		if err := handle.Database.QueryRowContext(t.Context(), readDaemonSQLFixture(t, "runtime_deferred_summary.sql")).Scan(&pending, &completed); err != nil {
			t.Fatal(err)
		}
		total += completed
	}
	return total
}
