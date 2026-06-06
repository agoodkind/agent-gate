package exec

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	"goodkind.io/agent-gate/internal/config"
)

func TestOSRunnerReportsCleanExitCode(t *testing.T) {
	var runner OSRunner
	res, err := runner.Run(context.Background(), []string{"/bin/sh", "-c", "exit 3"}, time.Second, nil, nil)
	if err != nil {
		t.Fatalf("clean non-zero exit should not be an error: %v", err)
	}
	if res.ExitCode != 3 {
		t.Fatalf("expected exit code 3, got %d", res.ExitCode)
	}
}

func TestOSRunnerCapturesStdout(t *testing.T) {
	var runner OSRunner
	res, err := runner.Run(context.Background(), []string{"/bin/sh", "-c", "echo hello"}, time.Second, nil, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.ExitCode != 0 || firstLine(res.Stdout) != "hello" {
		t.Fatalf("unexpected result: %+v", res)
	}
}

func TestOSRunnerTimesOut(t *testing.T) {
	var runner OSRunner
	_, err := runner.Run(context.Background(), []string{"/bin/sh", "-c", "sleep 2"}, 50*time.Millisecond, nil, nil)
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("expected timeout error, got %v", err)
	}
}

func TestOSRunnerSpawnFailureIsError(t *testing.T) {
	var runner OSRunner
	_, err := runner.Run(context.Background(), []string{"/nonexistent/agent-gate-validator"}, time.Second, nil, nil)
	if err == nil {
		t.Fatalf("expected spawn failure error")
	}
}

func TestBuildRequestCarriesJSONAndEnv(t *testing.T) {
	in := Input{
		Event:        "PreToolUse",
		System:       "claude",
		ToolName:     "Bash",
		Rule:         "grep-codebase-approval",
		Command:      "grep -rn foo .",
		EffectiveCWD: PathView{Raw: "/raw/cwd", Canonical: "/real/cwd", IsCanonical: true},
		FilePath:     PathView{Raw: "/raw/file", Canonical: "/real/file", IsCanonical: true},
		CacheKey:     PathView{Raw: "/raw/cwd", Canonical: "/real/cwd", IsCanonical: true},
		Matched:      []FieldValue{{Field: "tool_input.command", Value: "grep -rn foo ."}},
	}

	stdin, env, err := BuildRequest(in)
	if err != nil {
		t.Fatalf("BuildRequest: %v", err)
	}

	var decoded Input
	if err := json.Unmarshal(stdin, &decoded); err != nil {
		t.Fatalf("stdin is not valid JSON: %v", err)
	}
	if decoded.Rule != "grep-codebase-approval" || decoded.EffectiveCWD.Canonical != "/real/cwd" {
		t.Fatalf("decoded JSON missing fields: %+v", decoded)
	}
	if decoded.CacheKey.IsCanonical != true {
		t.Fatalf("expected cache_key canonical flag preserved")
	}

	wantEnv := []string{
		"AGENT_GATE_EVENT=PreToolUse",
		"AGENT_GATE_SYSTEM=claude",
		"AGENT_GATE_TOOL=Bash",
		"AGENT_GATE_RULE=grep-codebase-approval",
		"AGENT_GATE_CWD=/real/cwd",
		"AGENT_GATE_FILE_PATH=/real/file",
	}
	for _, want := range wantEnv {
		if !slices.Contains(env, want) {
			t.Fatalf("env missing %q in %v", want, env)
		}
	}
}

func TestInterpretBlockOnNonzero(t *testing.T) {
	c := &config.Condition{Kind: string(config.ConditionKindExec), BlockOn: config.BlockOnNonzero, OnError: config.OnErrorOpen}

	if Interpret(c, RunResult{ExitCode: 0}, nil).Block {
		t.Fatalf("exit 0 should allow under nonzero policy")
	}
	if !Interpret(c, RunResult{ExitCode: 1}, nil).Block {
		t.Fatalf("exit 1 should block under nonzero policy")
	}
}

func TestInterpretBlockOnZero(t *testing.T) {
	c := &config.Condition{Kind: string(config.ConditionKindExec), BlockOn: config.BlockOnZero, OnError: config.OnErrorOpen}

	if !Interpret(c, RunResult{ExitCode: 0}, nil).Block {
		t.Fatalf("exit 0 should block under zero policy")
	}
	if Interpret(c, RunResult{ExitCode: 1}, nil).Block {
		t.Fatalf("exit 1 should allow under zero policy")
	}
}

func TestInterpretErrorPolicy(t *testing.T) {
	open := &config.Condition{Kind: string(config.ConditionKindExec), BlockOn: config.BlockOnNonzero, OnError: config.OnErrorOpen}
	closed := &config.Condition{Kind: string(config.ConditionKindExec), BlockOn: config.BlockOnNonzero, OnError: config.OnErrorClosed}

	openVerdict := Interpret(open, RunResult{}, errors.New("boom"))
	if openVerdict.Block || !openVerdict.Errored {
		t.Fatalf("open policy should not block on error: %+v", openVerdict)
	}
	closedVerdict := Interpret(closed, RunResult{}, errors.New("boom"))
	if !closedVerdict.Block || !closedVerdict.Errored {
		t.Fatalf("closed policy should block on error: %+v", closedVerdict)
	}
}

func TestInterpretMessageOverrideFromFirstStdoutLine(t *testing.T) {
	c := &config.Condition{Kind: string(config.ConditionKindExec), BlockOn: config.BlockOnNonzero, OnError: config.OnErrorOpen}

	verdict := Interpret(c, RunResult{ExitCode: 1, Stdout: "codebase X not approved\ndetail line\n"}, nil)

	if !verdict.Block {
		t.Fatalf("expected block")
	}
	if verdict.Message != "codebase X not approved" {
		t.Fatalf("expected first stdout line as message, got %q", verdict.Message)
	}
}
