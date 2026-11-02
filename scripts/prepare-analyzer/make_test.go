package main_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestMakeAnalyzerSelection(t *testing.T) {
	project, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	root, base := makeFixture(t, project)
	development := t.TempDir()
	if err := os.CopyFS(development, os.DirFS(filepath.Join(root, ".make"))); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(development, "staticcheck", "cmd", "staticcheck-extra"), 0o755); err != nil {
		t.Fatal(err)
	}
	const install = "goodkind.io/go-makefile/staticcheck/cmd/staticcheck-extra@v0.1.1-0.20261102070521-03318cdb2aa5"
	defaultBinary := filepath.Join(root, ".make", "verified-tools", "staticcheck-extra", "v0.1.1-0.20261102070521-03318cdb2aa5", "staticcheck-extra")
	var defaultSelection map[string]string
	for _, scenario := range []struct {
		name        string
		arguments   []string
		environment []string
		local       string
		prepare     bool
		binary      string
		install     string
		source      string
	}{
		{name: "default", prepare: true, binary: defaultBinary, install: install},
		{name: "environment binary", environment: []string{"STATICCHECK_EXTRA_BIN=/chosen/analyzer"}, binary: "/chosen/analyzer", install: install},
		{name: "command binary", arguments: []string{"STATICCHECK_EXTRA_BIN=/chosen/analyzer"}, binary: "/chosen/analyzer", install: install},
		{name: "empty binary", arguments: []string{"STATICCHECK_EXTRA_BIN="}, install: install},
		{name: "empty environment binary", environment: []string{"STATICCHECK_EXTRA_BIN="}, install: install},
		{name: "environment install", environment: []string{"STATICCHECK_EXTRA_INSTALL=example.test/analyzer@v1.0.0"}, install: "example.test/analyzer@v1.0.0"},
		{name: "command install", arguments: []string{"STATICCHECK_EXTRA_INSTALL=example.test/analyzer@v1.0.0"}, install: "example.test/analyzer@v1.0.0"},
		{name: "empty install", arguments: []string{"STATICCHECK_EXTRA_INSTALL="}},
		{name: "empty environment install", environment: []string{"STATICCHECK_EXTRA_INSTALL="}},
		{name: "local binary", local: "binary.mk", binary: "/chosen/analyzer", install: install},
		{name: "local install", local: "install.mk", install: "example.test/analyzer@v1.0.0"},
		{name: "local empty binary", local: "empty-binary.mk", install: install},
		{name: "local empty install", local: "empty-install.mk"},
		{name: "explicit project binary", arguments: []string{"STATICCHECK_EXTRA_BIN=" + defaultBinary}, binary: defaultBinary, install: install},
		{name: "development source", arguments: []string{"STATICCHECK_EXTRA_BUILD_REPO=" + project}, install: install, source: project},
		{name: "development directory", arguments: []string{"GO_MK_DEV_DIR=" + development}, install: install, source: filepath.Join(development, "staticcheck")},
		{name: "binary before source", arguments: []string{"STATICCHECK_EXTRA_BIN=/chosen/analyzer", "STATICCHECK_EXTRA_BUILD_REPO=" + project}, binary: "/chosen/analyzer", install: install, source: project},
		{name: "source before install", arguments: []string{"STATICCHECK_EXTRA_BUILD_REPO=" + project, "STATICCHECK_EXTRA_INSTALL=example.test/analyzer@v1.0.0"}, install: "example.test/analyzer@v1.0.0", source: project},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			var localConfiguration []byte
			if scenario.local != "" {
				var err error
				localConfiguration, err = os.ReadFile(filepath.Join("testdata", scenario.local))
				if err != nil {
					t.Fatal(err)
				}
			}
			writeFile(t, filepath.Join(root, "config.mk"), localConfiguration, 0o644)
			arguments := append([]string{"-n", "staticcheck-extra-bin"}, scenario.arguments...)
			command := exec.Command("make", arguments...)
			command.Dir = root
			command.Env = append(base, scenario.environment...)
			output, runErr := command.CombinedOutput()
			if runErr != nil {
				t.Fatalf("make selection: %v\n%s", runErr, output)
			}
			if prepared := strings.Contains(string(output), "go run"); prepared != scenario.prepare {
				t.Fatalf("preparation = %t, want %t\n%s", prepared, scenario.prepare, output)
			}
			command = exec.Command("make", append([]string{"--no-print-directory", "selection"}, scenario.arguments...)...)
			command.Dir = root
			command.Env = append(base, scenario.environment...)
			selection := selectedValues(t, command)
			for key, expected := range map[string]string{"BIN": scenario.binary, "INSTALL": scenario.install, "SOURCE": scenario.source} {
				if selection[key] != expected {
					t.Errorf("%s = %q, want %q", key, selection[key], expected)
				}
			}
			if scenario.name == "default" {
				defaultSelection = selection
			}
			for _, key := range []string{"FLAGS", "TEST_FLAGS", "BASELINE"} {
				if selection[key] == "" || selection[key] != defaultSelection[key] {
					t.Errorf("%s changed with analyzer selection: %q", key, selection[key])
				}
			}
		})
	}
}

func TestMakeAnalyzerTargets(t *testing.T) {
	project, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	root, environment := makeFixture(t, project)
	for _, scenario := range []struct {
		targets string
		prepare bool
	}{
		{"build install build-gate build-check staticcheck-extra staticcheck-extra-bin lint-files lint-diff go-mk-notice staticcheck-extra-baseline staticcheck-extra-baseline-prune-fixed staticcheck-extra-baseline-accept-new baseline baseline-prune-fixed baseline-accept-new baseline-remove-fixed baseline-add-new staticcheck-extra-baseline-remove-fixed", true},
		{"ci-changed go-version-check fmt test vet lint-format lint-golangci lint-gocyclo lint-deadcode govulncheck go-mk-bin go-mk-cache-manifest go-mk-ci-job-layout version-info help", false},
	} {
		for _, target := range strings.Fields(scenario.targets) {
			t.Run(target, func(t *testing.T) {
				command := exec.Command("make", "-n", target)
				command.Dir = root
				command.Env = environment
				output, runErr := command.CombinedOutput()
				if runErr != nil {
					t.Fatalf("make %s: %v\n%s", target, runErr, output)
				}
				if prepared := strings.Contains(string(output), "go run"); prepared != scenario.prepare {
					t.Fatalf("%s preparation = %t, want %t\n%s", target, prepared, scenario.prepare, output)
				}
			})
		}
	}
	t.Run("recursive defaults", func(t *testing.T) {
		command := exec.Command("make", "-n", "recursive-analyzer")
		command.Dir = root
		command.Env = environment
		output, runErr := command.CombinedOutput()
		if runErr != nil || !strings.Contains(string(output), "go run") {
			t.Fatalf("recursive make did not prepare its inherited default: %v\n%s", runErr, output)
		}
	})
}

func TestMakeAnalyzerCrossCompilation(t *testing.T) {
	project, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	root, base := makeFixture(t, project)
	targetOS := "linux"
	if runtime.GOOS == targetOS {
		targetOS = "darwin"
	}
	goEnvironment := filepath.Join(t.TempDir(), "go-env")
	writeFile(t, goEnvironment, []byte("GOOS="+targetOS+"\nGOARCH=amd64\n"), 0o644)
	for _, selection := range []struct {
		name        string
		arguments   []string
		environment []string
	}{
		{"environment", []string{"GOOS=" + targetOS, "GOARCH=amd64"}, []string{"GOOS=" + targetOS, "GOARCH=amd64"}},
		{"Go environment file", nil, []string{"GOENV=" + goEnvironment}},
	} {
		t.Run(selection.name, func(t *testing.T) {
			arguments := append([]string{"staticcheck-extra-bin"}, selection.arguments...)
			command := exec.Command("make", arguments...)
			command.Dir = root
			command.Env = append(base, "GOPROXY=off", "GOMODCACHE="+t.TempDir(), "GOTOOLCHAIN=local")
			command.Env = append(command.Env, selection.environment...)
			output, runErr := command.CombinedOutput()
			if runErr == nil || !strings.Contains(string(output), "module lookup disabled") {
				t.Fatalf("the host preparer must report the offline download failure: %v\n%s", runErr, output)
			}
		})
	}
}

func makeFixture(t *testing.T, project string) (string, []string) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".make", "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"bootstrap.mk", ".make/go.mk", ".make/go-build.mk", ".make/golangci.yml", ".make/notices.txt"} {
		contents, err := os.ReadFile(filepath.Join(project, name))
		if err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(root, name), contents, 0o644)
	}
	engine, err := os.ReadFile(filepath.Join(project, ".make", "go-mk"))
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, ".make", "go-mk"), engine, 0o755)
	if err := os.CopyFS(filepath.Join(root, ".make", "scripts"), os.DirFS(filepath.Join(project, ".make", "scripts"))); err != nil {
		t.Fatal(err)
	}
	makefile, err := os.ReadFile("testdata/Makefile.mk")
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, "Makefile"), makefile, 0o644)
	writeFile(t, filepath.Join(root, "go.mod"), []byte("module example.test/project\n\ngo 1.27.0\n"), 0o644)
	var environment []string
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "STATICCHECK_EXTRA_") || strings.HasPrefix(key, "GO_MK_") ||
			strings.HasPrefix(key, "_GO_MK_") || strings.HasPrefix(key, "_AGATE_ANALYZER_") ||
			key == "MAKEFLAGS" || key == "MFLAGS" || key == "MAKELEVEL" {
			continue
		}
		environment = append(environment, entry)
	}
	environment = append(environment, "GO_MK_SKIP_FETCH=1", "_GO_MK_PROVISIONED=1",
		"GO_MK_BIN="+filepath.Join(project, ".make", "go-mk"), "AGATE_ANALYZER_PROJECT="+project)
	return root, environment
}

func selectedValues(t *testing.T, command *exec.Cmd) map[string]string {
	t.Helper()
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("inspect Make selection: %v\n%s", err, output)
	}
	result := make(map[string]string)
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		key, value, found := strings.Cut(line, "=")
		if found {
			result[key] = value
		}
	}
	return result
}
