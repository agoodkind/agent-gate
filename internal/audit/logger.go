// Package audit normalizes hook log records into audit events and writes them
// to configured outputs such as JSONL files and SQLite.
package audit

import (
	"context"
)

// Sink is the audit destination interface.
type Sink interface {
	// Log records one audit entry. The call must not block on disk I/O.
	// Implementations may drop entries under sustained pressure.
	Log(ctx context.Context, system, sessionID, eventName, level, msg string, attrs map[string]any)

	// Close flushes pending writes and releases resources.
	Close() error
}

// LocalSink is a Sink backed by a local EventLogger. Used by the daemon.
type LocalSink struct {
	logger *EventLogger
}

// NewLocalSink wraps an EventLogger as a Sink.
func NewLocalSink(logger *EventLogger) *LocalSink {
	return &LocalSink{logger: logger}
}

// Log forwards to the underlying EventLogger.
func (s *LocalSink) Log(_ context.Context, system, sessionID, eventName, level, msg string, attrs map[string]any) {
	if s == nil || s.logger == nil {
		return
	}
	s.logger.Log(system, sessionID, eventName, level, msg, attrs)
}

// Close closes the underlying EventLogger.
func (s *LocalSink) Close() error {
	if s == nil || s.logger == nil {
		return nil
	}
	return s.logger.Close()
}

// DiscardSink drops all entries. Useful as a safe fallback when no sink is
// configured.
type DiscardSink struct{}

// Log is a no-op.
func (DiscardSink) Log(context.Context, string, string, string, string, string, map[string]any) {
}

// Close is a no-op.
func (DiscardSink) Close() error { return nil }
