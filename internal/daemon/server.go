// Package daemon implements the agent-gate daemon gRPC server.
package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
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

const (
	overloadLogInterval = 5 * time.Second
)

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

// New creates a new daemon Server.
func New(log *slog.Logger, cfg *config.Config) (*Server, error) {
	if log == nil {
		log = slog.Default()
	}
	if cfg == nil {
		cfg = &config.Config{
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
			},
			Update: config.Update{
				Enabled:         nil,
				Mode:            "",
				Interval:        "",
				Repo:            "",
				AllowPrerelease: nil,
			},
			Telemetry: config.TelemetryConfig{OTLPEndpoint: "", SlowOpThresholdMs: 0},
			Rules:     nil,
		}
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
	if err := replayRuntimeSnapshotPending(deferredProcessor, ctx); err != nil {
		deferredProcessor.Close()
		if eventLogger != nil {
			_ = eventLogger.Close()
		}
		_ = closeIntakeStore(intakeStore, log)
		return nil, fmt.Errorf("replay pending intake: %w", err)
	}
	return &runtimeSnapshot{
		cfg:                cfg,
		eventLogger:        eventLogger,
		intakeStore:        intakeStore,
		evaluationRecorder: intakeStore.Evaluations(),
		deferredProcessor:  deferredProcessor,
		evaluateSlots:      make(chan struct{}, cfg.HookHotConcurrency()),
		evaluateQueueWait:  cfg.HookHotQueueWait(),
		hotEvaluate:        defaultHotEvaluate,
		execRuntime:        rules.NewExecRuntimeWithCache(nil, log, hotStore),
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
	candidate, err := config.LoadExisting(s.configPath)
	if err != nil {
		s.log.WarnContext(ctx, "config load or compile failed", "path", s.configPath, "err", err)
		return fmt.Errorf("config load or compile failed: %w", err)
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
		return failOpenEvaluateHookResponse(), nil
	}
	requestLog := s.log
	if peerInfo, ok := peer.FromContext(ctx); ok && peerInfo.Addr != nil {
		requestLog = requestLog.With("peer_addr", peerInfo.Addr.String())
	}
	if !s.acquireEvaluateSlot(ctx, snapshot) {
		s.logEvaluateOverload(ctx, snapshot)
		return failOpenEvaluateHookResponse(), nil
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

	appendResult, err := snapshot.intakeStore.Append(ctx, intakeRecord)
	if err != nil {
		requestLog.WarnContext(ctx, "append hook intake failed; failing open", "err", err)
		return failOpenEvaluateHookResponse(), nil
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

func failOpenEvaluateHookResponse() *daemonpb.EvaluateHookResponse {
	return &daemonpb.EvaluateHookResponse{
		ExitCode:   0,
		StdoutData: nil,
		StderrData: nil,
	}
}

var auditNow = time.Now

var hotEvalNow = time.Now

func (s *Server) logEvaluateOverload(ctx context.Context, snapshot *runtimeSnapshot) {
	if s == nil || s.log == nil || snapshot == nil {
		return
	}
	now := auditNow()
	s.overloadLogMu.Lock()
	if !s.lastOverloadLogTime.IsZero() && now.Sub(s.lastOverloadLogTime) < overloadLogInterval {
		s.overloadLogMu.Unlock()
		return
	}
	s.lastOverloadLogTime = now
	s.overloadLogMu.Unlock()

	s.log.WarnContext(ctx, "evaluate hook overloaded; failing open",
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
	return &daemonpb.StatusResponse{
		Pid:            int64(os.Getpid()),
		ExecutablePath: exe,
		SocketPath:     config.DaemonSocketPath(),
		Version:        gkversion.Version,
		Commit:         gkversion.Commit,
		Dirty:          gkversion.Dirty,
		BuildHash:      version.BuildHash(),
	}, nil
}

// KVGet implements the hot KV GET RPC.
func (s *Server) KVGet(_ context.Context, req *daemonpb.KVGetRequest) (*daemonpb.KVGetResponse, error) {
	entry, found, err := s.hotKV.Get(req.GetNamespace(), req.GetKey())
	if err != nil {
		return nil, kvStatusError(err)
	}
	if !found {
		return &daemonpb.KVGetResponse{Found: false, Entry: nil}, nil
	}
	return &daemonpb.KVGetResponse{Found: true, Entry: daemonKVEntry(entry)}, nil
}

// KVSet implements the hot KV SET RPC.
func (s *Server) KVSet(_ context.Context, req *daemonpb.KVSetRequest) (*daemonpb.KVSetResponse, error) {
	mode, err := parseKVSetMode(req.GetMode())
	if err != nil {
		return nil, err
	}
	ttlMs := req.GetTtlMs()
	if ttlMs < 0 {
		return nil, status.Error(codes.InvalidArgument, "ttl_ms must be non-negative")
	}
	entry, stored, err := s.hotKV.Set(req.GetNamespace(), req.GetKey(), req.GetValue(), hotkv.SetOptions{
		Mode: mode,
		TTL:  time.Duration(ttlMs) * time.Millisecond,
	})
	if err != nil {
		return nil, kvStatusError(err)
	}
	if !stored {
		return &daemonpb.KVSetResponse{Stored: false, Entry: nil}, nil
	}
	return &daemonpb.KVSetResponse{Stored: true, Entry: daemonKVEntry(entry)}, nil
}

// KVDelete implements the hot KV DEL RPC.
func (s *Server) KVDelete(_ context.Context, req *daemonpb.KVDeleteRequest) (*daemonpb.KVDeleteResponse, error) {
	deleted, err := s.hotKV.Delete(req.GetNamespace(), req.GetKey())
	if err != nil {
		return nil, kvStatusError(err)
	}
	return &daemonpb.KVDeleteResponse{Deleted: deleted}, nil
}

// KVExists implements the hot KV EXISTS RPC.
func (s *Server) KVExists(_ context.Context, req *daemonpb.KVExistsRequest) (*daemonpb.KVExistsResponse, error) {
	exists, err := s.hotKV.Exists(req.GetNamespace(), req.GetKey())
	if err != nil {
		return nil, kvStatusError(err)
	}
	return &daemonpb.KVExistsResponse{Exists: exists}, nil
}

// KVTTL implements the hot KV TTL RPC.
func (s *Server) KVTTL(_ context.Context, req *daemonpb.KVGetRequest) (*daemonpb.KVTTLResponse, error) {
	ttl, err := hotKVTTL(s.hotKV, req.GetNamespace(), req.GetKey(), false)
	if err != nil {
		return nil, err
	}
	return &daemonpb.KVTTLResponse{Ttl: ttl}, nil
}

// KVPTTL implements the hot KV PTTL RPC.
func (s *Server) KVPTTL(_ context.Context, req *daemonpb.KVGetRequest) (*daemonpb.KVPTTLResponse, error) {
	ttl, err := hotKVTTL(s.hotKV, req.GetNamespace(), req.GetKey(), true)
	if err != nil {
		return nil, err
	}
	return &daemonpb.KVPTTLResponse{Pttl: ttl}, nil
}

// KVExpire implements the hot KV EXPIRE RPC.
func (s *Server) KVExpire(_ context.Context, req *daemonpb.KVExpireRequest) (*daemonpb.KVExpireResponse, error) {
	ttlMs := req.GetTtlMs()
	if ttlMs < 0 {
		return nil, status.Error(codes.InvalidArgument, "ttl_ms must be non-negative")
	}
	updated, err := s.hotKV.Expire(req.GetNamespace(), req.GetKey(), time.Duration(ttlMs)*time.Millisecond)
	if err != nil {
		return nil, kvStatusError(err)
	}
	return &daemonpb.KVExpireResponse{Updated: updated}, nil
}

// KVGetDelete implements the hot KV GETDEL RPC.
func (s *Server) KVGetDelete(_ context.Context, req *daemonpb.KVGetDeleteRequest) (*daemonpb.KVGetDeleteResponse, error) {
	entry, found, err := s.hotKV.GetDelete(req.GetNamespace(), req.GetKey())
	if err != nil {
		return nil, kvStatusError(err)
	}
	if !found {
		return &daemonpb.KVGetDeleteResponse{Found: false, Entry: nil}, nil
	}
	return &daemonpb.KVGetDeleteResponse{Found: true, Entry: daemonKVEntry(entry)}, nil
}

// KVList implements the hot KV list RPC used by diagnostics.
func (s *Server) KVList(_ context.Context, req *daemonpb.KVListRequest) (*daemonpb.KVListResponse, error) {
	if req.GetLimit() < 0 {
		return nil, status.Error(codes.InvalidArgument, "limit must be non-negative")
	}
	entries, err := s.hotKV.List(req.GetNamespace(), req.GetPrefix(), int(req.GetLimit()), req.GetIncludeValues())
	if err != nil {
		return nil, kvStatusError(err)
	}
	out := make([]*daemonpb.KVEntry, 0, len(entries))
	for _, entry := range entries {
		out = append(out, daemonKVEntry(entry))
	}
	return &daemonpb.KVListResponse{Entries: out}, nil
}

func parseKVSetMode(mode string) (hotkv.SetMode, error) {
	switch strings.ToUpper(strings.TrimSpace(mode)) {
	case "":
		return hotkv.SetModeAny, nil
	case string(hotkv.SetModeNX):
		return hotkv.SetModeNX, nil
	case string(hotkv.SetModeXX):
		return hotkv.SetModeXX, nil
	default:
		return hotkv.SetModeAny, status.Errorf(codes.InvalidArgument, "unknown set mode %q", mode)
	}
}

func kvStatusError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, hotkv.ErrInvalidNamespace), errors.Is(err, hotkv.ErrInvalidKey):
		return status.Error(codes.InvalidArgument, err.Error())
	case errors.Is(err, hotkv.ErrValueTooLarge):
		return status.Error(codes.ResourceExhausted, err.Error())
	default:
		return status.Error(codes.Internal, err.Error())
	}
}

func hotKVTTL(store *hotkv.Store, namespace string, key string, precise bool) (int64, error) {
	ttl, found, expiring, err := store.PTTL(namespace, key)
	if err != nil {
		return 0, kvStatusError(err)
	}
	if !found {
		return -2, nil
	}
	if !expiring {
		return -1, nil
	}
	if precise {
		return ttl.Milliseconds(), nil
	}
	return ttl.Milliseconds() / 1000, nil
}

func daemonKVEntry(entry hotkv.Entry) *daemonpb.KVEntry {
	pttlMs := int64(-1)
	expiresUnixNano := int64(0)
	if !entry.ExpiresAt.IsZero() {
		expiresUnixNano = entry.ExpiresAt.UnixNano()
		ttl := entry.ExpiresAt.Sub(auditNow())
		if ttl < 0 {
			pttlMs = 0
		} else {
			pttlMs = ttl.Milliseconds()
		}
	}
	return &daemonpb.KVEntry{
		Namespace:       entry.Namespace,
		Key:             entry.Key,
		Value:           append([]byte(nil), entry.Value...),
		Version:         entry.Version,
		CreatedUnixNano: unixNanoOrZero(entry.CreatedAt),
		UpdatedUnixNano: unixNanoOrZero(entry.UpdatedAt),
		ExpiresUnixNano: expiresUnixNano,
		PttlMs:          pttlMs,
	}
}

func unixNanoOrZero(value time.Time) int64 {
	if value.IsZero() {
		return 0
	}
	return value.UnixNano()
}
