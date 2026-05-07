package audit

import (
	"bytes"
	"log/slog"
	"sync"
	"testing"
	"time"

	expirable "github.com/hashicorp/golang-lru/v2/expirable"
)

type blockingEventSink struct {
	release <-chan bool
}

func (s blockingEventSink) Write(Event, string) error {
	<-s.release
	return nil
}

func (s blockingEventSink) Close() error {
	return nil
}

func TestEventLogger_DropsWhenQueueIsFull(t *testing.T) {
	release := make(chan bool)
	logger := &EventLogger{
		minLevel: slog.LevelDebug,
		dedup:    expirable.NewLRU[string, struct{}](dedupCacheSize, nil, dedupTTL),
		outputs:  []eventSink{blockingEventSink{release: release}},
		rawHash:  false,
		enabled:  true,
		mu:       sync.Mutex{},
		queue:    nil,
		limit:    1,
		log:      slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
	}
	logger.cond = sync.NewCond(&logger.mu)
	logger.wg.Add(1)
	go func() {
		defer logger.wg.Done()
		logger.worker()
	}()

	attrs := Attrs{
		"system":     NewStringValue("codex"),
		"session_id": NewStringValue("session-1"),
		"event":      NewStringValue("PreToolUse"),
	}
	logger.Log("codex", "session-1", "PreToolUse", "info", "hook.received", attrs)
	waitForWorkerToBlock(t, logger)
	logger.Log("codex", "session-1", "PreToolUse", "info", "hook.allowed", attrs)
	logger.Log("codex", "session-1", "PreToolUse", "info", "hook.raw_payload", attrs)

	logger.mu.Lock()
	dropped := logger.dropped
	logger.mu.Unlock()
	if dropped == 0 {
		t.Fatal("dropped = 0, want at least one bounded-queue drop")
	}

	close(release)
	if err := logger.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func waitForWorkerToBlock(t *testing.T, logger *EventLogger) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		logger.mu.Lock()
		queueDepth := len(logger.queue)
		logger.mu.Unlock()
		if queueDepth == 0 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("audit worker did not consume initial queue item")
}
