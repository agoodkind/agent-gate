package hook

import (
	"encoding/json"
	"testing"
)

// TestToolInputAcceptsBothShapes is the regression for AGATE-1. Cursor sends
// tool_input as a JSON string. The decoder only accepted an object, so the whole
// payload was rejected with "cannot unmarshal string into Go struct field" and
// the tool call was refused with a parse error rather than evaluated.
//
// Reproduced against the installed binary on 2026-08-01: the object shape
// blocked a guarded command, and the identical command in the string shape
// failed to parse.
func TestToolInputAcceptsBothShapes(t *testing.T) {
	const command = "grep -rn 'func ' /repo/internal/"

	object := []byte(`{"command":"` + command + `","file_path":"/repo/a.go"}`)
	encoded, err := json.Marshal(string(object))
	if err != nil {
		t.Fatalf("marshal the string form: %v", err)
	}

	var fromObject, fromString ClaudeToolInput
	if err := json.Unmarshal(object, &fromObject); err != nil {
		t.Fatalf("object shape: %v", err)
	}
	if err := json.Unmarshal(encoded, &fromString); err != nil {
		t.Fatalf("string shape: %v", err)
	}

	if fromObject.Command != command {
		t.Fatalf("object Command = %q, want %q", fromObject.Command, command)
	}
	if fromString.Command != fromObject.Command {
		t.Fatalf("string Command = %q, want the same as the object form %q",
			fromString.Command, fromObject.Command)
	}
	if fromString.FilePath != fromObject.FilePath {
		t.Fatalf("string FilePath = %q, want %q", fromString.FilePath, fromObject.FilePath)
	}
}

// TestToolInputShapeEdgeCases keeps a malformed or absent tool_input from
// rejecting the call. A payload shape the decoder does not model must not
// decide policy; the rules simply see empty fields.
func TestToolInputShapeEdgeCases(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{"null", `null`},
		{"empty object", `{}`},
		{"empty string", `""`},
		{"string holding null", `"null"`},
		{"string that is not JSON", `"just some text"`},
		{"string holding an empty object", `"{}"`},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			var input ClaudeToolInput
			if err := json.Unmarshal([]byte(testCase.raw), &input); err != nil {
				t.Fatalf("unmarshal %s rejected the payload: %v", testCase.raw, err)
			}
			if input.Command != "" {
				t.Fatalf("Command = %q, want empty", input.Command)
			}
		})
	}
}

// TestToolInputStillRejectsBrokenJSON keeps the decoder honest: a string that
// opens an object and never closes it is corrupt, not a shape variant.
func TestToolInputStillRejectsBrokenJSON(t *testing.T) {
	var input ClaudeToolInput
	if err := json.Unmarshal([]byte(`"{\"command\": "`), &input); err == nil {
		t.Fatal("a truncated tool_input object was accepted")
	}
}
