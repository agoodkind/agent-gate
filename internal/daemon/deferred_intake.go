package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"goodkind.io/agent-gate/internal/audit"
	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/hook"
	"goodkind.io/agent-gate/internal/intake"
	"goodkind.io/agent-gate/internal/rules"
	"goodkind.io/agent-gate/internal/version"
	gkversion "goodkind.io/gklog/version"
)

type deferredProcessor struct {
	events             chan deferredWork
	store              intakeStore
	sink               audit.Sink
	cfg                *config.Config
	inferRuntime       *rules.InferRuntime
	evaluationRecorder evaluationRecorder
	log                *slog.Logger
	done               chan struct{}
	wg                 sync.WaitGroup
	stopping           atomic.Bool
	claimOwner         string
	claimLease         time.Duration
	claimRenewInterval time.Duration
}

const (
	deferredClaimLease         = 30 * time.Second
	deferredClaimRenewInterval = 10 * time.Second
)

var deferredProcessorSequence atomic.Uint64

type deferredWork struct {
	receiptID int64
	eventID   string
	hotEvent  hook.DeferredAuditEvent
}

func newDeferredProcessor(
	ctx context.Context,
	store intakeStore,
	sink audit.Sink,
	cfg *config.Config,
	inferRuntime *rules.InferRuntime,
	queueLimit int,
	workers int,
	log *slog.Logger,
) *deferredProcessor {
	if queueLimit <= 0 {
		queueLimit = 1
	}
	if log == nil {
		log = slog.Default()
	}

	processor := &deferredProcessor{
		events:             make(chan deferredWork, queueLimit),
		store:              store,
		sink:               sink,
		cfg:                cfg,
		inferRuntime:       inferRuntime,
		evaluationRecorder: nil,
		log:                log,
		done:               make(chan struct{}),
		wg:                 sync.WaitGroup{},
		stopping:           atomic.Bool{},
		claimOwner: fmt.Sprintf(
			"agent-gate-%d-%d", os.Getpid(), deferredProcessorSequence.Add(1),
		),
		claimLease:         deferredClaimLease,
		claimRenewInterval: deferredClaimRenewInterval,
	}

	for range workers {
		processor.wg.Go(func() {
			defer func() {
				if recovered := recover(); recovered != nil && processor.log != nil {
					processor.log.ErrorContext(ctx, "deferred intake worker panic recovered", "err", recovered)
				}
			}()
			processor.worker(ctx)
		})
	}
	return processor
}

func (p *deferredProcessor) ReplayPending(ctx context.Context) error {
	if p == nil || p.store == nil {
		return nil
	}

	receiptIDs, err := p.store.ListPending(ctx)
	if err != nil {
		if p.log != nil {
			p.log.WarnContext(ctx, "replay pending deferred intake failed", "err", err)
		}
		return fmt.Errorf("replay pending deferred intake: %w", err)
	}
	var emptyEvent hook.DeferredAuditEvent
	for _, receiptID := range receiptIDs {
		p.processEvent(ctx, deferredWork{
			receiptID: receiptID, eventID: "", hotEvent: emptyEvent,
		})
	}
	return nil
}

func (p *deferredProcessor) Enqueue(receiptID int64, eventID string, hotEvent hook.DeferredAuditEvent) bool {
	if p == nil || p.store == nil || receiptID <= 0 || eventID == "" || p.stopping.Load() {
		return false
	}

	select {
	case p.events <- deferredWork{receiptID: receiptID, eventID: eventID, hotEvent: hotEvent}:
		return true
	default:
		if p.log != nil {
			p.log.Warn("deferred intake queue full; leaving event durable for replay",
				"event_id", eventID,
				"queue_depth", len(p.events),
				"queue_limit", cap(p.events),
			)
		}
		return false
	}
}

func (p *deferredProcessor) Close() {
	if p == nil {
		return
	}
	if p.stopping.Swap(true) {
		return
	}
	close(p.done)
	p.wg.Wait()
}

func (p *deferredProcessor) worker(ctx context.Context) {
	replayTicker := time.NewTicker(deferredClaimLease)
	defer replayTicker.Stop()
	for {
		select {
		case work := <-p.events:
			p.processEvent(ctx, work)
		case <-replayTicker.C:
			_ = p.ReplayPending(ctx)
		case <-p.done:
			return
		}
	}
}

func (p *deferredProcessor) processEvent(ctx context.Context, work deferredWork) {
	record, claim, err := p.store.ClaimDeferred(
		ctx, work.receiptID, p.claimOwner, p.claimLease,
	)
	if err != nil {
		if errors.Is(err, intake.ErrDeferredClaimUnavailable) {
			return
		}
		if p.log != nil {
			p.log.WarnContext(ctx, "claim deferred intake failed", "event_id", work.eventID, "err", err)
		}
		return
	}
	var hotEvent *hook.DeferredAuditEvent
	if work.hotEvent.Valid {
		hotEvent = &work.hotEvent
	}
	processingCtx, cancel := context.WithCancel(ctx)
	stopRenewal := make(chan struct{})
	renewalDone := make(chan struct{})
	renewalStopped := false
	stopRenewalAndWait := func() {
		if renewalStopped {
			return
		}
		close(stopRenewal)
		<-renewalDone
		renewalStopped = true
	}
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil && p.log != nil {
				p.log.ErrorContext(
					processingCtx, "deferred claim renewal panic recovered", "err", recovered,
				)
			}
		}()
		p.renewClaim(processingCtx, cancel, claim, stopRenewal, renewalDone)
	}()
	defer stopRenewalAndWait()
	defer cancel()
	p.processRecord(processingCtx, ctx, record, claim, hotEvent, stopRenewalAndWait)
}

func (p *deferredProcessor) renewClaim(
	ctx context.Context,
	cancel context.CancelFunc,
	claim intake.DeferredClaim,
	stop <-chan struct{},
	done chan<- struct{},
) {
	defer close(done)
	ticker := time.NewTicker(p.claimRenewInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if err := p.store.RenewDeferredClaim(ctx, claim, p.claimLease); err != nil {
				if p.log != nil {
					p.log.WarnContext(
						ctx, "renew deferred intake claim failed",
						"receipt_id", claim.ReceiptID, "attempt", claim.Attempt, "err", err,
					)
				}
				cancel()
				return
			}
		case <-stop:
			return
		case <-ctx.Done():
			return
		}
	}
}

func (p *deferredProcessor) processRecord(
	ctx context.Context,
	auditCtx context.Context,
	record intake.Record,
	claim intake.DeferredClaim,
	hotEvent *hook.DeferredAuditEvent,
	afterCommit func(),
) {
	deferredEvent, ok := p.rebuildDeferredAudit(ctx, record, hotEvent)
	if !ok {
		p.releaseClaim(ctx, claim)
		return
	}
	mode := "deferred"
	if hotEvent == nil {
		mode = "deferred_replay"
	}
	attempt := claim.Attempt
	configHash, err := p.cfg.Identity()
	if err != nil {
		p.logDeferredFailure(ctx, record, "config_identity_failed", err)
		p.releaseClaim(ctx, claim)
		return
	}
	completedAt := hotEvalNow()
	startedAt := deferredEvent.Trace.Deterministic.StartedAt
	if startedAt.IsZero() {
		startedAt = completedAt
	}
	evaluationRecord := buildDeferredEvaluationRecord(deferredEvaluationRecordInput{
		ReceiptID: record.ReceiptID, EventID: record.EventID, Intake: record,
		Mode: mode, Attempt: attempt, ConfigHash: configHash,
		EngineVersion: gkversion.Version, EngineCommit: gkversion.Commit,
		EngineBuildHash: version.BuildHash(), StartedAt: startedAt,
		CompletedAt: completedAt, Event: deferredEvent,
	})
	if p.evaluationRecorder == nil {
		p.logDeferredFailure(ctx, record, "evaluation_recorder_unavailable", nil)
		p.releaseClaim(ctx, claim)
		return
	}
	if err := p.evaluationRecorder.CommitDeferredEvaluation(
		ctx, claim, evaluationRecord,
	); err != nil {
		p.logDeferredFailure(ctx, record, "evaluation_persistence_failed", err)
		p.releaseClaim(ctx, claim)
		return
	}
	if afterCommit != nil {
		afterCommit()
	}
	if err := writeDeferredAuditDurable(auditCtx, deferredEvent, p.sink); err != nil {
		p.logDeferredFailure(ctx, record, "audit_persistence_failed", err)
	}
}

func (p *deferredProcessor) releaseClaim(ctx context.Context, claim intake.DeferredClaim) {
	if err := p.store.ReleaseDeferredClaim(ctx, claim); err != nil &&
		!errors.Is(err, intake.ErrDeferredClaimLost) && p.log != nil {
		p.log.WarnContext(
			ctx, "release deferred intake claim failed",
			"receipt_id", claim.ReceiptID, "attempt", claim.Attempt, "err", err,
		)
	}
}

func (p *deferredProcessor) logDeferredFailure(
	ctx context.Context,
	record intake.Record,
	statusClass string,
	err error,
) {
	if p.log == nil {
		return
	}
	p.log.WarnContext(
		ctx, "record deferred evaluation failed; leaving receipt pending",
		"receipt_id", record.ReceiptID, "event_id", record.EventID,
		"status_class", statusClass, "err", err,
	)
}

type durableAuditForwarder struct {
	sink audit.DurableSink
	err  error
}

func (forwarder *durableAuditForwarder) Log(
	ctx context.Context,
	system string,
	sessionID string,
	eventName string,
	level string,
	msg string,
	attrs audit.Attrs,
) {
	if forwarder.err != nil {
		return
	}
	forwarder.err = forwarder.sink.LogDurable(
		ctx, system, sessionID, eventName, level, msg, attrs,
	)
}

func (forwarder *durableAuditForwarder) Close() error {
	return nil
}

func writeDeferredAuditDurable(
	ctx context.Context,
	event hook.DeferredAuditEvent,
	sink audit.Sink,
) error {
	if sink == nil {
		return nil
	}
	durableSink, ok := sink.(audit.DurableSink)
	if !ok {
		return fmt.Errorf("audit sink does not support durable writes")
	}
	forwarder := &durableAuditForwarder{sink: durableSink, err: nil}
	hook.WriteDeferredAudit(ctx, event, forwarder)
	return forwarder.err
}

func (p *deferredProcessor) rebuildDeferredAudit(
	ctx context.Context,
	record intake.Record,
	hotEvent *hook.DeferredAuditEvent,
) (hook.DeferredAuditEvent, bool) {
	getenv := func(key string) string {
		return record.EnvFingerprint[key]
	}
	hint := hook.SystemFromString(record.System)

	var merged hook.DeferredAuditEvent
	if hotEvent != nil && hotEvent.Valid {
		merged = *hotEvent
	} else {
		syncCfg := hook.ReplaySyncConfig(p.cfg)
		syncEval := hook.EvaluateHotWithEventID(
			ctx,
			record.RawPayload,
			syncCfg,
			hint,
			getenv,
			record.EventID,
		)
		if !syncEval.Deferred.Valid {
			if p.log != nil {
				p.log.WarnContext(ctx, "replay sync evaluation produced invalid deferred event", "event_id", record.EventID)
			}
			var empty hook.DeferredAuditEvent
			return empty, false
		}
		merged = syncEval.Deferred
	}
	syncRules, deferredRules := hook.PartitionRules(p.cfg)
	deferredCfg := hook.DeferredConfig(p.cfg)
	if hotEvent == nil || !hotEvent.Valid {
		replaySyncCfg := hook.ReplaySyncConfig(p.cfg)
		replayDeferredCfg := hook.ReplayDeferredConfig(p.cfg)
		syncRules = replaySyncCfg.Rules
		deferredRules = replayDeferredCfg.Rules
		deferredCfg = replayDeferredCfg
	}
	merged.Rules = append(append([]config.Rule(nil), syncRules...), deferredRules...)

	if len(deferredRules) > 0 {
		collector := &inferenceTraceSink{traces: nil}
		deferredCtx := rules.WithInferenceTraceCollector(ctx, collector)
		if p.inferRuntime != nil {
			deferredCtx = rules.WithInferRuntime(deferredCtx, p.inferRuntime)
		}
		deferredEval := hook.EvaluateHotWithEventID(
			deferredCtx,
			record.RawPayload,
			deferredCfg,
			hint,
			getenv,
			record.EventID,
		)
		if deferredEval.Deferred.Valid {
			merged.AuditOnlyViolations = append(
				append([]rules.Violation(nil), merged.AuditOnlyViolations...),
				deferredEval.Deferred.AuditOnlyViolations...,
			)
			merged.InferenceTraces = append(merged.InferenceTraces, collector.snapshot()...)
			merged.Trace = deferredEval.Trace
		} else if p.log != nil {
			p.log.WarnContext(ctx, "replay deferred evaluation produced invalid deferred event", "event_id", record.EventID)
		}
	}
	return merged, true
}
