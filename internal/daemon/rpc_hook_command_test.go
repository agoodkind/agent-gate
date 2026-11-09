package daemon_test

import (
	"bytes"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"goodkind.io/agent-gate/internal/daemon"
)

func TestManagedHookRejectsOversizedInput(t *testing.T) {
	newRPCHarness(t, 4, 2000)
	binary := filepath.Join(t.TempDir(), "agent-gate")
	build := exec.CommandContext(t.Context(), "go", "build", "-o", binary, "../../cmd/agent-gate")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build agent-gate: %v: %s", err, output)
	}

	payload := rpcPayload("hook-boundary", "forbidden")
	command := exec.CommandContext(t.Context(), binary, "managed-hook", "claude")
	command.Stdin = bytes.NewReader(payload)
	output, err := command.CombinedOutput()
	if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 2 ||
		!bytes.Contains(output, []byte("rpc-block")) {
		t.Fatalf("small input did not trigger enforcement: %v: %s", err, output)
	}

	oversized := append(
		payload,
		bytes.Repeat([]byte(" "), daemon.RPCMaxMessageBytes+1-len(payload))...,
	)
	command = exec.CommandContext(t.Context(), binary, "managed-hook", "claude")
	command.Stdin = bytes.NewReader(oversized)
	output, err = command.CombinedOutput()
	if err != nil {
		t.Fatalf("oversized input exit: %v: %s", err, output)
	}

	if !strings.Contains(string(output), "no rule was enforced") ||
		!strings.Contains(string(output), "hook input exceeds") {
		t.Fatalf("fail-open response = %q", output)
	}
}
