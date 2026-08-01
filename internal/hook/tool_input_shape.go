package hook

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// claudeToolInputAlias drops the custom unmarshaller so the object branch below
// can decode normally without recursing into it.
type claudeToolInputAlias ClaudeToolInput

// UnmarshalJSON accepts tool_input as either an object or a JSON string
// containing that object.
//
// Cursor sends the string form. Without this the decode failed with "cannot
// unmarshal string into Go struct field", the whole payload was rejected, and
// the tool call was refused with a parse error instead of being evaluated. Both
// shapes describe the same call, so both decode to the same value.
func (input *ClaudeToolInput) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil
	}

	if trimmed[0] == '"' {
		var encoded string
		if err := json.Unmarshal(trimmed, &encoded); err != nil {
			return fmt.Errorf("decode tool_input string: %w", err)
		}
		inner := bytes.TrimSpace([]byte(encoded))
		if len(inner) == 0 || bytes.Equal(inner, []byte("null")) {
			return nil
		}
		// A tool_input string that is not itself JSON carries no fields to read.
		// Accept it rather than rejecting the call, because a payload shape this
		// decoder does not model must not decide policy: rules simply see empty
		// fields, exactly as they would for a tool with no inputs.
		if inner[0] != '{' {
			return nil
		}
		var alias claudeToolInputAlias
		if err := json.Unmarshal(inner, &alias); err != nil {
			return fmt.Errorf("decode tool_input string body: %w", err)
		}
		*input = ClaudeToolInput(alias)
		return nil
	}

	var alias claudeToolInputAlias
	if err := json.Unmarshal(trimmed, &alias); err != nil {
		return fmt.Errorf("decode tool_input object: %w", err)
	}
	*input = ClaudeToolInput(alias)
	return nil
}
