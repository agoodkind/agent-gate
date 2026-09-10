package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"goodkind.io/agent-gate/internal/audit"
	"goodkind.io/agent-gate/internal/auditstorage"
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
	cancel             context.CancelFunc
	catalog            *auditstorage.Catalog
	bucket             auditstorage.Bucket
	now                func() time.Time
	notify             func()
	queueMu            sync.Mutex
	queued             map[ReplayKey]int
	queuedBytes        int
}

var deferredProcessorSequence atomic.Uint64

type deferredWork struct {
	bucket    auditstorage.Bucket
	receiptID int64
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
	ctx, cancel := context.WithCancel(ctx)

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
		claimLease:         cfg.DeferredClaimLease(),
		claimRenewInterval: cfg.DeferredClaimRenewInterval(),
		cancel:             cancel,
		catalog:            nil, bucket: auditstorage.Bucket{ID: "", Path: "", Start: time.Time{}}, now: time.Now, notify: nil,
		queueMu: sync.Mutex{}, queued: make(map[ReplayKey]int), queuedBytes: 0,
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

func (p *deferredProcessor) Close() {
	if p == nil {
		return
	}
	if p.stopping.Swap(true) {
		p.wg.Wait()
		return
	}
	close(p.done)
	if p.cancel != nil {
		p.cancel()
	}
	p.wg.Wait()
}

func (p *deferredProcessor) worker(ctx context.Context) {
	for {
		select {
		case work := <-p.events:
			p.processDispatch(ctx, work)
		case <-p.done:
			return
		case <-ctx.Done():
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
			p.log.WarnContext(ctx, "claim deferred intake failed", "receipt_id", work.receiptID, "err", err)
		}
		return
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
	p.processRecord(processingCtx, record, claim, stopRenewalAndWait)
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
	record intake.Record,
	claim intake.DeferredClaim,
	afterCommit func(),
) {
	deferredEvent, ok := p.rebuildDeferredAudit(ctx, record)
	if !ok || ctx.Err() != nil {
		p.releaseClaim(ctx, claim)
		return
	}
	mode := "deferred"
	if claim.Attempt > 1 {
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
	auditEntries, err := captureDeferredAudit(ctx, deferredEvent, p.sink)
	if err != nil {
		p.logDeferredFailure(ctx, record, "audit_normalization_failed", err)
		p.releaseClaim(ctx, claim)
		return
	}
	if err := p.evaluationRecorder.CommitDeferredEvaluation(
		ctx, claim, evaluationRecord, auditEntries,
	); err != nil {
		if errors.Is(err, intake.ErrDeferredClaimLost) {
			return
		}
		p.logDeferredFailure(ctx, record, "evaluation_persistence_failed", err)
		p.releaseClaim(ctx, claim)
		return
	}
	if afterCommit != nil {
		afterCommit()
	}
}

func (p *deferredProcessor) releaseClaim(ctx context.Context, claim intake.DeferredClaim) {
	now := p.now()
	nextAttempt := now.Add(auditRetryDelay(claim.Attempt))
	if !p.bucket.Start.IsZero() {
		policy := p.cfg.AuditStoragePolicy()
		expires := p.bucket.Start.Add(time.Duration(policy.RetentionBuckets) * policy.BucketInterval)
		if nextAttempt.After(expires) {
			nextAttempt = expires
		}
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := p.store.ScheduleDeferredRetry(ctx, claim, nextAttempt); err != nil &&
		!errors.Is(err, intake.ErrDeferredClaimLost) && p.log != nil {
		p.log.WarnContext(
			ctx, "schedule deferred intake retry failed",
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

type normalizedAuditCollector struct {
	sink    audit.ReplayableDurableSink
	entries []audit.NormalizedEntry
}

func (collector *normalizedAuditCollector) Log(
	_ context.Context,
	system string,
	sessionID string,
	eventName string,
	level string,
	msg string,
	attrs audit.Attrs,
) {
	entry := collector.sink.Normalize(
		system, sessionID, eventName, level, msg, attrs,
	)
	if entry.Event.EventID != "" {
		collector.entries = append(collector.entries, entry)
	}
}

func (collector *normalizedAuditCollector) Close() error {
	return nil
}

func captureDeferredAudit(
	ctx context.Context,
	event hook.DeferredAuditEvent,
	sink audit.Sink,
) ([]audit.NormalizedEntry, error) {
	if sink == nil || !event.Valid {
		return nil, nil
	}
	replayableSink, ok := sink.(audit.ReplayableDurableSink)
	if !ok {
		return nil, fmt.Errorf("audit sink does not support normalized replay")
	}
	collector := &normalizedAuditCollector{
		sink: replayableSink, entries: make([]audit.NormalizedEntry, 0, 3),
	}
	hook.WriteDeferredFindings(ctx, event, collector)
	return collector.entries, nil
}

func (p *deferredProcessor) rebuildDeferredAudit(
	ctx context.Context,
	record intake.Record,
) (hook.DeferredAuditEvent, bool) {
	getenv := func(key string) string {
		return record.EnvFingerprint[key]
	}
	hint := hook.SystemFromString(record.System)
	classification := replayClassification(record, hint)
	normalizedJSON := record.NormalizedJSON
	if len(normalizedJSON) == 0 {
		normalizedJSON = record.RawPayload
	}
	evaluationInput := hook.EvaluationInput{
		WireBytes:      record.RawPayload,
		NormalizedJSON: normalizedJSON,
		Classification: classification,
	}

	if p.inferRuntime != nil {
		ctx = rules.WithInferRuntime(ctx, p.inferRuntime)
	}
	result := hook.EvaluateClassifiedHotWithEventID(ctx, evaluationInput, hook.DeferredConfig(p.cfg), getenv, record.EventID)
	return result.Deferred, result.Deferred.Valid
}

func replayClassification(record intake.Record, hint hook.System) hook.Classification {
	var classification hook.Classification
	if len(record.ClassificationJSON) > 0 {
		if err := json.Unmarshal(record.ClassificationJSON, &classification); err == nil && classification.Result != "" {
			return classification
		}
	}
	return hook.Classify(record.RawPayload, hint, nil, record.EnvFingerprint)
}
