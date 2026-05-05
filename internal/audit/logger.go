// Package audit normalizes hook log records into audit events and writes them
// to configured outputs such as JSONL files and SQLite.
package audit

import (
	"context"
	"encoding/json"
)

// AttrValueKind discriminates the variant carried in [AttrValue].
type AttrValueKind int

// AttrValueKind variants.
const (
	// AttrValueKindInvalid is the zero value and represents an unset attribute.
	AttrValueKindInvalid AttrValueKind = iota
	// AttrValueKindString is a string attribute.
	AttrValueKindString
	// AttrValueKindBool is a boolean attribute.
	AttrValueKindBool
	// AttrValueKindInt is a 64-bit signed integer attribute.
	AttrValueKindInt
	// AttrValueKindFloat is a 64-bit floating point attribute.
	AttrValueKindFloat
	// AttrValueKindStringSlice is a slice of strings.
	AttrValueKindStringSlice
	// AttrValueKindViolationSlice is a slice of [Violation] records.
	AttrValueKindViolationSlice
)

// AttrValue is a typed audit attribute value. The Kind field selects which
// of the typed payload fields is meaningful for a given instance.
type AttrValue struct {
	Kind          AttrValueKind
	StringValue   string
	BoolValue     bool
	IntValue      int64
	FloatValue    float64
	StringList    []string
	ViolationList []Violation
}

// NewStringValue returns an [AttrValue] holding the given string.
func NewStringValue(value string) AttrValue {
	return AttrValue{
		Kind:          AttrValueKindString,
		StringValue:   value,
		BoolValue:     false,
		IntValue:      0,
		FloatValue:    0,
		StringList:    nil,
		ViolationList: nil,
	}
}

// NewBoolValue returns an [AttrValue] holding the given boolean.
func NewBoolValue(value bool) AttrValue {
	return AttrValue{
		Kind:          AttrValueKindBool,
		StringValue:   "",
		BoolValue:     value,
		IntValue:      0,
		FloatValue:    0,
		StringList:    nil,
		ViolationList: nil,
	}
}

// NewIntValue returns an [AttrValue] holding the given 64-bit integer.
func NewIntValue(value int64) AttrValue {
	return AttrValue{
		Kind:          AttrValueKindInt,
		StringValue:   "",
		BoolValue:     false,
		IntValue:      value,
		FloatValue:    0,
		StringList:    nil,
		ViolationList: nil,
	}
}

// NewFloatValue returns an [AttrValue] holding the given float64.
func NewFloatValue(value float64) AttrValue {
	return AttrValue{
		Kind:          AttrValueKindFloat,
		StringValue:   "",
		BoolValue:     false,
		IntValue:      0,
		FloatValue:    value,
		StringList:    nil,
		ViolationList: nil,
	}
}

// NewStringSliceValue returns an [AttrValue] holding a copy of the given slice.
func NewStringSliceValue(value []string) AttrValue {
	return AttrValue{
		Kind:          AttrValueKindStringSlice,
		StringValue:   "",
		BoolValue:     false,
		IntValue:      0,
		FloatValue:    0,
		StringList:    append([]string(nil), value...),
		ViolationList: nil,
	}
}

// NewViolationSliceValue returns an [AttrValue] holding a copy of the
// given violation slice.
func NewViolationSliceValue(value []Violation) AttrValue {
	return AttrValue{
		Kind:          AttrValueKindViolationSlice,
		StringValue:   "",
		BoolValue:     false,
		IntValue:      0,
		FloatValue:    0,
		StringList:    nil,
		ViolationList: append([]Violation(nil), value...),
	}
}

// ValueKind returns the discriminator of v.
func (v AttrValue) ValueKind() AttrValueKind {
	return v.Kind
}

// String returns the string payload when Kind is [AttrValueKindString],
// otherwise the empty string.
func (v AttrValue) String() string {
	if v.Kind != AttrValueKindString {
		return ""
	}
	return v.StringValue
}

// Strings returns a copy of the string slice payload when Kind is
// [AttrValueKindStringSlice], otherwise nil.
func (v AttrValue) Strings() []string {
	if v.Kind != AttrValueKindStringSlice {
		return nil
	}
	return append([]string(nil), v.StringList...)
}

// JSONBytes returns a JSON encoding of the active payload. Encoding errors
// degrade to a JSON null literal so the audit pipeline never produces
// malformed records.
func (v AttrValue) JSONBytes() []byte {
	const nullLiteral = "null"
	switch v.Kind {
	case AttrValueKindInvalid:
		return []byte(nullLiteral)
	case AttrValueKindString:
		bytes, err := json.Marshal(v.StringValue)
		if err != nil {
			return []byte(nullLiteral)
		}
		return bytes
	case AttrValueKindBool:
		bytes, err := json.Marshal(v.BoolValue)
		if err != nil {
			return []byte(nullLiteral)
		}
		return bytes
	case AttrValueKindInt:
		bytes, err := json.Marshal(v.IntValue)
		if err != nil {
			return []byte(nullLiteral)
		}
		return bytes
	case AttrValueKindFloat:
		// json.Marshal rejects NaN/Inf; map those to a JSON null literal.
		f := v.FloatValue
		if f != f || f > 1e308 || f < -1e308 {
			return []byte(nullLiteral)
		}
		bytes, err := json.Marshal(f)
		if err != nil {
			return []byte(nullLiteral)
		}
		return bytes
	case AttrValueKindStringSlice:
		bytes, err := json.Marshal(v.StringList)
		if err != nil {
			return []byte(nullLiteral)
		}
		return bytes
	case AttrValueKindViolationSlice:
		bytes, err := json.Marshal(v.ViolationList)
		if err != nil {
			return []byte(nullLiteral)
		}
		return bytes
	default:
		return []byte(nullLiteral)
	}
}

// Attrs stores normalized audit attributes used for event normalization
// and deduplication.
type Attrs map[string]AttrValue

// Sink is the audit destination interface.
type Sink interface {
	// Log records one audit entry. The call must not block on disk I/O.
	// Implementations may drop entries under sustained pressure.
	Log(ctx context.Context, system, sessionID, eventName, level, msg string, attrs Attrs)

	// Close flushes pending writes and releases resources.
	Close() error
}

// LocalSink is a [Sink] backed by a local [EventLogger]. Used by the daemon.
type LocalSink struct {
	logger *EventLogger
}

// NewLocalSink wraps an [EventLogger] as a [Sink].
func NewLocalSink(logger *EventLogger) *LocalSink {
	return &LocalSink{logger: logger}
}

// Log forwards to the underlying [EventLogger].
func (s *LocalSink) Log(_ context.Context, system, sessionID, eventName, level, msg string, attrs Attrs) {
	if s == nil || s.logger == nil {
		return
	}
	s.logger.Log(system, sessionID, eventName, level, msg, attrs)
}

// Close closes the underlying [EventLogger].
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
