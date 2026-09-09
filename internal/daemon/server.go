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
	"goodkind.io/agent-gate/internal/auditstorage"
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
	bucket             auditstorage.Bucket
	bucketHandle       *auditstorage.BucketHandle
	cfg                *config.Config
	eventLogger        *audit.EventLogger
	intakeStore        intakeStore
	evaluationRecorder evaluationRecorder
	deferredProcessor  *deferredProcessor
	evaluateSlots      chan struct{}
	evaluateQueueWait  time.Duration
	hotEvaluate        func(context.Context, hook.EvaluationInput, *config.Config, func(string) string, string) hook.HotEvaluation
	execRuntime        *rules.ExecRuntime
	inferRuntime       *rules.InferRuntime
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
	lifecycleMu   sync.Mutex
	closing       bool
	updateCancel  context.CancelFunc
	stopDaemon    func()
	catalog       *auditstorage.Catalog
	shutdown      <-chan struct{}
	auditCancel   context.CancelFunc
	cancel        context.CancelFunc
	now           func() time.Time
	auditTicks    <-chan time.Time
	auditWake     chan struct{}
	auditOnce     sync.Once
	auditWG       sync.WaitGroup
	auditStarted  chan struct{}
	closeOnce     sync.Once
	retryWait     func(context.Context, time.Duration) error

	overloadLogMu       sync.Mutex
	lastOverloadLogTime time.Time
}

// zeroConfig returns a fully-specified empty Config for a nil caller. Every
// top-level field is named so a new config section cannot be silently defaulted.
func zeroConfig() *config.Config {
	var emptyAuditStorage config.AuditStorage
	return &config.Config{
		Log: config.Log{Level: ""},
		Audit: config.Audit{
			Enabled: nil,
			Level:   "",
			Outputs: config.AuditOutput{SQLite: config.AuditSQLiteOutput{Path: ""}},
			Storage: emptyAuditStorage,
		},
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

// New creates a daemon runtime. StartAuditScheduler follows transport readiness.
func New(ctx context.Context, log *slog.Logger, cfg *config.Config) (*Server, error) {
	return newServer(ctx, log, cfg, time.Now)
}

func newServer(ctx context.Context, log *slog.Logger, cfg *config.Config, now func() time.Time) (*Server, error) {
	return newServerWithStorageWait(ctx, log, cfg, now, waitAuditRetry)
}

func newServerWithStorageWait(ctx context.Context, log *slog.Logger, cfg *config.Config, now func() time.Time, wait func(context.Context, time.Duration) error) (*Server, error) {
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
	initializeBuildIdentity(log)
	if !cfg.Unusable() {
		if err := cfg.PrepareAuditStorage(); err != nil {
			return nil, wrapServerError("prepare audit storage", err)
		}
	}

	hook.WarnCapabilityDowngrades(ctx, log, cfg)

	hotStore := hotkv.New(hotKVOptions(cfg))
	inferRuntime := rules.NewInferRuntimeWithCache(log, hotStore)
	lifetime, cancel := context.WithCancel(ctx)

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
		lifecycleMu:                   sync.Mutex{},
		updateCancel:                  nil,
		stopDaemon:                    nil,
		overloadLogMu:                 sync.Mutex{},
		lastOverloadLogTime:           time.Time{},
		catalog:                       nil, shutdown: lifetime.Done(), auditCancel: nil, cancel: cancel, now: now,
		auditTicks: nil, auditWake: make(chan struct{}, 1), auditOnce: sync.Once{}, auditWG: sync.WaitGroup{},
		closeOnce:    sync.Once{},
		retryWait:    wait,
		auditStarted: make(chan struct{}),
	}
	snapshot, err := s.initializeAuditStorage(lifetime, cfg)
	if err != nil {
		cancel()
		inferRuntime.Close()
		hotStore.Close()
		return nil, err
	}
	s.runtime.Store(snapshot)
	if err := s.startConfigWatcher(lifetime); err != nil {
		cancel()
		snapshot.close(ctx, log)
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

func newRuntimeSnapshotForBucket(ctx context.Context, cfg *config.Config, log *slog.Logger, hotStore *hotkv.Store, inferRuntime *rules.InferRuntime, handle *auditstorage.BucketHandle) (*runtimeSnapshot, error) {
	store, err := intake.NewStore(ctx, handle.Database, cfg.AuditStoragePolicy(), log)
	if err != nil {
		return nil, fmt.Errorf("create intake store: %w", err)
	}
	intakeStore := &sqliteIntakeStore{store: store, log: log}

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
		QueueLimit:    0,
		BatchMaxItems: 0,
		BatchMaxBytes: 0,
		QueueMaxBytes: 0,
		SharedDB:      intakeStore.Handle(),
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
	// The detached-validator deadline is pushed onto the runtime here rather
	// than read from config at the call site, because that call site is a retry
	// loop and would otherwise reload and recompile the config once per attempt.
	// A reload rebuilds the snapshot, so the new value takes effect with it.
	execRuntime := rules.NewExecRuntimeWithCache(nil, log, hotStore)
	execRuntime.SetBackgroundTimeout(cfg.ExecBackgroundTimeout())

	return &runtimeSnapshot{
		bucket: handle.Bucket, bucketHandle: handle,
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

func defaultHotEvaluate(
	ctx context.Context,
	input hook.EvaluationInput,
	cfg *config.Config,
	getenv func(string) string,
	eventID string,
) hook.HotEvaluation {
	return hook.EvaluateClassifiedHotWithEventID(ctx, input, cfg, getenv, eventID)
}

func (s *runtimeSnapshot) close(ctx context.Context, log *slog.Logger) {
	if s == nil {
		return
	}
	if s.deferredProcessor != nil {
		s.deferredProcessor.Close()
	}
	if s.eventLogger != nil {
		drainContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if err := s.eventLogger.CloseContext(drainContext); err != nil && log != nil {
			log.WarnContext(ctx, "audit logger close failed", "err", err)
		}
	}
	if s.bucketHandle != nil {
		if err := s.bucketHandle.Close(); err != nil && log != nil {
			log.WarnContext(ctx, "audit bucket close failed", "err", err)
		}
	}
}

func (s *Server) startConfigWatcher(ctx context.Context) error {
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
	s.log.InfoContext(ctx, "watching config", "path", s.configPath)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				s.log.ErrorContext(ctx, "config watcher panic", "err", r)
			}
		}()
		s.watchConfigFile(ctx)
	}()
	return nil
}

func (s *Server) watchConfigFile(ctx context.Context) {
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
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if s.closing {
		return nil
	}
	ctx, cancel := context.WithCancel(ctx)
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				s.log.ErrorContext(ctx, "reload cancellation panic", "err", recovered)
			}
		}()
		select {
		case <-s.shutdown:
			cancel()
		case <-ctx.Done():
		}
	}()
	defer cancel()
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
	if candidate.Unusable() {
		return fmt.Errorf("config did not decode; keeping previous config: %s", candidate.Failures()[0].Reason)
	}
	for _, failure := range candidate.Failures() {
		s.log.ErrorContext(ctx, "config degraded on reload", "path", s.configPath,
			"kind", failure.Kind, "scope", failure.Scope, "err", failure.Reason)
		// Reload cannot replace a valid storage plan with a degraded fallback.
		if failure.Kind == config.LoadFailureSection && failure.Scope == "audit.storage" {
			return fmt.Errorf("audit storage config invalid: %s", failure.Reason)
		}
	}
	if errs := hook.ValidateConfig(candidate); len(errs) > 0 {
		s.log.WarnContext(ctx, "hook config validation failed", "path", s.configPath, "err", errs[0])
		return fmt.Errorf("hook config validation failed: %w", errs[0])
	}

	hook.WarnCapabilityDowngrades(ctx, s.log, candidate)

	newSnapshot, err := s.replaceAuditSnapshot(ctx, candidate)
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
	s.cfgMu.Unlock()

	s.cfgMu.Lock()
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

	ctx = hookEvaluationContext(ctx, snapshot)
	envFingerprint := req.GetEnvFingerprint()
	evaluationInput, normalizationErr := prepareHookEvaluationInput(req)

	getenv := func(key string) string {
		if envFingerprint == nil {
			return ""
		}
		return envFingerprint[key]
	}

	evalStart := hotEvalNow()
	intakeRecord, intakeErr := buildClassifiedIntakeRecord(
		evaluationInput.WireBytes,
		evaluationInput.NormalizedJSON,
		evaluationInput.Classification,
		envFingerprint,
	)
	if normalizationErr != nil {
		intakeErr = normalizationErr
	}
	if intakeErr != nil {
		intakeRecord = buildInvalidIntakeRecord(
			evaluationInput.WireBytes,
			evaluationInput.Classification,
			envFingerprint,
		)
	}

	appendResult, err := snapshot.intakeStore.Append(ctx, intakeRecord)
	if err != nil {
		requestLog.WarnContext(ctx, "append hook intake failed; failing open", "err", err)
		return s.unevaluated(ctx, req, hook.FailOpenReasonIntakeWriteFailed, err.Error()), nil
	}
	if intakeErr == nil {
		observeUserPrompt(
			snapshot.execRuntime,
			intakeRecord.System,
			evaluationInput.NormalizedJSON,
			appendResult.ReceiptID,
		)
	}

	syncCfg := hook.SyncConfig(snapshot.cfg)
	result := snapshot.hotEvaluate(
		ctx,
		evaluationInput,
		syncCfg,
		getenv,
		appendResult.EventID,
	)
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

func hookEvaluationContext(
	ctx context.Context,
	snapshot *runtimeSnapshot,
) context.Context {
	ctx = rules.WithExecRuntime(ctx, snapshot.execRuntime)
	ctx = rules.WithInferRuntime(ctx, snapshot.inferRuntime)
	return rules.WithGitStateReader(ctx, gitbranch.ReadState)
}

func prepareHookEvaluationInput(
	request *daemonpb.EvaluateHookRequest,
) (hook.EvaluationInput, error) {
	wireInput := cloneBytes(request.GetRawJson())
	classification := hook.ClassifyWithContext(
		wireInput,
		request.GetProviderHint(),
		request.GetArgv(),
		request.GetEnvFingerprint(),
		invocationContextFromProto(request.GetInvocationContext()),
	)
	normalizedJSON := cloneBytes(wireInput)
	if cwd := request.GetCwd(); cwd != "" {
		normalizedJSON = injectCWD(normalizedJSON, cwd)
	}
	if classification.ResolvedSystem() == hook.SystemCopilot {
		var err error
		normalizedJSON, err = hook.NormalizeCopilotPayload(
			normalizedJSON,
			copilotEventHint(request.GetArgv()),
		)
		if err != nil {
			return hook.EvaluationInput{
				WireBytes:      wireInput,
				NormalizedJSON: cloneBytes(wireInput),
				Classification: classification,
			}, wrapServerError("normalize Copilot payload", err)
		}
	}
	return hook.EvaluationInput{
		WireBytes:      wireInput,
		NormalizedJSON: normalizedJSON,
		Classification: classification,
	}, nil
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
		if argv[index] == "copilot-hook" {
			if index+1 < len(argv) {
				return argv[index+1]
			}
			return ""
		}
		if argv[index] == "managed-hook" &&
			index+2 < len(argv) && argv[index+1] == "copilot" {
			return argv[index+2]
		}
	}
	return ""
}

func buildClassifiedIntakeRecord(
	wireInput []byte,
	normalizedJSON []byte,
	classification hook.Classification,
	envFingerprint map[string]string,
) (intake.Record, error) {
	system := classification.ResolvedSystem()
	payload, err := hook.ParseHookPayload(system, normalizedJSON)
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
	record.RawPayload = cloneBytes(wireInput)
	record.NormalizedJSON = cloneBytes(normalizedJSON)
	record.ClassificationJSON = hook.MarshalClassification(classification)
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
	wireInput []byte,
	classification hook.Classification,
	envFingerprint map[string]string,
) intake.Record {
	return intake.Record{
		ReceiptID: 0, ReceivedAt: time.Time{}, EventID: "", SchemaVersion: 0,
		RecordedAt: time.Time{}, System: classification.ResolvedProvider,
		SessionID: "_no-session", TurnID: "", EventName: "_invalid",
		ToolName: "", ToolUseID: "", Operation: intake.Operation{
			CWD: "", EffectiveCWD: "", Command: "", FilePath: "",
		},
		RawPayload: cloneBytes(wireInput), NormalizedJSON: json.RawMessage(`{}`),
		ClassificationJSON: hook.MarshalClassification(classification),
		RawPayloadHash:     "", EnvFingerprint: cloneStringMap(envFingerprint),
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

func cloneBytes(value []byte) []byte {
	cloned := make([]byte, len(value))
	copy(cloned, value)
	return cloned
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
	if cwd == "" || len(rawJSON) == 0 {
		return rawJSON
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(rawJSON, &payload); err != nil {
		return rawJSON
	}
	encodedCWD, err := json.Marshal(cwd)
	if err != nil {
		return rawJSON
	}
	payload["cwd"] = encodedCWD
	normalized, err := json.Marshal(payload)
	if err != nil {
		return rawJSON
	}
	return normalized
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
