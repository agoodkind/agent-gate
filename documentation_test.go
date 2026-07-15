package agentgate_test

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/hook"
)

var (
	markdownLinkPattern     = regexp.MustCompile(`\[[^]]+\]\(([^)]+)\)`)
	makeCommandPattern      = regexp.MustCompile(`(?m)^\s*(?:\$ )?make ([A-Za-z0-9_-]+)`)
	makeTargetPattern       = regexp.MustCompile(`(?m)^([A-Za-z0-9][A-Za-z0-9_-]*):(?:[ \t]|$)`)
	agentGateCommandPattern = regexp.MustCompile(`(?m)^\s*(?:\$ )?agent-gate ([a-z][a-z0-9-]*)(?: ([a-z][a-z0-9-]*))?`)
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
		"/Users/agoodkind/Sites/clyde",
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

func TestFirstPartyDocumentationLocalLinksResolve(t *testing.T) {
	for _, path := range []string{"README.md", "HOOKS.md", "docs/hook-schemas.md"} {
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
