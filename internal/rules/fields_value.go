package rules

import (
	"strconv"

	"goodkind.io/agent-gate/internal/config"
)

// String returns the string view of fields selected by the given
// [config.FieldSelector]. Unknown selectors yield the empty string.
func (fields FieldSet) String(selector config.FieldSelector) string {
	value, _ := fields.Value(selector)
	return value
}

// Value returns the string view of a selector and whether the source payload
// supplied it. String fields retain their existing non-empty availability
// semantics, while optional typed fields can distinguish zero from missing.
func (fields FieldSet) Value(selector config.FieldSelector) (string, bool) {
	if selector == config.FieldLoopCount {
		if fields.LoopCount == nil {
			return "", false
		}
		return strconv.Itoa(*fields.LoopCount), true
	}
	if accessor, ok := fieldStringAccessors[selector]; ok {
		value := accessor(fields)
		return value, value != ""
	}
	return "", false
}
