// Package audit writes per-conversation, per-event JSONL audit entries.
//
// Each hook event is written to a path of the form
//
//	<state>/conversations/<system>/<session_id>/<event_name>.jsonl
//
// The Sink interface abstracts the destination. The daemon owns a
// SessionLogger-backed LocalSink. Hook CLI processes use a DaemonSink that
// forwards entries to the daemon over gRPC.
package audit

import (
	"context"
)

// Sink is the audit destination interface. Implementations route entries
// to per-conversation JSONL files keyed by (system, sessionID, eventName).
type Sink interface {
	// Log records one audit entry. The call must not block on disk I/O.
	// Implementations may drop entries under sustained pressure.
	Log(ctx context.Context, system, sessionID, eventName, level, msg string, attrs map[string]any)

	// Close flushes pending writes and releases resources.
	Close() error
}

// LocalSink is a Sink backed by a local SessionLogger. Used by the daemon.
type LocalSink struct {
	logger *SessionLogger
}

// NewLocalSink wraps a SessionLogger as a Sink.
func NewLocalSink(logger *SessionLogger) *LocalSink {
	return &LocalSink{logger: logger}
}

// Log forwards to the underlying SessionLogger.
func (s *LocalSink) Log(_ context.Context, system, sessionID, eventName, level, msg string, attrs map[string]any) {
	if s == nil || s.logger == nil {
		return
	}
	s.logger.Log(system, sessionID, eventName, level, msg, attrs)
}

// Close closes the underlying SessionLogger.
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
