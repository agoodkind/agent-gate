// Package exec implements the external-validator condition kind. A rule's cheap
// in-process conditions act as a pre-filter; when they all match, this package
// runs an operator-configured program synchronously and turns its exit code
// into a block/allow verdict. The program is the authoritative, runtime-dynamic
// decision that a static pattern cannot express, which keeps the gate false
// positive free: the rule blocks only when both the pattern matches and the
// script confirms.
package exec

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	osexec "os/exec"
	"strings"
	"time"

	"goodkind.io/agent-gate/internal/config"
)

// PathView carries both the absolute pre-symlink path and its canonical real
// path so the script can choose how strict to be. IsCanonical is false when the
// path could not be resolved (for example it does not exist).
type PathView struct {
	Raw         string `json:"raw"`
	Canonical   string `json:"canonical"`
	IsCanonical bool   `json:"is_canonical"`
}

// FieldValue is one matched field path and its extracted value.
type FieldValue struct {
	Field string `json:"field"`
	Value string `json:"value"`
}

// Input is the decision context handed to the validator. The engine builds it
// (including canonicalizing the path views) and this package serializes it.
type Input struct {
	Event        string       `json:"event"`
	System       string       `json:"system"`
	ToolName     string       `json:"tool_name"`
	Rule         string       `json:"rule"`
	Command      string       `json:"command"`
	EffectiveCWD PathView     `json:"effective_cwd"`
	FilePath     PathView     `json:"file_path"`
	CacheKey     PathView     `json:"cache_key"`
	Matched      []FieldValue `json:"matched"`
}

// RunResult is the outcome of a clean validator run (the process started and
// returned an exit status). Errors that prevent a clean status (timeout, spawn
// failure, signal) are reported as a non-nil error from Runner.Run instead.
type RunResult struct {
	ExitCode int
	Stdout   string
}

// Runner executes the validator command. It is an interface so tests can inject
// a deterministic fake in place of forking a real process.
type Runner interface {
	Run(ctx context.Context, command []string, timeout time.Duration, stdin []byte, env []string) (RunResult, error)
}

// Verdict is the interpreted gate decision for one validator run.
type Verdict struct {
	Block   bool
	Message string
	Errored bool
}

// ErrSignaled reports that the validator process was killed by a signal rather
// than returning a normal exit status.
var ErrSignaled = errors.New("exec validator killed by signal")

// ErrTimeout reports that the validator exceeded its timeout.
var ErrTimeout = errors.New("exec validator timed out")

// BuildRequest serializes the decision context into the stdin JSON payload and
// the AGENT_GATE_* convenience env vars. Path env vars use the canonical real
// path so a script comparing against an allowlist sees the true target.
func BuildRequest(in Input) (stdin []byte, env []string, err error) {
	stdin, err = json.Marshal(in)
	if err != nil {
		return nil, nil, errors.New("marshal exec validator request: " + err.Error())
	}
	env = []string{
		"AGENT_GATE_EVENT=" + in.Event,
		"AGENT_GATE_SYSTEM=" + in.System,
		"AGENT_GATE_TOOL=" + in.ToolName,
		"AGENT_GATE_RULE=" + in.Rule,
		"AGENT_GATE_COMMAND=" + in.Command,
		"AGENT_GATE_CWD=" + in.EffectiveCWD.Canonical,
		"AGENT_GATE_FILE_PATH=" + in.FilePath.Canonical,
		"AGENT_GATE_CACHE_KEY=" + in.CacheKey.Canonical,
	}
	return stdin, env, nil
}

// Interpret maps a run outcome to a gate verdict per the condition's block_on
// and on_error policy. A non-nil runErr is the error path: the gate blocks only
// when on_error is closed, and the outcome is marked Errored so the caller never
// caches it. A clean run blocks per block_on, and a blocking run adopts the
// first stdout line as its message when the script provided one.
func Interpret(c *config.Condition, res RunResult, runErr error) Verdict {
	if runErr != nil {
		return Verdict{
			Block:   c.OnError == config.OnErrorClosed,
			Message: "",
			Errored: true,
		}
	}
	block := exitBlocks(c.BlockOn, res.ExitCode)
	message := ""
	if block {
		message = firstLine(res.Stdout)
	}
	return Verdict{Block: block, Message: message, Errored: false}
}

// exitBlocks reports whether exitCode blocks under policy. The nonzero policy
// blocks on any clean non-zero exit (exit 0 allows); the zero policy inverts it.
func exitBlocks(policy string, exitCode int) bool {
	if policy == config.BlockOnZero {
		return exitCode == 0
	}
	return exitCode != 0
}

func firstLine(s string) string {
	trimmed := strings.TrimLeft(s, "\r\n")
	if idx := strings.IndexAny(trimmed, "\r\n"); idx >= 0 {
		return strings.TrimSpace(trimmed[:idx])
	}
	return strings.TrimSpace(trimmed)
}

// OSRunner is the production Runner. It forks the command with no shell, feeds
// the stdin payload, bounds the run with a context timeout, and translates a
// clean non-zero exit into a RunResult while reporting timeout, spawn failure,
// and signal kills as errors.
type OSRunner struct{}

// Run executes command with a timeout, returning a clean exit status as a
// RunResult and a deadline, start failure, or signal kill as an error.
func (OSRunner) Run(ctx context.Context, command []string, timeout time.Duration, stdin []byte, env []string) (RunResult, error) {
	if len(command) == 0 {
		return RunResult{}, errors.New("exec validator command is empty")
	}
	runCtx := ctx
	var cancel context.CancelFunc
	if timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	cmd := osexec.CommandContext(runCtx, command[0], command[1:]...)
	cmd.Stdin = bytes.NewReader(stdin)
	cmd.Env = append(os.Environ(), env...)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = io.Discard

	err := cmd.Run()
	out := stdout.String()
	if runCtx.Err() == context.DeadlineExceeded {
		return RunResult{ExitCode: 0, Stdout: out}, ErrTimeout
	}
	if err != nil {
		var exitErr *osexec.ExitError
		if errors.As(err, &exitErr) {
			code := exitErr.ExitCode()
			if code < 0 {
				return RunResult{ExitCode: 0, Stdout: out}, ErrSignaled
			}
			return RunResult{ExitCode: code, Stdout: out}, nil
		}
		return RunResult{ExitCode: 0, Stdout: out}, errors.New("start exec validator: " + err.Error())
	}
	return RunResult{ExitCode: 0, Stdout: out}, nil
}
