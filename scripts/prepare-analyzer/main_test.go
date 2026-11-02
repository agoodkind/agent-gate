package main_test

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"debug/buildinfo"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	testModule  = "example.test/analyzer"
	testVersion = "v1.0.0"
)

func TestPrepareAnalyzer(t *testing.T) {
	helper := filepath.Join(t.TempDir(), "prepare-analyzer")
	build := exec.Command("go", "build", "-o", helper, "main.go")
	build.Env = environment(map[string]string{"GOWORK": "off"})
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build preparer: %v\n%s", err, output)
	}
	proxy, checksum := moduleProxy(t)
	baseEnvironment := map[string]string{
		"GOPROXY": "file://" + proxy, "GOSUMDB": "off", "GOWORK": "off",
		"GOMODCACHE": t.TempDir(), "GOTOOLCHAIN": "local", "GOFLAGS": "-modcacherw",
		"GOOS": "linux", "GOARCH": "386", "GOBIN": t.TempDir(),
	}
	run := func(root, sum string, changes map[string]string) *exec.Cmd {
		values := make(map[string]string, len(baseEnvironment)+len(changes))
		for key, value := range baseEnvironment {
			values[key] = value
		}
		for key, value := range changes {
			values[key] = value
		}
		command := exec.Command(helper, "-root", root, "-module", testModule,
			"-version", testVersion, "-sum", sum)
		command.Env = environment(values)
		return command
	}
	root := t.TempDir()
	destination := filepath.Join(root, ".make", "verified-tools", "staticcheck-extra", testVersion, "staticcheck-extra")
	t.Cleanup(func() {
		entries, err := os.ReadDir(filepath.Dir(destination))
		if err != nil {
			t.Error(err)
			return
		}
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), ".install-") {
				t.Errorf("preparation retained a staging directory: %s", entry.Name())
			}
		}
	})
	if output, err := run(root, checksum, nil).CombinedOutput(); err != nil {
		t.Fatalf("fresh preparation: %v\n%s", err, output)
	}
	assertAnalyzer(t, destination, checksum)
	before, err := os.Stat(destination)
	if err != nil {
		t.Fatal(err)
	}
	t.Run("warm offline reuse", func(t *testing.T) {
		if output, err := run(root, checksum, map[string]string{
			"GOPROXY": "off", "GOMODCACHE": t.TempDir(),
		}).CombinedOutput(); err != nil {
			t.Fatalf("offline reuse: %v\n%s", err, output)
		}
		after, statErr := os.Stat(destination)
		if statErr != nil {
			t.Fatal(statErr)
		}
		if !os.SameFile(before, after) || !before.ModTime().Equal(after.ModTime()) {
			t.Fatal("warm preparation rewrote the executable")
		}
	})
	t.Run("failures preserve the executable", func(t *testing.T) {
		original, readErr := os.ReadFile(destination)
		if readErr != nil {
			t.Fatal(readErr)
		}
		for _, scenario := range []struct {
			name    string
			changes map[string]string
			message string
		}{
			{"download", map[string]string{"GOPROXY": "off", "GOMODCACHE": t.TempDir()}, "module lookup disabled"},
			{"identity", nil, "analyzer identity mismatch"},
		} {
			t.Run(scenario.name, func(t *testing.T) {
				output, runErr := run(root, "h1:wrong", scenario.changes).CombinedOutput()
				if runErr == nil || !bytes.Contains(output, []byte(scenario.message)) {
					t.Fatalf("expected %s failure, got %v\n%s", scenario.name, runErr, output)
				}
				current, currentErr := os.ReadFile(destination)
				if currentErr != nil || !bytes.Equal(original, current) {
					t.Fatalf("failed preparation changed the previous executable: %v", currentErr)
				}
				assertAnalyzer(t, destination, checksum)
			})
		}
	})
	t.Run("concurrent repair replaces a symlink", func(t *testing.T) {
		previous := filepath.Join(t.TempDir(), "previous-analyzer")
		contents, readErr := os.ReadFile(destination)
		if readErr != nil {
			t.Fatal(readErr)
		}
		writeFile(t, previous, contents, 0o755)
		if renameErr := os.Rename(destination, filepath.Join(t.TempDir(), "original")); renameErr != nil {
			t.Fatal(renameErr)
		}
		if symlinkErr := os.Symlink(previous, destination); symlinkErr != nil {
			t.Fatal(symlinkErr)
		}
		var waiting sync.WaitGroup
		for range 2 {
			waiting.Go(func() {
				if output, runErr := run(root, checksum, nil).CombinedOutput(); runErr != nil {
					t.Errorf("concurrent preparation: %v\n%s", runErr, output)
				}
			})
		}
		waiting.Wait()
		assertAnalyzer(t, destination, checksum)
		file, statErr := os.Lstat(destination)
		if statErr != nil || !file.Mode().IsRegular() {
			t.Fatalf("repair did not create a regular executable: %v", statErr)
		}
		unchanged, readErr := os.ReadFile(previous)
		if readErr != nil || !bytes.Equal(contents, unchanged) {
			t.Fatalf("repair changed the symlink target: %v", readErr)
		}
	})
	t.Run("interrupted installation removes staging", func(t *testing.T) {
		requested := make(chan struct{}, 1)
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			select {
			case requested <- struct{}{}:
			default:
			}
			<-request.Context().Done()
		}))
		defer server.Close()
		defer server.CloseClientConnections()
		command := run(root, "h1:wrong", map[string]string{
			"GOPROXY": server.URL, "GOMODCACHE": t.TempDir(),
		})
		var output bytes.Buffer
		command.Stdout = &output
		command.Stderr = &output
		if err := command.Start(); err != nil {
			t.Fatal(err)
		}
		select {
		case <-requested:
		case <-time.After(10 * time.Second):
			command.Process.Kill()
			command.Wait()
			t.Fatal("installation did not request the module")
		}
		if err := command.Process.Signal(os.Interrupt); err != nil {
			t.Fatal(err)
		}
		if err := command.Wait(); err == nil || !strings.Contains(output.String(), "install analyzer") {
			t.Fatalf("interrupted installation did not fail visibly: %v\n%s", err, &output)
		}
		assertAnalyzer(t, destination, checksum)
	})
	t.Run("cold download failure", func(t *testing.T) {
		output, runErr := run(t.TempDir(), checksum, map[string]string{
			"GOPROXY": "off", "GOMODCACHE": t.TempDir(),
		}).CombinedOutput()
		if runErr == nil || !bytes.Contains(output, []byte("module lookup disabled")) {
			t.Fatalf("expected a visible download failure: %v\n%s", runErr, output)
		}
	})
}

func assertAnalyzer(t *testing.T, path, checksum string) {
	t.Helper()
	info, err := buildinfo.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Main.Path != testModule || info.Main.Version != testVersion || info.Main.Sum != checksum {
		t.Fatalf("unexpected analyzer module: %+v", info.Main)
	}
	output, err := exec.Command(path).CombinedOutput()
	if err != nil || string(output) != runtime.GOOS+"/"+runtime.GOARCH+"\n" {
		t.Fatalf("analyzer did not run on the host: %v\n%s", err, output)
	}
}

func moduleProxy(t *testing.T) (string, string) {
	t.Helper()
	proxy := t.TempDir()
	directory := filepath.Join(proxy, testModule, "@v")
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"go.mod":                        "module " + testModule + "\n\ngo 1.27.0\n",
		"cmd/staticcheck-extra/main.go": "package main\nimport (\"fmt\"; \"runtime\")\nfunc main() { fmt.Println(runtime.GOOS + \"/\" + runtime.GOARCH) }\n",
	}
	var archive bytes.Buffer
	writer := zip.NewWriter(&archive)
	var names []string
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	hash := sha256.New()
	for _, name := range names {
		fullName := testModule + "@" + testVersion + "/" + name
		entry, err := writer.Create(fullName)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write([]byte(files[name])); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(hash, "%x  %s\n", sha256.Sum256([]byte(files[name])), fullName)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(directory, testVersion+".zip"), archive.Bytes(), 0o644)
	writeFile(t, filepath.Join(directory, testVersion+".mod"), []byte(files["go.mod"]), 0o644)
	writeFile(t, filepath.Join(directory, testVersion+".info"), []byte(`{"Version":"v1.0.0","Time":"2026-11-01T00:00:00Z"}`), 0o644)
	writeFile(t, filepath.Join(directory, "list"), []byte(testVersion+"\n"), 0o644)
	return proxy, "h1:" + base64.StdEncoding.EncodeToString(hash.Sum(nil))
}

func writeFile(t *testing.T, path string, contents []byte, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, contents, mode); err != nil {
		t.Fatal(err)
	}
}

func environment(values map[string]string) []string {
	var result []string
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if _, replaced := values[key]; !replaced {
			result = append(result, entry)
		}
	}
	for key, value := range values {
		result = append(result, key+"="+value)
	}
	return result
}
