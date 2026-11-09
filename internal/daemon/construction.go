package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"goodkind.io/agent-gate/api/daemonpb"
	"goodkind.io/agent-gate/internal/audit"
	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/hook"
	"goodkind.io/agent-gate/internal/hotkv"
	"goodkind.io/agent-gate/internal/intake"
	"goodkind.io/agent-gate/internal/rules"
)

// New creates the daemon runtime and configuration watcher.
//
// New validates hook rules and prepares audit storage before starting
// the watcher. StartAuditScheduler starts the audit scheduler after
// transport readiness.
//
//	server, err := New(ctx, log, cfg)
func New(ctx context.Context, log *slog.Logger, cfg *config.Config) (*Server, error) {
	return newServer(ctx, log, cfg, time.Now)
}

func newServer(
	ctx context.Context,
	log *slog.Logger,
	cfg *config.Config,
	now func() time.Time,
) (*Server, error) {
	return newServerWithStorageWait(ctx, log, cfg, now, waitAuditRetry)
}

func newServerWithStorageWait(
	ctx context.Context,
	log *slog.Logger,
	cfg *config.Config,
	now func() time.Time,
	wait func(context.Context, time.Duration) error,
) (*Server, error) {
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
	lifetime, cancel := context.WithCancel(ctx)

	server := newServerState(log, hotStore, lifetime, cancel, now, wait)
	server.deferredProcessor = newDeferredProcessor(
		lifetime,
		nil,
		nil,
		cfg,
		nil,
		cfg.HookDeferredQueueLimit(),
		0,
		log,
	)
	server.deferredProcessor.service.mu.Lock()
	server.deferredProcessor.cfg = nil
	server.deferredProcessor.service.server = server
	server.deferredProcessor.notify = server.wakeAuditScheduler
	server.deferredProcessor.now = server.now
	server.deferredProcessor.service.mu.Unlock()
	snapshot, err := server.initializeAuditStorage(lifetime, cfg)
	if err != nil {
		cancel()
		server.deferredProcessor.Close()
		hotStore.Close()

		return nil, err
	}

	server.publishRuntimeLocked(snapshot)
	server.deferredProcessor.configure(cfg)
	if err := server.startConfigWatcher(lifetime); err != nil {
		cancel()
		snapshot.close(ctx, log)
		server.deferredProcessor.Close()
		hotStore.Close()

		return nil, err
	}

	return server, nil
}

func hotKVOptions(cfg *config.Config) hotkv.Options {
	return hotkv.Options{
		MaxEntries:    cfg.HookCacheMaxEntries(),
		MaxValueBytes: cfg.HookCacheMaxValueBytes(),
		MaxBytes:      cfg.HookCacheMaxBytes(),
		PruneInterval: cfg.HookCachePruneInterval(),
	}
}

func newRuntimeSnapshotForBucket(
	ctx context.Context,
	cfg *config.Config,
	log *slog.Logger,
	hotStore *hotkv.Store,
	owner *fileOwner,
	inference *inferenceOwner,
) (*runtimeSnapshot, error) {
	handle := owner.handle
	store, err := intake.NewStore(ctx, handle.Database, cfg.AuditStoragePolicy(), log)
	if err != nil {
		return nil, fmt.Errorf("create intake store: %w", err)
	}

	intakeStore := &sqliteIntakeStore{store: store, log: log}

	if inference == nil {
		inference = &inferenceOwner{
			runtime:    rules.NewInferRuntimeWithCache(log, hotStore),
			references: atomic.Int64{},
		}
		inference.runtime.SetJudgeTranscript(
			cfg.JudgeTranscriptEndpoint(),
			cfg.JudgeTranscriptMaxTokens(),
			cfg.JudgeTranscriptTokenModel(),
			cfg.JudgeTranscriptTimeout(),
			cfg.JudgeTranscriptOnError(),
		)
	}

	inference.references.Add(1)

	eventLogger, err := audit.NewEventLoggerWithOptions(
		ctx,
		cfg,
		log,
		audit.LoggerOptions{
			QueueLimit:    0,
			BatchMaxItems: 0,
			BatchMaxBytes: 0,
			QueueMaxBytes: 0,
			SharedDB:      intakeStore.Handle(),
		},
	)
	if err != nil {
		inference.release()
		if log != nil {
			log.WarnContext(ctx, "create event logger failed", "err", err)
		}

		return nil, fmt.Errorf("create event logger: %w", err)
	}

	// The detached-validator deadline is pushed onto the runtime here rather
	// than read from config at the call site, because that call site is a retry
	// loop and would otherwise reload and recompile the config once per attempt.
	// A reload rebuilds the snapshot, so the new value takes effect with it.
	execRuntime := rules.NewExecRuntimeWithCache(nil, log, hotStore)
	execRuntime.SetBackgroundTimeout(cfg.ExecBackgroundTimeout())

	snapshot := &runtimeSnapshot{
		assignmentGeneration: 0,
		owner:                owner,
		catalog:              nil,
		bucket:               handle.Bucket,
		bucketHandle:         handle,
		cfg:                  cfg,
		eventLogger:          eventLogger,
		intakeStore:          intakeStore,
		evaluationRecorder:   intakeStore.Evaluations(),
		deferredProcessor:    nil,
		evaluateSlots:        make(chan struct{}, cfg.HookHotConcurrency()),
		evaluateQueueWait:    cfg.HookHotQueueWait(),
		hotEvaluate:          defaultHotEvaluate,
		execRuntime:          execRuntime,
		inferRuntime:         inference.runtime,
		inference:            inference,
	}

	return snapshot, nil
}

func newServerState(
	log *slog.Logger,
	hotStore *hotkv.Store,
	lifetime context.Context,
	cancel context.CancelFunc,
	now func() time.Time,
	wait func(context.Context, time.Duration) error,
) *Server {
	return &Server{
		UnimplementedAgentGateDServer: daemonpb.UnimplementedAgentGateDServer{},
		log:                           log,
		cfgMu:                         sync.RWMutex{},
		runtimeMu:                     sync.RWMutex{},
		runtime:                       atomic.Pointer[runtimeSnapshot]{},
		configWatcher:                 nil,
		configPath:                    config.Path(),
		hotKV:                         hotStore,
		deferredProcessor:             nil,
		owners:                        fileOwners{mu: sync.Mutex{}, files: nil},
		closing:                       false,
		assignmentGeneration:          0,
		lifecycleMu:                   sync.Mutex{},
		storageTransition:             nil,
		updateCancel:                  nil,
		stopDaemon:                    nil,
		overloadLogMu:                 sync.Mutex{},
		lastOverloadLogTime:           time.Time{},
		catalog:                       nil,
		shutdown:                      lifetime.Done(),
		auditCancel:                   nil,
		cancel:                        cancel,
		now:                           now,
		auditTicks:                    nil,
		auditWake:                     make(chan struct{}, 1),
		auditOnce:                     sync.Once{},
		auditWG:                       sync.WaitGroup{},
		closeOnce:                     sync.Once{},
		retryWait:                     wait,
		auditStarted:                  make(chan struct{}),
		auditRequested:                false,
	}
}
