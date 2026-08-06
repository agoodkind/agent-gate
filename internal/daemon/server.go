// Package daemon implements the agent-gate daemon gRPC server.
package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fsnotify/fsnotify"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"

	"goodkind.io/agent-gate/api/daemonpb"
	"goodkind.io/agent-gate/internal/audit"
	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/gitbranch"
	"goodkind.io/agent-gate/internal/hook"
	"goodkind.io/agent-gate/internal/hotkv"
	"goodkind.io/agent-gate/internal/intake"
	"goodkind.io/agent-gate/internal/rules"
	"goodkind.io/agent-gate/internal/version"
	gkversion "goodkind.io/gklog/version"
	"goodkind.io/gksyntax/shelldecomp"
)

const configReloadDebounce = 200 * time.Millisecond

const intakeParseFailed = "intake_parse_failed"

type runtimeSnapshot struct {
	cfg                *config.Config
	eventLogger        *audit.EventLogger
	intakeStore        intakeStore
	evaluationRecorder evaluationRecorder
	deferredProcessor  *deferredProcessor
	evaluateSlots      chan struct{}
	evaluateQueueWait  time.Duration
	hotEvaluate        func(context.Context, []byte, *config.Config, hook.System, func(string) string, string) hook.HotEvaluation
	execRuntime        *rules.ExecRuntime
	inferRuntime       *rules.InferRuntime
}

type inferenceTraceSink struct {
	traces []rules.InferenceTrace
}

func (sink *inferenceTraceSink) CollectInferenceTrace(trace rules.InferenceTrace) {
	sink.traces = append(sink.traces, trace)
}

func (sink *inferenceTraceSink) snapshot() []rules.InferenceTrace {
	if sink == nil {
		return nil
	}
	return append([]rules.InferenceTrace(nil), sink.traces...)
}

// Server implements the AgentGateD gRPC service.
type Server struct {
	daemonpb.UnimplementedAgentGateDServer

	log           *slog.Logger
	cfgMu         sync.RWMutex
	runtimeMu     sync.RWMutex
	runtime       atomic.Pointer[runtimeSnapshot]
	configWatcher *fsnotify.Watcher
	configPath    string
	hotKV         *hotkv.Store
	inferRuntime  *rules.InferRuntime
	closing       bool
	updateCancel  context.CancelFunc
	stopDaemon    func()

	overloadLogMu       sync.Mutex
	lastOverloadLogTime time.Time
}

// zeroConfig returns a fully-specified empty Config for a nil caller. Every
// field is named so a new config section cannot be silently defaulted here.
func zeroConfig() *config.Config {
	return &config.Config{
		Log:   config.Log{Level: ""},
		Audit: config.Audit{Enabled: nil, Level: "", Outputs: config.AuditOutput{SQLite: config.AuditSQLiteOutput{Path: ""}}},
		Paths: config.Paths{ConversationsDir: ""},
		Performance: config.Performance{
			Hook: config.HookPerformance{
				HotConcurrency:          0,
				HotQueueWaitMS:          0,
				InferencePhaseTimeoutMS: 0,
				DeferredQueueLimit:      0,
				DeferredWorkers:         0,
				Cache: config.HookCachePerformance{
					MaxEntries:      0,
					MaxValueBytes:   0,
					PruneIntervalMS: 0,
				},
			},
			Timeouts: config.TimeoutPerformance{
				HookEvaluateMS: 0, ExecDefaultMS: 0, ExecMaxMS: 0,
				ExecBackgroundMS: 0, ExecMaxRetryCount: 0,
				InferDefaultMS: 0, InferMaxMS: 0,
			},
			Limits: config.LimitPerformance{
				RegexMatchLimit: 0, RegexDepthLimit: 0, AuditQueueLimit: 0,
				AuditDedupCacheSize: 0, HookInferencePhaseMaxMs: 0,
			},
			Intervals: config.IntervalPerformance{
				AuditDedupTTLMs: 0, AuditDropLogIntervalMs: 0,
				OverloadLogIntervalMs: 0, DeferredClaimLeaseMs: 0,
				DeferredClaimRenewMs: 0,
			},
		},
		Update: config.Update{
			Enabled:         nil,
			Mode:            "",
			Interval:        "",
			Repo:            "",
			AllowPrerelease: nil,
		},
		Telemetry: config.TelemetryConfig{OTLPEndpoint: "", SlowOpThresholdMs: 0},
		Messages:  config.Messages{BlockFooter: ""},
		Judge: config.Judge{
			TranscriptEndpoint:   "",
			TranscriptMaxTokens:  0,
			TranscriptTokenModel: "",
			TranscriptTimeoutMS:  0,
			TranscriptOnError:    "",
			Pricing:              nil,
		},
		Inference: nil,
		Rules:     nil,
	}
}

// New creates a new daemon Server.
func New(log *slog.Logger, cfg *config.Config) (*Server, error) {
	if log == nil {
		log = slog.Default()
	}
	if cfg == nil {
		cfg = zeroConfig()
	}
	if errs := hook.ValidateConfig(cfg); len(errs) > 0 {
		log.Error("invalid hook config", slog.Any("err", errs[0]))
		return nil, fmt.Errorf("invalid hook config: %w", errs[0])
	}

	hook.WarnCapabilityDowngrades(context.Background(), log, cfg)

	hotStore := hotkv.New(hotKVOptions(cfg))
	inferRuntime := rules.NewInferRuntimeWithCache(log, hotStore)
	snapshot, err := newRuntimeSnapshot(context.Background(), cfg, log, hotStore, inferRuntime)
	if err != nil {
		inferRuntime.Close()
		hotStore.Close()
		log.Error("failed to create runtime snapshot", slog.Any("err", err))
		return nil, err
	}

	s := &Server{
		UnimplementedAgentGateDServer: daemonpb.UnimplementedAgentGateDServer{},
		log:                           log,
		cfgMu:                         sync.RWMutex{},
		runtimeMu:                     sync.RWMutex{},
		runtime:                       atomic.Pointer[runtimeSnapshot]{},
		configWatcher:                 nil,
		configPath:                    config.Path(),
		hotKV:                         hotStore,
		inferRuntime:                  inferRuntime,
		closing:                       false,
		updateCancel:                  nil,
		stopDaemon:                    nil,
		overloadLogMu:                 sync.Mutex{},
		lastOverloadLogTime:           time.Time{},
	}
	s.runtime.Store(snapshot)
	if err := s.startConfigWatcher(); err != nil {
		snapshot.close(context.Background(), log)
		inferRuntime.Close()
		hotStore.Close()
		return nil, err
	}
	return s, nil
}

func hotKVOptions(cfg *config.Config) hotkv.Options {
	return hotkv.Options{
		MaxEntries:    cfg.HookCacheMaxEntries(),
		MaxValueBytes: cfg.HookCacheMaxValueBytes(),
		PruneInterval: cfg.HookCachePruneInterval(),
	}
}

var replayRuntimeSnapshotPending = (*deferredProcessor).ReplayPending

func newRuntimeSnapshot(ctx context.Context, cfg *config.Config, log *slog.Logger, hotStore *hotkv.Store, inferRuntime *rules.InferRuntime) (*runtimeSnapshot, error) {
	// The intake store is created first so the audit event logger can share its
	// single SQLite connection pool. One pool serializes intake and audit writes
	// to audit.db, avoiding the cross-pool SQLITE_BUSY that two pools hit during
	// the startup replay.
	intakeStore, err := newSQLiteIntakeStore(ctx, cfg, log)
	if err != nil {
		return nil, fmt.Errorf("create intake store: %w", err)
	}

	// Refresh the judge-level transcript settings on the daemon-owned runtime, so a
	// config reload (which rebuilds the snapshot but reuses the runtime) picks up
	// the new [judge] table.
	inferRuntime.SetJudgeTranscript(
		cfg.JudgeTranscriptEndpoint(),
		cfg.JudgeTranscriptMaxTokens(),
		cfg.JudgeTranscriptTokenModel(),
		cfg.JudgeTranscriptTimeout(),
		cfg.JudgeTranscriptOnError(),
	)

	eventLogger, err := audit.NewEventLoggerWithOptions(ctx, cfg, log, audit.LoggerOptions{
		QueueLimit: 0,
		SharedDB:   intakeStore.Handle(),
	})
	if err != nil {
		if log != nil {
			log.WarnContext(ctx, "create event logger failed", "err", err)
		}
		return nil, fmt.Errorf("create event logger: %w", err)
	}

	var sink audit.Sink
	if eventLogger.Enabled() {
		sink = audit.NewLocalSink(eventLogger)
	}
	deferredProcessor := newDeferredProcessor(
		ctx,
		intakeStore,
		sink,
		cfg,
		inferRuntime,
		cfg.HookDeferredQueueLimit(),
		cfg.HookDeferredWorkers(),
		log,
	)
	deferredProcessor.evaluationRecorder = intakeStore.Evaluations()
	// Replay the pending deferred backlog in the background so the daemon serves the
	// gate socket immediately. A synchronous replay blocks Serve for as long as the
	// backlog takes to re-run (each pending event re-runs inference), which leaves the
	// hook fail-open for the whole startup. Replay is audit backfill, not gate
	// enforcement, so a replay error is logged rather than aborting startup.
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil && log != nil {
				log.ErrorContext(ctx, "replay pending intake panic recovered", slog.Any("err", recovered))
			}
		}()
		if err := replayRuntimeSnapshotPending(deferredProcessor, ctx); err != nil {
			if log != nil {
				log.WarnContext(ctx, "replay pending intake failed", slog.Any("err", err))
			}
		}
	}()
	// The detached-validator deadline is pushed onto the runtime here rather
	// than read from config at the call site, because that call site is a retry
	// loop and would otherwise reload and recompile the config once per attempt.
	// A reload rebuilds the snapshot, so the new value takes effect with it.
	execRuntime := rules.NewExecRuntimeWithCache(nil, log, hotStore)
	execRuntime.SetBackgroundTimeout(cfg.ExecBackgroundTimeout())

	return &runtimeSnapshot{
		cfg:                cfg,
		eventLogger:        eventLogger,
		intakeStore:        intakeStore,
		evaluationRecorder: intakeStore.Evaluations(),
		deferredProcessor:  deferredProcessor,
		evaluateSlots:      make(chan struct{}, cfg.HookHotConcurrency()),
		evaluateQueueWait:  cfg.HookHotQueueWait(),
		hotEvaluate:        defaultHotEvaluate,
		execRuntime:        execRuntime,
		inferRuntime:       inferRuntime,
	}, nil
}

func defaultHotEvaluate(ctx context.Context, rawJSON []byte, cfg *config.Config, hint hook.System, getenv func(string) string, eventID string) hook.HotEvaluation {
	if eventID == "" {
		return hook.EvaluateHot(ctx, rawJSON, cfg, hint, getenv)
	}
	return hook.EvaluateHotWithEventID(ctx, rawJSON, cfg, hint, getenv, eventID)
}

func (s *runtimeSnapshot) close(ctx context.Context, log *slog.Logger) {
	if s == nil {
		return
	}
	if s.deferredProcessor != nil {
		s.deferredProcessor.Close()
	}
	if s.eventLogger != nil {
		if err := s.eventLogger.Close(); err != nil && log != nil {
			log.WarnContext(ctx, "audit logger close failed", "err", err)
		}
	}
	if s.intakeStore != nil {
		if err := s.intakeStore.Close(); err != nil && log != nil {
			log.WarnContext(ctx, "intake store close failed", "err", err)
		}
	}
}

// Close shuts down daemon-owned resources.
func (s *Server) Close() {
	s.cfgMu.Lock()
	s.closing = true
	s.runtimeMu.Lock()
	snapshot := s.runtime.Swap(nil)
	s.runtimeMu.Unlock()
	s.cfgMu.Unlock()

	if s.configWatcher != nil {
		_ = s.configWatcher.Close()
	}
	if s.updateCancel != nil {
		s.updateCancel()
	}
	snapshot.close(context.Background(), s.log)
	if s.inferRuntime != nil {
		s.inferRuntime.Close()
	}
	if s.hotKV != nil {
		s.hotKV.Close()
	}
	s.log.InfoContext(context.Background(), "daemon closed")
}

func (s *Server) startConfigWatcher() error {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		s.log.Error("create config watcher failed", slog.Any("err", err))
		return fmt.Errorf("create config watcher: %w", err)
	}

	configDir := filepath.Dir(s.configPath)
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		s.log.Error("create config directory failed", slog.String("dir", configDir), slog.Any("err", err))
		_ = watcher.Close()
		return fmt.Errorf("create config directory %s: %w", configDir, err)
	}
	if err := watcher.Add(configDir); err != nil {
		s.log.Error("watch config directory failed", slog.String("dir", configDir), slog.Any("err", err))
		_ = watcher.Close()
		return fmt.Errorf("watch config directory %s: %w", configDir, err)
	}

	s.configWatcher = watcher
	s.log.InfoContext(context.Background(), "watching config", "path", s.configPath)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				s.log.ErrorContext(context.Background(), "config watcher panic", "err", r)
			}
		}()
		s.watchConfigFile()
	}()
	return nil
}

func (s *Server) watchConfigFile() {
	ctx := context.Background()
	timer := time.NewTimer(configReloadDebounce)
	if !timer.Stop() {
		<-timer.C
	}
	pending := false
	defer func() { _ = timer.Stop() }()

	for {
		select {
		case event, ok := <-s.configWatcher.Events:
			if !ok {
				return
			}
			if s.shouldReloadConfig(event) {
				pending = true
				resetTimer(timer, configReloadDebounce)
				s.log.DebugContext(ctx, "config change detected", "path", s.configPath, "event", event.Op.String())
			}

		case <-timer.C:
			if !pending {
				continue
			}
			pending = false
			if err := s.reloadConfig(ctx); err != nil {
				s.log.WarnContext(ctx, "config reload rejected", "path", s.configPath, "err", err)
			}

		case err, ok := <-s.configWatcher.Errors:
			if !ok {
				return
			}
			s.log.WarnContext(ctx, "config watcher error", "path", s.configPath, "err", err)
		}
	}
}

func (s *Server) shouldReloadConfig(event fsnotify.Event) bool {
	if filepath.Clean(event.Name) != filepath.Clean(s.configPath) {
		return false
	}
	reloadEvents := fsnotify.Write | fsnotify.Create | fsnotify.Rename | fsnotify.Remove | fsnotify.Chmod
	return event.Op&reloadEvents != 0
}

func resetTimer(timer *time.Timer, duration time.Duration) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	timer.Reset(duration)
}

func (s *Server) reloadConfig(ctx context.Context) error {
	// Reload degraded, for the same reason the daemon starts degraded: a rule
	// that will not compile costs that rule, not the whole rule set. A reload
	// that refuses the file leaves the previous snapshot in place, which is
	// safe, but an edit that drops one rule should still deliver the other
	// seventy-two rather than silently keeping a stale set.
	candidate, err := config.LoadDegradedPath(s.configPath)
	if err != nil {
		s.log.WarnContext(ctx, "config load or compile failed", "path", s.configPath, "err", err)
		return fmt.Errorf("config load or compile failed: %w", err)
	}
	for _, failure := range candidate.Failures() {
		s.log.ErrorContext(ctx, "config degraded on reload", "path", s.configPath,
			"kind", failure.Kind, "scope", failure.Scope, "err", failure.Reason)
	}
	if errs := hook.ValidateConfig(candidate); len(errs) > 0 {
		s.log.WarnContext(ctx, "hook config validation failed", "path", s.configPath, "err", errs[0])
		return fmt.Errorf("hook config validation failed: %w", errs[0])
	}

	hook.WarnCapabilityDowngrades(ctx, s.log, candidate)

	newSnapshot, err := newRuntimeSnapshot(ctx, candidate, s.log, s.hotKV, s.inferRuntime)
	if err != nil {
		s.log.WarnContext(ctx, "create runtime snapshot for reloaded config failed", "path", s.configPath, "err", err)
		return fmt.Errorf("failed to create runtime snapshot for reloaded config: %w", err)
	}

	s.cfgMu.Lock()
	if s.closing {
		s.cfgMu.Unlock()
		newSnapshot.close(ctx, s.log)
		return nil
	}
	if s.hotKV != nil {
		s.hotKV.Configure(hotKVOptions(candidate))
	}
	s.runtimeMu.Lock()
	oldSnapshot := s.runtime.Swap(newSnapshot)
	s.runtimeMu.Unlock()
	updateCancel := s.updateCancel
	stopDaemon := s.stopDaemon
	s.cfgMu.Unlock()

	if updateCancel != nil {
		updateCancel()
	}
	if stopDaemon != nil {
		s.StartUpdateScheduler(ctx, stopDaemon)
	}
	oldSnapshot.close(ctx, s.log)
	s.log.InfoContext(ctx, "config reloaded", "path", s.configPath, "rules", len(candidate.Rules), "audit_enabled", candidate.AuditEnabled())
	return nil
}

// EvaluateHook processes a hook event through daemon-owned enforcement.
func (s *Server) EvaluateHook(ctx context.Context, req *daemonpb.EvaluateHookRequest) (*daemonpb.EvaluateHookResponse, error) {
	s.runtimeMu.RLock()
	defer s.runtimeMu.RUnlock()
	snapshot := s.runtime.Load()
	if snapshot == nil {
		return s.unevaluated(ctx, req, hook.FailOpenReasonDaemonNotReady,
			"daemon has no runtime snapshot yet"), nil
	}
	requestLog := s.log
	if peerInfo, ok := peer.FromContext(ctx); ok && peerInfo.Addr != nil {
		requestLog = requestLog.With("peer_addr", peerInfo.Addr.String())
	}
	// A config that did not decode leaves no rules, so the daemon is running but
	// enforcing nothing. Answering with a clean allow would be worse than being
	// down, because a hook that reaches no daemon at least says so. Report every
	// call as unevaluated and record it, the same as any other fail-open.
	if snapshot.cfg.Unusable() {
		system := hook.SystemFromString(req.GetProviderHint())
		diagnostic := unusableConfigDiagnostic(snapshot.cfg)
		RecordFailOpen(
			string(hook.FailOpenReasonConfigUnusable), system.String(),
			"", "", req.GetCwd(), diagnostic,
		)
		requestLog.ErrorContext(ctx, "config unusable; call allowed without enforcement",
			"err", diagnostic)
		return failOpenEvaluateHookResponseFor(
			system, hook.FailOpenReasonConfigUnusable, diagnostic,
		), nil
	}
	if !s.acquireEvaluateSlot(ctx, snapshot) {
		s.logEvaluateOverload(ctx, snapshot)
		return s.unevaluated(ctx, req, hook.FailOpenReasonOverloaded,
			"every evaluation slot was busy past the queue deadline"), nil
	}
	defer s.releaseEvaluateSlot(snapshot)

	ctx = rules.WithExecRuntime(ctx, snapshot.execRuntime)
	ctx = rules.WithInferRuntime(ctx, snapshot.inferRuntime)
	ctx = rules.WithGitStateReader(ctx, gitbranch.ReadState)
	var traceSink *inferenceTraceSink
	if configHasInference(snapshot.cfg) {
		traceSink = &inferenceTraceSink{traces: nil}
		ctx = rules.WithInferenceTraceCollector(ctx, traceSink)
	}
	rawJSON := req.GetRawJson()
	if cwd := req.GetCwd(); cwd != "" {
		rawJSON = injectCWD(rawJSON, cwd)
	}
	if req.GetProviderHint() == hook.SystemCopilot.String() {
		var normalizeErr error
		rawJSON, normalizeErr = hook.NormalizeCopilotPayload(rawJSON, copilotEventHint(req.GetArgv()))
		if normalizeErr != nil {
			return s.unevaluated(ctx, req, hook.FailOpenReasonPayloadUnreadable,
				normalizeErr.Error()), nil
		}
	}

	envFingerprint := req.GetEnvFingerprint()
	getenv := func(key string) string {
		if envFingerprint == nil {
			return ""
		}
		return envFingerprint[key]
	}

	evalStart := hotEvalNow()
	intakeRecord, intakeErr := buildIntakeRecord(rawJSON, req.GetProviderHint(), envFingerprint)
	if intakeErr != nil {
		intakeRecord = buildInvalidIntakeRecord(
			rawJSON, req.GetProviderHint(), envFingerprint,
		)
	}

	// Detached from the caller for the same reason the commit is: the event
	// happened, so the record of it must not depend on the caller still
	// listening. Measured on 2026-08-05, every one of the 77 append failures on
	// record was the caller cancelling or its deadline expiring, and none was
	// database contention.
	appendCtx, cancelAppend := context.WithTimeout(
		context.WithoutCancel(ctx), durableWriteCeiling,
	)
	appendResult, err := snapshot.intakeStore.Append(appendCtx, intakeRecord)
	cancelAppend()
	if err != nil {
		requestLog.WarnContext(ctx, "append hook intake failed; failing open", "err", err)
		return s.unevaluated(ctx, req, hook.FailOpenReasonIntakeWriteFailed, err.Error()), nil
	}
	if intakeErr == nil {
		observeUserPrompt(
			snapshot.execRuntime,
			intakeRecord.System,
			rawJSON,
			appendResult.ReceiptID,
		)
	}

	syncCfg := hook.SyncConfig(snapshot.cfg)
	result := snapshot.hotEvaluate(ctx, rawJSON, syncCfg, hook.SystemFromString(req.GetProviderHint()), getenv, appendResult.EventID)
	result.Deferred.InferenceTraces = traceSink.snapshot()
	systemError := ""
	errorMessage := ""
	if intakeErr != nil {
		systemError = intakeParseFailed
		errorMessage = intakeErr.Error()
	}
	return s.commitHotEvaluation(ctx, hotEvaluationCommitInput{
		Log: requestLog, Snapshot: snapshot, Intake: intakeRecord,
		AppendResult: appendResult, StartedAt: evalStart, Result: result,
		SystemError: systemError, ErrorMessage: errorMessage,
	}), nil
}

func observeUserPrompt(
	runtime *rules.ExecRuntime,
	system string,
	rawJSON []byte,
	receiptID int64,
) {
	payload, err := hook.ParseHookPayload(hook.SystemFromString(system), rawJSON)
	if err != nil {
		return
	}
	prompt, ok := hook.UserPrompt(payload)
	if !ok {
		return
	}
	runtime.ObserveUserPrompt(system, payload.Fields(), receiptID, prompt)
}

func copilotEventHint(argv []string) string {
	for index := range argv {
		if argv[index] != "copilot-hook" {
			continue
		}
		if index+1 < len(argv) {
			return argv[index+1]
		}
		return ""
	}
	return ""
}

func configHasInference(cfg *config.Config) bool {
	if cfg == nil {
		return false
	}
	for ruleIndex := range cfg.Rules {
		for conditionIndex := range cfg.Rules[ruleIndex].Conditions {
			if config.ConditionKind(cfg.Rules[ruleIndex].Conditions[conditionIndex].Kind) == config.ConditionKindInfer {
				return true
			}
		}
	}
	return false
}

func buildIntakeRecord(rawJSON []byte, providerHint string, envFingerprint map[string]string) (intake.Record, error) {
	detectionPayload, err := hook.ParseDetectionPayload(rawJSON)
	if err != nil {
		return intake.Record{}, wrapServerError("parse intake detection payload", err)
	}
	system := hook.DetectWithEnv(detectionPayload, hook.SystemFromString(providerHint), func(key string) string {
		return envFingerprint[key]
	})
	payload, err := hook.ParseHookPayload(system, rawJSON)
	if err != nil {
		return intake.Record{}, wrapServerError("parse intake hook payload", err)
	}

	fields := payload.Fields()
	var record intake.Record
	record.System = system.String()
	record.SessionID = payload.SessionID()
	record.TurnID = fields.TurnID
	record.EventName = payload.EventName()
	record.ToolName = fields.ToolName
	record.ToolUseID = fields.ToolUseID
	record.RawPayload = append([]byte(nil), rawJSON...)
	record.NormalizedJSON = append([]byte(nil), rawJSON...)
	record.EnvFingerprint = cloneStringMap(envFingerprint)
	record.Operation.CWD = firstNonEmpty(fields.CWD, payload.CWD())
	effectiveCwd := fields.String(config.FieldEffectiveCWD)
	if effectiveCwd == shelldecomp.Unresolvable {
		// Store the unknown directory as empty; the marker's NUL byte must
		// not leak into the intake database.
		effectiveCwd = ""
	}
	record.Operation.EffectiveCWD = effectiveCwd
	record.Operation.Command = fields.CommandValue()
	record.Operation.FilePath = fields.FilePathValue()
	return record, nil
}

func buildInvalidIntakeRecord(
	rawJSON []byte,
	providerHint string,
	envFingerprint map[string]string,
) intake.Record {
	return intake.Record{
		ReceiptID: 0, ReceivedAt: time.Time{}, EventID: "", SchemaVersion: 0,
		RecordedAt: time.Time{}, System: hook.SystemFromString(providerHint).String(),
		SessionID: "_no-session", TurnID: "", EventName: "_invalid",
		ToolName: "", ToolUseID: "", Operation: intake.Operation{
			CWD: "", EffectiveCWD: "", Command: "", FilePath: "",
		},
		RawPayload: append([]byte(nil), rawJSON...), NormalizedJSON: json.RawMessage(`{}`),
		RawPayloadHash: "", EnvFingerprint: cloneStringMap(envFingerprint),
		DeferredState: intake.DeferredStateNone, PendingAt: nil, CompletedAt: nil,
		LastReplayAt: nil, DeferredReplays: 0, Sequence: 0,
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func cloneStringMap(values map[string]string) map[string]string {
	if len(values) == 0 {
		return map[string]string{}
	}
	cloned := make(map[string]string, len(values))
	maps.Copy(cloned, values)
	return cloned
}

func enqueueDeferredReplay(
	snapshot *runtimeSnapshot,
	appendResult intake.AppendResult,
	deferredEvent hook.DeferredAuditEvent,
) {
	if !deferredEvent.Valid {
		return
	}
	if snapshot.deferredProcessor != nil {
		snapshot.deferredProcessor.Enqueue(appendResult.ReceiptID, appendResult.EventID, deferredEvent)
	}
}

func failOpenHotEvaluation(result hook.HotEvaluation) hook.HotEvaluation {
	result.Stdout = nil
	result.Stderr = nil
	result.ExitCode = 0
	return result
}

func wrapServerError(message string, err error) error {
	if err == nil {
		return nil
	}
	slog.Warn(message+" failed", "err", err)
	return fmt.Errorf("%s: %w", message, err)
}

func (s *Server) acquireEvaluateSlot(ctx context.Context, snapshot *runtimeSnapshot) bool {
	if s == nil || snapshot == nil || snapshot.evaluateSlots == nil {
		return true
	}
	select {
	case snapshot.evaluateSlots <- struct{}{}:
		return true
	default:
	}

	waitCtx := ctx
	cancel := func() {}
	if snapshot.evaluateQueueWait > 0 {
		waitCtx, cancel = context.WithTimeout(ctx, snapshot.evaluateQueueWait)
	}
	defer cancel()

	select {
	case snapshot.evaluateSlots <- struct{}{}:
		return true
	case <-waitCtx.Done():
		return false
	}
}

func (s *Server) releaseEvaluateSlot(snapshot *runtimeSnapshot) {
	if s == nil || snapshot == nil || snapshot.evaluateSlots == nil {
		return
	}
	select {
	case <-snapshot.evaluateSlots:
	default:
	}
}

// failOpenEvaluateHookResponseFor renders an allow that says the call was not
// evaluated, for the provider that asked.
//
// Used where the daemon knows enforcement is absent rather than merely delayed,
// which today means a config that did not decode. An allow nobody evaluated has
// to be distinguishable from one that passed every rule.
func failOpenEvaluateHookResponseFor(
	system hook.System,
	reason hook.FailOpenReason,
	diagnostic string,
) *daemonpb.EvaluateHookResponse {
	rendered := hook.FailOpenResponse(system, "", diagnostic, reason)
	// A fail-open renderer only ever produces 0, and a value outside int32
	// could not be a process exit code anyway, so an out-of-range result is
	// clamped to the allow it is meant to be rather than wrapping.
	exitCode := int32(0)
	if rendered.ExitCode > 0 && rendered.ExitCode <= math.MaxInt32 {
		exitCode = int32(rendered.ExitCode)
	}
	return &daemonpb.EvaluateHookResponse{
		ExitCode:   exitCode,
		StdoutData: rendered.Stdout,
		StderrData: rendered.Stderr,
	}
}

var auditNow = time.Now

var hotEvalNow = time.Now

func (s *Server) logEvaluateOverload(ctx context.Context, snapshot *runtimeSnapshot) {
	if s == nil || s.log == nil || snapshot == nil {
		return
	}
	now := auditNow()
	// Read the interval from the snapshot rather than a field captured at
	// construction, so a config reload takes effect. The snapshot is replaced
	// on reload, and its other tuning values below are read the same way.
	interval := snapshot.cfg.OverloadLogInterval()
	s.overloadLogMu.Lock()
	if !s.lastOverloadLogTime.IsZero() && now.Sub(s.lastOverloadLogTime) < interval {
		s.overloadLogMu.Unlock()
		return
	}
	s.lastOverloadLogTime = now
	s.overloadLogMu.Unlock()

	s.log.WarnContext(
		ctx, "evaluate hook overloaded; failing open",
		"max_concurrency", cap(snapshot.evaluateSlots),
		"queue_wait_ms", snapshot.evaluateQueueWait.Milliseconds(),
	)
}

// clampExitCode reduces an int exit code to the int32 range expected by the
// gRPC response. Process exit codes are conventionally in [0,255] so the
// clamp is a defense-in-depth check rather than a correctness fix.
func clampExitCode(exitCode int) int32 {
	const maxInt32 = int(^uint32(0) >> 1)
	const minInt32 = -maxInt32 - 1
	if exitCode > maxInt32 {
		return int32(maxInt32)
	}
	if exitCode < minInt32 {
		return int32(minInt32)
	}
	return int32(exitCode)
}

func injectCWD(rawJSON []byte, cwd string) []byte {
	if cwd == "" || len(rawJSON) == 0 || rawJSON[len(rawJSON)-1] != '}' {
		return rawJSON
	}
	insert := []byte(`,"cwd":"` + escapeJSONString(cwd) + `"}`)
	out := make([]byte, 0, len(rawJSON)+len(insert))
	out = append(out, rawJSON[:len(rawJSON)-1]...)
	out = append(out, insert...)
	return out
}

func escapeJSONString(value string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`, "\r", `\r`, "\t", `\t`)
	return replacer.Replace(value)
}

// Status implements the AgentGateD Status RPC and returns a snapshot of
// daemon-side identifying information.
func (s *Server) Status(_ context.Context, _ *daemonpb.StatusRequest) (*daemonpb.StatusResponse, error) {
	exe, err := os.Executable()
	if err != nil {
		s.log.Error("resolve executable failed", slog.Any("err", err))
		return nil, status.Errorf(codes.Internal, "resolve executable: %v", err)
	}
	// Reported so an operator can tell a healthy daemon from one that is running
	// and enforcing nothing. Those look identical from the outside otherwise,
	// which is how a ten hour outage went unnoticed.
	rulesLoaded := 0
	configError := ""
	if snapshot := s.runtime.Load(); snapshot != nil && snapshot.cfg != nil {
		rulesLoaded = len(snapshot.cfg.Rules)
		if snapshot.cfg.Unusable() {
			configError = unusableConfigDiagnostic(snapshot.cfg)
		}
	}
	return &daemonpb.StatusResponse{
		Pid:            int64(os.Getpid()),
		ExecutablePath: exe,
		SocketPath:     config.DaemonSocketPath(),
		Version:        gkversion.Version,
		Commit:         gkversion.Commit,
		Dirty:          gkversion.Dirty,
		BuildHash:      version.BuildHash(),
		RulesLoaded:    int64(rulesLoaded),
		ConfigError:    configError,
	}, nil
}

// unusableConfigDiagnostic names why the config could not be used, so the
// warning an agent sees points at the file rather than only saying enforcement
// is gone.
func unusableConfigDiagnostic(cfg *config.Config) string {
	for _, failure := range cfg.Failures() {
		if failure.Kind == config.LoadFailureDocument {
			return fmt.Sprintf("config %s did not decode: %s", failure.Scope, failure.Reason)
		}
	}
	return "config did not decode"
}

// unevaluated renders and records an allow for a call no rule was applied to.
//
// Every daemon path that reaches it allowed the call without evaluating it: no
// runtime snapshot, no free slot before the queue deadline, a payload that
// would not normalize, or an intake record that would not persist. A delayed
// evaluation that never ran is an unevaluated call, so each of those says so
// rather than returning an empty allow the agent cannot tell from compliance.
func (s *Server) unevaluated(
	ctx context.Context,
	req *daemonpb.EvaluateHookRequest,
	reason hook.FailOpenReason,
	diagnostic string,
) *daemonpb.EvaluateHookResponse {
	system := hook.SystemFromString(req.GetProviderHint())
	RecordFailOpen(string(reason), system.String(), "", "", req.GetCwd(), diagnostic)
	if s != nil && s.log != nil {
		s.log.ErrorContext(ctx, "call allowed without enforcement",
			"reason", string(reason), "err", diagnostic)
	}
	return failOpenEvaluateHookResponseFor(system, reason, diagnostic)
}
