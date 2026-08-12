package installer

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"
)

type serviceStatusRunner struct {
	output []byte
	err    error
	calls  []string
}

type blockingServiceStatusRunner struct {
	entered chan struct{}
	release chan struct{}
}

func (runner *blockingServiceStatusRunner) OutputContext(
	ctx context.Context,
	_ string,
	_ ...string,
) ([]byte, error) {
	close(runner.entered)
	select {
	case <-runner.release:
		return nil, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (runner *serviceStatusRunner) OutputContext(
	_ context.Context,
	name string,
	args ...string,
) ([]byte, error) {
	runner.calls = append(runner.calls, strings.TrimSpace(name+" "+strings.Join(args, " ")))
	return runner.output, runner.err
}

func TestFullCompactLaunchdStatusConfirmsManagedRunningBinary(t *testing.T) {
	runner := &serviceStatusRunner{output: []byte(`gui/501/io.goodkind.agent-gate = {
	state = running
	program = /opt/agent-gate
	arguments = {
		/opt/agent-gate
		daemon
	}
}`)}
	state, err := InspectService(t.Context(), ServiceStatusOptions{
		Platform: "darwin", BinaryPath: "/opt/agent-gate", UserID: 501, Runner: runner,
	})
	if err != nil {
		t.Fatalf("InspectService: %v", err)
	}
	want := ServiceState{
		Platform: "launchd", Managed: true, Running: true, BinaryPath: "/opt/agent-gate",
	}
	if !reflect.DeepEqual(state, want) {
		t.Fatalf("state = %#v, want %#v", state, want)
	}
	wantCalls := []string{"launchctl print gui/501/io.goodkind.agent-gate"}
	if !reflect.DeepEqual(runner.calls, wantCalls) {
		t.Fatalf("calls = %v, want %v", runner.calls, wantCalls)
	}
}

func TestFullCompactSystemdStatusConfirmsManagedStoppedBinary(t *testing.T) {
	runner := &serviceStatusRunner{output: []byte(
		"LoadState=loaded\nActiveState=inactive\n" +
			"ExecStart={ path=/opt/agent-gate ; argv[]=/opt/agent-gate daemon ; }\n",
	)}
	state, err := InspectService(t.Context(), ServiceStatusOptions{
		Platform: "linux", BinaryPath: "/opt/agent-gate", Runner: runner,
	})
	if err != nil {
		t.Fatalf("InspectService: %v", err)
	}
	want := ServiceState{
		Platform: "systemd", Managed: true, Running: false, BinaryPath: "/opt/agent-gate",
	}
	if !reflect.DeepEqual(state, want) {
		t.Fatalf("state = %#v, want %#v", state, want)
	}
	wantCalls := []string{
		"systemctl --user show agent-gate.service --property=LoadState --property=ActiveState --property=ExecStart",
	}
	if !reflect.DeepEqual(runner.calls, wantCalls) {
		t.Fatalf("calls = %v, want %v", runner.calls, wantCalls)
	}
}

func TestFullCompactServiceStatusRejectsWrongRunningBinary(t *testing.T) {
	runner := &serviceStatusRunner{output: []byte(
		"LoadState=loaded\nActiveState=active\n" +
			"ExecStart={ path=/tmp/other ; argv[]=/tmp/other daemon ; }\n",
	)}
	_, err := InspectService(context.Background(), ServiceStatusOptions{
		Platform: "linux", BinaryPath: "/opt/agent-gate", Runner: runner,
	})
	if err == nil || !strings.Contains(err.Error(), "service binary") {
		t.Fatalf("InspectService error = %v, want service binary mismatch", err)
	}
}

func TestFullCompactServiceStatusRequiresDaemonArgument(t *testing.T) {
	runner := &serviceStatusRunner{output: []byte(
		"LoadState=loaded\nActiveState=active\n" +
			"ExecStart={ path=/opt/daemon/agent-gate ; argv[]=/opt/daemon/agent-gate ; }\n",
	)}
	_, err := InspectService(t.Context(), ServiceStatusOptions{
		Platform: "linux", BinaryPath: "/opt/daemon/agent-gate", Runner: runner,
	})
	if err == nil || !strings.Contains(err.Error(), "daemon") {
		t.Fatalf("InspectService error = %v, want missing daemon argument", err)
	}
}

func TestReviewServiceStatusCancellationStopsInspection(t *testing.T) {
	runner := &blockingServiceStatusRunner{entered: make(chan struct{}), release: make(chan struct{})}
	t.Cleanup(func() { close(runner.release) })
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		_, err := InspectService(ctx, ServiceStatusOptions{
			Platform: "darwin", BinaryPath: "/opt/agent-gate", UserID: 501, Runner: runner,
		})
		done <- err
	}()
	<-runner.entered
	cancel()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "canceled") {
			t.Fatalf("InspectService error = %v, want cancellation", err)
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("InspectService ignored cancellation while status command was running")
	}
}

func TestReviewLaunchdRequiresDaemonInArguments(t *testing.T) {
	runner := &serviceStatusRunner{output: []byte(`gui/501/io.goodkind.agent-gate = {
	state = running
	program = /opt/agent-gate
	arguments = {
		/opt/agent-gate
		serve
	}
	environment = {
		ROLE = daemon
	}
}`)}
	_, err := InspectService(t.Context(), ServiceStatusOptions{
		Platform: "darwin", BinaryPath: "/opt/agent-gate", UserID: 501, Runner: runner,
	})
	if err == nil || !strings.Contains(err.Error(), "daemon") {
		t.Fatalf("InspectService error = %v, want exact daemon argument rejection", err)
	}
}

func TestReviewSystemdRequiresDaemonInSelectedExecStart(t *testing.T) {
	runner := &serviceStatusRunner{output: []byte(
		"LoadState=loaded\nActiveState=active\n" +
			"ExecStart={ path=/opt/agent-gate ; argv[]=/opt/agent-gate serve ; note=daemon ; }\n",
	)}
	_, err := InspectService(t.Context(), ServiceStatusOptions{
		Platform: "linux", BinaryPath: "/opt/agent-gate", Runner: runner,
	})
	if err == nil || !strings.Contains(err.Error(), "daemon") {
		t.Fatalf("InspectService error = %v, want exact daemon argument rejection", err)
	}
}
