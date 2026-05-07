package hook

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"goodkind.io/agent-gate/internal/audit"
	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/rules"
)

const deferredAuditDropLogInterval = 5 * time.Second

// HotEvaluation is the synchronous hook decision plus optional cool-path audit work.
type HotEvaluation struct {
	Stdout   []byte
	Stderr   []byte
	ExitCode int
	Deferred DeferredAuditEvent
}

// DeferredAuditEvent is the compact hook decision record processed after
// provider response rendering.
type DeferredAuditEvent struct {
	Valid               bool
	RawBytes            []byte
	System              HookSystem
	SystemString        string
	EventName           string
	SessionID           string
	CWD                 string
	Fields              rules.FieldSet
	Rules               []config.Rule
	BlockingViolations  []rules.MatchViolation
	AuditOnlyViolations []rules.MatchViolation
	Decision            ResponseDecision
	DiagnosticText      string
}

// DeferredAuditQueueOptions tunes the hook cool-path audit queue.
type DeferredAuditQueueOptions struct {
	QueueLimit int
	Workers    int
	Log        *slog.Logger
}

// DeferredAuditQueue drops audit/enrichment work under pressure without
// changing already-rendered hook responses.
type DeferredAuditQueue struct {
	events   chan DeferredAuditEvent
	sink     audit.Sink
	log      *slog.Logger
	done     chan struct{}
	wg       sync.WaitGroup
	stopping atomic.Bool

	dropMu          sync.Mutex
	dropped         uint64
	lastDropLogTime time.Time
}

var deferredAuditNow = time.Now

// NewDeferredAuditQueue starts a bounded queue for cool hook audit work.
func NewDeferredAuditQueue(ctx context.Context, sink audit.Sink, options DeferredAuditQueueOptions) *DeferredAuditQueue {
	queueLimit := options.QueueLimit
	if queueLimit <= 0 {
		queueLimit = 1
	}
	queue := &DeferredAuditQueue{
		events:          make(chan DeferredAuditEvent, queueLimit),
		sink:            sink,
		log:             options.Log,
		done:            make(chan struct{}),
		wg:              sync.WaitGroup{},
		stopping:        atomic.Bool{},
		dropMu:          sync.Mutex{},
		dropped:         0,
		lastDropLogTime: time.Time{},
	}
	for range options.Workers {
		queue.wg.Add(1)
		go func() {
			defer func() {
				if recovered := recover(); recovered != nil && queue.log != nil {
					queue.log.ErrorContext(ctx, "deferred hook audit worker panic recovered", "err", recovered)
				}
			}()
			queue.worker(ctx)
		}()
	}
	return queue
}

// Enqueue records cool hook audit work if queue capacity is available.
func (q *DeferredAuditQueue) Enqueue(event DeferredAuditEvent) bool {
	if q == nil || !event.Valid || q.stopping.Load() {
		return false
	}
	select {
	case q.events <- event:
		return true
	default:
		q.recordDrop(event)
		return false
	}
}

// Close drains queued events and stops workers.
func (q *DeferredAuditQueue) Close() {
	if q == nil {
		return
	}
	if q.stopping.Swap(true) {
		return
	}
	close(q.done)
	q.wg.Wait()
}

func (q *DeferredAuditQueue) worker(ctx context.Context) {
	defer q.wg.Done()
	for {
		select {
		case event := <-q.events:
			WriteDeferredAudit(ctx, event, q.sink)
		case <-q.done:
			q.drain(ctx)
			return
		}
	}
}

func (q *DeferredAuditQueue) drain(ctx context.Context) {
	for {
		select {
		case event := <-q.events:
			WriteDeferredAudit(ctx, event, q.sink)
		default:
			return
		}
	}
}

func (q *DeferredAuditQueue) recordDrop(event DeferredAuditEvent) {
	q.dropMu.Lock()
	q.dropped++
	dropped := q.dropped
	now := deferredAuditNow()
	if !q.lastDropLogTime.IsZero() && now.Sub(q.lastDropLogTime) < deferredAuditDropLogInterval {
		q.dropMu.Unlock()
		return
	}
	q.lastDropLogTime = now
	queueDepth := len(q.events)
	queueLimit := cap(q.events)
	q.dropMu.Unlock()

	if q.log != nil {
		q.log.Warn("deferred hook audit queue full; dropping event",
			"system", event.SystemString,
			"session_id", event.SessionID,
			"event", event.EventName,
			"queue_depth", queueDepth,
			"queue_limit", queueLimit,
			"dropped", dropped,
		)
	}
}
