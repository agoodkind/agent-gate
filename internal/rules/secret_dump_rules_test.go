package rules_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/hook"
	"goodkind.io/agent-gate/internal/rules"
	execconcern "goodkind.io/agent-gate/internal/rules/concerns/exec"
)

const secretDumpRulesPath = "../../examples/rules/no-secret-dumps.toml"

type secretDumpCase struct {
	name    string
	command string
}

func loadSecretDumpConfig(t *testing.T) *config.Config {
	t.Helper()
	body, err := os.ReadFile(filepath.Clean(secretDumpRulesPath))
	if err != nil {
		t.Fatalf("read secret dump rules: %v", err)
	}
	cfg := loadRuleConfig(t, string(body))
	validationErrors := hook.ValidateConfig(cfg)
	if len(validationErrors) != 0 {
		t.Fatalf("secret dump rules are invalid for shipped hook schemas: %v", validationErrors)
	}
	return cfg
}

func secretDumpViolations(t *testing.T, cfg *config.Config, command string) []rules.Violation {
	t.Helper()
	runtime := rules.NewExecRuntime(execconcern.OSRunner{}, nil)
	ctx := rules.WithExecRuntime(context.Background(), runtime)
	payload := map[string]any{
		"cwd":        t.TempDir(),
		"tool_name":  "Bash",
		"tool_input": map[string]any{"command": command},
	}
	return rules.EvaluateAll(ctx, "claude", "PreToolUse", testFields(payload), cfg.Rules, nil)
}

func TestSecretDumpRulesBlockCredentialPrinting(t *testing.T) {
	cfg := loadSecretDumpConfig(t)
	blocked := []secretDumpCase{
		{"make short flag", "make -p"},
		{"make combined flag group", "make -qp"},
		{"make combined flag group with n", "make -pn"},
		{"make long flag", "make --print-data-base"},
		{"make flag after target", "make build -p"},
		{"make with canary environment prefix", "CANARY_TOKEN=canary-0000-not-a-secret make -p"},
		{"make behind env wrapper", "env CANARY_TOKEN=canary-0000-not-a-secret make -p"},
		{"make after cd", "cd /tmp && make -p"},
		{"env without command", "env"},
		{"env piped to a filter", "env | sort"},
		{"env with assignment only", "env CANARY_TOKEN=canary-0000-not-a-secret"},
		{"env null flag without command", "env -0"},
		{"env null flag piped to a filter", "env -0 | sort -z"},
		{"env null flag with assignment only", "env -0 CANARY_TOKEN=canary-0000-not-a-secret"},
		{"env by absolute path", "/usr/bin/env"},
		{"printenv without name", "printenv"},
		{"printenv with null flag", "printenv -0"},
		{"export list flag", "export -p"},
		{"export without names", "export"},
		{"declare exported listing", "declare -x"},
		{"declare print listing", "declare -p"},
		{"typeset exported listing", "typeset -x"},
		{"typeset without names", "typeset"},
		{"export list piped to a filter", "export -p | sort"},
		{"typeset listing piped with stderr", "typeset -x 2>&1 | sort"},
		{"declare listing after another command", "cd /tmp && declare -p"},
		{"declare listing on a later line", "true\ndeclare -x"},
		{"readonly list flag", "readonly -p"},
		{"readonly without names", "readonly"},
		{"bash script runs env", "bash -c 'env'"},
		{"sh script runs printenv", `sh -c "printenv"`},
		{"login shell script runs env after cd", "bash -lc 'cd /tmp && env | sort'"},
		{"zsh script runs export listing", "zsh -c 'export -p'"},
		{"cat reads proc cmdline", "cat /proc/1/cmdline"},
		{"set without arguments", "set"},
		{"ps BSD environment modifier", "ps eww"},
		{"ps BSD user listing with environment", "ps auxe"},
		{"ps BSD full listing", "ps aux"},
		{"ps environment flag", "ps -E"},
		{"ps full format flag", "ps -ef"},
		{"ps arguments column", "ps -o pid,args"},
		{"pgrep full command listing", "pgrep -a canary"},
		{"pgrep full match with name listing", "pgrep -fl canary"},
		{"pgrep separate full match and listing flags", "pgrep -f -l canary"},
		{"pgrep long full listing", "pgrep --list-full canary"},
		{"claude mcp get", "claude mcp get canary-server"},
		{"claude mcp list", "claude mcp list"},
		{"cat reads proc environ", "cat /proc/1/environ"},
		{"redirect reads proc environ", `tr '\0' '\n' < /proc/self/environ`},
		{"xargs reads proc environ", "xargs -0 -a /proc/4242/environ"},
	}
	for _, testCase := range blocked {
		t.Run(testCase.name, func(t *testing.T) {
			violations := secretDumpViolations(t, cfg, testCase.command)
			if len(violations) == 0 {
				t.Fatalf("command %q was allowed", testCase.command)
			}
			for _, violation := range violations {
				if !strings.HasPrefix(violation.RuleName, "no-secret-dump-") {
					t.Fatalf("command %q matched unexpected rule %q", testCase.command, violation.RuleName)
				}
			}
		})
	}
}

func TestSecretDumpRulesSeparateProcessEnvironmentFromArguments(t *testing.T) {
	cfg := loadSecretDumpConfig(t)
	const environmentRule = "no-secret-dump-ps-environment"
	const argumentsRule = "no-secret-dump-ps-arguments"
	cases := []struct {
		command         string
		wantEnvironment bool
		wantArguments   bool
	}{
		{"ps eww", true, true},
		{"ps -E", true, false},
		{"ps -o pid,environment", true, false},
		{"ps aux", false, true},
		{"ps -ef", false, true},
		{"ps -p 4242 -o pid,etime,command", false, true},
		{"ps -o pid,args -p 4242", false, true},
	}
	for _, testCase := range cases {
		t.Run(testCase.command, func(t *testing.T) {
			matched := map[string]bool{}
			for _, violation := range secretDumpViolations(t, cfg, testCase.command) {
				matched[violation.RuleName] = true
			}
			if matched[environmentRule] != testCase.wantEnvironment {
				t.Errorf("%s matched = %t, want %t", environmentRule, matched[environmentRule], testCase.wantEnvironment)
			}
			if matched[argumentsRule] != testCase.wantArguments {
				t.Errorf("%s matched = %t, want %t", argumentsRule, matched[argumentsRule], testCase.wantArguments)
			}
		})
	}
}

func TestSecretDumpRulesAllowOrdinaryCommands(t *testing.T) {
	cfg := loadSecretDumpConfig(t)
	allowed := []secretDumpCase{
		{"env runs a command with an assignment", "env CANARY_TOKEN=canary-0000-not-a-secret true"},
		{"assignment prefix runs a command", "CANARY_TOKEN=canary-0000-not-a-secret true"},
		{"env unset flag runs a command", "env -u CANARY_TOKEN true"},
		{"env null flag runs a command", "env -0 true"},
		{"env null flag runs a command with an assignment", "env -0 CANARY_TOKEN=canary-0000-not-a-secret true"},
		{"printenv with a name", "printenv CANARY_TOKEN"},
		{"echo expands one variable", `echo "$CANARY_TOKEN"`},
		{"make build", "make build"},
		{"make check", "make check"},
		{"make test", "make test"},
		{"make dry run", "make -n build"},
		{"make directory flag", "make -C subdir build"},
		{"make directory named p", "make -C p build"},
		{"make parallel flag", "make -j8 build"},
		{"make file flag", "make -f Makefile build"},
		{"make with canary environment prefix", "CANARY_TOKEN=canary-0000-not-a-secret make build"},
		{"make variable quotes a test skip flag", `make test "TEST_ARGS=-count=1 -v -skip 'TestCanary'"`},
		{"patch body lists dump commands", "*** Begin Patch\n*** Add File: canary.sh\n+env\n+set\n+ps aux\n+export -p\n*** End Patch"},
		{"export assigns a value", "export CANARY_TOKEN=canary-0000-not-a-secret"},
		{"declare prints one variable", "declare -p CANARY_TOKEN"},
		{"declare exports an assignment", "declare -x CANARY_TOKEN=canary-0000-not-a-secret"},
		{"export marks one variable", "export CANARY_TOKEN"},
		{"export unmarks one variable", "export -n CANARY_TOKEN"},
		{"typeset declares an integer", "typeset -i canary_count=0"},
		{"declare lists function identifiers", "declare -F"},
		{"echo quotes export", `echo "export -p"`},
		{"heredoc body mentions declarations", "cat <<'EOF'\nexport -p\ndeclare -x\ntypeset\nEOF"},
		{"comment mentions export", "true # export -p"},
		{"readonly marks one variable", "readonly CANARY_LIMIT=1"},
		{"bash script sets errexit", "bash -c 'set -e; make build'"},
		{"bash script runs env with a command", "bash -c 'env CANARY_TOKEN=canary-0000-not-a-secret true'"},
		{"bash script prints one variable", "bash -c 'printenv CANARY_TOKEN'"},
		{"bash script echoes the word env", "bash -c 'echo env'"},
		{"bash runs a script file", "bash scripts/build.sh"},
		{"echo mentions proc cmdline", "echo /proc/1/cmdline"},
		{"set errexit flags", "set -euo pipefail"},
		{"ps selects metadata columns", "ps -o pid,comm"},
		{"ps selects one process by id", "ps -p 4242 -o pid=,etime="},
		{"pgrep exact name", "pgrep -x canary"},
		{"pgrep full match without a listing flag", "pgrep -f canary"},
		{"claude mcp help", "claude mcp --help"},
		{"claude mcp get help", "claude mcp get --help"},
		{"claude mcp add", "claude mcp add canary-server -- canary-command"},
		{"authenticated gh request", "gh api user"},
		{"cat reads an ordinary file", "cat /tmp/canary.txt"},
		{"echo mentions proc environ", "echo /proc/1/environ"},
		{"commit message quotes make", `git commit -m "document make -p and env and export -p"`},
		{"commit message quotes ps and pgrep", `git commit -m "block ps eww, ps aux, and pgrep -fl"`},
		{"commit message quotes proc environ", `git commit -m "block cat /proc/1/environ"`},
		{"commit message quotes claude mcp", `git commit -m "block claude mcp get"`},
		{"echo quotes env", `echo "env"`},
		{"echo quotes set", `echo "set"`},
		{"echo quotes printenv", `echo 'printenv'`},
		{"heredoc body mentions dump commands", "git commit -F - <<'EOF'\nmake -p\nenv\nset\nps eww\nEOF"},
	}
	for _, testCase := range allowed {
		t.Run(testCase.name, func(t *testing.T) {
			violations := secretDumpViolations(t, cfg, testCase.command)
			if len(violations) != 0 {
				t.Fatalf(
					"command %q was blocked by rule %q: %s",
					testCase.command,
					violations[0].RuleName,
					violations[0].Message,
				)
			}
		})
	}
}
