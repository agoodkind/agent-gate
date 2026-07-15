package main

import (
	"io"
	"os"
	"strings"
	"testing"
)

func TestRunInstallRequiresBinPath(t *testing.T) {
	testCases := []struct {
		name string
		args []string
		want string
	}{
		{
			name: "hooks",
			args: []string{"hooks"},
			want: "usage: agent-gate install hooks --bin-path PATH",
		},
		{
			name: "service",
			args: []string{"service"},
			want: "usage: agent-gate install service --bin-path PATH",
		},
		{
			name: "all",
			args: []string{"all"},
			want: "usage: agent-gate install all --bin-path PATH",
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			exitCode, stderr := captureInstallStderr(t, testCase.args)
			if exitCode != 2 {
				t.Fatalf("exitCode = %d, want 2", exitCode)
			}
			if !strings.Contains(stderr, testCase.want) {
				t.Fatalf("stderr = %q, want %q", stderr, testCase.want)
			}
		})
	}
}

func captureInstallStderr(t *testing.T, args []string) (int, string) {
	t.Helper()
	readPipe, writePipe, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	defer func() {
		_ = readPipe.Close()
	}()

	originalStderr := os.Stderr
	os.Stderr = writePipe
	exitCode := runInstall(args)
	_ = writePipe.Close()
	os.Stderr = originalStderr

	output, err := io.ReadAll(readPipe)
	if err != nil {
		t.Fatalf("ReadAll stderr: %v", err)
	}
	return exitCode, string(output)
}
