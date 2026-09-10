package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"goodkind.io/agent-gate/internal/config"
	installer "goodkind.io/agent-gate/internal/install"
)

type resetInstallationFixture struct {
	t         *testing.T
	home, bin string
	child     *exec.Cmd
	done      chan struct{}
	loaded    bool
	oldPID    int
	preserved map[string]os.FileMode
	oldPaths  []string
	install   installDependencies
}

func newResetInstallationFixture(t *testing.T) *resetInstallationFixture {
	t.Helper()
	home := t.TempDir()
	runtimeRoot, err := os.MkdirTemp("/tmp", "agate-reset-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(runtimeRoot) })
	fixture := &resetInstallationFixture{t: t, home: home, bin: filepath.Join(home, "bin", "agent-gate"), preserved: make(map[string]os.FileMode)}
	if err := os.MkdirAll(filepath.Dir(fixture.bin), 0o700); err != nil {
		t.Fatal(err)
	}
	build := exec.CommandContext(t.Context(), "go", "build", "-o", fixture.bin, ".")
	buildStart := time.Now()
	if err := os.Remove(fixture.bin); err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, output)
	}
	info, err := os.Stat(fixture.bin)
	if err != nil || info.ModTime().Before(buildStart.Truncate(time.Second)) {
		t.Fatalf("build artifact is not fresh: %v", err)
	}
	fixture.AssertExecutableRestoredAndRunnable()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	t.Setenv("XDG_RUNTIME_DIR", runtimeRoot)
	fixture.bin, err = filepath.EvalSymlinks(fixture.bin)
	if err != nil {
		t.Fatal(err)
	}
	fixture.install = defaultInstallDependencies()
	fixture.install.resolveExecutable = func() (string, error) { return fixture.bin, nil }
	fixture.install.prepareInstallation = func(options installer.InstallationOptions) (*installer.InstallationPlan, error) {
		if options.Service != nil {
			options.Service.Runner = fixture
			options.Service.HomeDir = home
		}
		return installer.PrepareInstallation(options)
	}
	t.Cleanup(fixture.stopChild)
	return fixture
}

func (f *resetInstallationFixture) InstallUsingExistingInstaller() {
	f.t.Helper()
	if code := runInstallWithDependencies([]string{"all", "--bin-path", f.bin, "--auto-update", "off"}, f.install); code != 0 {
		f.t.Fatalf("install = %d", code)
	}
	f.oldPID = f.child.Process.Pid
}

func (f *resetInstallationFixture) SeedAuditAndInstallationState() {
	f.t.Helper()
	for _, path := range []string{filepath.Join(config.DefaultStateDir(), "old-state"), filepath.Join(config.DefaultCacheDir(), "old-cache"), filepath.Join(config.RuntimeDir(), "old-runtime"), filepath.Join(config.DefaultConfigDir(), "old-config"), config.DefaultAuditSQLitePath()} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			f.t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("old installation bytes"), 0o600); err != nil {
			f.t.Fatal(err)
		}
		f.oldPaths = append(f.oldPaths, path)
	}
}

func (f *resetInstallationFixture) ReadPreservedFiles() map[string][]byte {
	f.t.Helper()
	paths := []string{config.Path(), filepath.Join(f.home, ".claude", "settings.json"), filepath.Join(f.home, ".codex", "config.toml"), filepath.Join(f.home, ".cursor", "hooks.json"), filepath.Join(f.home, ".gemini", "settings.json"), filepath.Join(f.home, ".copilot", "hooks", "agent-gate.json")}
	result := make(map[string][]byte)
	for _, path := range paths {
		content, err := os.ReadFile(path)
		if err != nil {
			f.t.Fatal(err)
		}
		info, err := os.Stat(path)
		if err != nil {
			f.t.Fatal(err)
		}
		result[path] = content
		f.preserved[path] = info.Mode()
	}
	return result
}

func (f *resetInstallationFixture) RunReset() int {
	dependencies := defaultResetDependencies()
	dependencies.resolveExecutable = func() (string, error) { return f.bin, nil }
	dependencies.prepareReset = func(options installer.ResetOptions) (*installer.ResetPlan, error) {
		options.Service.Runner = f
		options.Control.Runner = f
		return installer.PrepareReset(options)
	}
	dependencies.install = f.install
	return runResetWithDependencies(nil, dependencies)
}

func (f *resetInstallationFixture) AssertPreservedFilesEqual(before map[string][]byte) {
	f.t.Helper()
	for path, want := range before {
		got, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(got, want) {
			f.t.Fatalf("preserved file %s changed: %v", path, err)
		}
		info, err := os.Stat(path)
		if err != nil || info.Mode() != f.preserved[path] {
			f.t.Fatalf("preserved mode %s changed: %v", path, err)
		}
	}
}

func (f *resetInstallationFixture) AssertOldAuditAndStateAbsent() {
	f.t.Helper()
	for _, path := range f.oldPaths {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			f.t.Fatalf("old state remains %s: %v", path, err)
		}
	}
}

func (f *resetInstallationFixture) AssertExecutableRestoredAndRunnable() {
	f.t.Helper()
	if output, err := exec.CommandContext(f.t.Context(), f.bin, "version").CombinedOutput(); err != nil || len(output) == 0 {
		f.t.Fatalf("restored executable: %s %v", output, err)
	}
}

func (f *resetInstallationFixture) AssertServiceReadyWithNewProcess() {
	f.t.Helper()
	if f.child == nil || f.child.Process.Pid == f.oldPID {
		f.t.Fatal("daemon was not replaced")
	}
	if err := waitForInstalledDaemon(f.bin); err != nil {
		f.t.Fatal(err)
	}
}

func (f *resetInstallationFixture) stopChild() {
	if f.child == nil {
		return
	}
	_ = f.child.Process.Signal(syscall.SIGTERM)
	select {
	case <-f.done:
	case <-time.After(5 * time.Second):
		_ = f.child.Process.Kill()
		<-f.done
	}
}

func (f *resetInstallationFixture) Run(name string, args ...string) error {
	_, err := f.Output(name, args...)
	return err
}
func (f *resetInstallationFixture) Output(name string, args ...string) ([]byte, error) {
	return f.OutputContext(context.Background(), name, args...)
}
func (f *resetInstallationFixture) OutputContext(_ context.Context, name string, args ...string) ([]byte, error) {
	command := strings.Join(args, " ")
	if name == "pgrep" || name == "lsof" {
		return nil, installer.ErrServiceAbsent
	}
	if name != "launchctl" && name != "systemctl" {
		return nil, fmt.Errorf("unexpected OS command %s %v", name, args)
	}
	if strings.Contains(command, "print ") || strings.Contains(command, " show ") {
		if !f.loaded {
			if runtime.GOOS == "linux" {
				return []byte("LoadState=not-found\n"), nil
			}
			return nil, installer.ErrServiceAbsent
		}
		pid := f.child.Process.Pid
		if runtime.GOOS == "darwin" {
			return []byte(fmt.Sprintf("program = %s\narguments = {\n%s\ndaemon\n}\nstate = running\npid = %d\n", f.bin, f.bin, pid)), nil
		}
		return []byte(fmt.Sprintf("LoadState=loaded\nActiveState=active\nMainPID=%d\nExecStart={ path=%s ; argv[]=%s daemon ; }\n", pid, f.bin, f.bin)), nil
	}
	if strings.HasPrefix(command, "bootout ") || strings.Contains(command, " stop ") {
		f.stopChild()
		f.loaded = false
		return nil, nil
	}
	if strings.HasPrefix(command, "bootstrap ") || strings.Contains(command, " restart ") || strings.Contains(command, " enable --now ") {
		f.child = exec.Command(f.bin, "daemon")
		f.child.Env = os.Environ()
		if err := f.child.Start(); err != nil {
			return nil, err
		}
		f.done = make(chan struct{})
		child, done := f.child, f.done
		go func() { _ = child.Wait(); close(done) }()
		f.loaded = true
		return []byte(strconv.Itoa(f.child.Process.Pid)), nil
	}
	return nil, nil
}

func TestResetFixtureSystemdEnableStartsDaemon(t *testing.T) {
	fixture := newResetInstallationFixture(t)
	if err := fixture.Run("systemctl", "--user", "enable", "--now", "agent-gate.service"); err != nil {
		t.Fatal(err)
	}
	if fixture.child == nil {
		t.Fatal("systemctl enable --now did not start the fixture daemon")
	}
	if err := waitForInstalledDaemon(fixture.bin); err != nil {
		t.Fatal(err)
	}
}

func TestResetReinstallsAndPreservesConfiguration(t *testing.T) {
	fixture := newResetInstallationFixture(t)
	fixture.InstallUsingExistingInstaller()
	fixture.SeedAuditAndInstallationState()
	before := fixture.ReadPreservedFiles()
	if code := fixture.RunReset(); code != 0 {
		t.Fatalf("reset exit = %d", code)
	}
	fixture.AssertPreservedFilesEqual(before)
	fixture.AssertOldAuditAndStateAbsent()
	fixture.AssertExecutableRestoredAndRunnable()
	fixture.AssertServiceReadyWithNewProcess()
	fixture.oldPID = fixture.child.Process.Pid
	if code := fixture.RunReset(); code != 0 {
		t.Fatalf("repeated reset = %d", code)
	}
	fixture.AssertServiceReadyWithNewProcess()
	fixture.AssertPreservedFilesEqual(before)
	fixture.stopChild()
	fixture.loaded = false
	if err := os.Remove(config.DaemonSocketPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: config.DaemonSocketPath(), Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	listener.SetUnlinkOnClose(false)
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	if code := fixture.RunReset(); code != 0 {
		t.Fatalf("reset stale socket = %d", code)
	}
	fixture.AssertServiceReadyWithNewProcess()
	fixture.SeedAuditAndInstallationState()
	ready := fixture.install.waitForReady
	fixture.install.waitForReady = func(string) error { return errors.New("injected readiness failure") }
	if code := fixture.RunReset(); code == 0 {
		t.Fatal("readiness failure reported success")
	}
	fixture.AssertExecutableRestoredAndRunnable()
	fixture.AssertPreservedFilesEqual(before)
	fixture.AssertOldAuditAndStateAbsent()
	fixture.install.waitForReady = ready
	if code := fixture.RunReset(); code != 0 {
		t.Fatalf("retry after readiness failure = %d", code)
	}
	fixture.AssertServiceReadyWithNewProcess()
}

func TestResetRejectsArgumentsBeforeMutation(t *testing.T) {
	dependencies := resetDependencies{resolveExecutable: func() (string, error) { t.Fatal("reset started mutation preparation"); return "", nil }}
	if code := runResetWithDependencies([]string{"extra"}, dependencies); code != 2 {
		t.Fatalf("extra argument exit = %d", code)
	}
	if code := runCLIWithHook([]string{"reset", "extra"}, &bytes.Buffer{}, &bytes.Buffer{}, func(hookRoute) int { t.Fatal("reset routed to hook"); return 0 }); code != 2 {
		t.Fatalf("routing exit = %d", code)
	}
}
