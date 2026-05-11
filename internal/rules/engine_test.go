package rules_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/hook"
	"goodkind.io/agent-gate/internal/regex"
	"goodkind.io/agent-gate/internal/rules"
)

func testFields(payload map[string]any) rules.FieldSet {
	fields := rules.FieldSet{}
	if value, ok := payload["cwd"].(string); ok {
		fields.CWD = value
	}
	if value, ok := payload["effective_cwd"].(string); ok {
		fields.EffectiveCWD = value
	}
	if value, ok := payload["command"].(string); ok {
		fields.Command = value
	}
	if value, ok := payload["assistant_message"].(string); ok {
		fields.AssistantMessage = value
	}
	if value, ok := payload["last_assistant_message"].(string); ok {
		fields.LastAssistantMessage = value
	}
	if value, ok := payload["text"].(string); ok {
		fields.Text = value
	}
	if value, ok := payload["tool_name"].(string); ok {
		fields.ToolName = value
	}
	if value, ok := payload["file_path"].(string); ok {
		fields.FilePath = value
	}
	if value, ok := payload["path"].(string); ok {
		fields.Path = value
	}
	if value, ok := payload["tool_response"].(string); ok {
		fields.ToolResponse = value
	}
	if value, ok := payload["tool_output"].(string); ok {
		fields.ToolOutput = value
	}
	if value, ok := payload["tool_input"].(map[string]any); ok {
		fields.ToolInputCommand = testString(value, "command")
		fields.ToolInputFilePath = testString(value, "file_path")
		fields.ToolInputContent = testString(value, "content")
		fields.ToolInputNewString = testString(value, "new_string")
		fields.ToolInputOldString = testString(value, "old_string")
		fields.ToolInputPrompt = testString(value, "prompt")
		fields.ToolInputDescription = testString(value, "description")
		fields.ToolInputWorkdir = testString(value, "workdir")
		fields.ToolInputPath = testString(value, "path")
	}
	if edits, ok := payload["edits"].([]any); ok {
		for _, edit := range edits {
			editFields, ok := edit.(map[string]any)
			if !ok {
				continue
			}
			fields.EditsNewString = append(fields.EditsNewString, testString(editFields, "new_string"))
			fields.EditsOldString = append(fields.EditsOldString, testString(editFields, "old_string"))
		}
	}
	return fields
}

func testString(values map[string]any, key string) string {
	value, ok := values[key].(string)
	if !ok {
		return ""
	}
	return value
}

func testDoubleHyphen() string {
	return strings.Repeat("-", 2)
}

// systemFor infers "claude" or "cursor" from a PascalCase/camelCase event name.
func systemFor(event string) string {
	r, _ := utf8.DecodeRuneInString(event)
	if unicode.IsUpper(r) {
		return "claude"
	}
	return "cursor"
}

// loadRule builds a single compiled Rule without touching the filesystem.
func loadRule(t *testing.T, name, pattern string, events, fieldPaths []string, message string) config.Rule {
	t.Helper()
	re, err := regex.Compile(pattern)
	if err != nil {
		t.Fatalf("compile pattern %q: %v", pattern, err)
	}
	return config.NewSimpleRule(name, pattern, re, events, fieldPaths, "block", message)
}

// redirectionRule returns the canonical no-shell-redirection rule used in production.
func redirectionRule(t *testing.T) config.Rule {
	t.Helper()
	return loadRule(t,
		"no-shell-redirection",
		`(\d+>&\d+|>&\d+|&>|\|&|>/dev/null|2>/dev/null|>>/dev/null|2>>/dev/null|&>/dev/null)`,
		[]string{"PreToolUse", "beforeShellExecution"},
		[]string{"tool_input.command", "command"},
		"Shell redirection is not permitted.",
	)
}

func unboundedRootFindRule(t *testing.T) config.Rule {
	t.Helper()
	cmdCond, err := config.NewCondition(
		[]string{"cmd_segments"},
		`(?:^|\s)/+(?:\s|$)`,
		`(?:^|\s)(?:-maxdepth\s+\d+|-xdev)(?:\s|$)`,
	)
	if err != nil {
		t.Fatalf("compile root find command condition: %v", err)
	}
	cmdCond.Kind = string(config.ConditionKindCommand)
	cmdCond.Argv0 = "find"
	cmdCond.StripEnv = true
	cmdCond.StripArgs = []string{"sudo", "doas", "env", "time", "command"}
	return config.Rule{
		Name:             "no-unbounded-root-find",
		Events:           []string{"PreToolUse", "beforeShellExecution", "preToolUse", "BeforeTool"},
		Conditions:       []config.Condition{cmdCond},
		Action:           "block",
		ViolationMessage: "Do not run unbounded recursive find from filesystem root.",
	}
}

func TestEvaluate_UnboundedRootFindBlocked(t *testing.T) {
	rule := unboundedRootFindRule(t)

	cases := []struct {
		name    string
		event   string
		payload map[string]any
	}{
		{
			name:  "plain root find",
			event: "PreToolUse",
			payload: map[string]any{
				"cwd":        "/Users/agoodkind/Sites/agent-gate",
				"tool_input": map[string]any{"command": "find /"},
			},
		},
		{
			name:  "root find with name filter",
			event: "PreToolUse",
			payload: map[string]any{
				"cwd":        "/Users/agoodkind/Sites/agent-gate",
				"tool_input": map[string]any{"command": "find / -name foo"},
			},
		},
		{
			name:  "sudo root find",
			event: "PreToolUse",
			payload: map[string]any{
				"cwd":        "/Users/agoodkind/Sites/agent-gate",
				"tool_input": map[string]any{"command": "sudo find / -type f"},
			},
		},
		{
			name:  "env root find",
			event: "PreToolUse",
			payload: map[string]any{
				"cwd":        "/Users/agoodkind/Sites/agent-gate",
				"tool_input": map[string]any{"command": "env FOO=bar find / -name foo"},
			},
		},
		{
			name:  "cursor shell root find",
			event: "beforeShellExecution",
			payload: map[string]any{
				"cwd":     "/Users/agoodkind/Sites/agent-gate",
				"command": "find / -type f",
			},
		},
		{
			name:  "root find after cd chain",
			event: "PreToolUse",
			payload: map[string]any{
				"cwd":        "/Users/agoodkind",
				"tool_input": map[string]any{"command": "cd /tmp && find / -name foo"},
			},
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			v := rules.Evaluate(context.Background(), systemFor(testCase.event), testCase.event, testFields(testCase.payload), []config.Rule{rule})
			if v == nil {
				t.Fatalf("expected unbounded root find violation for %#v", testCase.payload)
			}
		})
	}
}

func TestEvaluate_RootFindWithExplicitBoundsAllowed(t *testing.T) {
	rule := unboundedRootFindRule(t)

	cases := []struct {
		name    string
		event   string
		payload map[string]any
	}{
		{
			name:  "root find maxdepth one",
			event: "PreToolUse",
			payload: map[string]any{
				"cwd":        "/Users/agoodkind/Sites/agent-gate",
				"tool_input": map[string]any{"command": "find / -maxdepth 1 -name foo"},
			},
		},
		{
			name:  "root find xdev",
			event: "PreToolUse",
			payload: map[string]any{
				"cwd":        "/Users/agoodkind/Sites/agent-gate",
				"tool_input": map[string]any{"command": "find / -xdev -type f"},
			},
		},
		{
			name:  "current directory find",
			event: "PreToolUse",
			payload: map[string]any{
				"cwd":        "/Users/agoodkind/Sites/agent-gate",
				"tool_input": map[string]any{"command": "find . -name foo"},
			},
		},
		{
			name:  "user sites find",
			event: "PreToolUse",
			payload: map[string]any{
				"cwd":        "/Users/agoodkind/Sites/agent-gate",
				"tool_input": map[string]any{"command": "find /Users/agoodkind/Sites -name foo"},
			},
		},
		{
			name:  "non find command with root path",
			event: "PreToolUse",
			payload: map[string]any{
				"cwd":        "/Users/agoodkind/Sites/agent-gate",
				"tool_input": map[string]any{"command": "ls /"},
			},
		},
		{
			name:  "non-matching event",
			event: "PostToolUse",
			payload: map[string]any{
				"cwd":        "/Users/agoodkind/Sites/agent-gate",
				"tool_input": map[string]any{"command": "find /"},
			},
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			v := rules.Evaluate(context.Background(), systemFor(testCase.event), testCase.event, testFields(testCase.payload), []config.Rule{rule})
			if v != nil {
				t.Fatalf("expected allow, got rule %q", v.RuleName)
			}
		})
	}
}

const lossyLogSamplingPattern = `(?im)(?:\b(?:journalctl|docker\s+logs|kubectl\s+logs|log\s+show|agent-gate\s+query|cat\s+\S*(?:\.log|/logs?/)|grep\b|rg\b)[^;&\n|]*\|\s*(?:head|tail)\b|\b(?:head|tail)\b[^;&\n]*(?:\.log\b|/logs?/|journal|daemon|audit|trace|error|incident))`

func lossyLogSamplingRule(t *testing.T) config.Rule {
	t.Helper()
	return loadRule(t,
		"no-lossy-log-sampling",
		lossyLogSamplingPattern,
		[]string{"PreToolUse", "preToolUse", "beforeShellExecution"},
		[]string{"cmd_segments", "tool_input.command", "command"},
		"Do not sample diagnostic output with head or tail.",
	)
}

func TestEvaluate_LossyLogSamplingBlocked(t *testing.T) {
	rule := lossyLogSamplingRule(t)

	cases := []struct {
		name    string
		system  string
		event   string
		payload map[string]any
	}{
		{
			name:   "docker logs piped to tail",
			system: "codex",
			event:  "PreToolUse",
			payload: map[string]any{
				"tool_input": map[string]any{"command": "docker logs api | tail -100"},
			},
		},
		{
			name:   "journalctl piped to head",
			system: "claude",
			event:  "PreToolUse",
			payload: map[string]any{
				"tool_input": map[string]any{"command": "journalctl -u service | head"},
			},
		},
		{
			name:   "agent gate query piped to tail",
			system: "codex",
			event:  "PreToolUse",
			payload: map[string]any{
				"tool_input": map[string]any{"command": "agent-gate query seen --json | tail -20"},
			},
		},
		{
			name:   "rg log search piped to tail",
			system: "codex",
			event:  "PreToolUse",
			payload: map[string]any{
				"tool_input": map[string]any{"command": "rg ERROR app.log | tail"},
			},
		},
		{
			name:   "direct tail of system log",
			system: "cursor",
			event:  "beforeShellExecution",
			payload: map[string]any{
				"command": "tail -50 /var/log/system.log",
			},
		},
		{
			name:   "chained docker logs command",
			system: "codex",
			event:  "PreToolUse",
			payload: map[string]any{
				"tool_input": map[string]any{"command": "echo checking && docker logs api | tail -100"},
			},
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			v := rules.Evaluate(context.Background(), testCase.system, testCase.event, testFields(testCase.payload), []config.Rule{rule})
			if v == nil {
				t.Fatalf("expected lossy log sampling violation for %#v", testCase.payload)
			}
		})
	}
}

func TestEvaluate_LossyLogSamplingAllowed(t *testing.T) {
	rule := lossyLogSamplingRule(t)

	cases := []struct {
		name    string
		system  string
		event   string
		payload map[string]any
	}{
		{
			name:   "explicit sed line range",
			system: "codex",
			event:  "PreToolUse",
			payload: map[string]any{
				"tool_input": map[string]any{"command": "sed -n '1200,1300p' app.log"},
			},
		},
		{
			name:   "rg search with context",
			system: "codex",
			event:  "PreToolUse",
			payload: map[string]any{
				"tool_input": map[string]any{"command": "rg -n 'error' app.log -C 20"},
			},
		},
		{
			name:   "head non log data file",
			system: "codex",
			event:  "PreToolUse",
			payload: map[string]any{
				"tool_input": map[string]any{"command": "head -5 README.md"},
			},
		},
		{
			name:   "tail non log data file",
			system: "cursor",
			event:  "beforeShellExecution",
			payload: map[string]any{
				"command": "tail -n 2 table.csv",
			},
		},
		{
			name:   "head unrelated pipeline",
			system: "codex",
			event:  "PreToolUse",
			payload: map[string]any{
				"tool_input": map[string]any{"command": "printf 'a\\nb\\n' | head -1"},
			},
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			v := rules.Evaluate(context.Background(), testCase.system, testCase.event, testFields(testCase.payload), []config.Rule{rule})
			if v != nil {
				t.Fatalf("expected allow, got rule %q", v.RuleName)
			}
		})
	}
}

const secretReadPathPattern = `(?i)(?:(?:^|[/\\])[^/\\\n]*(?:1password|onepassword|op[ _.-]?export|app[ _.-]?store|asc[ _.-]?api|api[ _.-]?key|private[ _.-]?key|secret|credential|credentials|token|password|passwd|auth)[^/\\\n]*(?:[/\\].*)?\.(?:json|p8|pem|key|pkcs8|p12|pfx)$|(?:^|[/\\])[^/\\\n]*\.(?:p8|pem|key|pkcs8|p12|pfx)$)`

func secretReadPathRule(t *testing.T) config.Rule {
	t.Helper()
	return loadRule(t,
		"no-credential-file-reads",
		secretReadPathPattern,
		[]string{"PreToolUse", "preToolUse", "beforeReadFile", "beforeTabFileRead", "BeforeTool"},
		[]string{"tool_input.file_path", "tool_input.path", "file_path"},
		"Do not read likely credential files into the agent transcript.",
	)
}

func secretOutputRule(t *testing.T) config.Rule {
	t.Helper()
	return loadRule(t,
		"no-secrets-in-output",
		`\x2d\x2d\x2d\x2d\x2dBEGIN (?:RSA |EC |DSA |OPENSSH |PGP |ENCRYPTED )?PRIVATE KEY\x2d\x2d\x2d\x2d\x2d`,
		[]string{"PostToolUse", "postToolUse", "AfterTool"},
		[]string{"tool_response", "tool_output"},
		"Tool output contains credential material.",
	)
}

func syntheticPrivateKeyHeader() string {
	return "-----BEGIN " + "PRIVATE KEY-----"
}

func knownSecretAssignmentRule(t *testing.T) config.Rule {
	t.Helper()
	return loadRule(t,
		"no-secrets-in-output",
		`(?i)\b(?:password|passwd|pwd|secret|token|api[_-]?key|access[_-]?key|client[_-]?secret|auth(?:orization)?|bearer|aws[_-]?secret[_-]?access[_-]?key|aws[_-]?access[_-]?key[_-]?id)\s*[:=]\s*['"]?(?:bearer\s+)?[A-Za-z0-9._+/=-]{16,}`,
		[]string{"PostToolUse", "postToolUse", "AfterTool"},
		[]string{"tool_response", "tool_output"},
		"Tool output contains credential material.",
	)
}

func TestEvaluate_CredentialFileReadPathsBlocked(t *testing.T) {
	rule := secretReadPathRule(t)

	cases := []struct {
		name    string
		event   string
		payload map[string]any
	}{
		{
			name:  "codex p8 path",
			event: "PreToolUse",
			payload: map[string]any{
				"tool_input": map[string]any{"path": "/Users/agoodkind/Downloads/AuthKey_ABC123.p8"},
			},
		},
		{
			name:  "codex pem file path",
			event: "PreToolUse",
			payload: map[string]any{
				"tool_input": map[string]any{"file_path": "/tmp/service-account.pem"},
			},
		},
		{
			name:  "cursor before read 1password json basename",
			event: "beforeReadFile",
			payload: map[string]any{
				"file_path": "/Users/agoodkind/Downloads/1Password Export.json",
			},
		},
		{
			name:  "cursor before read 1password directory",
			event: "beforeReadFile",
			payload: map[string]any{
				"file_path": "/Users/agoodkind/Downloads/1Password Export/items.json",
			},
		},
		{
			name:  "cursor before tab read token json",
			event: "beforeTabFileRead",
			payload: map[string]any{
				"file_path": "/Users/agoodkind/Downloads/service-token.json",
			},
		},
		{
			name:  "gemini private key path",
			event: "BeforeTool",
			payload: map[string]any{
				"tool_input": map[string]any{"path": "/Users/agoodkind/secrets/private-key.pkcs8"},
			},
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			v := rules.Evaluate(context.Background(), systemFor(testCase.event), testCase.event, testFields(testCase.payload), []config.Rule{rule})
			if v == nil {
				t.Fatalf("expected credential read path violation for %#v", testCase.payload)
			}
		})
	}
}

func TestEvaluate_CredentialFileReadPathsAllowed(t *testing.T) {
	rule := secretReadPathRule(t)

	cases := []struct {
		name    string
		event   string
		payload map[string]any
	}{
		{
			name:  "ordinary package json",
			event: "PreToolUse",
			payload: map[string]any{
				"tool_input": map[string]any{"path": "/Users/agoodkind/Sites/app/package.json"},
			},
		},
		{
			name:  "ordinary readme markdown",
			event: "beforeReadFile",
			payload: map[string]any{
				"file_path": "/Users/agoodkind/Sites/app/docs/private-key-handling.md",
			},
		},
		{
			name:  "secret phrase in non-credential extension",
			event: "PreToolUse",
			payload: map[string]any{
				"tool_input": map[string]any{"file_path": "/Users/agoodkind/Sites/app/secret-notes.txt"},
			},
		},
		{
			name:  "non-read event",
			event: "SessionStart",
			payload: map[string]any{
				"path": "/Users/agoodkind/Downloads/AuthKey_ABC123.p8",
			},
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			v := rules.Evaluate(context.Background(), systemFor(testCase.event), testCase.event, testFields(testCase.payload), []config.Rule{rule})
			if v != nil {
				t.Fatalf("expected allow, got rule %q", v.RuleName)
			}
		})
	}
}

func TestEvaluate_SecretOutputBlocksPrivateKey(t *testing.T) {
	rule := secretOutputRule(t)

	cases := []struct {
		name    string
		event   string
		payload map[string]any
	}{
		{
			name:  "codex tool response",
			event: "PostToolUse",
			payload: map[string]any{
				"tool_response": "header\n" + syntheticPrivateKeyHeader() + "\nbody",
			},
		},
		{
			name:  "cursor tool output",
			event: "postToolUse",
			payload: map[string]any{
				"tool_output": "header\n" + syntheticPrivateKeyHeader() + "\nbody",
			},
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			v := rules.Evaluate(context.Background(), systemFor(testCase.event), testCase.event, testFields(testCase.payload), []config.Rule{rule})
			if v == nil {
				t.Fatalf("expected secret output violation")
			}
		})
	}
}

func TestEvaluate_SecretOutputAssignmentBlocksKnownCredentialValue(t *testing.T) {
	rule := knownSecretAssignmentRule(t)
	secretValue := strings.Repeat("a", 20)

	v := rules.Evaluate(context.Background(), "codex", "PostToolUse", rules.FieldSet{
		ToolResponse: "export " + "AWS_SECRET_ACCESS_KEY=" + secretValue,
	}, []config.Rule{rule})
	if v == nil {
		t.Fatal("expected known credential assignment violation")
	}
}

func TestEvaluate_SecretOutputAssignmentAllowsGenericMakeContractText(t *testing.T) {
	rule := knownSecretAssignmentRule(t)

	cases := []struct {
		name  string
		value string
	}{
		{
			name:  "placeholder token value",
			value: "Required:\n  " + "BASELINE_TOKEN=<today-token>",
		},
		{
			name:  "token command value",
			value: "Token source: " + "BASELINE_TOKEN_CMD='printf expected'",
		},
		{
			name:  "token command with url",
			value: "Token source: " + "BASELINE_TOKEN_CMD='curl -fsSL https://en.wikipedia.org/api/rest_v1/feed/featured/2026/05/09'",
		},
		{
			name:  "generic uppercase token assignment",
			value: "BASELINE_TOKEN=sample",
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			v := rules.Evaluate(context.Background(), "codex", "PostToolUse", rules.FieldSet{
				ToolResponse: testCase.value,
			}, []config.Rule{rule})
			if v != nil {
				t.Fatalf("expected allow, got rule %q", v.RuleName)
			}
		})
	}
}

// TestEvaluate_RedirectionBlocked verifies that common redirect patterns are blocked.
func TestEvaluate_RedirectionBlocked(t *testing.T) {
	rule := redirectionRule(t)

	cases := []struct {
		name    string
		event   string
		payload map[string]any
	}{
		{
			name:  "claude stderr-to-stdout",
			event: "PreToolUse",
			payload: map[string]any{
				"tool_input": map[string]any{"command": "ls 2>&1"},
			},
		},
		{
			name:  "claude discard stdout",
			event: "PreToolUse",
			payload: map[string]any{
				"tool_input": map[string]any{"command": "make build >/dev/null"},
			},
		},
		{
			name:  "claude discard stderr",
			event: "PreToolUse",
			payload: map[string]any{
				"tool_input": map[string]any{"command": "go test ./... 2>/dev/null"},
			},
		},
		{
			name:  "cursor shell execution fd redirect",
			event: "beforeShellExecution",
			payload: map[string]any{
				"command": "cat file.txt 2>&1",
			},
		},
		{
			name:  "cursor combined redirect",
			event: "beforeShellExecution",
			payload: map[string]any{
				"command": "./script.sh &>/dev/null",
			},
		},
		{
			name:  "cursor pipe with stderr",
			event: "beforeShellExecution",
			payload: map[string]any{
				"command": "find . |& grep foo",
			},
		},
		{
			name:  "append redirect to null",
			event: "PreToolUse",
			payload: map[string]any{
				"tool_input": map[string]any{"command": "cmd 2>>/dev/null"},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := rules.Evaluate(context.Background(), systemFor(tc.event), tc.event, testFields(tc.payload), []config.Rule{rule})
			if v == nil {
				t.Errorf("expected violation for command %q, got nil", tc.payload)
			}
		})
	}
}

// TestEvaluate_CleanCommandAllowed verifies that normal commands pass through.
func TestEvaluate_CleanCommandAllowed(t *testing.T) {
	rule := redirectionRule(t)

	cases := []struct {
		name    string
		event   string
		payload map[string]any
	}{
		{
			name:  "simple build",
			event: "PreToolUse",
			payload: map[string]any{
				"tool_input": map[string]any{"command": "go build ./..."},
			},
		},
		{
			name:  "pipe without redirect",
			event: "PreToolUse",
			payload: map[string]any{
				"tool_input": map[string]any{"command": "ls | grep foo"},
			},
		},
		{
			name:  "write to explicit file (allowed)",
			event: "PreToolUse",
			payload: map[string]any{
				"tool_input": map[string]any{"command": "echo hello > /tmp/out.txt"},
			},
		},
		{
			name:  "cursor clean command",
			event: "beforeShellExecution",
			payload: map[string]any{
				"command": "git status",
			},
		},
		{
			name:  "non-matching event with redirect in payload",
			event: "SessionStart",
			payload: map[string]any{
				"tool_input": map[string]any{"command": "ls 2>/dev/null"},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := rules.Evaluate(context.Background(), systemFor(tc.event), tc.event, testFields(tc.payload), []config.Rule{rule})
			if v != nil {
				t.Errorf("expected no violation, got rule %q: %s", v.RuleName, v.Message)
			}
		})
	}
}

// TestEvaluate_EventFilter verifies rules only fire for their configured events.
func TestEvaluate_EventFilter(t *testing.T) {
	rule := redirectionRule(t)

	// A redirect in a PostToolUse payload should not fire the rule
	// because PostToolUse is not in the rule's events list.
	v := rules.Evaluate(context.Background(), "claude", "PostToolUse", rules.FieldSet{ToolInputCommand: "ls 2>/dev/null"}, []config.Rule{rule})

	if v != nil {
		t.Errorf("rule fired for non-matching event PostToolUse, expected nil")
	}
}

// TestEvaluate_EmptyEventList verifies that an empty events list matches all events.
func TestEvaluate_EmptyEventList(t *testing.T) {
	rule := loadRule(t, "catch-all", `forbidden`, nil,
		[]string{"command"}, "forbidden keyword")

	v := rules.Evaluate(context.Background(), "unknown", "AnyEvent", rules.FieldSet{Command: "do something forbidden here"}, []config.Rule{rule})

	if v == nil {
		t.Error("expected violation for catch-all rule, got nil")
	}
}

// TestEvaluate_UnknownFieldSelector verifies unknown selectors are ignored.
func TestEvaluate_UnknownFieldSelector(t *testing.T) {
	rule := loadRule(t, "deep-path", `secret`,
		[]string{"PreToolUse"},
		[]string{"a.b.c"},
		"found it",
	)

	v := rules.Evaluate(context.Background(), "claude", "PreToolUse", rules.FieldSet{}, []config.Rule{rule})
	if v != nil {
		t.Errorf("expected nil for unknown selector, got violation %q", v.RuleName)
	}
}

// TestCheckedRuleNames verifies correct rule name reporting for a given event.
func TestCheckedRuleNames(t *testing.T) {
	rules1 := redirectionRule(t)
	allEvents := loadRule(t, "global", `x`, nil, []string{"command"}, "msg")

	rulesSlice := []config.Rule{rules1, allEvents}

	// PreToolUse matches both rules.
	names := rules.CheckedRuleNames("claude", "PreToolUse", rulesSlice)
	if len(names) != 2 {
		t.Errorf("expected 2 checked rules for PreToolUse, got %d: %v", len(names), names)
	}

	// Stop matches only the catch-all.
	names = rules.CheckedRuleNames("claude", "Stop", rulesSlice)
	if len(names) != 1 || names[0] != "global" {
		t.Errorf("expected [global] for Stop, got %v", names)
	}
}

// loadConditionRule builds the no-git-write-from-home-cwd rule used in production.
// loadConditionRule builds the no-git-write-from-home-cwd rule used in production.
// Uses "effective_cwd" so that cd chains are simulated before matching.
func loadConditionRule(t *testing.T) config.Rule {
	t.Helper()
	cwdCond, err := config.NewCondition(
		[]string{"effective_cwd"},
		`^/Users/agoodkind$`,
		"",
	)
	if err != nil {
		t.Fatalf("compile effective_cwd condition: %v", err)
	}
	cmdCond, err := config.NewCondition(
		[]string{"cmd_segments"},
		`(?m)^git\s+(add|commit|push|reset|rm|mv|rebase|merge|stash|clean|restore|switch|tag)`,
		`(\s-C\s[/~.]|\s + testDoubleHyphen() + git-dir=|\s + testDoubleHyphen() + work-tree=)`,
	)
	if err != nil {
		t.Fatalf("compile cmd condition: %v", err)
	}
	return config.Rule{
		Name:             "no-git-write-from-home-cwd",
		Events:           []string{"PreToolUse", "preToolUse"},
		Conditions:       []config.Condition{cwdCond, cmdCond},
		Action:           "block",
		ViolationMessage: "git write operations are not permitted from the home directory.",
	}
}

func TestEvaluate_MultiCondition_HomeCWD(t *testing.T) {
	t.Setenv("HOME", "/Users/agoodkind")
	rule := loadConditionRule(t)

	homePayload := func(cmd string) rules.FieldSet {
		return rules.FieldSet{
			CWD:              "/Users/agoodkind",
			ToolInputCommand: cmd,
		}
	}

	blocked := []string{
		"git add .",
		"git commit -m 'oops'",
		"git push origin main",
		"git reset --hard HEAD",
		// Compound: first op is read-only but second is a write op from home.
		"git status && git commit -m msg",
	}
	for _, cmd := range blocked {
		t.Run("blocked/"+cmd, func(t *testing.T) {
			v := rules.Evaluate(context.Background(), "claude", "PreToolUse", homePayload(cmd), []config.Rule{rule})
			if v == nil {
				t.Errorf("expected block for %q in home cwd, got nil", cmd)
			}
		})
	}

	allowed := []struct {
		name    string
		payload map[string]any
	}{
		{
			name:    "git with -C flag escapes home",
			payload: map[string]any{"cwd": "/Users/agoodkind", "tool_input": map[string]any{"command": "git -C /Users/agoodkind/Sites/proj commit -m msg"}},
		},
		{
			name:    "git with --git-dir escapes home",
			payload: map[string]any{"cwd": "/Users/agoodkind", "tool_input": map[string]any{"command": "git --git-dir=/Users/agoodkind/Sites/proj/.git commit -m msg"}},
		},
		{
			name:    "git with --work-tree escapes home",
			payload: map[string]any{"cwd": "/Users/agoodkind", "tool_input": map[string]any{"command": "git --work-tree=/Users/agoodkind/Sites/proj commit -m msg"}},
		},
		{
			name:    "cwd is subdir not home",
			payload: map[string]any{"cwd": "/Users/agoodkind/Sites/proj", "tool_input": map[string]any{"command": "git commit -m msg"}},
		},
		{
			name:    "read-only git op from home",
			payload: map[string]any{"cwd": "/Users/agoodkind", "tool_input": map[string]any{"command": "git status"}},
		},
		{
			name:    "non-git command from home",
			payload: map[string]any{"cwd": "/Users/agoodkind", "tool_input": map[string]any{"command": "ls -la"}},
		},
		// Regression: git subcommand appearing inside an argument must not be matched.
		{
			name:    "git log with grep for commit keyword",
			payload: map[string]any{"cwd": "/Users/agoodkind", "tool_input": map[string]any{"command": `git log --grep="git commit"`}},
		},
		{
			name:    "cd to project subdir then commit",
			payload: map[string]any{"cwd": "/Users/agoodkind", "tool_input": map[string]any{"command": "cd /Users/agoodkind/Sites/proj && git commit -m msg"}},
		},
		{
			name:    "cd tilde-path then commit",
			payload: map[string]any{"cwd": "/Users/agoodkind", "tool_input": map[string]any{"command": "cd ~/Sites/proj && git commit -m msg"}},
		},
		{
			name:    "cd absolute non-home then commit",
			payload: map[string]any{"cwd": "/Users/agoodkind", "tool_input": map[string]any{"command": "cd /tmp/work && git commit -m msg"}},
		},
		{
			name:    "ls then cd to project then commit",
			payload: map[string]any{"cwd": "/Users/agoodkind", "tool_input": map[string]any{"command": "ls && cd ~/Sites/proj && git commit -m msg"}},
		},
	}
	for _, tc := range allowed {
		t.Run("allowed/"+tc.name, func(t *testing.T) {
			v := rules.Evaluate(context.Background(), "claude", "PreToolUse", testFields(tc.payload), []config.Rule{rule})
			if v != nil {
				t.Errorf("expected allow, got block: %s", v.Message)
			}
		})
	}
}

// TestEvaluate_EffectiveCwd_StillHome verifies that cd back to home is still blocked.
func TestEvaluate_EffectiveCwd_StillHome(t *testing.T) {
	t.Setenv("HOME", "/Users/agoodkind")
	rule := loadConditionRule(t)

	cases := []struct {
		name    string
		payload map[string]any
	}{
		{
			name:    "cd to home explicitly then commit",
			payload: map[string]any{"cwd": "/Users/agoodkind", "tool_input": map[string]any{"command": "cd ~ && git commit -m msg"}},
		},
		{
			name:    "cd to home absolute then commit",
			payload: map[string]any{"cwd": "/Users/agoodkind", "tool_input": map[string]any{"command": "cd /Users/agoodkind && git commit -m msg"}},
		},
		{
			name:    "cd to project then cd back home then commit",
			payload: map[string]any{"cwd": "/Users/agoodkind", "tool_input": map[string]any{"command": "cd ~/Sites/proj && cd ~ && git commit -m msg"}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := rules.Evaluate(context.Background(), "claude", "PreToolUse", testFields(tc.payload), []config.Rule{rule})
			if v == nil {
				t.Errorf("expected block (effective cwd is still home), got nil")
			}
		})
	}
}

// TestApplyCdChain directly tests the cd simulation logic.
func TestApplyCdChain(t *testing.T) {
	home := "/Users/agoodkind"
	start := "/Users/agoodkind"

	cases := []struct {
		command string
		want    string
	}{
		{"git commit", "/Users/agoodkind"},
		{"cd /tmp && git commit", "/tmp"},
		{"cd ~/Sites/proj && git commit", "/Users/agoodkind/Sites/proj"},
		{"cd ~ && git commit", "/Users/agoodkind"},
		{"cd /tmp && cd ~/Sites && git commit", "/Users/agoodkind/Sites"},
		{"ls && cd ~/Sites && git commit", "/Users/agoodkind/Sites"},
		{"cd \"/Users/agoodkind/Sites/my proj\" && git commit", "/Users/agoodkind/Sites/my proj"},
		{"cd '../sibling'", "/Users/sibling"},
	}

	for _, tc := range cases {
		t.Run(tc.command, func(t *testing.T) {
			got := rules.ApplyCdChain(start, home, tc.command)
			if got != tc.want {
				t.Errorf("ApplyCdChain(%q) = %q, want %q", tc.command, got, tc.want)
			}
		})
	}
}

// emdashDashPattern is the Unicode dash class used by no-emdashes rules in these tests.
const (
	emdashDashPattern        = `[\x{2010}-\x{2015}\x{2212}\x{2E3A}\x{2E3B}\x{FE31}\x{FE32}\x{FE58}\x{FE63}\x{FF0D}]`
	doubleHyphenProsePattern = `(?m)(?|(?:` + "`" + `[^` + "`" + `\n]+` + "`" + `|\b(?!(?:bash|sh|zsh|fish|exec|command|env|xargs|sudo|doas|go|git|make|npm|pnpm|yarn|node|python|python3|ruby|perl|cargo|docker|kubectl|helm|terraform|ansible|rg|grep|sed|awk|jq|curl|ssh|scp|rsync)\b)[A-Za-z][A-Za-z0-9_./-]*)\s+(--)(?=\s+[A-Za-z][A-Za-z0-9_./-]*)(?![^\n]*\s--[A-Za-z0-9_][A-Za-z0-9_-]*(?:=|\b))|(?<!-)\b(?!(?:bash|sh|zsh|fish|exec|command|env|xargs|sudo|doas|go|git|make|npm|pnpm|yarn|node|python|python3|ruby|perl|cargo|docker|kubectl|helm|terraform|ansible|rg|grep|sed|awk|jq|curl|ssh|scp|rsync)\b)[A-Za-z][A-Za-z0-9_./-]*(--)(?=[A-Za-z][A-Za-z0-9_./-]*))`
)

func TestEmdashDashPatternMatchesU2011(t *testing.T) {
	t.Helper()
	re := regex.MustCompile(emdashDashPattern)
	if !re.MatchString("non\u2011breaking") {
		t.Fatal("emdashDashPattern should match U+2011 (non-breaking hyphen)")
	}
}

// emdashMainRule returns the broad no-emdashes rule (tool_input.prompt excluded; see emdashPromptUnlessTaskRule).
func emdashMainRule(t *testing.T) config.Rule {
	t.Helper()
	return loadRule(t,
		"no-emdashes",
		emdashDashPattern,
		[]string{"PreToolUse", "preToolUse", "beforeShellExecution", "Stop", "SubagentStop", "afterAgentResponse"},
		[]string{"tool_input.content", "tool_input.new_string", "tool_input.command", "tool_input.description", "command", "assistant_message", "last_assistant_message", "text"},
		"Unicode dashes are not permitted.",
	)
}

// emdashPromptUnlessTaskRule blocks typographic dashes in tool_input.prompt unless tool_name is Task (sub-agent).
func emdashPromptUnlessTaskRule(t *testing.T) config.Rule {
	t.Helper()
	promptCond, err := config.NewCondition(
		[]string{"tool_input.prompt"},
		emdashDashPattern,
		"",
	)
	if err != nil {
		t.Fatalf("compile prompt condition: %v", err)
	}
	toolCond, err := config.NewCondition(
		[]string{"tool_name"},
		"",
		`(?i)^task$`,
	)
	if err != nil {
		t.Fatalf("compile tool_name condition: %v", err)
	}
	return config.Rule{
		Name:             "no-emdashes-tool-input-prompt-unless-subagent-task",
		ClaudeEvents:     []string{"PreToolUse"},
		CursorEvents:     []string{"preToolUse"},
		Conditions:       []config.Condition{promptCond, toolCond},
		Action:           "block",
		ViolationMessage: "Unicode dashes are not permitted in tool_input.prompt.",
	}
}

// emdashRules returns the main no-emdashes rule plus the conditional prompt rule (production order).
func emdashRules(t *testing.T) []config.Rule {
	t.Helper()
	return []config.Rule{emdashMainRule(t), emdashPromptUnlessTaskRule(t)}
}

// TestEvaluate_EmdashBlocked verifies that Unicode dash variants are blocked.
func TestEvaluate_EmdashBlocked(t *testing.T) {
	rulesSlice := emdashRules(t)

	cases := []struct {
		name    string
		event   string
		payload map[string]any
	}{
		{
			name:  "em dash in file content (Write tool)",
			event: "PreToolUse",
			payload: map[string]any{
				"tool_input": map[string]any{"content": "This is important \u2014 very important."},
			},
		},
		{
			name:  "en dash in edit replacement (Edit tool)",
			event: "PreToolUse",
			payload: map[string]any{
				"tool_input": map[string]any{"new_string": "pages 10\u201320"},
			},
		},
		{
			name:  "figure dash in command",
			event: "PreToolUse",
			payload: map[string]any{
				"tool_input": map[string]any{"command": "echo 'value\u2012dash'"},
			},
		},
		{
			name:  "non-breaking hyphen in prompt",
			event: "PreToolUse",
			payload: map[string]any{
				"tool_input": map[string]any{"prompt": "non\u2011breaking text"},
			},
		},
		{
			name:  "horizontal bar in description",
			event: "PreToolUse",
			payload: map[string]any{
				"tool_input": map[string]any{"description": "separator\u2015here"},
			},
		},
		{
			name:  "unicode hyphen U+2010",
			event: "PreToolUse",
			payload: map[string]any{
				"tool_input": map[string]any{"content": "soft\u2010hyphen"},
			},
		},
		{
			name:  "cursor shell command with em dash",
			event: "beforeShellExecution",
			payload: map[string]any{
				"command": "echo 'text \u2014 more text'",
			},
		},
		{
			name:  "cursor preToolUse with en dash",
			event: "preToolUse",
			payload: map[string]any{
				"tool_input": map[string]any{"content": "2020\u20132025"},
			},
		},
		{
			name:  "em dash in assistant_message (Stop)",
			event: "Stop",
			payload: map[string]any{
				"assistant_message": "Here is the result \u2014 it works.",
			},
		},
		{
			name:  "en dash in last_assistant_message (SubagentStop)",
			event: "SubagentStop",
			payload: map[string]any{
				"last_assistant_message": "Pages 10\u201320 are relevant.",
			},
		},
		{
			name:  "em dash in Cursor afterAgentResponse text",
			event: "afterAgentResponse",
			payload: map[string]any{
				"text": "The result \u2014 as expected \u2014 is correct.",
			},
		},
		{
			name:  "minus sign U+2212",
			event: "PreToolUse",
			payload: map[string]any{
				"tool_input": map[string]any{"content": "5 \u2212 3 = 2"},
			},
		},
		{
			name:  "two-em dash U+2E3A",
			event: "PreToolUse",
			payload: map[string]any{
				"tool_input": map[string]any{"content": "word\u2E3Abreak"},
			},
		},
		{
			name:  "three-em dash U+2E3B",
			event: "PreToolUse",
			payload: map[string]any{
				"tool_input": map[string]any{"content": "word\u2E3Bbreak"},
			},
		},
		{
			name:  "fullwidth hyphen-minus U+FF0D",
			event: "PreToolUse",
			payload: map[string]any{
				"tool_input": map[string]any{"content": "full\uFF0Dwidth"},
			},
		},
		{
			name:  "small em dash U+FE58",
			event: "PreToolUse",
			payload: map[string]any{
				"tool_input": map[string]any{"content": "small\uFE58dash"},
			},
		},
		{
			name:  "vertical em dash U+FE31",
			event: "PreToolUse",
			payload: map[string]any{
				"tool_input": map[string]any{"content": "vert\uFE31dash"},
			},
		},
		{
			name:  "em dash in tool_input.prompt with non-Task tool_name",
			event: "PreToolUse",
			payload: map[string]any{
				"tool_name": "Write",
				"tool_input": map[string]any{
					"prompt": "Instructions \u2014 with a dash.",
				},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := rules.Evaluate(context.Background(), systemFor(tc.event), tc.event, testFields(tc.payload), rulesSlice)
			if v == nil {
				t.Error("expected violation, got nil")
			}
		})
	}
}

// TestEvaluate_EmdashAllowed verifies that regular hyphens and clean text pass through.
func TestEvaluate_EmdashAllowed(t *testing.T) {
	rulesSlice := emdashRules(t)

	cases := []struct {
		name    string
		event   string
		payload map[string]any
	}{
		{
			name:  "kebab-case filename",
			event: "PreToolUse",
			payload: map[string]any{
				"tool_input": map[string]any{"content": "my-component-file.tsx"},
			},
		},
		{
			name:  "CLI flags with hyphens",
			event: "PreToolUse",
			payload: map[string]any{
				"tool_input": map[string]any{"command": "go test --count=1 --race ./..."},
			},
		},
		{
			name:  "plain text no dashes",
			event: "PreToolUse",
			payload: map[string]any{
				"tool_input": map[string]any{"content": "This is a normal sentence with no special dashes."},
			},
		},
		{
			name:  "hyphen-minus in code",
			event: "PreToolUse",
			payload: map[string]any{
				"tool_input": map[string]any{"new_string": "x := a - b"},
			},
		},
		{
			name:  "non-matching event",
			event: "PostToolUse",
			payload: map[string]any{
				"tool_input": map[string]any{"content": "has em dash \u2014 here"},
			},
		},
		{
			name:    "empty payload",
			event:   "PreToolUse",
			payload: map[string]any{},
		},
		{
			name:  "em dash in Task tool prompt (Claude PreToolUse)",
			event: "PreToolUse",
			payload: map[string]any{
				"tool_name": "Task",
				"tool_input": map[string]any{
					"prompt": "Sub-agent brief \u2014 with a dash.",
				},
			},
		},
		{
			name:  "em dash in Task tool prompt case-insensitive tool_name",
			event: "PreToolUse",
			payload: map[string]any{
				"tool_name": "task",
				"tool_input": map[string]any{
					"prompt": "Sub-agent brief \u2014 with a dash.",
				},
			},
		},
		{
			name:  "em dash in Task tool prompt (Cursor preToolUse)",
			event: "preToolUse",
			payload: map[string]any{
				"tool_name": "Task",
				"tool_input": map[string]any{
					"prompt": "Sub-agent brief \u2014 with a dash.",
				},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := rules.Evaluate(context.Background(), systemFor(tc.event), tc.event, testFields(tc.payload), rulesSlice)
			if v != nil {
				t.Errorf("expected no violation, got rule %q: %s", v.RuleName, v.Message)
			}
		})
	}
}

func doubleHyphenProseRule(t *testing.T) config.Rule {
	t.Helper()
	rule := loadRule(t,
		"no-double-hyphen-prose",
		doubleHyphenProsePattern,
		[]string{"PreToolUse", "preToolUse", "Stop", "afterAgentResponse"},
		[]string{"tool_input.content", "tool_input.new_string", "tool_input.description", "edits[*].new_string", "assistant_message", "last_assistant_message", "text"},
		"ASCII double-hyphen is not permitted as a prose dash.",
	)
	rule.DiagnosticGroup = 1
	return rule
}

func TestEvaluateAllUsesDiagnosticGroupSpan(t *testing.T) {
	rule := loadRule(t,
		"capture-only",
		`prefix (bad) suffix`,
		[]string{"Stop"},
		[]string{"assistant_message"},
		"capture is blocked.",
	)
	rule.DiagnosticGroup = 1
	value := "prefix bad suffix"
	payload := map[string]any{
		"assistant_message": value,
	}

	got := rules.EvaluateAll(context.Background(), "claude", "Stop", testFields(payload), []config.Rule{rule}, nil)
	if len(got) != 1 {
		t.Fatalf("EvaluateAll returned %d matches, want 1: %#v", len(got), got)
	}

	wantStart := strings.Index(value, "bad")
	wantEnd := wantStart + len("bad")
	if got[0].Start != wantStart || got[0].End != wantEnd {
		t.Fatalf("match span = [%d,%d), want [%d,%d)", got[0].Start, got[0].End, wantStart, wantEnd)
	}
}

func TestEvaluateAllUsesConditionDiagnosticGroupSpan(t *testing.T) {
	condition, err := config.NewCondition(
		[]string{"assistant_message"},
		`prefix (bad) suffix`,
		"",
	)
	if err != nil {
		t.Fatalf("compile condition: %v", err)
	}
	condition.DiagnosticGroup = 1
	rule := config.Rule{
		Name:             "condition-capture-only",
		Events:           []string{"Stop"},
		Conditions:       []config.Condition{condition},
		Action:           "block",
		ViolationMessage: "capture is blocked.",
	}
	value := "prefix bad suffix"
	payload := map[string]any{
		"assistant_message": value,
	}

	got := rules.EvaluateAll(context.Background(), "claude", "Stop", testFields(payload), []config.Rule{rule}, nil)
	if len(got) != 1 {
		t.Fatalf("EvaluateAll returned %d matches, want 1: %#v", len(got), got)
	}

	wantStart := strings.Index(value, "bad")
	wantEnd := wantStart + len("bad")
	if got[0].Start != wantStart || got[0].End != wantEnd {
		t.Fatalf("match span = [%d,%d), want [%d,%d)", got[0].Start, got[0].End, wantStart, wantEnd)
	}
}

func TestEvaluate_DoubleHyphenProseBlocked(t *testing.T) {
	rule := doubleHyphenProseRule(t)
	cases := []struct {
		name    string
		event   string
		payload map[string]any
	}{
		{
			name:  "spaced lazy em dash",
			event: "PreToolUse",
			payload: map[string]any{
				"tool_input": map[string]any{"new_string": "Word " + testDoubleHyphen() + " word"},
			},
		},
		{
			name:  "unspaced lazy em dash",
			event: "PreToolUse",
			payload: map[string]any{
				"tool_input": map[string]any{"new_string": "Word" + testDoubleHyphen() + "word"},
			},
		},
		{
			name:  "backticked command label in prose",
			event: "PreToolUse",
			payload: map[string]any{
				"tool_input": map[string]any{"new_string": "`mwan cutover` " + testDoubleHyphen() + " deprecated migration/rollback tooling"},
			},
		},
		{
			name:  "assistant response prose",
			event: "Stop",
			payload: map[string]any{
				"assistant_message": "This works " + testDoubleHyphen() + " but it should be rewritten.",
			},
		},
		{
			name:  "cursor edit array",
			event: "afterAgentResponse",
			payload: map[string]any{
				"edits": []any{
					map[string]any{"new_string": "Clean"},
					map[string]any{"new_string": "Old text " + testDoubleHyphen() + " new text"},
				},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := rules.Evaluate(context.Background(), systemFor(tc.event), tc.event, testFields(tc.payload), []config.Rule{rule})
			if v == nil {
				t.Fatal("expected violation, got nil")
			}
		})
	}
}

func TestEvaluate_DoubleHyphenProseMatchSpan(t *testing.T) {
	rule := doubleHyphenProseRule(t)
	value := "// allocator is only used for temporary allocations " + testDoubleHyphen() + " all memory"
	payload := map[string]any{
		"tool_input": map[string]any{"content": value},
	}

	got := rules.EvaluateAll(context.Background(), "claude", "PreToolUse", testFields(payload), []config.Rule{rule}, nil)
	if len(got) != 1 {
		t.Fatalf("EvaluateAll returned %d matches, want 1: %#v", len(got), got)
	}

	wantStart := strings.Index(value, testDoubleHyphen())
	wantEnd := wantStart + len(testDoubleHyphen())
	if got[0].Start != wantStart || got[0].End != wantEnd {
		t.Fatalf("match span = [%d,%d), want [%d,%d)", got[0].Start, got[0].End, wantStart, wantEnd)
	}
}

func TestEvaluate_DoubleHyphenProseAllowed(t *testing.T) {
	rule := doubleHyphenProseRule(t)
	cases := []struct {
		name    string
		event   string
		payload map[string]any
	}{
		{
			name:  "command field with flags is ignored",
			event: "PreToolUse",
			payload: map[string]any{
				"tool_input": map[string]any{"command": "go test --count=1 ./..."},
			},
		},
		{
			name:  "bare flag in prose field",
			event: "PreToolUse",
			payload: map[string]any{
				"tool_input": map[string]any{"new_string": "Use --count=1 for this test."},
			},
		},
		{
			name:  "exec option separator in prose field",
			event: "PreToolUse",
			payload: map[string]any{
				"tool_input": map[string]any{"new_string": "Example: exec -- input"},
			},
		},
		{
			name:  "regular hyphenated prose",
			event: "PreToolUse",
			payload: map[string]any{
				"tool_input": map[string]any{"new_string": "Use well-formed words and kebab-case identifiers."},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := rules.Evaluate(context.Background(), systemFor(tc.event), tc.event, testFields(tc.payload), []config.Rule{rule})
			if v != nil {
				t.Fatalf("expected allow, got %s", v.RuleName)
			}
		})
	}
}

// arrayPathRule returns a rule that matches em dashes in edits[*].new_string.
func arrayPathRule(t *testing.T) config.Rule {
	t.Helper()
	return loadRule(t,
		"no-emdashes-in-edits",
		`[\x{2010}-\x{2015}\x{2212}\x{2E3A}\x{2E3B}\x{FE31}\x{FE32}\x{FE58}\x{FE63}\x{FF0D}]`,
		[]string{"afterFileEdit"},
		[]string{"edits[*].new_string"},
		"Unicode dashes are not permitted in edits.",
	)
}

// TestNavigatePath_ArrayWildcard verifies [*] path traversal through arrays.
func TestNavigatePath_ArrayWildcard(t *testing.T) {
	rule := arrayPathRule(t)

	blocked := []struct {
		name    string
		payload map[string]any
	}{
		{
			name: "em dash in first edit",
			payload: map[string]any{
				"edits": []any{
					map[string]any{"old_string": "before", "new_string": "after \u2014 done"},
				},
			},
		},
		{
			name: "en dash in second edit",
			payload: map[string]any{
				"edits": []any{
					map[string]any{"old_string": "a", "new_string": "clean"},
					map[string]any{"old_string": "b", "new_string": "pages 10\u201320"},
				},
			},
		},
		{
			name: "unicode hyphen in one of three edits",
			payload: map[string]any{
				"edits": []any{
					map[string]any{"new_string": "normal text"},
					map[string]any{"new_string": "soft\u2010hyphen here"},
					map[string]any{"new_string": "also clean"},
				},
			},
		},
	}

	for _, tc := range blocked {
		t.Run("blocked/"+tc.name, func(t *testing.T) {
			v := rules.Evaluate(context.Background(), "cursor", "afterFileEdit", testFields(tc.payload), []config.Rule{rule})
			if v == nil {
				t.Error("expected violation, got nil")
			}
		})
	}

	allowed := []struct {
		name    string
		payload map[string]any
	}{
		{
			name: "all edits are clean",
			payload: map[string]any{
				"edits": []any{
					map[string]any{"new_string": "clean text"},
					map[string]any{"new_string": "also-clean-with-hyphen"},
				},
			},
		},
		{
			name: "empty edits array",
			payload: map[string]any{
				"edits": []any{},
			},
		},
		{
			name: "no edits key",
			payload: map[string]any{
				"tool_input": map[string]any{"content": "clean"},
			},
		},
	}

	for _, tc := range allowed {
		t.Run("allowed/"+tc.name, func(t *testing.T) {
			v := rules.Evaluate(context.Background(), "cursor", "afterFileEdit", testFields(tc.payload), []config.Rule{rule})
			if v != nil {
				t.Errorf("expected no violation, got rule %q: %s", v.RuleName, v.Message)
			}
		})
	}
}

// TestCmdSegments verifies the cmd_segments virtual field splits correctly.
func TestCmdSegments(t *testing.T) {
	cases := []struct {
		name    string
		payload map[string]any
		want    string
	}{
		{
			name:    "single command",
			payload: map[string]any{"tool_input": map[string]any{"command": "git commit -m msg"}},
			want:    "git commit -m msg",
		},
		{
			name:    "and-and chain",
			payload: map[string]any{"tool_input": map[string]any{"command": "git status && git commit -m msg"}},
			want:    "git status\ngit commit -m msg",
		},
		{
			name:    "semicolon chain",
			payload: map[string]any{"tool_input": map[string]any{"command": "cd /tmp; git commit -m msg"}},
			want:    "cd /tmp\ngit commit -m msg",
		},
		{
			name:    "argument with keyword inside does not split",
			payload: map[string]any{"tool_input": map[string]any{"command": `git log --grep="git commit"`}},
			want:    `git log --grep="git commit"`,
		},
		{
			name:    "no command field returns empty",
			payload: map[string]any{},
			want:    "",
		},
		{
			name:    "cursor-style command field",
			payload: map[string]any{"command": "ls && git push"},
			want:    "ls\ngit push",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := rules.CmdSegments(testFields(tc.payload))
			if got != tc.want {
				t.Errorf("CmdSegments() = %q, want %q", got, tc.want)
			}
		})
	}
}

func makeGoThroughMakeRule() config.Rule {
	return config.Rule{
		Name:         "go-build-test-through-make",
		ClaudeEvents: []string{"PreToolUse"},
		CursorEvents: []string{"preToolUse", "beforeShellExecution"},
		CodexEvents:  []string{"PreToolUse"},
		GeminiEvents: []string{"BeforeTool"},
		Conditions: []config.Condition{
			{
				Kind:        "command",
				Argv0:       "go",
				Subcommands: []string{"build", "test"},
				StripEnv:    true,
				StripArgs:   []string{"env", "time", "command"},
				CwdFlags:    []string{"-C"},
				Pattern:     `^(?:build(?:\s|$)|test(?:\s+.*)?\s(?:\./\.\.\.|all)(?:\s|$))`,
			},
			{
				Kind:        "project",
				RootMarkers: []string{"go.mod"},
				RequireAny:  []string{"Makefile", "makefile", "GNUmakefile"},
			},
		},
		Action:           "block",
		ViolationMessage: "Use make.",
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestEvaluate_CommandAndProjectConditions_GoThroughMake(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "go.mod"), "module example.test/project\n")
	writeFile(t, filepath.Join(root, "Makefile"), "test:\n\tgo test ./...\n")

	outside := t.TempDir()
	subdir := filepath.Join(root, "cmd", "server")
	if err := os.MkdirAll(subdir, 0o755); err != nil {
		t.Fatalf("mkdir subdir: %v", err)
	}

	rule := makeGoThroughMakeRule()
	cases := []struct {
		name    string
		system  string
		event   string
		payload map[string]any
		want    bool
	}{
		{
			name:   "claude go build in module with makefile",
			system: "claude",
			event:  "PreToolUse",
			payload: map[string]any{
				"cwd":        root,
				"tool_input": map[string]any{"command": "go build ./..."},
			},
			want: true,
		},
		{
			name:   "codex env-prefixed go test in module with makefile",
			system: "codex",
			event:  "PreToolUse",
			payload: map[string]any{
				"cwd":        root,
				"tool_input": map[string]any{"command": "CGO_ENABLED=1 go test ./..."},
			},
			want: true,
		},
		{
			name:   "operation workdir beats chat cwd",
			system: "codex",
			event:  "PreToolUse",
			payload: map[string]any{
				"cwd":           outside,
				"effective_cwd": root,
				"tool_input":    map[string]any{"command": "go test ./..."},
			},
			want: true,
		},
		{
			name:   "tool input workdir beats chat cwd",
			system: "codex",
			event:  "PreToolUse",
			payload: map[string]any{
				"cwd": outside,
				"tool_input": map[string]any{
					"command": "go test ./...",
					"workdir": root,
				},
			},
			want: true,
		},
		{
			name:   "env wrapper go test in module with makefile",
			system: "codex",
			event:  "PreToolUse",
			payload: map[string]any{
				"cwd":        root,
				"tool_input": map[string]any{"command": "env CGO_ENABLED=1 go test ./..."},
			},
			want: true,
		},
		{
			name:   "time wrapper go test in module with makefile",
			system: "codex",
			event:  "PreToolUse",
			payload: map[string]any{
				"cwd":        root,
				"tool_input": map[string]any{"command": "time go test ./..."},
			},
			want: true,
		},
		{
			name:   "go -C uses command-specific cwd",
			system: "codex",
			event:  "PreToolUse",
			payload: map[string]any{
				"cwd":        outside,
				"tool_input": map[string]any{"command": "go -C " + root + " test ./..."},
			},
			want: true,
		},
		{
			name:   "go -C equals uses command-specific cwd",
			system: "codex",
			event:  "PreToolUse",
			payload: map[string]any{
				"cwd":        outside,
				"tool_input": map[string]any{"command": "go -C=" + root + " test ./..."},
			},
			want: true,
		},
		{
			name:   "cursor command field in module subdir",
			system: "cursor",
			event:  "beforeShellExecution",
			payload: map[string]any{
				"cwd":     subdir,
				"command": "/opt/homebrew/bin/go test ./...",
			},
			want: true,
		},
		{
			name:   "cd into module before go test",
			system: "claude",
			event:  "PreToolUse",
			payload: map[string]any{
				"cwd":        outside,
				"tool_input": map[string]any{"command": "cd " + root + " && go test ./..."},
			},
			want: true,
		},
		{
			name:   "go test before cd into module uses original cwd",
			system: "claude",
			event:  "PreToolUse",
			payload: map[string]any{
				"cwd":        outside,
				"tool_input": map[string]any{"command": "go test ./... && cd " + root},
			},
			want: false,
		},
		{
			name:   "heredoc content with go build is allowed",
			system: "claude",
			event:  "PreToolUse",
			payload: map[string]any{
				"cwd":        root,
				"tool_input": map[string]any{"command": "cat <<'EOF' > Makefile\nbuild:\n\tgo build ./...\ntest:\n\tgo test ./...\nEOF"},
			},
			want: false,
		},
		{
			name:   "heredoc with later direct go test still blocks",
			system: "claude",
			event:  "PreToolUse",
			payload: map[string]any{
				"cwd":        root,
				"tool_input": map[string]any{"command": "cat <<'EOF' > Makefile\nbuild:\n\tgo build ./...\nEOF\ngo test ./..."},
			},
			want: true,
		},
		{
			name:   "make test is allowed",
			system: "claude",
			event:  "PreToolUse",
			payload: map[string]any{
				"cwd":        root,
				"tool_input": map[string]any{"command": "make test"},
			},
			want: false,
		},
		{
			name:   "go list is allowed",
			system: "claude",
			event:  "PreToolUse",
			payload: map[string]any{
				"cwd":        root,
				"tool_input": map[string]any{"command": "go list ./..."},
			},
			want: false,
		},
		{
			name:   "targeted go test package is allowed",
			system: "claude",
			event:  "PreToolUse",
			payload: map[string]any{
				"cwd":        root,
				"tool_input": map[string]any{"command": "go test ./internal/rules"},
			},
			want: false,
		},
		{
			name:   "targeted go test with run filter is allowed",
			system: "codex",
			event:  "PreToolUse",
			payload: map[string]any{
				"cwd":        root,
				"tool_input": map[string]any{"command": "go test ./internal/rules -run TestEvaluate_CommandAndProjectConditions_GoThroughMake"},
			},
			want: false,
		},
		{
			name:   "current package go test is allowed",
			system: "cursor",
			event:  "beforeShellExecution",
			payload: map[string]any{
				"cwd":     subdir,
				"command": "go test",
			},
			want: false,
		},
		{
			name:   "go test all is blocked",
			system: "claude",
			event:  "PreToolUse",
			payload: map[string]any{
				"cwd":        root,
				"tool_input": map[string]any{"command": "go test all"},
			},
			want: true,
		},
		{
			name:   "go test ellipsis with flags is blocked",
			system: "codex",
			event:  "PreToolUse",
			payload: map[string]any{
				"cwd":        root,
				"tool_input": map[string]any{"command": "go test -count=1 -run TestThing ./..."},
			},
			want: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := rules.Evaluate(context.Background(), tc.system, tc.event, testFields(tc.payload), []config.Rule{rule})
			if got := v != nil; got != tc.want {
				t.Fatalf("blocked = %v, want %v; violation = %#v", got, tc.want, v)
			}
		})
	}
}

func TestEvaluate_CommandAndProjectConditions_OrderIndependent(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "go.mod"), "module example.test/project\n")
	writeFile(t, filepath.Join(root, "Makefile"), "test:\n\tgo test ./...\n")

	rule := makeGoThroughMakeRule()
	rule.Conditions[0], rule.Conditions[1] = rule.Conditions[1], rule.Conditions[0]

	v := rules.Evaluate(context.Background(), "claude", "PreToolUse", rules.FieldSet{CWD: t.TempDir(), ToolInputCommand: "cd " + root + " && go test ./..."}, []config.Rule{rule})

	if v == nil {
		t.Fatal("expected project condition to use matched command cwd regardless of condition order")
	}
}

func TestEvaluate_ProjectCondition_AllowsWhenProjectDoesNotSupportMake(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "go.mod"), "module example.test/project\n")

	v := rules.Evaluate(context.Background(), "claude", "PreToolUse", rules.FieldSet{CWD: root, ToolInputCommand: "go test ./..."}, []config.Rule{makeGoThroughMakeRule()})

	if v != nil {
		t.Fatalf("expected allow without Makefile, got %s", v.Message)
	}
}

func TestEvaluate_ProjectCondition_AllowsOutsideMarkedProject(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "Makefile"), "test:\n\tgo test ./...\n")

	v := rules.Evaluate(context.Background(), "claude", "PreToolUse", rules.FieldSet{CWD: root, ToolInputCommand: "go test ./..."}, []config.Rule{makeGoThroughMakeRule()})

	if v != nil {
		t.Fatalf("expected allow outside Go module, got %s", v.Message)
	}
}

// makeDiffRule builds a rule with a single diff condition matching the
// pattern against tool_input.new_string but allowing existing matches.
func makeDiffRule(t *testing.T, pattern string) config.Rule {
	t.Helper()
	cond, err := config.NewCondition(nil, pattern, "")
	if err != nil {
		t.Fatalf("NewCondition: %v", err)
	}
	cond.Kind = string(config.ConditionKindDiff)
	cond.FieldPair = "tool_input.old_string,tool_input.new_string"
	return config.Rule{
		Name:             "block-additive",
		Description:      "",
		Events:           []string{"PreToolUse"},
		ClaudeEvents:     nil,
		CursorEvents:     nil,
		CodexEvents:      nil,
		GeminiEvents:     nil,
		Conditions:       []config.Condition{cond},
		FieldPaths:       nil,
		Pattern:          "",
		Action:           "block",
		ViolationMessage: "additive content blocked",
		DiagnosticGroup:  0,
		AuditOnly:        false,
		DisableIfEnv:     nil,
	}
}

func TestEvaluateAll_DiffCondition_BlocksAdditive(t *testing.T) {
	rule := makeDiffRule(t, `internal/foo\.go`)
	rule.Conditions[0].FieldPaths = []string{"tool_input.new_string"}
	rule.Conditions[0].Selectors() // touch to ensure parse-equivalent state
	// Compile fieldPair via Load equivalent: simulate by going through ParseFieldPair.
	pair, err := config.ParseFieldPair(rule.Conditions[0].FieldPair)
	if err != nil {
		t.Fatalf("ParseFieldPair: %v", err)
	}
	// We need the loadPath path to populate fieldPairs; substitute by going via TOML below.
	_ = pair

	// The cleanest path is via TOML decode.
	const tomlBody = `
[[rules]]
name = "block-additive-foo"
events = ["PreToolUse"]
action = "block"
violation_message = "additive content blocked"

[[rules.conditions]]
kind = "diff"
field_pair = "tool_input.old_string,tool_input.new_string"
pattern = '''internal/foo\.go'''
`
	cfg := loadTOML(t, tomlBody)
	if errs := loadValidate(t, cfg); len(errs) != 0 {
		t.Fatalf("ValidateConfig: %v", errs)
	}

	cases := []struct {
		name    string
		fields  rules.FieldSet
		blocked bool
	}{
		{
			name: "additive only",
			fields: rules.FieldSet{
				ToolInputOldString: "no reference here",
				ToolInputNewString: "added: internal/foo.go",
			},
			blocked: true,
		},
		{
			name: "present in both",
			fields: rules.FieldSet{
				ToolInputOldString: "internal/foo.go was here",
				ToolInputNewString: "internal/foo.go was here, slightly changed",
			},
			blocked: false,
		},
		{
			name: "deletion only",
			fields: rules.FieldSet{
				ToolInputOldString: "internal/foo.go was here",
				ToolInputNewString: "no reference",
			},
			blocked: false,
		},
		{
			name: "neither side has it",
			fields: rules.FieldSet{
				ToolInputOldString: "clean old",
				ToolInputNewString: "clean new",
			},
			blocked: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := rules.EvaluateAll(context.Background(), "claude", "PreToolUse", tc.fields, cfg.Rules, nil)
			if (len(got) > 0) != tc.blocked {
				t.Fatalf("blocked=%v, got %d violations: %#v", tc.blocked, len(got), got)
			}
		})
	}
}

func TestEvaluateAll_ShellWriteCondition_BlocksGlobMatch(t *testing.T) {
	const tomlBody = `
[[rules]]
name = "block-baseline-bash-tampering"
events = ["PreToolUse"]
action = "block"
violation_message = "writing to a baseline file is not permitted"

[[rules.conditions]]
kind = "shell_write"
field_paths = ["tool_input.command"]
globs = ["*-baseline.txt", "*.golangci-lint-baseline.txt"]
`
	cfg := loadTOML(t, tomlBody)
	if errs := loadValidate(t, cfg); len(errs) != 0 {
		t.Fatalf("ValidateConfig: %v", errs)
	}

	cases := []struct {
		name    string
		command string
		blocked bool
	}{
		{name: "redirect append", command: "echo entry >> .golangci-lint-baseline.txt", blocked: true},
		{name: "tee append", command: "tee -a .golangci-lint-baseline.txt < extras.txt", blocked: true},
		{name: "sed in-place", command: "sed -i 's/^/-/' .golangci-lint-baseline.txt", blocked: true},
		{name: "unparseable bash -c sentinel", command: `bash -c "echo hi >> /tmp/log"`, blocked: true},
		{name: "read-only cat allowed", command: "cat README.md", blocked: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fields := rules.FieldSet{
				CWD:              "/work",
				ToolInputCommand: tc.command,
			}
			got := rules.EvaluateAll(context.Background(), "claude", "PreToolUse", fields, cfg.Rules, nil)
			if (len(got) > 0) != tc.blocked {
				t.Fatalf("blocked=%v, got %d violations: %#v", tc.blocked, len(got), got)
			}
		})
	}
}

func TestEvaluateAllSkipsWhenEnvGuardSet(t *testing.T) {
	const tomlBody = `
[[rules]]
name = "block-additive-foo"
events = ["PreToolUse"]
action = "block"
violation_message = "blocked"
disable_if_env = ["LINT_BASELINE_REFRESH"]

[[rules.conditions]]
kind = "diff"
field_pair = "tool_input.old_string,tool_input.new_string"
pattern = '''internal/foo\.go'''
`
	cfg := loadTOML(t, tomlBody)

	fields := rules.FieldSet{
		ToolInputOldString: "no reference",
		ToolInputNewString: "added: internal/foo.go",
	}

	cases := []struct {
		name    string
		getenv  func(string) string
		blocked bool
	}{
		{
			name:    "no getenv blocks",
			getenv:  nil,
			blocked: true,
		},
		{
			name:    "env unset blocks",
			getenv:  func(_ string) string { return "" },
			blocked: true,
		},
		{
			name: "env set to non-empty allows",
			getenv: func(key string) string {
				if key == "LINT_BASELINE_REFRESH" {
					return "1"
				}
				return ""
			},
			blocked: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := rules.EvaluateAll(context.Background(), "claude", "PreToolUse", fields, cfg.Rules, tc.getenv)
			if (len(got) > 0) != tc.blocked {
				t.Fatalf("blocked=%v, got %d violations: %#v", tc.blocked, len(got), got)
			}
		})
	}
}

// loadTOML decodes a TOML body string through the same path as a real config
// file: it writes a tempfile and calls config.LoadExisting so loadPath wires
// up compiled regexes, selectors, and field pairs.
func loadTOML(t *testing.T, body string) *config.Config {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "agent-gate.toml")
	writeFile(t, path, body)
	cfg, err := config.LoadExisting(path)
	if err != nil {
		t.Fatalf("LoadExisting: %v", err)
	}
	return cfg
}

// loadValidate calls hook.ValidateConfig and returns its findings. Wrapped
// here so engine_test does not need to import hook just for this helper at
// every call site.
func loadValidate(t *testing.T, cfg *config.Config) []error {
	t.Helper()
	return hook.ValidateConfig(cfg)
}
