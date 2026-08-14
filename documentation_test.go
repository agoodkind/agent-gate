package agentgate_test

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"testing"

	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/hook"
)

var (
	markdownLinkPattern     = regexp.MustCompile(`\[[^]]+\]\(([^)]+)\)`)
	makeCommandPattern      = regexp.MustCompile(`(?m)^\s*(?:\$ )?make ([A-Za-z0-9_-]+)`)
	makeTargetPattern       = regexp.MustCompile(`(?m)^([A-Za-z0-9][A-Za-z0-9_-]*):(?:[ \t]|$)`)
	agentGateCommandPattern = regexp.MustCompile(`(?m)^\s*(?:\$ )?agent-gate ([a-z][a-z0-9-]*)(?: ([a-z][a-z0-9-]*))?`)
	installerFlagPattern    = regexp.MustCompile(`--[a-z][a-z0-9-]*`)
)

func TestConfigExampleLoadsAsProductionConfig(t *testing.T) {
	configPath := filepath.Join("config.toml.example")
	loadedConfig, err := config.LoadExisting(configPath)
	if err != nil {
		t.Fatalf("config.toml.example does not load as production config: %v", err)
	}
	if validationErrors := hook.ValidateConfig(loadedConfig); len(validationErrors) != 0 {
		t.Fatalf("config.toml.example is not valid for shipped hook schemas: %v", validationErrors)
	}
}

func TestTemporalExecContextDocumentationAndExample(t *testing.T) {
	configReference := readFiles(t, "config.toml.example")
	for _, want := range []string{
		"loop_count",
		"last_user_message",
		"last_response_output",
		"response_output",
		"available",
		"process-local",
		"config reload",
		"restart or eviction",
		"stale verdict",
	} {
		if !strings.Contains(configReference, want) {
			t.Errorf("config.toml.example omits temporal exec context %q", want)
		}
	}

	hookContract := readFiles(t, "HOOKS.md")
	for _, want := range []string{
		"send gate",
		"script decides",
		"complete stdout",
	} {
		if !strings.Contains(hookContract, want) {
			t.Errorf("HOOKS.md omits exec response contract %q", want)
		}
	}

	scriptPath := filepath.Join("examples", "validators", "temporal-response-gate.sh")
	scriptInfo, err := os.Stat(scriptPath)
	if err != nil {
		t.Fatalf("stat temporal exec example: %v", err)
	}
	if scriptInfo.Mode()&0o111 == 0 {
		t.Errorf("%s is not executable", scriptPath)
	}
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skipf("temporal response gate example requires jq: %v", err)
	}

	runGate := func(t *testing.T, input string) ([]byte, error) {
		t.Helper()
		command := exec.Command("bash", scriptPath)
		command.Stdin = strings.NewReader(input)
		return command.Output()
	}
	assertPolicySuppression := func(t *testing.T, input string) {
		t.Helper()
		_, err := runGate(t, input)
		exitError, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("temporal exec example policy suppression error = %v", err)
		}
		if exitError.ExitCode() != 1 {
			t.Errorf("temporal exec example policy suppression exit code = %d, want 1", exitError.ExitCode())
		}
	}

	firstStop := `{"matched":[{"field":"last_user_message","value":"new request","available":true},{"field":"last_response_output","value":"","available":false},{"field":"response_output","value":"Continue the new request.","available":true},{"field":"loop_count","value":"0","available":true}]}`
	output, err := runGate(t, firstStop)
	if err != nil {
		t.Fatalf("temporal exec example first stop: %v", err)
	}
	if string(output) != "Continue the new request." {
		t.Errorf("temporal exec example first stop output = %q", output)
	}

	equalPreviousResponse := `{"matched":[{"field":"last_user_message","value":"Continue the new request.","available":true},{"field":"last_response_output","value":"Continue the new request.","available":true},{"field":"response_output","value":"Continue the new request.","available":true},{"field":"loop_count","value":"1","available":true}]}`
	assertPolicySuppression(t, equalPreviousResponse)

	laterRequest := `{"matched":[{"field":"last_user_message","value":"a different later request","available":true},{"field":"last_response_output","value":"Continue the new request.","available":true},{"field":"response_output","value":"Continue the later request.","available":true},{"field":"loop_count","value":"2","available":true}]}`
	output, err = runGate(t, laterRequest)
	if err != nil {
		t.Fatalf("temporal exec example later request: %v", err)
	}
	if string(output) != "Continue the later request." {
		t.Errorf("temporal exec example later request output = %q", output)
	}

	trailingNewlines := `{"matched":[{"field":"last_user_message","value":"new request\n","available":true},{"field":"last_response_output","value":"new request","available":true},{"field":"response_output","value":"line one\n\n","available":true},{"field":"loop_count","value":"2","available":true}]}`
	output, err = runGate(t, trailingNewlines)
	if err != nil {
		t.Fatalf("temporal exec example trailing newlines: %v", err)
	}
	if string(output) != "line one\n\n" {
		t.Errorf("temporal exec example trailing newline output = %q", output)
	}

	for _, testCase := range []struct {
		name  string
		input string
	}{
		{
			name:  "unavailable last user message",
			input: `{"matched":[{"field":"last_user_message","value":"","available":false},{"field":"last_response_output","value":"previous response","available":true},{"field":"response_output","value":"Continue.","available":true},{"field":"loop_count","value":"1","available":true}]}`,
		},
		{
			name:  "unavailable response output",
			input: `{"matched":[{"field":"last_user_message","value":"new request","available":true},{"field":"last_response_output","value":"previous response","available":true},{"field":"response_output","value":"","available":false},{"field":"loop_count","value":"1","available":true}]}`,
		},
		{
			name:  "unavailable prior response on later loop",
			input: `{"matched":[{"field":"last_user_message","value":"Continue.","available":true},{"field":"last_response_output","value":"","available":false},{"field":"response_output","value":"Continue.","available":true},{"field":"loop_count","value":"1","available":true}]}`,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			assertPolicySuppression(t, testCase.input)
		})
	}
}

func TestTemporalExecExampleReportsMissingJQ(t *testing.T) {
	binDirectory := t.TempDir()

	command := exec.Command("bash", "examples/validators/temporal-response-gate.sh")
	command.Env = []string{"PATH=" + binDirectory}
	command.Stdin = strings.NewReader(`{"matched":[]}`)
	var stderr bytes.Buffer
	command.Stderr = &stderr

	err := command.Run()
	exitError, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("temporal exec example missing jq error = %v", err)
	}
	waitStatus, ok := exitError.ProcessState.Sys().(syscall.WaitStatus)
	if !ok || !waitStatus.Signaled() || waitStatus.Signal() != syscall.SIGTERM {
		t.Errorf("temporal exec example missing jq status = %v, want SIGTERM", exitError.ProcessState)
	}
	if !strings.Contains(stderr.String(), "jq") {
		t.Errorf("temporal exec example missing jq stderr = %q", stderr.String())
	}
}

func TestTemporalExecExampleReportsJQFailure(t *testing.T) {
	falsePath, err := exec.LookPath("false")
	if err != nil {
		t.Fatalf("find false: %v", err)
	}
	binDirectory := t.TempDir()
	if err := os.Symlink(falsePath, filepath.Join(binDirectory, "jq")); err != nil {
		t.Fatalf("link jq stub: %v", err)
	}

	command := exec.Command("bash", "examples/validators/temporal-response-gate.sh")
	command.Env = []string{"PATH=" + binDirectory}
	command.Stdin = strings.NewReader(`{"matched":[]}`)
	var stderr bytes.Buffer
	command.Stderr = &stderr

	err = command.Run()
	exitError, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("temporal exec example jq failure = %v", err)
	}
	waitStatus, ok := exitError.ProcessState.Sys().(syscall.WaitStatus)
	if !ok || !waitStatus.Signaled() || waitStatus.Signal() != syscall.SIGTERM {
		t.Errorf("temporal exec example jq failure status = %v, want SIGTERM", exitError.ProcessState)
	}
	if !strings.Contains(stderr.String(), "jq failed") {
		t.Errorf("temporal exec example jq failure stderr = %q", stderr.String())
	}
}

func TestFirstPartyDocumentationRejectsStaleClaims(t *testing.T) {
	documentationPaths := []string{
		"README.md",
		"HOOKS.md",
		"docs/hook-schemas.md",
		"config.toml.example",
	}
	staleStrings := []string{
		"events/YYYY",
		"payloads/sha256",
		"first matching rule",
		"make install-hooks",
		"make install-service",
		"make daemon-restart",
		"_release_build.yml",
		"/Users/agoodkind/.local/bin/clyde",
	}

	for _, path := range documentationPaths {
		contents, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		for _, stale := range staleStrings {
			if strings.Contains(string(contents), stale) {
				t.Errorf("%s contains stale string %q", path, stale)
			}
		}
	}
}

func TestDocumentedShellInstallerFlagsAreSupported(t *testing.T) {
	supportedFlags := map[string]bool{
		"--bin-dir":             true,
		"--require-attestation": true,
		"--version":             true,
	}
	for _, path := range []string{"README.md", "HOOKS.md", "docs/hook-schemas.md"} {
		contents, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		for lineNumber, line := range strings.Split(string(contents), "\n") {
			if !strings.Contains(line, "install.sh") {
				continue
			}
			for _, flag := range installerFlagPattern.FindAllString(line, -1) {
				if !supportedFlags[flag] {
					t.Errorf("%s:%d documents unsupported install.sh flag %q", path, lineNumber+1, flag)
				}
			}
		}
	}
}

func TestReleaseInstallerRoutesSetupAndCleansTemporaryFiles(t *testing.T) {
	command := exec.Command("bash", "scripts/test-install-setup.sh")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("release installer setup test: %v\n%s", err, output)
	}
	if !bytes.Contains(output, []byte("test-install-setup.sh: PASS")) {
		t.Fatalf("release installer setup output = %q", output)
	}
}

func TestFirstPartyDocumentationLocalLinksResolve(t *testing.T) {
	for _, path := range firstPartyDocumentationPaths(t) {
		contents, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		for _, match := range markdownLinkPattern.FindAllStringSubmatch(string(contents), -1) {
			target := match[1]
			if strings.HasPrefix(target, "https://") || strings.HasPrefix(target, "http://") || strings.HasPrefix(target, "#") {
				continue
			}
			target, _, _ = strings.Cut(target, "#")
			resolved := filepath.Clean(filepath.Join(filepath.Dir(path), target))
			if _, err := os.Stat(resolved); err != nil {
				t.Errorf("%s link %q does not resolve: %v", path, target, err)
			}
		}
	}
}

func firstPartyDocumentationPaths(t *testing.T) []string {
	t.Helper()
	paths := []string{"README.md", "HOOKS.md"}
	if _, err := os.Stat("CONTRIBUTING.md"); err == nil {
		paths = append(paths, "CONTRIBUTING.md")
	} else if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("inspect CONTRIBUTING.md: %v", err)
	}
	err := filepath.WalkDir("docs", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if path == filepath.Join("docs", "superpowers") {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) == ".md" {
			paths = append(paths, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("discover first-party documentation: %v", err)
	}
	sort.Strings(paths)
	return paths
}

func TestDocumentedMakeTargetsExist(t *testing.T) {
	documentation := readFiles(t, "README.md", "HOOKS.md")
	makeSources := readGlobbedFiles(t, "Makefile", ".make/*.mk")
	knownTargets := make(map[string]bool)
	for _, match := range makeTargetPattern.FindAllStringSubmatch(makeSources, -1) {
		knownTargets[match[1]] = true
	}
	for _, match := range makeCommandPattern.FindAllStringSubmatch(documentation, -1) {
		if !knownTargets[match[1]] {
			t.Errorf("documentation names unknown Make target %q", match[1])
		}
	}
}

func TestDocumentedCLICommandNamesExist(t *testing.T) {
	documentation := readFiles(t, "README.md", "HOOKS.md")
	commandSource := readFiles(t, "cmd/agent-gate/main.go", "cmd/agent-gate/install.go")
	for _, match := range agentGateCommandPattern.FindAllStringSubmatch(documentation, -1) {
		for _, command := range match[1:] {
			if command == "" {
				continue
			}
			if !strings.Contains(commandSource, `"`+command+`"`) {
				t.Errorf("documentation names unknown agent-gate command %q", command)
			}
		}
	}
}

func TestDocumentedProvidersMatchShippedTemplates(t *testing.T) {
	templates, err := filepath.Glob("hooks/*")
	if err != nil {
		t.Fatalf("list hook templates: %v", err)
	}
	providerSet := make(map[string]bool)
	for _, path := range templates {
		name := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
		providerSet[name] = true
	}
	providers := make([]string, 0, len(providerSet))
	for provider := range providerSet {
		providers = append(providers, provider)
	}
	sort.Strings(providers)
	if got := strings.Join(providers, ","); got != "claude,codex,copilot,cursor,gemini" {
		t.Fatalf("shipped providers = %q", got)
	}

	readme := readFiles(t, "README.md")
	hooks := readFiles(t, "HOOKS.md")
	schemas := readFiles(t, "docs/hook-schemas.md")
	providerNames := map[string]string{
		"claude":  "Claude",
		"codex":   "Codex",
		"copilot": "Copilot",
		"cursor":  "Cursor",
		"gemini":  "Gemini",
	}
	for _, provider := range providers {
		displayName := providerNames[provider]
		if !strings.Contains(readme, displayName) {
			t.Errorf("README.md omits shipped provider %s", displayName)
		}
		if !strings.Contains(hooks, "## "+displayName) {
			t.Errorf("HOOKS.md omits shipped provider section %s", displayName)
		}
		if !strings.Contains(schemas, "## "+displayName) {
			t.Errorf("docs/hook-schemas.md omits shipped provider section %s", displayName)
		}
	}
}

func readFiles(t *testing.T, paths ...string) string {
	t.Helper()
	var contents strings.Builder
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		contents.Write(data)
		contents.WriteByte('\n')
	}
	return contents.String()
}

func readGlobbedFiles(t *testing.T, paths ...string) string {
	t.Helper()
	var files []string
	for _, pattern := range paths {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			t.Fatalf("glob %s: %v", pattern, err)
		}
		files = append(files, matches...)
	}
	return readFiles(t, files...)
}
