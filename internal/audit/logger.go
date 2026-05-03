// Package audit normalizes hook log records into audit events and writes them
// to configured outputs such as JSONL files and SQLite.
package audit

import (
	"context"
	"encoding/json"
)

type AttrValueKind int

const (
	AttrValueKindInvalid AttrValueKind = iota
	AttrValueKindString
	AttrValueKindBool
	AttrValueKindInt
	AttrValueKindFloat
	AttrValueKindStringSlice
	AttrValueKindViolationSlice
)

type AttrValue struct {
	Kind          AttrValueKind
	StringValue   string
	BoolValue     bool
	IntValue      int64
	FloatValue    float64
	StringList    []string
	ViolationList []Violation
}

func NewStringValue(value string) AttrValue {
	return AttrValue{Kind: AttrValueKindString, StringValue: value}
}

func NewBoolValue(value bool) AttrValue {
	return AttrValue{Kind: AttrValueKindBool, BoolValue: value}
}

func NewIntValue(value int64) AttrValue {
	return AttrValue{Kind: AttrValueKindInt, IntValue: value}
}

func NewFloatValue(value float64) AttrValue {
	return AttrValue{Kind: AttrValueKindFloat, FloatValue: value}
}

func NewStringSliceValue(value []string) AttrValue {
	return AttrValue{Kind: AttrValueKindStringSlice, StringList: append([]string(nil), value...)}
}

func NewViolationSliceValue(value []Violation) AttrValue {
	return AttrValue{Kind: AttrValueKindViolationSlice, ViolationList: append([]Violation(nil), value...)}
}

func (v AttrValue) ValueKind() AttrValueKind {
	return v.Kind
}

func (v AttrValue) String() string {
	if v.Kind != AttrValueKindString {
		return ""
	}
	return v.StringValue
}

func (v AttrValue) Strings() []string {
	if v.Kind != AttrValueKindStringSlice {
		return nil
	}
	return append([]string(nil), v.StringList...)
}

func (v AttrValue) JSONBytes() []byte {
	var bytes []byte
	switch v.Kind {
	case AttrValueKindString:
		bytes, _ = json.Marshal(v.StringValue)
	case AttrValueKindBool:
		bytes, _ = json.Marshal(v.BoolValue)
	case AttrValueKindInt:
		bytes, _ = json.Marshal(v.IntValue)
	case AttrValueKindFloat:
		bytes, _ = json.Marshal(v.FloatValue)
	case AttrValueKindStringSlice:
		bytes, _ = json.Marshal(v.StringList)
	case AttrValueKindViolationSlice:
		bytes, _ = json.Marshal(v.ViolationList)
	default:
		bytes = []byte("null")
	}
	return bytes
}

// Attrs stores normalized audit attributes used for event normalization and deduplication.
type Attrs map[string]AttrValue

// Sink is the audit destination interface.
type Sink interface {
	// Log records one audit entry. The call must not block on disk I/O.
	// Implementations may drop entries under sustained pressure.
	Log(ctx context.Context, system, sessionID, eventName, level, msg string, attrs Attrs)

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
func (s *LocalSink) Log(_ context.Context, system, sessionID, eventName, level, msg string, attrs Attrs) {
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
func (DiscardSink) Log(context.Context, string, string, string, string, string, Attrs) {}

// Close is a no-op.
func (DiscardSink) Close() error { return nil }
