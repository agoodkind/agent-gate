package installer

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
	"goodkind.io/agent-gate/internal/auditstorage"
)

type resetTestRunner func(string, ...string) ([]byte, error)

func (runner resetTestRunner) OutputContext(_ context.Context, name string, args ...string) ([]byte, error) {
	return runner(name, args...)
}

func (runner resetTestRunner) Output(name string, args ...string) ([]byte, error) {
	return runner(name, args...)
}

func (runner resetTestRunner) Run(name string, args ...string) error {
	_, err := runner(name, args...)
	return err
}

func resetTestOptions(t *testing.T) ResetOptions {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	absent := resetTestRunner(func(string, ...string) ([]byte, error) { return nil, ErrServiceAbsent })
	options := ResetOptions{ExecutablePath: filepath.Join(root, "bin", "agent-gate"), Service: ServiceOptions{HomeDir: root, Runner: absent}, Control: ServiceStatusOptions{Runner: absent}, StateDir: filepath.Join(root, "state", "agent-gate"), CacheDir: filepath.Join(root, "cache", "agent-gate"), RuntimeDir: filepath.Join(root, "runtime", "agent-gate"), ConfigDir: filepath.Join(root, "config", "agent-gate"), Targets: DefaultResetTargets()}
	options.PreservedPaths = []string{filepath.Join(options.ConfigDir, "config.toml"), filepath.Join(root, "hooks.json")}
	options.Audit = auditstorage.CatalogOptions{BasePath: filepath.Join(root, "shared", "audit.db"), StatePath: filepath.Join(options.StateDir, "audit-storage.json"), CoordinationPath: filepath.Join(root, "runtime", "agent-gate-audit.lock")}
	for _, path := range []string{options.StateDir, options.CacheDir, options.RuntimeDir, options.ConfigDir, filepath.Dir(options.Audit.BasePath)} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range append([]string{options.Audit.BasePath}, options.PreservedPaths...) {
		if err := os.WriteFile(path, []byte("unchanged fixture bytes"), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Dir(options.ExecutablePath), 0o700); err != nil {
		t.Fatal(err)
	}
	build := exec.CommandContext(t.Context(), "go", "build", "-o", options.ExecutablePath, "./testdata/reset-executable")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build reset fixture: %s %v", output, err)
	}
	return options
}

func assertResetBytes(t *testing.T, path string, want []byte) {
	t.Helper()
	actual, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(actual, want) {
		t.Fatalf("%s = %q, %v", path, actual, err)
	}
}

func TestResetAbsentRepeatedRestoresBytesAndCustomFamily(t *testing.T) {
	options := resetTestOptions(t)
	before, err := os.ReadFile(options.ExecutablePath)
	if err != nil {
		t.Fatal(err)
	}
	mode, err := os.Stat(options.ExecutablePath)
	if err != nil {
		t.Fatal(err)
	}
	attribute := "user.agent-gate-reset-test"
	if runtime.GOOS == "darwin" {
		attribute = "io.goodkind.reset-test"
	}
	if err := unix.Setxattr(options.ExecutablePath, attribute, []byte("signed-attribute-fixture"), 0); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(filepath.Dir(options.ExecutablePath), "alias")
	if err := os.Symlink(options.ExecutablePath, alias); err != nil {
		t.Fatal(err)
	}
	options.ExecutablePath = alias
	unrelated := filepath.Join(filepath.Dir(options.Audit.BasePath), "unrelated.db")
	if err := os.WriteFile(unrelated, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		plan, err := PrepareReset(options)
		if err != nil {
			t.Fatal(err)
		}
		newBucket := strings.TrimSuffix(options.Audit.BasePath, ".db") + "-20260908T000000Z.db-wal"
		if err := os.WriteFile(newBucket, []byte("newly found old WAL"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := ApplyReset(t.Context(), plan); err != nil {
			t.Fatal(err)
		}
		assertResetBytes(t, options.ExecutablePath, before)
		info, err := os.Stat(options.ExecutablePath)
		if err != nil || info.Mode() != mode.Mode() {
			t.Fatalf("mode changed: %v", err)
		}
		attrs, err := resetAttributes(options.ExecutablePath)
		if err != nil || string(attrs[attribute]) != "signed-attribute-fixture" {
			t.Fatalf("attributes = %v %v", attrs, err)
		}
		assertResetBytes(t, unrelated, []byte("keep"))
		for _, path := range options.PreservedPaths {
			assertResetBytes(t, path, []byte("unchanged fixture bytes"))
			info, err := os.Stat(path)
			if err != nil || info.Mode().Perm() != 0o640 {
				t.Fatalf("preserved permissions: %v", err)
			}
		}
		for _, path := range []string{options.Audit.BasePath, newBucket, options.Audit.StatePath} {
			if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("old file remains %s: %v", path, err)
			}
		}
	}
}

func TestResetRejectsProtectedPathAliases(t *testing.T) {
	for _, kind := range []string{"direct", "symlink", "ancestor", "case"} {
		t.Run(kind, func(t *testing.T) {
			options := resetTestOptions(t)
			protected := options.PreservedPaths[0]
			target := protected
			switch kind {
			case "symlink":
				alias := filepath.Join(filepath.Dir(options.Audit.BasePath), "alias")
				if err := os.Symlink(filepath.Dir(protected), alias); err != nil {
					t.Fatal(err)
				}
				target = filepath.Join(alias, filepath.Base(protected))
			case "ancestor":
				if err := os.RemoveAll(options.CacheDir); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(options.ConfigDir, options.CacheDir); err != nil {
					t.Fatal(err)
				}
			case "case":
				target = filepath.Join(filepath.Dir(protected), "CONFIG.TOML")
				info, err := os.Stat(target)
				if errors.Is(err, os.ErrNotExist) {
					t.Skip("filesystem is case sensitive")
				}
				original, originalErr := os.Stat(protected)
				if err != nil || originalErr != nil || !os.SameFile(info, original) {
					t.Fatalf("case alias probe: %v %v", err, originalErr)
				}
			}
			options.Audit.BasePath = target
			plan, err := PrepareReset(options)
			if err == nil {
				err = ApplyReset(t.Context(), plan)
			}
			if err == nil {
				t.Fatal("protected removal accepted")
			}
			assertResetBytes(t, protected, []byte("unchanged fixture bytes"))
			assertResetBytes(t, options.Audit.BasePath, []byte("unchanged fixture bytes"))
		})
	}
}

func TestResetDiscardsCorruptMetadataWithoutDatabaseInspection(t *testing.T) {
	options := resetTestOptions(t)
	if err := os.WriteFile(options.Audit.StatePath, []byte("incomplete old metadata"), 0o600); err != nil {
		t.Fatal(err)
	}
	plan, err := PrepareReset(options)
	if err != nil {
		t.Fatal(err)
	}
	if err := ApplyReset(t.Context(), plan); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(options.Audit.StatePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old metadata remains: %v", err)
	}
	for _, path := range options.PreservedPaths {
		assertResetBytes(t, path, []byte("unchanged fixture bytes"))
	}
}

func TestResetFailedShutdownLeavesDatabase(t *testing.T) {
	options := resetTestOptions(t)
	runner := resetTestRunner(func(name string, args ...string) ([]byte, error) {
		if name == "pgrep" || name == "lsof" {
			return nil, ErrServiceAbsent
		}
		if strings.Contains(strings.Join(args, " "), "bootout") || strings.Contains(strings.Join(args, " "), " stop ") {
			return nil, errors.New("shutdown denied")
		}
		if runtime.GOOS == "darwin" {
			return []byte(fmt.Sprintf("program = %s\narguments = {\n%s\ndaemon\n}\nstate = waiting\n", options.ExecutablePath, options.ExecutablePath)), nil
		}
		return []byte(fmt.Sprintf("LoadState=loaded\nActiveState=inactive\nMainPID=0\nExecStart={ path=%s ; argv[]=%s daemon ; }\n", options.ExecutablePath, options.ExecutablePath)), nil
	})
	options.Control.Runner = runner
	plan, err := PrepareReset(options)
	if err != nil {
		t.Fatal(err)
	}
	if err := ApplyReset(t.Context(), plan); err == nil || !strings.Contains(err.Error(), "shutdown denied") {
		t.Fatalf("shutdown: %v", err)
	}
	assertResetBytes(t, options.Audit.BasePath, []byte("unchanged fixture bytes"))
}

func TestResetTermResistantChild(t *testing.T) {
	child := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestResetChildProcess$")
	child.Env = append(os.Environ(), "AGENT_GATE_RESET_CHILD=1")
	stdout, err := child.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = child.Process.Kill(); _ = child.Wait() })
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil || line != "ready\n" {
		t.Fatalf("child readiness: %q %v", line, err)
	}
	control := NativeProcessControl{}
	identity, err := control.Inspect(child.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	wrong := identity
	wrong.Start += "-reused"
	if err := control.Signal(wrong, syscall.SIGTERM); err == nil {
		t.Fatal("changed process identity accepted")
	}
	if err := stopResetProcess(t.Context(), control, identity, 40*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if _, err := control.Inspect(identity.PID); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("child still exists: %v", err)
	}
}

func TestResetChildProcess(t *testing.T) {
	if os.Getenv("AGENT_GATE_RESET_CHILD") != "1" {
		return
	}
	signal.Ignore(syscall.SIGTERM)
	fmt.Println("ready")
	for {
		time.Sleep(time.Hour)
	}
}
