package installer

import (
	"bufio"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestResetStopsSymlinkDaemonWithAbsentService(t *testing.T) {
	options := resetTestOptions(t)
	alias := filepath.Join(filepath.Dir(options.ExecutablePath), "daemon-link")
	if err := os.Symlink(options.ExecutablePath, alias); err != nil {
		t.Fatal(err)
	}
	child := exec.Command(alias, "daemon")
	stdout, err := child.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = child.Process.Kill(); _ = child.Wait() })
	if line, err := bufio.NewReader(stdout).ReadString('\n'); err != nil || line != "ready\n" {
		t.Fatalf("child readiness: %q %v", line, err)
	}
	identity, err := (NativeProcessControl{}).Inspect(child.Process.Pid)
	if err != nil || identity.Executable != options.ExecutablePath {
		t.Fatalf("canonical identity: %+v %v", identity, err)
	}
	options.Control.Runner = resetTestRunner(func(name string, args ...string) ([]byte, error) {
		if name == "pgrep" || name == "lsof" {
			return (ExecRunner{}).OutputContext(context.Background(), name, args...)
		}
		return nil, ErrServiceAbsent
	})
	plan, err := PrepareReset(options)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("verified symlink child PID=%d; captured reset identities=%d", identity.PID, len(plan.processes))
	if err := ApplyReset(t.Context(), plan); err != nil {
		t.Fatal(err)
	}
	_, auditErr := os.Stat(options.Audit.BasePath)
	after, processErr := (NativeProcessControl{}).Inspect(child.Process.Pid)
	if !errors.Is(auditErr, os.ErrNotExist) {
		t.Fatalf("audit remains: %v", auditErr)
	}
	if processErr == nil && after == identity {
		t.Fatal("reset deleted audit while verified symlink-launched daemon remained alive")
	}
	if !errors.Is(processErr, os.ErrNotExist) {
		t.Fatalf("daemon exit unproven: %v", processErr)
	}
}
