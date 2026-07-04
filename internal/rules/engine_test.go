package rules_test

import (
	"context"
	"fmt"
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
	"goodkind.io/gksyntax/shelldecomp"
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

func devNullPath() string {
	return string([]byte{'/', 'd', 'e', 'v', '/', 'n', 'u', 'l', 'l'})
}

func stdoutPath() string {
	return string([]byte{'/', 'd', 'e', 'v', '/', 's', 't', 'd', 'o', 'u', 't'})
}

func stderrPath() string {
	return string([]byte{'/', 'd', 'e', 'v', '/', 's', 't', 'd', 'e', 'r', 'r'})
}

func fdDupToStdout() string {
	return "2" + string([]byte{'>', '&'}) + "1"
}

func quietStdout() string {
	return string([]byte{'>'}) + devNullPath()
}

func quietStderr() string {
	return "2" + string([]byte{'>'}) + devNullPath()
}

func quietBoth() string {
	return string([]byte{'&', '>'}) + devNullPath()
}

func appendStderrToNull() string {
	return "2" + string([]byte{'>', '>'}) + devNullPath()
}

func pipeStdErr() string {
	return string([]byte{'|', '&'})
}

func stdoutToFile(path string) string {
	return string([]byte{'>'}) + " " + path
}

func stderrToFile(path string) string {
	return "2" + string([]byte{'>'}) + " " + path
}

func bothToStdoutPath() string {
	return string([]byte{'&', '>'}) + stdoutPath()
}

func pluginPathToken() string {
	triple := strings.Repeat("-", 3)
	return "$HOME/.local/share/zinit/plugins/zsh-users" + triple + "zsh-autosuggestions/src/config.zsh"
}

func rgRuleOptionPattern() string {
	return `(?:\s+-[^\s]+|\s+\x2d\x2d(?:[^\s=]+(?:=\S+)?)?)*`
}

func rgRuleShellWordPattern() string {
	return `(?:"[^"\n]+"|\x27[^\x27\n]+\x27|[^-\s;&|][^\s;&|]*)`
}

func rgPathRulePattern() string {
	return `(?m)(?:^|[;&]|\&\&|\|\|)\s*` + "rg" +
		rgRuleOptionPattern() +
		`\s+` + rgRuleShellWordPattern() +
		`\s*(?:$|[;&]|\&\&|\|\||\|)`
}

func rgPathRuleNotPattern() string {
	return `(?m)(?:^|[;&]|\&\&|\|\|)\s*` + "rg" +
		`(?:` +
		rgRuleOptionPattern() +
		`(?:\s+` + rgRuleShellWordPattern() + `){2,}` +
		`|(?=[^;&\n|]*\s+\x2d\x2d(?:files|help|version|type-list)(?:\s|$))[^;&\n|]*` +
		`)` +
		`\s*(?:$|[;&]|\&\&|\|\||\|)`
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
		`.+`,
		[]string{"PreToolUse", "beforeShellExecution"},
		[]string{"cmd_redirections"},
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

const lossyOutputSamplingPattern = `(?m)(?:[^;&\n|]+\|\s*(?:head|tail)\b|(?:^|[;&\n]|\|\|)\s*(?:head|tail)\b[^;&\n]*(?i:(?:\.log\b|/logs?/|journal|daemon|audit|trace|error|incident)))`

func lossyOutputSamplingRule(t *testing.T) config.Rule {
	t.Helper()
	return loadRule(t,
		"no-lossy-output-sampling",
		lossyOutputSamplingPattern,
		[]string{"PreToolUse", "preToolUse", "beforeShellExecution"},
		[]string{"cmd_segments", "tool_input.command", "command"},
		"Do not sample command output with head or tail.",
	)
}

func TestEvaluate_LossyOutputSamplingBlocked(t *testing.T) {
	rule := lossyOutputSamplingRule(t)

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
		{
			name:   "ls directory piped to head",
			system: "claude",
			event:  "PreToolUse",
			payload: map[string]any{
				"tool_input": map[string]any{"command": "ls /Users/agoodkind/Sites/clyde-dev/clyde 2>&1 | head -40"},
			},
		},
		{
			name:   "printf piped to head",
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
			if v == nil {
				t.Fatalf("expected lossy output sampling violation for %#v", testCase.payload)
			}
		})
	}
}

func TestEvaluate_LossyOutputSamplingAllowed(t *testing.T) {
	rule := lossyOutputSamplingRule(t)

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
			name:   "git checkout HEAD with log path argument",
			system: "claude",
			event:  "PreToolUse",
			payload: map[string]any{
				"tool_input": map[string]any{"command": "git checkout HEAD -- internal/cli/logs/inventory.go"},
			},
		},
		{
			name:   "git checkout lowercase head with log path argument",
			system: "claude",
			event:  "PreToolUse",
			payload: map[string]any{
				"tool_input": map[string]any{"command": "git checkout head -- internal/cli/logs/inventory.go"},
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

func TestEvaluate_SimpleRuleNotPattern(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.toml")
	configText := fmt.Sprintf(`
[[rules]]
name = "rg-requires-path"
events = ["PreToolUse"]
field_paths = ["tool_input.command"]
pattern = '''%s'''
not_pattern = '''%s'''
action = "block"
violation_message = "rg path required"
`, rgPathRulePattern(), rgPathRuleNotPattern())
	if err := os.WriteFile(configPath, []byte(configText), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	cfg, err := config.LoadExisting(configPath)
	if err != nil {
		t.Fatalf("LoadExisting: %v", err)
	}
	rule := cfg.Rules[0]

	allowed := rules.Evaluate(context.Background(), "codex", "PreToolUse", testFields(map[string]any{
		"tool_input": map[string]any{"command": `rg -n "config|secret" /Users/agoodkind/.codex/memories/MEMORY.md`},
	}), []config.Rule{rule})
	if allowed != nil {
		t.Fatalf("expected explicit path allow, got %q", allowed.RuleName)
	}

	allowedQuoted := rules.Evaluate(context.Background(), "codex", "PreToolUse", testFields(map[string]any{
		"tool_input": map[string]any{"command": `rg -n "config.toml|secret|secrets|api_key|token|file path|file_path|path support|adapter\..*key" /Users/agoodkind/.codex/memories/MEMORY.md`},
	}), []config.Rule{rule})
	if allowedQuoted != nil {
		t.Fatalf("expected quoted pattern with explicit path allow, got %q", allowedQuoted.RuleName)
	}

	allowedHomeEnv := rules.Evaluate(context.Background(), "codex", "PreToolUse", testFields(map[string]any{
		"tool_input": map[string]any{"command": `rg -n "config|secret" $HOME/.codex/memories/MEMORY.md`},
	}), []config.Rule{rule})
	if allowedHomeEnv != nil {
		t.Fatalf("expected $HOME path allow, got %q", allowedHomeEnv.RuleName)
	}

	allowedBraceHomeEnv := rules.Evaluate(context.Background(), "codex", "PreToolUse", testFields(map[string]any{
		"tool_input": map[string]any{"command": `rg -n "config|secret" ${HOME}/.codex/memories/MEMORY.md`},
	}), []config.Rule{rule})
	if allowedBraceHomeEnv != nil {
		t.Fatalf("expected ${HOME} path allow, got %q", allowedBraceHomeEnv.RuleName)
	}

	allowedQuotedAbsolutePath := rules.Evaluate(context.Background(), "codex", "PreToolUse", testFields(map[string]any{
		"tool_input": map[string]any{"command": `rg --line-number -- '^(Address|AllowedIPs|Endpoint|PersistentKeepalive)\b' '/Users/agoodkind/Sites/iphone-cell-tunnel/example.conf'`},
	}), []config.Rule{rule})
	if allowedQuotedAbsolutePath != nil {
		t.Fatalf("expected quoted absolute path allow, got %q", allowedQuotedAbsolutePath.RuleName)
	}

	allowedRelativePath := rules.Evaluate(context.Background(), "codex", "PreToolUse", testFields(map[string]any{
		"tool_input": map[string]any{"command": `rg -n "ipv6|IPv6|AllowedIPs|Endpoint|route|packet" Daemon/internal/tunnel`},
	}), []config.Rule{rule})
	if allowedRelativePath != nil {
		t.Fatalf("expected relative path allow, got %q", allowedRelativePath.RuleName)
	}

	allowedMultipleRelativeDirs := rules.Evaluate(context.Background(), "codex", "PreToolUse", testFields(map[string]any{
		"tool_input": map[string]any{"command": `rg -n "print\(|debugPrint\(|dump\(" Apps Sources Tests Tools`},
	}), []config.Rule{rule})
	if allowedMultipleRelativeDirs != nil {
		t.Fatalf("expected multiple path args allow, got %q", allowedMultipleRelativeDirs.RuleName)
	}

	for _, command := range []string{
		`rg --files`,
		`rg --files .`,
		`rg --hidden --files .`,
		`rg --type-list`,
		`rg --help`,
		`rg --version`,
	} {
		allowedFileMode := rules.Evaluate(context.Background(), "codex", "PreToolUse", testFields(map[string]any{
			"tool_input": map[string]any{"command": command},
		}), []config.Rule{rule})
		if allowedFileMode != nil {
			t.Fatalf("expected file-list/help command %q to allow, got %q", command, allowedFileMode.RuleName)
		}
	}

	blocked := rules.Evaluate(context.Background(), "codex", "PreToolUse", testFields(map[string]any{
		"tool_input": map[string]any{"command": `rg -n "config|secret"`},
	}), []config.Rule{rule})
	if blocked == nil {
		t.Fatalf("expected pathless rg violation")
	}

	blockedFilesWithMatches := rules.Evaluate(context.Background(), "codex", "PreToolUse", testFields(map[string]any{
		"tool_input": map[string]any{"command": `rg --files-with-matches "config|secret"`},
	}), []config.Rule{rule})
	if blockedFilesWithMatches == nil {
		t.Fatalf("expected pathless rg --files-with-matches violation")
	}
}

func shellReadSecretRule(t *testing.T) config.Rule {
	t.Helper()
	configPath := filepath.Join(t.TempDir(), "config.toml")
	configText := `
[[rules]]
name = "no-shell-read-secrets"
events = ["PreToolUse", "beforeShellExecution"]
action = "block"
redact_diagnostics = true
violation_message = "Do not read secret-bearing files into the agent transcript."

[[rules.conditions]]
kind = "shell_read_secret"
field_paths = ["tool_input.command", "command"]
pattern = '''(?i)\b(?:api[_-]?key|token|secret|password)\s*[:=]'''
path_pattern = '''(?i)(?:^|[/~])(?:\.env|credentials|config\.toml)$|(?:^|[/~])\.aws/credentials$|(?:^|[/~])\.config/clyde/config\.toml$|(?:secret|token|password|api[_-]?key)'''
max_bytes = 1048576
remote_policy = "block_risky"

[[rules.conditions.read_specs]]
name = "plain-readers"
argv0 = ["cat", "less", "more", "strings"]
path_arg_start = 1

[[rules.conditions.read_specs]]
name = "search-readers"
argv0 = ["grep", "rg"]
path_arg_start = 1
skip_positionals = 1
skip_flags_with_values = ["-e", "--regexp", "-f", "--file", "-g", "--glob"]
skip_flag_values_as_positionals = ["-e", "--regexp", "-f", "--file"]

[[rules.conditions.read_specs]]
name = "shell-c"
argv0 = ["bash", "sh", "zsh"]
nested_command = true
nested_command_flag = "-c"

[[rules.conditions.read_specs]]
name = "ssh"
argv0 = ["ssh"]
path_arg_start = 2
path_arg_start_if_flags = ["-p"]
path_arg_start_if_flags_value = 4
skip_flags_with_values = ["-p", "-i", "-F", "-l"]
nested_command = true
nested_remote = true

[[rules.conditions.read_specs]]
name = "remote-copy"
argv0 = ["scp", "rsync"]
path_arg_start = 1
skip_flags_with_values = ["-P", "-e", "-i", "-F"]
remote_sources = true
`
	if err := os.WriteFile(configPath, []byte(configText), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	cfg, err := config.LoadExisting(configPath)
	if err != nil {
		t.Fatalf("LoadExisting: %v", err)
	}
	return cfg.Rules[0]
}

func TestEvaluate_ShellReadSecretBlocksLocalContent(t *testing.T) {
	root := t.TempDir()
	secretConfig := filepath.Join(root, "config.toml")
	writeFile(t, secretConfig, "api_key = \"redacted\"\n")
	rule := shellReadSecretRule(t)

	v := rules.Evaluate(context.Background(), "codex", "PreToolUse", testFields(map[string]any{
		"cwd":        root,
		"tool_input": map[string]any{"command": "cat config.toml"},
	}), []config.Rule{rule})
	if v == nil {
		t.Fatal("expected local content violation")
	}
}

func TestEvaluate_ShellReadSecretAllowsCleanLocalFile(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "README.md"), "public project notes\n")
	rule := shellReadSecretRule(t)

	v := rules.Evaluate(context.Background(), "codex", "PreToolUse", testFields(map[string]any{
		"cwd":        root,
		"tool_input": map[string]any{"command": "rg -n api_key README.md"},
	}), []config.Rule{rule})
	if v != nil {
		t.Fatalf("expected clean local file allow, got %q", v.RuleName)
	}
}

func TestEvaluate_ShellReadSecretBlocksRiskyLocalProbeMiss(t *testing.T) {
	root := t.TempDir()
	rule := shellReadSecretRule(t)

	v := rules.Evaluate(context.Background(), "codex", "PreToolUse", testFields(map[string]any{
		"cwd":        root,
		"tool_input": map[string]any{"command": "cat .env"},
	}), []config.Rule{rule})
	if v == nil {
		t.Fatal("expected risky path probe-miss violation")
	}
}

func TestEvaluate_ShellReadSecretBlocksRemoteRiskyPath(t *testing.T) {
	rule := shellReadSecretRule(t)

	v := rules.Evaluate(context.Background(), "codex", "PreToolUse", testFields(map[string]any{
		"cwd":        "/work",
		"tool_input": map[string]any{"command": "ssh -p 2222 host 'grep TOKEN ~/.config/clyde/config.toml'"},
	}), []config.Rule{rule})
	if v == nil {
		t.Fatal("expected remote risky path violation")
	}
}

func TestEvaluate_ShellReadSecretBlocksRemoteCopySource(t *testing.T) {
	rule := shellReadSecretRule(t)

	v := rules.Evaluate(context.Background(), "codex", "PreToolUse", testFields(map[string]any{
		"cwd":        "/work",
		"tool_input": map[string]any{"command": "scp host:~/.aws/credentials ."},
	}), []config.Rule{rule})
	if v == nil {
		t.Fatal("expected remote copy source violation")
	}
}

func TestEvaluate_ShellReadSecretAllowsRemoteNonRiskyCommand(t *testing.T) {
	rule := shellReadSecretRule(t)

	v := rules.Evaluate(context.Background(), "codex", "PreToolUse", testFields(map[string]any{
		"cwd":        "/work",
		"tool_input": map[string]any{"command": "ssh host uptime"},
	}), []config.Rule{rule})
	if v != nil {
		t.Fatalf("expected remote uptime allow, got %q", v.RuleName)
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
		deterministicSecretOutputPattern,
		[]string{"PostToolUse", "postToolUse", "AfterTool"},
		[]string{"tool_response", "tool_output"},
		"Tool output contains credential material.",
	)
}

const deterministicSecretOutputPattern = `(\x24ANSIBLE_VAULT;\d+\.\d+;[A-Z0-9]+|\x2d\x2d\x2d\x2d\x2dBEGIN (?:RSA |EC |DSA |OPENSSH |PGP |ENCRYPTED )?PRIVATE KEY\x2d\x2d\x2d\x2d\x2d|\x2d\x2d\x2d\x2d\x2dBEGIN CERTIFICATE\x2d\x2d\x2d\x2d\x2d|\bAKIA[0-9A-Z]{16}\b|\bgh[pousr]_[A-Za-z0-9]{36}\b|\bsk-(?:ant-|proj-)?[A-Za-z0-9_-]{20,}|\bxox[abprs]-[A-Za-z0-9-]{10,}|\b(?:sk|pk|rk)_(?:test|live)_[A-Za-z0-9]{20,}|\bAC[a-f0-9]{32}\b|\bSK[a-f0-9]{32}\b|\bkey-[a-f0-9]{32}\b|\bSG\.[A-Za-z0-9_-]{16,32}\.[A-Za-z0-9_-]{32,68}\b|\bAIza[A-Za-z0-9_-]{35}\b|\beyJ[A-Za-z0-9+/=_-]{80,}|\bapi-[0-9A-F]{32}\b)`

func syntheticPrivateKeyHeader() string {
	return "-----BEGIN " + "PRIVATE KEY-----"
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

func TestEvaluate_SecretOutputBlocksDeterministicCredentialValues(t *testing.T) {
	rule := secretOutputRule(t)
	values := []string{
		"$" + "ANSIBLE_VAULT;1.1;AES256",
		"AKIA" + strings.Repeat("A", 16),
		"ghp_" + strings.Repeat("A", 36),
		"sk-" + "ant-" + strings.Repeat("A", 20),
		"sk-" + "proj-" + strings.Repeat("A", 20),
		"SG." + strings.Repeat("A", 20) + "." + strings.Repeat("B", 40),
		"AIza" + strings.Repeat("A", 35),
		"eyJ" + strings.Repeat("A", 80),
	}

	for _, value := range values {
		v := rules.Evaluate(context.Background(), "codex", "PostToolUse", rules.FieldSet{
			ToolResponse: "output: " + value,
		}, []config.Rule{rule})
		if v == nil {
			t.Fatalf("expected deterministic credential violation for %q", value[:min(len(value), 12)])
		}
	}
}

func TestEvaluate_SecretOutputAllowsGenericSourceAndContractText(t *testing.T) {
	rule := secretOutputRule(t)

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
		{
			name:  "generic long assignment",
			value: "AWS_SECRET_ACCESS_KEY=" + strings.Repeat("a", 20),
		},
		{
			name:  "rust auth provider parameter",
			value: "pub fn new(provider: Provider, auth: SharedAuthProvider) -> Self",
		},
		{
			name:  "rust token type field",
			value: "token: CancellationToken",
		},
		{
			name:  "rust secret type field",
			value: "secret" + ": SharedSecret",
		},
		{
			name:  "rust authorization header type field",
			value: "authorization: HeaderValue",
		},
		{
			name:  "rust cookie header type field",
			value: "cookie: HeaderValue",
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
			name:  "claude discard stdout",
			event: "PreToolUse",
			payload: map[string]any{
				"tool_input": map[string]any{"command": "make build " + quietStdout()},
			},
		},
		{
			name:  "claude discard stderr",
			event: "PreToolUse",
			payload: map[string]any{
				"tool_input": map[string]any{"command": "go test ./... " + quietStderr()},
			},
		},
		{
			name:  "cursor shell execution writes stdout file",
			event: "beforeShellExecution",
			payload: map[string]any{
				"command": "echo hello " + stdoutToFile("/tmp/out.txt"),
			},
		},
		{
			name:  "cursor combined redirect to null",
			event: "beforeShellExecution",
			payload: map[string]any{
				"command": "./script.sh " + quietBoth(),
			},
		},
		{
			name:  "cursor pipe with stderr",
			event: "beforeShellExecution",
			payload: map[string]any{
				"command": "find . " + pipeStdErr() + " grep foo",
			},
		},
		{
			name:  "append redirect to null",
			event: "PreToolUse",
			payload: map[string]any{
				"tool_input": map[string]any{"command": "cmd " + appendStderrToNull()},
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
			name:  "stderr to stdout is allowed",
			event: "PreToolUse",
			payload: map[string]any{
				"tool_input": map[string]any{"command": "ls " + fdDupToStdout()},
			},
		},
		{
			name:  "combined output to stdout device is allowed",
			event: "PreToolUse",
			payload: map[string]any{
				"tool_input": map[string]any{"command": "cmd " + bothToStdoutPath()},
			},
		},
		{
			name:  "heredoc script creation is allowed",
			event: "PreToolUse",
			payload: map[string]any{
				"tool_input": map[string]any{"command": "cat <<EOF " + stdoutToFile("script.sh") + "\n#!/usr/bin/env bash\ncmd " + quietStdout() + "\nEOF"},
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
				"tool_input": map[string]any{"command": "ls " + quietStderr()},
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
		`(\s-C\s[/~.]|\s`+testDoubleHyphen()+`git-dir=|\s`+testDoubleHyphen()+`work-tree=)`,
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

// TestApplyCdChain exercises the cd-chain simulation behind the
// effective_cwd field. The simulation now runs on shelldecomp rather than a cd
// regex, so it is driven through the public FieldEffectiveCWD selector instead
// of the retired rules.ApplyCdChain helper. Home is read dynamically because
// effective_cwd expands a leading tilde against the real user home.
func TestApplyCdChain(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("user home dir unavailable: %v", err)
	}
	start := home

	cases := []struct {
		command string
		want    string
	}{
		{"git commit", start},
		{"cd /tmp && git commit", "/tmp"},
		{"cd ~/Sites/proj && git commit", filepath.Join(home, "Sites/proj")},
		{"cd ~ && git commit", home},
		{"cd /tmp && cd ~/Sites && git commit", filepath.Join(home, "Sites")},
		{"ls && cd ~/Sites && git commit", filepath.Join(home, "Sites")},
		{"cd \"" + filepath.Join(home, "Sites/my proj") + "\" && git commit", filepath.Join(home, "Sites/my proj")},
		{"cd '../sibling'", filepath.Join(filepath.Dir(start), "sibling")},
		// New contract: an unresolvable cd target (cd into an expansion) no
		// longer fabricates start/$VAR; shelldecomp poisons the cwd to its
		// Unresolvable sentinel so an index-aware check cannot pin a real dir.
		{"cd \"$VAR\" && git commit", shelldecomp.Unresolvable},
	}

	for _, tc := range cases {
		t.Run(tc.command, func(t *testing.T) {
			fields := rules.FieldSet{CWD: start, ToolInputCommand: tc.command}
			got := fields.String(config.FieldEffectiveCWD)
			if got != tc.want {
				t.Errorf("effective_cwd(%q) = %q, want %q", tc.command, got, tc.want)
			}
		})
	}
}

// emdashDashPattern is the Unicode dash class used by no-emdashes rules in these tests.
const (
	emdashDashPattern        = `[\x{2010}-\x{2015}\x{2212}\x{2E3A}\x{2E3B}\x{FE31}\x{FE32}\x{FE58}\x{FE63}\x{FF0D}]`
	doubleHyphenProsePattern = `(?m)(?|(?:` + "`" + `[^` + "`" + `\n]+` + "`" + `|\b(?!(?:bash|sh|zsh|fish|exec|command|env|xargs|sudo|doas|go|git|make|npm|pnpm|yarn|node|python|python3|ruby|perl|cargo|docker|kubectl|helm|terraform|ansible|rg|grep|sed|awk|jq|curl|ssh|scp|rsync)\b)[A-Za-z][A-Za-z0-9_./-]*)\s+(--)(?=\s+[A-Za-z][A-Za-z0-9_./-]*)(?![^\n]*\s--[A-Za-z0-9_][A-Za-z0-9_-]*(?:=|\b))|(?<![-/_.])\b(?!(?:bash|sh|zsh|fish|exec|command|env|xargs|sudo|doas|go|git|make|npm|pnpm|yarn|node|python|python3|ruby|perl|cargo|docker|kubectl|helm|terraform|ansible|rg|grep|sed|awk|jq|curl|ssh|scp|rsync)\b)[A-Za-z][A-Za-z0-9_./-]*(--)(?=[A-Za-z][A-Za-z0-9_./-]*))`
)

func TestEmdashDashPatternMatchesU2011(t *testing.T) {
	t.Helper()
	re := regex.MustCompile(emdashDashPattern)
	if !re.MatchString("non\u2011breaking") {
		t.Fatal("emdashDashPattern should match U+2011 (non-breaking hyphen)")
	}
}

// emdashMainRule returns the broad no-emdashes rule (tool_input.prompt excluded; see emdashPromptUnlessSubagentLaunchRule).
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

// emdashPromptUnlessSubagentLaunchRule blocks typographic dashes in tool_input.prompt unless the prompt launches a subagent.
func emdashPromptUnlessSubagentLaunchRule(t *testing.T) config.Rule {
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
		`(?i)^(task|agent)$`,
	)
	if err != nil {
		t.Fatalf("compile tool_name condition: %v", err)
	}
	return config.Rule{
		Name:             "no-emdashes-tool-input-prompt-unless-subagent-launch",
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
	return []config.Rule{emdashMainRule(t), emdashPromptUnlessSubagentLaunchRule(t)}
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
			name:  "em dash in tool_input.prompt with normal tool_name",
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

func TestEvaluate_EmdashBlocked_CodexPreToolUseCommand(t *testing.T) {
	unicodeDash := string([]byte{0xe2, 0x80, 0x94})
	v := rules.Evaluate(
		context.Background(),
		"codex",
		"PreToolUse",
		testFields(map[string]any{
			"tool_input": map[string]any{
				"command": "printf 'hello " + unicodeDash + " world\\n'",
			},
		}),
		emdashRules(t),
	)
	if v == nil {
		t.Fatal("expected codex PreToolUse command with em dash to block")
	}
}

func TestEvaluate_EmdashBlocked_CodexPreToolUseCommandComment(t *testing.T) {
	unicodeDash := string([]byte{0xe2, 0x80, 0x94})
	v := rules.Evaluate(
		context.Background(),
		"codex",
		"PreToolUse",
		testFields(map[string]any{
			"tool_input": map[string]any{
				"command": "# provenance check " + unicodeDash + " block this comment\nxattr -l /tmp/app",
			},
		}),
		emdashRules(t),
	)
	if v == nil {
		t.Fatal("expected codex PreToolUse command comment with em dash to block")
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
			name:  "em dash in Agent tool prompt (Claude PreToolUse)",
			event: "PreToolUse",
			payload: map[string]any{
				"tool_name": "Agent",
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
		[]string{"tool_input.content", "tool_input.new_string", "tool_input.description", "edits[*].new_string", "cmd_comments", "cmd_double_hyphen_prose", "assistant_message", "last_assistant_message", "text"},
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
			name:  "sentence-shaped fused prose",
			event: "PreToolUse",
			payload: map[string]any{
				"tool_input": map[string]any{"new_string": "this--is an ugly way of writing--it sounds unfinished"},
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
		{
			name:  "command comment prose",
			event: "PreToolUse",
			payload: map[string]any{
				"tool_input": map[string]any{
					"command": "# this--is an ugly way of writing\nbundle exec -- bin/arils",
				},
			},
		},
		{
			name:  "commit message prose",
			event: "PreToolUse",
			payload: map[string]any{
				"tool_input": map[string]any{
					"command": "git commit -m \"fix the bug" + testDoubleHyphen() + "it was null\"",
				},
			},
		},
		{
			name:  "echo into prose file",
			event: "PreToolUse",
			payload: map[string]any{
				"tool_input": map[string]any{
					"command": "echo \"this" + testDoubleHyphen() + "is sloppy\" > notes.md",
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
			name:  "command field with version flag is ignored",
			event: "PreToolUse",
			payload: map[string]any{
				"tool_input": map[string]any{"command": "/Users/agoodkind/.local/bin/clyde --version"},
			},
		},
		{
			name:  "command field with bundler option separator is ignored",
			event: "PreToolUse",
			payload: map[string]any{
				"tool_input": map[string]any{"command": "bundle exec -- bin/arils"},
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
			name:  "bundler option separator in prose field",
			event: "PreToolUse",
			payload: map[string]any{
				"tool_input": map[string]any{"new_string": "Run bundle exec -- bin/arils for this task."},
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
		{
			name:  "claude project path with doubled hyphen",
			event: "PreToolUse",
			payload: map[string]any{
				"tool_input": map[string]any{"new_string": "~/.claude/projects/-Users-agoodkind--dotfiles/59d024c1-9a71-4668-928f-79655de2e53a.jsonl"},
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

func TestCmdComments(t *testing.T) {
	cases := []struct {
		name    string
		payload map[string]any
		want    string
	}{
		{
			name: "single comment",
			payload: map[string]any{
				"tool_input": map[string]any{"command": "# inspect provenance\nxattr -l /tmp/app"},
			},
			want: "inspect provenance",
		},
		{
			name: "multiple comments",
			payload: map[string]any{
				"tool_input": map[string]any{"command": "echo ok # first\n# second\nbundle exec -- bin/arils"},
			},
			want: "first\nsecond",
		},
		{
			name: "quoted hash ignored",
			payload: map[string]any{
				"tool_input": map[string]any{"command": `printf '%s\n' '# not a comment'`},
			},
			want: "",
		},
		{
			name:    "cursor-style command field",
			payload: map[string]any{"command": "echo ok # cursor comment"},
			want:    "cursor comment",
		},
		{
			name:    "no command field returns empty",
			payload: map[string]any{},
			want:    "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := testFields(tc.payload).CmdComments()
			if got != tc.want {
				t.Errorf("CmdComments() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestCmdDoubleHyphenProse(t *testing.T) {
	cases := []struct {
		name    string
		payload map[string]any
		want    string
	}{
		{
			name: "flag token allowed",
			payload: map[string]any{
				"tool_input": map[string]any{"command": "tool --version --output=result"},
			},
			want: "",
		},
		{
			name: "option separator allowed",
			payload: map[string]any{
				"tool_input": map[string]any{"command": "bundle exec -- bin/arils"},
			},
			want: "",
		},
		{
			name: "bare command token ignored",
			payload: map[string]any{
				"tool_input": map[string]any{"command": "echo this" + testDoubleHyphen() + "is-ugly"},
			},
			want: "",
		},
		{
			name: "comment token ignored",
			payload: map[string]any{
				"tool_input": map[string]any{"command": "echo ok # this--is a comment"},
			},
			want: "",
		},
		{
			name:    "cursor-style commit message",
			payload: map[string]any{"command": "git commit -m \"this" + testDoubleHyphen() + "is-ugly\""},
			want:    "this" + testDoubleHyphen() + "is-ugly",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := testFields(tc.payload).CmdDoubleHyphenProse()
			if got != tc.want {
				t.Errorf("CmdDoubleHyphenProse() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestCmdDoubleHyphenProseSkipsPathTokens(t *testing.T) {
	payload := map[string]any{
		"tool_input": map[string]any{
			"command": "tool " + pluginPathToken(),
		},
	}

	got := testFields(payload).CmdDoubleHyphenProse()
	if got != "" {
		t.Errorf("CmdDoubleHyphenProse() = %q, want empty", got)
	}
}

func TestCmdDoubleHyphenProse_ContextScoped(t *testing.T) {
	dh := testDoubleHyphen()
	hf := "models" + dh + "mlx-community" + dh + "gemma-4-31b-it-8bit"
	cases := []struct {
		name    string
		command string
		want    string
	}{
		// Non-prose command contexts: a bare shell command is code, so nothing
		// is scanned no matter how many double hyphens it carries.
		{"hf cache cleanup assignment", `keep_gemmas="` + hf + " models" + dh + "nomic-ai" + dh + `nomic-embed-code"; echo done`, ""},
		{"hf bare ls", "ls models" + dh + "mlx-community" + dh + "gemma-4-31b-it-8bit", ""},
		{"flags", "tool --version --output=result", ""},
		{"option separator", "bundle exec -- bin/arils", ""},
		{"bem search pattern", "rg block" + dh + "modifier src/", ""},
		{"bare echo not redirected", "echo this" + dh + "is-ugly", ""},
		{"bare printf not redirected", "printf fixed" + dh + "but", ""},
		{"echo into code file", `echo "block` + dh + `modifier" > styles.css`, ""},
		{"commit message identifier only", `git commit -m "bump ` + hf + `"`, ""},
		{"patch envelope exempt", "apply_patch <<'EOF'\n*** Begin Patch\nfix the bug" + dh + "it\n*** End Patch\nEOF", ""},

		// Prose-bearing contexts with a genuine fused double hyphen: emitted.
		{"commit message glued", `git commit -m "fix the bug` + dh + `it was null"`, "fix the bug" + dh + "it was null"},
		{"commit message spaced", `git commit -m "cache is full ` + dh + ` delete it"`, "cache is full " + dh + " delete it"},
		{"commit drops identifier keeps prose", `git commit -m "bump models` + dh + "a" + dh + "b but fix bug" + dh + `it"`, "bump but fix bug" + dh + "it"},
		{"gh pr body short flags", `gh pr create -t "x" -b "this` + dh + `is sloppy"`, "this" + dh + "is sloppy"},
		{"gh release notes", `gh release create v1 --notes "ship` + dh + `it now"`, "ship" + dh + "it now"},
		{"echo into markdown", `echo "the bug` + dh + `it persists" > notes.md`, "the bug" + dh + "it persists"},
		{"printf into text append", `printf "done` + dh + `and broken" >> log.txt`, "done" + dh + "and broken"},
		{"tee into readme", `echo "ship` + dh + `it now" | tee README.md`, "ship" + dh + "it now"},
		{"heredoc into markdown", "cat > notes.md <<EOF\nrefactor done" + dh + "it works\nEOF", "refactor done" + dh + "it works"},
		{"bem in commit message flagged", `git commit -m "rename block` + dh + `modifier here"`, "rename block" + dh + "modifier here"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			payload := map[string]any{"tool_input": map[string]any{"command": tc.command}}
			got := testFields(payload).CmdDoubleHyphenProse()
			if got != tc.want {
				t.Errorf("CmdDoubleHyphenProse() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestCmdRedirections(t *testing.T) {
	cases := []struct {
		name    string
		payload map[string]any
		want    string
	}{
		{
			name: "stderr to stdout is allowed",
			payload: map[string]any{
				"tool_input": map[string]any{"command": "ls " + fdDupToStdout()},
			},
			want: "",
		},
		{
			name: "stdout to null is blocked",
			payload: map[string]any{
				"tool_input": map[string]any{"command": "make build " + quietStdout()},
			},
			want: quietStdout(),
		},
		{
			name: "stderr to file is blocked",
			payload: map[string]any{
				"tool_input": map[string]any{"command": "go test ./... " + stderrToFile("err.log")},
			},
			want: stderrToFile("err.log"),
		},
		{
			name: "combined stderr pipe is blocked",
			payload: map[string]any{
				"tool_input": map[string]any{"command": "find . " + pipeStdErr() + " grep foo"},
			},
			want: pipeStdErr(),
		},
		{
			name: "combined output to stdout device is allowed",
			payload: map[string]any{
				"tool_input": map[string]any{"command": "cmd " + bothToStdoutPath()},
			},
			want: "",
		},
		{
			name: "heredoc script creation is ignored",
			payload: map[string]any{
				"tool_input": map[string]any{"command": "cat <<EOF " + stdoutToFile("script.sh") + "\n#!/usr/bin/env bash\ncmd " + quietStdout() + "\nEOF"},
			},
			want: "",
		},
		{
			name: "quoted redirect text is ignored",
			payload: map[string]any{
				"tool_input": map[string]any{"command": "printf '%s\\n' \"cmd " + quietStdout() + "\""},
			},
			want: "",
		},
		{
			name: "non shell tool payload is ignored",
			payload: map[string]any{
				"tool_name":  "apply_patch",
				"tool_input": map[string]any{"command": "if len(result.FileHashes) > 0 {"},
			},
			want: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := testFields(tc.payload).CmdRedirections()
			if got != tc.want {
				t.Errorf("CmdRedirections() = %q, want %q", got, tc.want)
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

func makeGoBuildThroughMakeRule() config.Rule {
	return config.Rule{
		Name:         "use-make-build-not-go-build-direct",
		ClaudeEvents: []string{"PreToolUse"},
		CursorEvents: []string{"preToolUse", "beforeShellExecution"},
		CodexEvents:  []string{"PreToolUse"},
		GeminiEvents: []string{"BeforeTool"},
		Conditions: []config.Condition{
			{
				Kind:        "command",
				Argv0:       "go",
				Subcommands: []string{"build"},
				StripEnv:    true,
				StripArgs:   []string{"env", "time", "command"},
				CwdFlags:    []string{"-C"},
				Pattern:     `^build(?:\s|$)`,
			},
			{
				Kind:        "project",
				RootMarkers: []string{"go.mod"},
				RequireAny:  []string{"Makefile", "makefile", "GNUmakefile"},
			},
		},
		Action:           "block",
		ViolationMessage: "Run make build instead of go build in Go modules that provide a Makefile.",
	}
}

func makeSwiftBuildThroughMakeRule() config.Rule {
	return config.Rule{
		Name:         "use-make-not-swift-build-direct",
		ClaudeEvents: []string{"PreToolUse"},
		CursorEvents: []string{"preToolUse", "beforeShellExecution"},
		CodexEvents:  []string{"PreToolUse"},
		GeminiEvents: []string{"BeforeTool"},
		Conditions: []config.Condition{
			{
				Kind:        "command",
				Argv0:       "swift",
				Subcommands: []string{"build"},
				StripEnv:    true,
				StripArgs:   []string{"env", "time", "command", "xcrun"},
				CwdFlags:    []string{"--package-path"},
			},
			{
				Kind:        "project",
				RootMarkers: []string{"Package.swift"},
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

func TestEvaluate_CommandAndProjectConditions_SwiftBuildThroughMake(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "Package.swift"), "// swift-tools-version: 6.0\n")
	writeFile(t, filepath.Join(root, "Makefile"), "build:\n\tswift build\n")

	noMakefile := t.TempDir()
	writeFile(t, filepath.Join(noMakefile, "Package.swift"), "// swift-tools-version: 6.0\n")

	outside := t.TempDir()
	rule := makeSwiftBuildThroughMakeRule()
	cases := []struct {
		name    string
		system  string
		event   string
		payload map[string]any
		want    bool
	}{
		{
			name:   "swift build in package with makefile",
			system: "codex",
			event:  "PreToolUse",
			payload: map[string]any{
				"cwd":        root,
				"tool_input": map[string]any{"command": "swift build -c release"},
			},
			want: true,
		},
		{
			name:   "swift package path build uses command cwd",
			system: "codex",
			event:  "PreToolUse",
			payload: map[string]any{
				"cwd":        outside,
				"tool_input": map[string]any{"command": "swift --package-path " + root + " build -c release"},
			},
			want: true,
		},
		{
			name:   "xcrun wrapper swift build in package with makefile",
			system: "claude",
			event:  "PreToolUse",
			payload: map[string]any{
				"cwd":        root,
				"tool_input": map[string]any{"command": "xcrun swift build"},
			},
			want: true,
		},
		{
			name:   "swift build without makefile is allowed",
			system: "codex",
			event:  "PreToolUse",
			payload: map[string]any{
				"cwd":        noMakefile,
				"tool_input": map[string]any{"command": "swift build"},
			},
			want: false,
		},
		{
			name:   "swift test is allowed",
			system: "codex",
			event:  "PreToolUse",
			payload: map[string]any{
				"cwd":        root,
				"tool_input": map[string]any{"command": "swift test"},
			},
			want: false,
		},
		{
			name:   "swift tool script is allowed",
			system: "codex",
			event:  "PreToolUse",
			payload: map[string]any{
				"cwd":        root,
				"tool_input": map[string]any{"command": "swift Tools/lmd-dev.swift build Release"},
			},
			want: false,
		},
		{
			name:   "make build is allowed",
			system: "codex",
			event:  "PreToolUse",
			payload: map[string]any{
				"cwd":        root,
				"tool_input": map[string]any{"command": "make build"},
			},
			want: false,
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

func TestEvaluate_CommandAndProjectConditions_GoBuildThroughMake(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "go.mod"), "module example.test/project\n")
	writeFile(t, filepath.Join(root, "Makefile"), "build:\n\tgo build ./...\n")

	noMakefile := t.TempDir()
	writeFile(t, filepath.Join(noMakefile, "go.mod"), "module example.test/no_makefile\n")

	rule := makeGoBuildThroughMakeRule()
	cases := []struct {
		name        string
		command     string
		cwd         string
		wantBlocked bool
	}{
		{
			name:        "go build ellipsis in module with makefile blocks",
			command:     "go build ./...",
			cwd:         root,
			wantBlocked: true,
		},
		{
			name:        "make build in module with makefile is allowed",
			command:     "make build",
			cwd:         root,
			wantBlocked: false,
		},
		{
			name:        "go build without makefile is allowed",
			command:     "go build ./...",
			cwd:         noMakefile,
			wantBlocked: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := rules.Evaluate(
				context.Background(),
				"codex",
				"PreToolUse",
				rules.FieldSet{CWD: tc.cwd, ToolInputCommand: tc.command},
				[]config.Rule{rule},
			)
			if got := v != nil; got != tc.wantBlocked {
				t.Fatalf("blocked = %v, want %v; violation = %#v", got, tc.wantBlocked, v)
			}
			if tc.wantBlocked && v.Message != rule.ViolationMessage {
				t.Fatalf("violation message = %q, want %q", v.Message, rule.ViolationMessage)
			}
		})
	}
}

// A command rule that pairs subcommands with an anchored pattern must still
// match when leading global flags (git -c name=value, git -C path) sit before
// the subcommand. The pattern is evaluated against the tail normalized from the
// resolved subcommand, not from the raw words, so `git -c user.email=a commit`
// cannot slip past `^commit`.
func TestEvaluate_CommandSubcommand_LeadingGlobalFlagsDoNotDefeatPattern(t *testing.T) {
	rule := config.Rule{
		Name:         "git-commit-anchored",
		ClaudeEvents: []string{"PreToolUse"},
		CodexEvents:  []string{"PreToolUse"},
		Conditions: []config.Condition{
			{
				Kind:        "command",
				Argv0:       "git",
				Subcommands: []string{"commit"},
				StripEnv:    true,
				StripArgs:   []string{"env", "time", "command"},
				CwdFlags:    []string{"-C"},
				Pattern:     `^commit(?:\s|$)`,
			},
		},
		Action:           "block",
		ViolationMessage: "no commit",
	}

	cases := []struct {
		name        string
		command     string
		wantBlocked bool
	}{
		{
			name:        "plain commit blocks",
			command:     "git commit -m x",
			wantBlocked: true,
		},
		{
			name:        "config global flag before commit still blocks",
			command:     "git -c user.email=a@a.com commit -m x",
			wantBlocked: true,
		},
		{
			name:        "cwd flag and config global flag before commit still blocks",
			command:     "git -C /tmp -c user.email=a@a.com commit -m x",
			wantBlocked: true,
		},
		{
			name:        "unlisted subcommand is allowed",
			command:     "git -c user.email=a@a.com status",
			wantBlocked: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := rules.Evaluate(
				context.Background(),
				"codex",
				"PreToolUse",
				rules.FieldSet{CWD: t.TempDir(), ToolInputCommand: tc.command},
				[]config.Rule{rule},
			)
			if got := v != nil; got != tc.wantBlocked {
				t.Fatalf("blocked = %v, want %v; violation = %#v", got, tc.wantBlocked, v)
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
