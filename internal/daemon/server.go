// Package daemon implements the agent-gate daemon gRPC server.
package daemon

import (
	"context"
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
	"goodkind.io/agent-gate/internal/hook"
	"goodkind.io/agent-gate/internal/intake"
	"goodkind.io/agent-gate/internal/version"
)

const configReloadDebounce = 200 * time.Millisecond

const (
	overloadLogInterval = 5 * time.Second
)

type runtimeSnapshot struct {
	cfg               *config.Config
	eventLogger       *audit.EventLogger
	intakeStore       intakeStore
	deferredProcessor *deferredProcessor
	evaluateSlots     chan struct{}
	evaluateQueueWait time.Duration
	hotEvaluate       func(context.Context, []byte, *config.Config, hook.HookSystem, func(string) string, string) hook.HotEvaluation
}

// Server implements the AgentGateD gRPC service.
type Server struct {
	daemonpb.UnimplementedAgentGateDServer

	log           *slog.Logger
	cfgMu         sync.RWMutex
	runtime       atomic.Pointer[runtimeSnapshot]
	configWatcher *fsnotify.Watcher
	configPath    string
	closing       bool

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
			Log:   config.Log{},
			Audit: config.Audit{Enabled: nil, Level: "", Outputs: config.AuditOutput{}, Query: config.AuditQuery{}},
			Paths: config.Paths{},
			Performance: config.Performance{
				Hook: config.HookPerformance{
					HotConcurrency:     0,
					HotQueueWaitMS:     0,
					DeferredQueueLimit: 0,
					DeferredWorkers:    0,
				},
			},
			Rules: nil,
		}
	}
	if errs := hook.ValidateConfig(cfg); len(errs) > 0 {
		log.Error("invalid hook config", slog.Any("err", errs[0]))
		return nil, fmt.Errorf("invalid hook config: %w", errs[0])
	}

	hook.WarnCapabilityDowngrades(context.Background(), log, cfg)

	snapshot, err := newRuntimeSnapshot(context.Background(), cfg, log)
	if err != nil {
		log.Error("failed to create runtime snapshot", slog.Any("err", err))
		return nil, err
	}

	s := &Server{
		UnimplementedAgentGateDServer: daemonpb.UnimplementedAgentGateDServer{},
		log:                           log,
		cfgMu:                         sync.RWMutex{},
		runtime:                       atomic.Pointer[runtimeSnapshot]{},
		configWatcher:                 nil,
		configPath:                    config.Path(),
		closing:                       false,
		overloadLogMu:                 sync.Mutex{},
		lastOverloadLogTime:           time.Time{},
	}
	s.runtime.Store(snapshot)
	if err := s.startConfigWatcher(); err != nil {
		snapshot.close(context.Background(), log)
		return nil, err
	}
	return s, nil
}

func newRuntimeSnapshot(ctx context.Context, cfg *config.Config, log *slog.Logger) (*runtimeSnapshot, error) {
	eventLogger, err := audit.NewEventLoggerContext(ctx, cfg, log)
	if err != nil {
		if log != nil {
			log.WarnContext(ctx, "create event logger failed", "err", err)
		}
		return nil, fmt.Errorf("create event logger: %w", err)
	}

	intakeStore, err := newSQLiteIntakeStore(ctx, cfg, log)
	if err != nil {
		if eventLogger != nil {
			_ = eventLogger.Close()
		}
		return nil, fmt.Errorf("create intake store: %w", err)
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
		cfg.HookDeferredQueueLimit(),
		cfg.HookDeferredWorkers(),
		log,
	)
	if err := deferredProcessor.ReplayPending(ctx); err != nil {
		deferredProcessor.Close()
		if eventLogger != nil {
			_ = eventLogger.Close()
		}
		return nil, fmt.Errorf("replay pending intake: %w", err)
	}

	return &runtimeSnapshot{
		cfg:               cfg,
		eventLogger:       eventLogger,
		intakeStore:       intakeStore,
		deferredProcessor: deferredProcessor,
		evaluateSlots:     make(chan struct{}, cfg.HookHotConcurrency()),
		evaluateQueueWait: cfg.HookHotQueueWait(),
		hotEvaluate:       defaultHotEvaluate,
	}, nil
}

func defaultHotEvaluate(ctx context.Context, rawJSON []byte, cfg *config.Config, hint hook.HookSystem, getenv func(string) string, eventID string) hook.HotEvaluation {
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
	snapshot := s.runtime.Swap(nil)
	s.cfgMu.Unlock()

	if s.configWatcher != nil {
		_ = s.configWatcher.Close()
	}
	snapshot.close(context.Background(), s.log)
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

	newSnapshot, err := newRuntimeSnapshot(ctx, candidate, s.log)
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
	oldSnapshot := s.runtime.Swap(newSnapshot)
	s.cfgMu.Unlock()

	oldSnapshot.close(ctx, s.log)
	s.log.InfoContext(ctx, "config reloaded", "path", s.configPath, "rules", len(candidate.Rules), "audit_enabled", candidate.AuditEnabled())
	return nil
}

// EvaluateHook processes a hook event through daemon-owned enforcement.
func (s *Server) EvaluateHook(ctx context.Context, req *daemonpb.EvaluateHookRequest) (*daemonpb.EvaluateHookResponse, error) {
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

	intakeRecord, err := buildIntakeRecord(rawJSON, req.GetProviderHint(), envFingerprint)
	if err != nil {
		result := snapshot.hotEvaluate(ctx, rawJSON, snapshot.cfg, hook.SystemFromString(req.GetProviderHint()), getenv, "")
		return &daemonpb.EvaluateHookResponse{
			ExitCode:   clampExitCode(result.ExitCode),
			StdoutData: append([]byte(nil), result.Stdout...),
			StderrData: append([]byte(nil), result.Stderr...),
		}, nil
	}

	appendResult, err := snapshot.intakeStore.Append(ctx, intakeRecord)
	if err != nil {
		requestLog.WarnContext(ctx, "append hook intake failed; failing open", "err", err)
		return failOpenEvaluateHookResponse(), nil
	}

	syncCfg := hook.SyncConfig(snapshot.cfg)
	result := snapshot.hotEvaluate(ctx, rawJSON, syncCfg, hook.SystemFromString(req.GetProviderHint()), getenv, appendResult.EventID)
	if err := enqueueDeferredReplay(ctx, requestLog, snapshot, appendResult, result.Deferred.Valid); err != nil {
		return failOpenEvaluateHookResponse(), nil
	}
	return &daemonpb.EvaluateHookResponse{
		ExitCode:   clampExitCode(result.ExitCode),
		StdoutData: append([]byte(nil), result.Stdout...),
		StderrData: append([]byte(nil), result.Stderr...),
	}, nil
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
	record.Operation.EffectiveCWD = fields.String(config.FieldEffectiveCWD)
	record.Operation.Command = fields.CommandValue()
	record.Operation.FilePath = fields.FilePathValue()
	return record, nil
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

func enqueueDeferredReplay(ctx context.Context, log *slog.Logger, snapshot *runtimeSnapshot, appendResult intake.AppendResult, deferredValid bool) error {
	if !deferredValid {
		return nil
	}

	shouldEnqueue := appendResult.Inserted
	if !appendResult.Inserted {
		record, err := snapshot.intakeStore.Get(ctx, appendResult.EventID)
		if err != nil {
			log.WarnContext(ctx, "load duplicate hook intake failed; failing open", "event_id", appendResult.EventID, "err", err)
			return fmt.Errorf("load duplicate hook intake %q: %w", appendResult.EventID, err)
		}
		shouldEnqueue = record.DeferredState != intake.DeferredStateComplete
	}
	if !shouldEnqueue {
		return nil
	}

	if err := snapshot.intakeStore.MarkDeferredPending(ctx, appendResult.EventID); err != nil {
		log.WarnContext(ctx, "mark deferred intake pending failed; failing open", "event_id", appendResult.EventID, "err", err)
		return fmt.Errorf("mark deferred intake pending %q: %w", appendResult.EventID, err)
	}
	if snapshot.deferredProcessor != nil {
		snapshot.deferredProcessor.Enqueue(appendResult.EventID)
	}
	return nil
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
		Version:        version.Version,
		Commit:         version.Commit,
		Dirty:          version.Dirty,
		BuildHash:      version.BuildHash(),
	}, nil
}
