package hook

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// NullableString is a string that distinguishes JSON null from empty string.
type NullableString struct {
	Value string
	Valid bool
}

// UnmarshalJSON parses a JSON string or null into the receiver.
func (s *NullableString) UnmarshalJSON(data []byte) error {
	text := strings.TrimSpace(string(data))
	if text == "null" {
		s.Value = ""
		s.Valid = false
		return nil
	}
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return fmt.Errorf("decode nullable string: %w", err)
	}
	s.Value = value
	s.Valid = true
	return nil
}

// String returns the wrapped string value, or the empty string when null.
func (s *NullableString) String() string {
	return s.Value
}

// Number is a JSON number decoded into both float and int views.
type Number struct {
	Float float64
	Int   int
	Valid bool
}

// UnmarshalJSON parses a JSON number into the receiver.
func (n *Number) UnmarshalJSON(data []byte) error {
	var value float64
	if err := json.Unmarshal(data, &value); err != nil {
		return fmt.Errorf("decode number: %w", err)
	}
	n.Float = value
	n.Int = int(value)
	n.Valid = true
	return nil
}

// EditRange is the source range covered by an edit operation.
type EditRange struct {
	StartLine   int `json:"start_line"`
	StartColumn int `json:"start_column"`
	EndLine     int `json:"end_line"`
	EndColumn   int `json:"end_column"`
}

// Edit is a single text replacement carried by file-edit hook payloads.
type Edit struct {
	OldString string    `json:"old_string"`
	NewString string    `json:"new_string"`
	Range     EditRange `json:"range"`
	OldLine   string    `json:"old_line"`
	NewLine   string    `json:"new_line"`
}

// Attachment is a single user-attached file referenced by a payload.
type Attachment struct {
	Type     string `json:"type"`
	FilePath string `json:"file_path"`
}

// Replacement is a snake_case/camelCase tolerant replacement record carried
// by some hook payloads (notably VS Code).
type Replacement struct {
	FilePath       string `json:"file_path"`
	FilePathCamel  string `json:"filePath"`
	OldString      string `json:"old_string"`
	OldStringCamel string `json:"oldString"`
	NewString      string `json:"new_string"`
	NewStringCamel string `json:"newString"`
}

// NormalizedFilePath returns the file path, preferring snake_case.
func (r Replacement) NormalizedFilePath() string {
	return firstNonEmpty(r.FilePath, r.FilePathCamel)
}

// NormalizedOldString returns the old string, preferring snake_case.
func (r Replacement) NormalizedOldString() string {
	return firstNonEmpty(r.OldString, r.OldStringCamel)
}

// NormalizedNewString returns the new string, preferring snake_case.
func (r Replacement) NormalizedNewString() string {
	return firstNonEmpty(r.NewString, r.NewStringCamel)
}

// ToolProperties is a bag of tool-scoped identifiers used by some payloads.
type ToolProperties struct {
	WorkspaceSlug     string `json:"workspace_slug"`
	ProjectIdentifier string `json:"project_identifier"`
	IssueIdentifier   string `json:"issue_identifier"`
	NodeID            string `json:"node_id"`
	NodeType          string `json:"node_type"`
	State             string `json:"state"`
}

// ToolFilters is a query filter envelope used by some payloads.
type ToolFilters struct {
	Oldest string `json:"oldest"`
	Latest string `json:"latest"`
	Limit  int    `json:"limit"`
}

// ClaudeToolInput is the union of Claude tool input fields seen in the wild.
type ClaudeToolInput struct {
	Command           string         `json:"command"`
	FilePath          string         `json:"file_path"`
	FilePathCamel     string         `json:"filePath"`
	Content           string         `json:"content"`
	OldString         string         `json:"old_string"`
	OldStringCamel    string         `json:"oldString"`
	NewString         string         `json:"new_string"`
	NewStringCamel    string         `json:"newString"`
	Description       string         `json:"description"`
	Prompt            string         `json:"prompt"`
	URL               string         `json:"url"`
	Query             string         `json:"query"`
	Pattern           string         `json:"pattern"`
	WorkspaceSlug     string         `json:"workspace_slug"`
	ProjectIdentifier string         `json:"project_identifier"`
	IssueIdentifier   string         `json:"issue_identifier"`
	NodeID            string         `json:"node_id"`
	NodeType          string         `json:"node_type"`
	State             string         `json:"state"`
	Explanation       string         `json:"explanation"`
	Goal              string         `json:"goal"`
	Mode              string         `json:"mode"`
	Name              string         `json:"name"`
	Path              string         `json:"path"`
	FileText          string         `json:"file_text"`
	StartLine         int            `json:"startLine"`
	EndLine           int            `json:"endLine"`
	Timeout           int            `json:"timeout"`
	MaxResults        int            `json:"maxResults"`
	IsRegexp          bool           `json:"isRegexp"`
	ReplaceAll        bool           `json:"replace_all"`
	WaitForOutput     bool           `json:"waitForOutput"`
	URLs              []string       `json:"urls"`
	IncludePattern    string         `json:"includePattern"`
	Workdir           string         `json:"workdir"`
	WorkingDirectory  string         `json:"working_directory"`
	Directory         string         `json:"directory"`
	Properties        ToolProperties `json:"properties"`
	Filters           ToolFilters    `json:"filters"`
	Replacements      []Replacement  `json:"replacements"`
}

// NormalizedFilePath returns the file path, preferring snake_case.
func (input ClaudeToolInput) NormalizedFilePath() string {
	return firstNonEmpty(input.FilePath, input.FilePathCamel)
}

// NormalizedOldString returns the old string, preferring snake_case.
func (input ClaudeToolInput) NormalizedOldString() string {
	return firstNonEmpty(input.OldString, input.OldStringCamel)
}

// NormalizedNewString returns the new string, preferring snake_case.
func (input ClaudeToolInput) NormalizedNewString() string {
	return firstNonEmpty(input.NewString, input.NewStringCamel)
}

// CursorToolInput is the union of Cursor IDE tool input fields.
type CursorToolInput struct {
	FilePath          string         `json:"file_path"`
	Content           string         `json:"content"`
	Command           string         `json:"command"`
	CWD               string         `json:"cwd"`
	Timeout           int            `json:"timeout"`
	Pattern           string         `json:"pattern"`
	OutputMode        string         `json:"output_mode"`
	Glob              string         `json:"glob"`
	URL               string         `json:"url"`
	Query             string         `json:"query"`
	SearchTerm        string         `json:"search_term"`
	Explanation       string         `json:"explanation"`
	Description       string         `json:"description"`
	Prompt            string         `json:"prompt"`
	Model             string         `json:"model"`
	Resume            string         `json:"resume"`
	Interrupt         bool           `json:"interrupt"`
	SubagentType      string         `json:"subagent_type"`
	Readonly          bool           `json:"readonly"`
	RunInBackground   bool           `json:"run_in_background"`
	FileAttachments   []string       `json:"file_attachments"`
	WorkspaceSlug     string         `json:"workspace_slug"`
	ProjectIdentifier string         `json:"project_identifier"`
	IssueIdentifier   string         `json:"issue_identifier"`
	NodeID            string         `json:"node_id"`
	NodeType          string         `json:"node_type"`
	State             string         `json:"state"`
	Workdir           string         `json:"workdir"`
	WorkingDirectory  string         `json:"working_directory"`
	Directory         string         `json:"directory"`
	Properties        ToolProperties `json:"properties"`
	Filters           ToolFilters    `json:"filters"`
	Name              string         `json:"name"`
	Server            string         `json:"server"`
	URI               string         `json:"uri"`
	DownloadPath      string         `json:"download_path"`
	Oldest            string         `json:"oldest"`
	Latest            string         `json:"latest"`
	Action            string         `json:"action"`
	Limit             int            `json:"limit"`
}

// UnmarshalJSON accepts both the normal object-shaped Cursor tool_input and
// the one-off string-shaped form Cursor can emit around MCP tool execution.
//
// This is deliberately broader than the struct tags imply. Cursor's
// beforeMCPExecution hook has been observed blocking before rule evaluation
// with:
//
//	json: cannot unmarshal string into Go struct field
//	CursorBeforeMCPExecutionPayload.tool_input of type hook.CursorToolInput
//
// That failure happens in the hook boundary, so agent-gate cannot apply rules
// or return the normal allow/deny response. Treating string tool_input values
// as a compatibility input keeps the hook transport alive. If the string is a
// JSON object, we decode it into the same typed fields used for normal Cursor
// payloads. If it is plain text, or if the JSON-object-looking string is
// malformed, we preserve the original value as tool_input.content so existing
// generic content rules can still inspect it without adding a new selector.
func (input *CursorToolInput) UnmarshalJSON(data []byte) error {
	text := strings.TrimSpace(string(data))
	if text == "null" {
		var empty CursorToolInput
		*input = empty
		return nil
	}

	type cursorToolInput CursorToolInput
	var decoded cursorToolInput
	if err := json.Unmarshal(data, &decoded); err == nil {
		*input = CursorToolInput(decoded)
		return nil
	}

	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return fmt.Errorf("decode cursor tool input: %w", err)
	}

	objectText := strings.TrimSpace(value)
	if strings.HasPrefix(objectText, "{") && strings.HasSuffix(objectText, "}") {
		var nested cursorToolInput
		if err := json.Unmarshal([]byte(objectText), &nested); err == nil {
			*input = CursorToolInput(nested)
			return nil
		}
	}

	var fallback CursorToolInput
	fallback.Content = value
	*input = fallback
	return nil
}

// GeminiToolInput is the union of Gemini tool input fields.
type GeminiToolInput struct {
	FilePath    string `json:"file_path"`
	Content     string `json:"content"`
	Command     string `json:"command"`
	OldString   string `json:"old_string"`
	NewString   string `json:"new_string"`
	Pattern     string `json:"pattern"`
	Path        string `json:"path"`
	URL         string `json:"url"`
	Query       string `json:"query"`
	Description string `json:"description"`
	Workdir     string `json:"workdir"`
	Directory   string `json:"directory"`
}

// CodexToolInput is the union of Codex tool input fields.
type CodexToolInput struct {
	Command     string `json:"command"`
	FilePath    string `json:"file_path"`
	Content     string `json:"content"`
	OldString   string `json:"old_string"`
	NewString   string `json:"new_string"`
	Description string `json:"description"`
	Prompt      string `json:"prompt"`
	URL         string `json:"url"`
	Query       string `json:"query"`
	Pattern     string `json:"pattern"`
	Path        string `json:"path"`
	Workdir     string `json:"workdir"`
	Directory   string `json:"directory"`
}

// VSCodeToolInput is the union of VS Code tool input fields.
type VSCodeToolInput struct {
	FilePath     string        `json:"filePath"`
	OldString    string        `json:"oldString"`
	NewString    string        `json:"newString"`
	Replacements []Replacement `json:"replacements"`
	Command      string        `json:"command"`
	Content      string        `json:"content"`
	Prompt       string        `json:"prompt"`
}

// NormalizedOldStrings returns all observed old-string values from the
// payload, with the top-level OldString first when set.
func (input VSCodeToolInput) NormalizedOldStrings() []string {
	values := stringsFromReplacements(input.Replacements, func(replacement Replacement) string {
		return replacement.NormalizedOldString()
	})
	if input.OldString != "" {
		values = append([]string{input.OldString}, values...)
	}
	return values
}

// NormalizedNewStrings returns all observed new-string values from the
// payload, with the top-level NewString first when set.
func (input VSCodeToolInput) NormalizedNewStrings() []string {
	values := stringsFromReplacements(input.Replacements, func(replacement Replacement) string {
		return replacement.NormalizedNewString()
	})
	if input.NewString != "" {
		values = append([]string{input.NewString}, values...)
	}
	return values
}

func stringsFromReplacements(replacements []Replacement, extract func(Replacement) string) []string {
	values := make([]string, 0, len(replacements))
	for _, replacement := range replacements {
		if value := extract(replacement); value != "" {
			values = append(values, value)
		}
	}
	return values
}

// TextOrObject is a JSON value that may decode as a string or as an object.
// The original raw bytes are preserved for object payloads.
type TextOrObject struct {
	Text string
	JSON string
}

type (
	searchableJSONArray  []json.RawMessage
	searchableJSONObject map[string]json.RawMessage
)

// UnmarshalJSON parses a JSON string into Text or stores raw object bytes
// in JSON.
func (value *TextOrObject) UnmarshalJSON(data []byte) error {
	if len(data) == 0 || string(data) == "null" {
		return nil
	}
	if data[0] == '"' {
		if err := json.Unmarshal(data, &value.Text); err != nil {
			return fmt.Errorf("decode text-or-object string: %w", err)
		}
		return nil
	}
	value.JSON = string(data)
	return nil
}

// String returns the textual view of the value, preferring Text over JSON.
func (value *TextOrObject) String() string {
	return firstNonEmpty(value.Text, value.JSON)
}

// SearchableText returns the semantic text view used by rule matching.
func (value *TextOrObject) SearchableText() string {
	if value.Text != "" {
		return value.Text
	}
	if value.JSON == "" {
		return ""
	}

	rawJSON := json.RawMessage(value.JSON)
	if !json.Valid(rawJSON) {
		return value.JSON
	}

	var builder strings.Builder
	appendSearchableJSONText(&builder, "", rawJSON)
	return strings.TrimRight(builder.String(), "\n")
}

func appendSearchableJSONText(builder *strings.Builder, key string, rawJSON json.RawMessage) {
	trimmed := bytes.TrimSpace(rawJSON)
	if len(trimmed) == 0 {
		return
	}

	switch trimmed[0] {
	case '{':
		var values searchableJSONObject
		if err := json.Unmarshal(trimmed, &values); err != nil {
			appendSearchableScalar(builder, key, string(trimmed))
			return
		}
		appendSearchableJSONMap(builder, values)
	case '[':
		var values searchableJSONArray
		if err := json.Unmarshal(trimmed, &values); err != nil {
			appendSearchableScalar(builder, key, string(trimmed))
			return
		}
		for _, item := range values {
			appendSearchableJSONText(builder, "", item)
		}
	case '"':
		var value string
		if err := json.Unmarshal(trimmed, &value); err != nil {
			appendSearchableScalar(builder, key, string(trimmed))
			return
		}
		appendSearchableScalar(builder, key, value)
	case 't', 'f':
		var value bool
		if err := json.Unmarshal(trimmed, &value); err != nil {
			appendSearchableScalar(builder, key, string(trimmed))
			return
		}
		appendSearchableScalar(builder, key, strconv.FormatBool(value))
	case 'n':
		return
	default:
		appendSearchableScalar(builder, key, string(trimmed))
	}
}

func appendSearchableJSONMap(builder *strings.Builder, values searchableJSONObject) {
	for key, value := range values {
		if skipsSearchableJSONField(values, key) {
			continue
		}
		appendSearchableJSONText(builder, key, value)
	}
}

func skipsSearchableJSONField(values searchableJSONObject, key string) bool {
	if key != "data" {
		return false
	}
	return isImagePayload(values)
}

func isImagePayload(values searchableJSONObject) bool {
	if searchableJSONString(values["type"]) == "image" {
		return true
	}
	if strings.HasPrefix(searchableJSONString(values["mimeType"]), "image/") {
		return true
	}
	if strings.HasPrefix(searchableJSONString(values["mime_type"]), "image/") {
		return true
	}
	return false
}

func searchableJSONString(rawJSON json.RawMessage) string {
	if len(rawJSON) == 0 {
		return ""
	}
	var value string
	if err := json.Unmarshal(rawJSON, &value); err != nil {
		return ""
	}
	return value
}

func appendSearchableScalar(builder *strings.Builder, key string, value string) {
	if value == "" {
		return
	}
	if key != "" {
		builder.WriteString(key)
		builder.WriteString(": ")
	}
	builder.WriteString(value)
	builder.WriteByte('\n')
}

// PermissionSuggestion is a single Claude-side permission suggestion.
type PermissionSuggestion struct {
	Behavior string `json:"behavior"`
	Message  string `json:"message"`
}

// LLMRequest is a free-form LLM request body wrapper.
type LLMRequest struct {
	Text string `json:"text"`
}

// LLMResponse is a free-form LLM response body wrapper.
type LLMResponse struct {
	Text string `json:"text"`
}
