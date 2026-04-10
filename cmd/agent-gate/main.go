package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/agoodkind/agent-gate/internal/audit"
	"github.com/agoodkind/agent-gate/internal/config"
	"github.com/agoodkind/agent-gate/internal/hook"
)

func main() {
	// Fail closed: any unrecovered panic exits 2, blocking the pending action.
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(os.Stderr, "hookguard: panic: %v\n", r)
			os.Exit(2)
		}
	}()

	os.Exit(run())
}

// run contains all logic so that deferred cleanup (logger.Close) works correctly
// and the exit code can be returned as a value rather than called directly.
func run() int {
	// Both Claude and Cursor send a JSON object on stdin.
	data, err := io.ReadAll(os.Stdin)
	if err != nil {
		fmt.Fprintf(os.Stderr, "hookguard: read stdin: %v\n", err)
		return 2
	}

	// Tolerate empty stdin (e.g. manual invocation during testing).
	if len(data) == 0 {
		fmt.Fprintln(os.Stderr, "hookguard: empty stdin — nothing to process")
		return 0
	}

	var raw hook.RawPayload
	if err := json.Unmarshal(data, &raw); err != nil {
		fmt.Fprintf(os.Stderr, "hookguard: parse stdin JSON: %v\n", err)
		return 2
	}

	// Load config from the XDG path, writing defaults on first run.
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "hookguard: load config: %v\n", err)
		return 2
	}

	// Open the audit log (creates directories if needed).
	logger, err := audit.New(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "hookguard: open audit logger: %v\n", err)
		return 2
	}
	defer logger.Close()

	// Dispatch to the hook handler.
	stdout, stderr, exitCode := hook.Handle(raw, cfg, logger)

	if len(stdout) > 0 {
		if _, err := os.Stdout.Write(stdout); err != nil {
			fmt.Fprintf(os.Stderr, "hookguard: write stdout: %v\n", err)
		}
	}
	if len(stderr) > 0 {
		if _, err := os.Stderr.Write(stderr); err != nil {
			// Nothing useful we can do here — stderr itself is failing.
			_ = err
		}
	}

	return exitCode
}
