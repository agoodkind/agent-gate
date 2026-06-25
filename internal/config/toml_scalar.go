package config

import (
	"fmt"
	"strconv"
)

// TOMLScalarKind tags which scalar type a TOML value decoded to.
type TOMLScalarKind string

// TOML scalar kinds used by stdout_json_equals.
const (
	TOMLScalarUnset  TOMLScalarKind = ""
	TOMLScalarBool   TOMLScalarKind = "bool"
	TOMLScalarString TOMLScalarKind = "string"
	TOMLScalarInt    TOMLScalarKind = "int"
	TOMLScalarFloat  TOMLScalarKind = "float"
)

// TOMLScalarValue holds one scalar value decoded from TOML. Exec rules use it
// for JSON output predicates such as stdout_json_equals = true.
type TOMLScalarValue struct {
	kind        TOMLScalarKind
	boolValue   bool
	stringValue string
	intValue    int64
	floatValue  float64
}

// NewBoolScalar constructs a boolean scalar for tests and programmatic callers.
func NewBoolScalar(value bool) TOMLScalarValue {
	var out TOMLScalarValue
	out.kind = TOMLScalarBool
	out.boolValue = value
	return out
}

// NewStringScalar constructs a string scalar for tests and programmatic callers.
func NewStringScalar(value string) TOMLScalarValue {
	var out TOMLScalarValue
	out.kind = TOMLScalarString
	out.stringValue = value
	return out
}

// NewIntScalar constructs an integer scalar for tests and programmatic callers.
func NewIntScalar(value int64) TOMLScalarValue {
	var out TOMLScalarValue
	out.kind = TOMLScalarInt
	out.intValue = value
	return out
}

// NewFloatScalar constructs a float scalar for tests and programmatic callers.
func NewFloatScalar(value float64) TOMLScalarValue {
	var out TOMLScalarValue
	out.kind = TOMLScalarFloat
	out.floatValue = value
	return out
}

// IsSet reports whether the value was explicitly configured.
func (v TOMLScalarValue) IsSet() bool {
	return v.kind != TOMLScalarUnset
}

// Kind reports which scalar kind the value carries.
func (v TOMLScalarValue) Kind() TOMLScalarKind {
	return v.kind
}

// BoolValue returns the boolean payload.
func (v TOMLScalarValue) BoolValue() bool {
	return v.boolValue
}

// StringValue returns the string payload.
func (v TOMLScalarValue) StringValue() string {
	return v.stringValue
}

// IntValue returns the integer payload.
func (v TOMLScalarValue) IntValue() int64 {
	return v.intValue
}

// FloatValue returns the float payload.
func (v TOMLScalarValue) FloatValue() float64 {
	return v.floatValue
}

// CanonicalString returns a stable textual rendering for hashing and diagnostics.
func (v TOMLScalarValue) CanonicalString() string {
	switch v.kind {
	case TOMLScalarUnset:
		return ""
	case TOMLScalarBool:
		if v.boolValue {
			return "true"
		}
		return "false"
	case TOMLScalarString:
		return fmt.Sprintf("%q", v.stringValue)
	case TOMLScalarInt:
		return strconv.FormatInt(v.intValue, 10)
	case TOMLScalarFloat:
		return fmt.Sprintf("%g", v.floatValue)
	default:
		return ""
	}
}
