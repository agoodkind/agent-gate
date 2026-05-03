package hook

import (
	"encoding/json"
	"strings"
)

type NullableString struct {
	Value string
	Valid bool
}

func (s *NullableString) UnmarshalJSON(data []byte) error {
	text := strings.TrimSpace(string(data))
	if text == "null" {
		s.Value = ""
		s.Valid = false
		return nil
	}
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	s.Value = value
	s.Valid = true
	return nil
}

func (s NullableString) String() string {
	return s.Value
}

type Number struct {
	Float float64
	Int   int
	Valid bool
}

func (n *Number) UnmarshalJSON(data []byte) error {
	var value float64
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	n.Float = value
	n.Int = int(value)
	n.Valid = true
	return nil
}

type EditRange struct {
	StartLine   int `json:"start_line"`
	StartColumn int `json:"start_column"`
	EndLine     int `json:"end_line"`
	EndColumn   int `json:"end_column"`
}

type Edit struct {
	OldString string    `json:"old_string"`
	NewString string    `json:"new_string"`
	Range     EditRange `json:"range"`
	OldLine   string    `json:"old_line"`
	NewLine   string    `json:"new_line"`
}

type Attachment struct {
	Type     string `json:"type"`
	FilePath string `json:"file_path"`
}

type Replacement struct {
	FilePath       string `json:"file_path"`
	FilePathCamel  string `json:"filePath"`
	OldString      string `json:"old_string"`
	OldStringCamel string `json:"oldString"`
	NewString      string `json:"new_string"`
	NewStringCamel string `json:"newString"`
}

func (r Replacement) NormalizedFilePath() string {
	return firstNonEmpty(r.FilePath, r.FilePathCamel)
}

func (r Replacement) NormalizedOldString() string {
	return firstNonEmpty(r.OldString, r.OldStringCamel)
}

func (r Replacement) NormalizedNewString() string {
	return firstNonEmpty(r.NewString, r.NewStringCamel)
}

type ToolProperties struct {
	WorkspaceSlug     string `json:"workspace_slug"`
	ProjectIdentifier string `json:"project_identifier"`
	IssueIdentifier   string `json:"issue_identifier"`
	NodeID            string `json:"node_id"`
	NodeType          string `json:"node_type"`
	State             string `json:"state"`
}

type ToolFilters struct {
	Oldest string `json:"oldest"`
	Latest string `json:"latest"`
	Limit  int    `json:"limit"`
}

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

func (input ClaudeToolInput) NormalizedFilePath() string {
	return firstNonEmpty(input.FilePath, input.FilePathCamel)
}

func (input ClaudeToolInput) NormalizedOldString() string {
	return firstNonEmpty(input.OldString, input.OldStringCamel)
}

func (input ClaudeToolInput) NormalizedNewString() string {
	return firstNonEmpty(input.NewString, input.NewStringCamel)
}

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

type VSCodeToolInput struct {
	FilePath     string        `json:"filePath"`
	OldString    string        `json:"oldString"`
	NewString    string        `json:"newString"`
	Replacements []Replacement `json:"replacements"`
	Command      string        `json:"command"`
	Content      string        `json:"content"`
	Prompt       string        `json:"prompt"`
}

func (input VSCodeToolInput) NormalizedOldStrings() []string {
	values := stringsFromReplacements(input.Replacements, func(replacement Replacement) string {
		return replacement.NormalizedOldString()
	})
	if input.OldString != "" {
		values = append([]string{input.OldString}, values...)
	}
	return values
}

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

type TextOrObject struct {
	Text string
	JSON string
}

func (value *TextOrObject) UnmarshalJSON(data []byte) error {
	if len(data) == 0 || string(data) == "null" {
		return nil
	}
	if data[0] == '"' {
		return json.Unmarshal(data, &value.Text)
	}
	value.JSON = string(data)
	return nil
}

func (value TextOrObject) String() string {
	return firstNonEmpty(value.Text, value.JSON)
}

type PermissionSuggestion struct {
	Behavior string `json:"behavior"`
	Message  string `json:"message"`
}

type LLMRequest struct {
	Text string `json:"text"`
}

type LLMResponse struct {
	Text string `json:"text"`
}
