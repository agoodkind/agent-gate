package rules_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/hotkv"
	"goodkind.io/agent-gate/internal/rules"
	execconcern "goodkind.io/agent-gate/internal/rules/concerns/exec"
)

type runnerResponse struct {
	res execconcern.RunResult
	err error
}

// countingRunner records how often the validator forked and returns scripted
// responses in order (the last repeats), so tests assert fork counts without
// spawning a real process.
type countingRunner struct {
	mu        sync.Mutex
	calls     int
	responses []runnerResponse
}

func newCountingRunner(res execconcern.RunResult, err error) *countingRunner {
	return &countingRunner{responses: []runnerResponse{{res: res, err: err}}}
}

func (r *countingRunner) Run(_ context.Context, _ []string, _ time.Duration, _ []byte, _ []string) (execconcern.RunResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	idx := r.calls
	r.calls++
	if idx >= len(r.responses) {
		idx = len(r.responses) - 1
	}
	resp := r.responses[idx]
	return resp.res, resp.err
}

func (r *countingRunner) Calls() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

// slowRunner blocks for a fixed delay before answering, so a test can hold a
// validator past the rule's synchronous budget without a real process.
type slowRunner struct {
	mu    sync.Mutex
	calls int
	delay time.Duration
	res   execconcern.RunResult
}

func (r *slowRunner) Run(ctx context.Context, _ []string, _ time.Duration, _ []byte, _ []string) (execconcern.RunResult, error) {
	r.mu.Lock()
	r.calls++
	r.mu.Unlock()
	select {
	case <-time.After(r.delay):
		return r.res, nil
	case <-ctx.Done():
		return execconcern.RunResult{}, ctx.Err()
	}
}

func (r *slowRunner) Calls() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

type commandRunnerFunc func([]string) (execconcern.RunResult, error)

type recordingCommandRunner struct {
	mu       sync.Mutex
	calls    int
	commands [][]string
	run      commandRunnerFunc
}

func (r *recordingCommandRunner) Run(_ context.Context, command []string, _ time.Duration, _ []byte, _ []string) (execconcern.RunResult, error) {
	r.mu.Lock()
	r.calls++
	copied := make([]string, len(command))
	copy(copied, command)
	r.commands = append(r.commands, copied)
	run := r.run
	r.mu.Unlock()
	if run == nil {
		return execconcern.RunResult{}, nil
	}
	return run(copied)
}

func (r *recordingCommandRunner) Calls() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

func (r *recordingCommandRunner) Commands() [][]string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([][]string, 0, len(r.commands))
	for _, command := range r.commands {
		copied := make([]string, len(command))
		copy(copied, command)
		out = append(out, copied)
	}
	return out
}

type recordingInputRunner struct {
	mu     sync.Mutex
	inputs []execconcern.Input
}

func (r *recordingInputRunner) Run(
	_ context.Context,
	_ []string,
	_ time.Duration,
	stdin []byte,
	_ []string,
) (execconcern.RunResult, error) {
	var input execconcern.Input
	if err := json.Unmarshal(stdin, &input); err != nil {
		return execconcern.RunResult{}, err
	}
	r.mu.Lock()
	r.inputs = append(r.inputs, input)
	r.mu.Unlock()
	return execconcern.RunResult{ExitCode: 1}, nil
}

func (r *recordingInputRunner) LastInput() execconcern.Input {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.inputs[len(r.inputs)-1]
}

func (r *recordingInputRunner) Inputs() []execconcern.Input {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]execconcern.Input(nil), r.inputs...)
}

type temporalEchoRunner struct {
	mu       sync.Mutex
	calls    int
	exitCode int
}

func (r *temporalEchoRunner) Run(
	_ context.Context,
	_ []string,
	_ time.Duration,
	stdin []byte,
	_ []string,
) (execconcern.RunResult, error) {
	var input execconcern.Input
	if err := json.Unmarshal(stdin, &input); err != nil {
		return execconcern.RunResult{}, err
	}
	for _, field := range input.Matched {
		if field.Field != "last_user_message" {
			continue
		}
		r.mu.Lock()
		r.calls++
		r.mu.Unlock()
		return execconcern.RunResult{
			ExitCode: r.exitCode,
			Stdout:   field.Value,
		}, nil
	}
	return execconcern.RunResult{}, fmt.Errorf("last_user_message missing from validator input")
}

func (r *temporalEchoRunner) Calls() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

type blockingFieldEchoRunner struct {
	mu      sync.Mutex
	calls   int
	field   string
	started chan struct{}
	release chan struct{}
}

func (r *blockingFieldEchoRunner) Run(
	_ context.Context,
	_ []string,
	_ time.Duration,
	stdin []byte,
	_ []string,
) (execconcern.RunResult, error) {
	var input execconcern.Input
	if err := json.Unmarshal(stdin, &input); err != nil {
		return execconcern.RunResult{}, err
	}
	for _, field := range input.Matched {
		if field.Field != r.field {
			continue
		}
		r.mu.Lock()
		r.calls++
		r.mu.Unlock()
		r.started <- struct{}{}
		<-r.release
		return execconcern.RunResult{
			ExitCode: 0,
			Stdout:   field.Value,
		}, nil
	}
	return execconcern.RunResult{}, fmt.Errorf("%s missing from validator input", r.field)
}

func (r *blockingFieldEchoRunner) Calls() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

func loadExecRule(t *testing.T, body string) config.Rule {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	cfg, err := config.LoadExisting(path)
	if err != nil {
		t.Fatalf("LoadExisting: %v", err)
	}
	return cfg.Rules[0]
}

func TestExecTemporalSelectorsAreOptInAndExplicit(t *testing.T) {
	rule := loadExecRule(t, `
[[rules]]
name = "temporal-response"
cursor_events = ["stop"]
action = "inject"
output = "configured fallback"

[[rules.conditions]]
kind = "exec"
command = ["/bin/validator"]
field_paths = [
    "status",
    "loop_count",
    "last_user_message",
    "last_response_output",
    "response_output",
]
cache_ttl_ms = 0
`)
	store := hotkv.New(hotkv.Options{PruneInterval: 0})
	defer store.Close()
	runner := &recordingInputRunner{mu: sync.Mutex{}, inputs: nil}
	runtime := rules.NewExecRuntimeWithCache(runner, nil, store)
	loopCount := 0
	fields := rules.FieldSet{
		ConversationID: "conversation-1",
		Status:         "completed",
		LoopCount:      &loopCount,
	}
	if !runtime.ObserveUserPrompt("cursor", fields, 10, "latest prompt") {
		t.Fatal("ObserveUserPrompt did not store the prompt")
	}
	if !runtime.ObserveResponseOutput(
		"cursor",
		fields,
		"stop",
		config.ActionInject,
		"context",
		9,
		"previous combined output",
	) {
		t.Fatal("ObserveResponseOutput did not store the response")
	}

	ctx := rules.WithExecRuntime(context.Background(), runtime)
	ctx = withExecResponseTarget(ctx, "context")
	rules.EvaluateAll(ctx, "cursor", "stop", fields, []config.Rule{rule}, nil)

	matched := runner.LastInput().Matched
	if len(matched) != 5 {
		t.Fatalf("matched = %#v, want five requested fields", matched)
	}
	want := []execconcern.FieldValue{
		{Field: "status", Value: "completed", Available: nil},
		{Field: "loop_count", Value: "0", Available: boolPointer(true)},
		{Field: "last_user_message", Value: "latest prompt", Available: boolPointer(true)},
		{
			Field: "last_response_output", Value: "previous combined output",
			Available: boolPointer(true),
		},
		{Field: "response_output", Value: "configured fallback", Available: boolPointer(true)},
	}
	if !reflect.DeepEqual(matched, want) {
		t.Fatalf("matched = %#v, want %#v", matched, want)
	}
}

func TestExecResponseOutputIsUnavailableWithoutConfiguredFallback(t *testing.T) {
	for _, action := range []string{string(config.ActionInject), string(config.ActionMutate)} {
		t.Run(action, func(t *testing.T) {
			rule := loadExecRule(t, fmt.Sprintf(`
[[rules]]
name = "exec-only-response"
events = ["stop"]
action = %q

[[rules.conditions]]
kind = "exec"
command = ["/bin/validator"]
field_paths = ["response_output"]
cache_ttl_ms = 0
`, action))
			runner := &recordingInputRunner{mu: sync.Mutex{}, inputs: nil}
			runtime := rules.NewExecRuntime(runner, nil)
			ctx := rules.WithExecRuntime(context.Background(), runtime)
			rules.EvaluateAll(
				ctx,
				"cursor",
				"stop",
				rules.FieldSet{ConversationID: "conversation-1"},
				[]config.Rule{rule},
				nil,
			)

			input := runner.LastInput()
			if len(input.Matched) != 1 {
				t.Fatalf("matched = %#v, want one requested field", input.Matched)
			}
			assertTemporalField(t, input.Matched[0], "response_output", "", false)
		})
	}
}

func boolPointer(value bool) *bool {
	return &value
}

func TestExecTemporalStateIsolationOrderingReloadAndRestart(t *testing.T) {
	rule := loadExecRule(t, `
[[rules]]
name = "temporal-response"
events = ["stop", "postToolUse"]
action = "inject"
output = "fallback"

[[rules.conditions]]
kind = "exec"
command = ["/bin/validator"]
field_paths = ["last_user_message", "last_response_output"]
cache_ttl_ms = 0
`)
	store := hotkv.New(hotkv.Options{PruneInterval: 0})
	defer store.Close()
	runner := &recordingInputRunner{mu: sync.Mutex{}, inputs: nil}
	runtime := rules.NewExecRuntimeWithCache(runner, nil, store)
	fields := rules.FieldSet{ConversationID: "conversation-1"}

	if !runtime.ObserveUserPrompt("cursor", fields, 20, "new prompt") {
		t.Fatal("ObserveUserPrompt did not store new prompt")
	}
	if runtime.ObserveUserPrompt("cursor", fields, 10, "old prompt") {
		t.Fatal("older prompt replaced newer receipt")
	}
	if !runtime.ObserveResponseOutput(
		"cursor",
		fields,
		"stop",
		config.ActionInject,
		"context",
		20,
		"new response",
	) {
		t.Fatal("ObserveResponseOutput did not store new response")
	}
	if runtime.ObserveResponseOutput(
		"cursor",
		fields,
		"stop",
		config.ActionInject,
		"context",
		10,
		"old response",
	) {
		t.Fatal("older response replaced newer receipt")
	}

	type evaluationCase struct {
		name              string
		system            string
		eventName         string
		fields            rules.FieldSet
		rule              config.Rule
		target            string
		wantPrompt        string
		wantResponse      string
		promptAvailable   bool
		responseAvailable bool
	}
	mutateRule := rule
	mutateRule.Action = config.ActionMutate
	tests := []evaluationCase{
		{
			name: "matching identity and response channel", system: "cursor", eventName: "stop",
			fields: fields, rule: rule, target: "context",
			wantPrompt: "new prompt", wantResponse: "new response",
			promptAvailable: true, responseAvailable: true,
		},
		{
			name: "provider isolation", system: "claude", eventName: "stop",
			fields: fields, rule: rule, target: "context",
		},
		{
			name: "conversation isolation", system: "cursor", eventName: "stop",
			fields: rules.FieldSet{ConversationID: "conversation-2"},
			rule:   rule, target: "context",
		},
		{
			name: "event isolation", system: "cursor", eventName: "postToolUse",
			fields: fields, rule: rule, target: "context",
			wantPrompt: "new prompt", promptAvailable: true,
		},
		{
			name: "action independence", system: "cursor", eventName: "stop",
			fields: fields, rule: mutateRule, target: "context",
			wantPrompt: "new prompt", wantResponse: "new response",
			promptAvailable: true, responseAvailable: true,
		},
		{
			name: "target isolation", system: "cursor", eventName: "stop",
			fields: fields, rule: rule, target: "tool_output",
			wantPrompt: "new prompt", promptAvailable: true,
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			inputCount := len(runner.Inputs())
			ctx := rules.WithExecRuntime(context.Background(), runtime)
			ctx = withExecResponseTarget(ctx, testCase.target)
			rules.EvaluateAll(
				ctx,
				testCase.system,
				testCase.eventName,
				testCase.fields,
				[]config.Rule{testCase.rule},
				nil,
			)
			if len(runner.Inputs()) != inputCount+1 {
				t.Fatal("temporal exec condition did not run")
			}
			input := runner.LastInput()
			if len(input.Matched) != 2 {
				t.Fatalf("matched = %#v, want two temporal fields", input.Matched)
			}
			assertTemporalField(
				t,
				input.Matched[0],
				"last_user_message",
				testCase.wantPrompt,
				testCase.promptAvailable,
			)
			assertTemporalField(
				t,
				input.Matched[1],
				"last_response_output",
				testCase.wantResponse,
				testCase.responseAvailable,
			)
		})
	}

	reloadedRuntime := rules.NewExecRuntimeWithCache(runner, nil, store)
	ctx := rules.WithExecRuntime(context.Background(), reloadedRuntime)
	ctx = withExecResponseTarget(ctx, "context")
	rules.EvaluateAll(ctx, "cursor", "stop", fields, []config.Rule{rule}, nil)
	reloaded := runner.LastInput()
	assertTemporalField(t, reloaded.Matched[0], "last_user_message", "new prompt", true)
	assertTemporalField(t, reloaded.Matched[1], "last_response_output", "new response", true)

	freshStore := hotkv.New(hotkv.Options{PruneInterval: 0})
	defer freshStore.Close()
	freshRuntime := rules.NewExecRuntimeWithCache(runner, nil, freshStore)
	ctx = rules.WithExecRuntime(context.Background(), freshRuntime)
	ctx = withExecResponseTarget(ctx, "context")
	rules.EvaluateAll(ctx, "cursor", "stop", fields, []config.Rule{rule}, nil)
	restarted := runner.LastInput()
	assertTemporalField(t, restarted.Matched[0], "last_user_message", "", false)
	assertTemporalField(t, restarted.Matched[1], "last_response_output", "", false)

	sessionFields := rules.FieldSet{SessionID: "session-1"}
	if !runtime.ObserveUserPrompt("cursor", sessionFields, 30, "session prompt") {
		t.Fatal("session fallback did not store prompt")
	}
	ctx = rules.WithExecRuntime(context.Background(), runtime)
	ctx = withExecResponseTarget(ctx, "context")
	rules.EvaluateAll(ctx, "cursor", "stop", sessionFields, []config.Rule{rule}, nil)
	sessionInput := runner.LastInput()
	assertTemporalField(t, sessionInput.Matched[0], "last_user_message", "session prompt", true)
}

func TestExecTemporalReceiptOrderingAcrossReloadRuntimes(t *testing.T) {
	rule := loadExecRule(t, `
[[rules]]
name = "temporal-response"
events = ["stop"]
action = "inject"
output = "fallback"

[[rules.conditions]]
kind = "exec"
command = ["/bin/validator"]
field_paths = ["last_user_message"]
cache_ttl_ms = 0
`)
	store := hotkv.New(hotkv.Options{PruneInterval: 0})
	defer store.Close()
	runner := &recordingInputRunner{mu: sync.Mutex{}, inputs: nil}
	firstRuntime := rules.NewExecRuntimeWithCache(runner, nil, store)
	reloadedRuntime := rules.NewExecRuntimeWithCache(runner, nil, store)
	fields := rules.FieldSet{ConversationID: "conversation-1"}

	var waitGroup sync.WaitGroup
	for receiptID := int64(1); receiptID <= 500; receiptID++ {
		waitGroup.Add(1)
		runtime := firstRuntime
		if receiptID%2 == 0 {
			runtime = reloadedRuntime
		}
		go func() {
			defer waitGroup.Done()
			runtime.ObserveUserPrompt(
				"cursor",
				fields,
				receiptID,
				fmt.Sprintf("prompt-%d", receiptID),
			)
		}()
	}
	waitGroup.Wait()

	ctx := rules.WithExecRuntime(context.Background(), reloadedRuntime)
	rules.EvaluateAll(ctx, "cursor", "stop", fields, []config.Rule{rule}, nil)
	assertTemporalField(
		t,
		runner.LastInput().Matched[0],
		"last_user_message",
		"prompt-500",
		true,
	)
}

func TestExecTemporalStateBecomesUnavailableAtStorageBoundaries(t *testing.T) {
	rule := loadExecRule(t, `
[[rules]]
name = "temporal-response"
events = ["stop"]
action = "inject"
output = "fallback"

[[rules.conditions]]
kind = "exec"
command = ["/bin/validator"]
field_paths = ["last_user_message", "last_response_output"]
cache_ttl_ms = 0
`)
	fields := rules.FieldSet{ConversationID: "conversation-1"}

	boundedStore := hotkv.New(hotkv.Options{
		MaxEntries: 1, MaxValueBytes: hotkv.DefaultMaxValueBytes, PruneInterval: 0,
	})
	defer boundedStore.Close()
	runner := &recordingInputRunner{mu: sync.Mutex{}, inputs: nil}
	runtime := rules.NewExecRuntimeWithCache(runner, nil, boundedStore)
	if runtime.ObserveUserPrompt("cursor", rules.FieldSet{}, 1, "missing identity") {
		t.Fatal("prompt with missing identity was stored")
	}
	if runtime.ObserveResponseOutput(
		"cursor",
		fields,
		"stop",
		config.ActionInject,
		"",
		1,
		"missing target",
	) {
		t.Fatal("response with missing target was stored")
	}
	if !runtime.ObserveUserPrompt("cursor", fields, 2, "evicted prompt") {
		t.Fatal("prompt was not stored")
	}
	if !runtime.ObserveResponseOutput(
		"cursor",
		fields,
		"stop",
		config.ActionInject,
		"context",
		3,
		"retained response",
	) {
		t.Fatal("response was not stored")
	}
	ctx := rules.WithExecRuntime(context.Background(), runtime)
	ctx = withExecResponseTarget(ctx, "context")
	rules.EvaluateAll(ctx, "cursor", "stop", fields, []config.Rule{rule}, nil)
	evicted := runner.LastInput()
	assertTemporalField(t, evicted.Matched[0], "last_user_message", "", false)
	assertTemporalField(
		t,
		evicted.Matched[1],
		"last_response_output",
		"retained response",
		true,
	)

	oversizeStore := hotkv.New(hotkv.Options{
		MaxEntries: 2, MaxValueBytes: 16, PruneInterval: 0,
	})
	defer oversizeStore.Close()
	oversizeRuntime := rules.NewExecRuntimeWithCache(runner, nil, oversizeStore)
	if !oversizeRuntime.ObserveUserPrompt(
		"cursor",
		fields,
		4,
		"this prompt is larger than the store value bound",
	) {
		t.Fatal("oversized prompt did not write an unavailable tombstone")
	}
	ctx = rules.WithExecRuntime(context.Background(), oversizeRuntime)
	ctx = withExecResponseTarget(ctx, "context")
	rules.EvaluateAll(ctx, "cursor", "stop", fields, []config.Rule{rule}, nil)
	oversized := runner.LastInput()
	assertTemporalField(t, oversized.Matched[0], "last_user_message", "", false)
}

func assertTemporalField(
	t *testing.T,
	got execconcern.FieldValue,
	field string,
	value string,
	available bool,
) {
	t.Helper()
	if got.Field != field || got.Value != value || got.Available == nil ||
		*got.Available != available {
		t.Fatalf(
			"field = %#v, want field=%q value=%q available=%v",
			got,
			field,
			value,
			available,
		)
	}
}

func withExecResponseTarget(ctx context.Context, target string) context.Context {
	return rules.WithExecResponseTargetResolver(ctx, func(string) string {
		return target
	})
}

func execRuleTOML(extraExec string) string {
	return fmt.Sprintf(`
[[rules]]
name = "exec-rule"
events = ["PreToolUse"]
action = "block"
violation_message = "static message"

[[rules.conditions]]
kind = "regex"
field_paths = ["tool_input.command"]
pattern = "grepcode"

[[rules.conditions]]
kind = "exec"
command = ["/bin/true"]
%s
`, extraExec)
}

func evalRule(runner execconcern.Runner, rule config.Rule, payload map[string]any) []rules.Violation {
	runtime := rules.NewExecRuntime(runner, nil)
	ctx := rules.WithExecRuntime(context.Background(), runtime)
	return rules.EvaluateAll(ctx, "claude", "PreToolUse", testFields(payload), []config.Rule{rule}, nil)
}

func TestExecGateShortCircuitsWhenCheaperConditionFails(t *testing.T) {
	runner := newCountingRunner(execconcern.RunResult{ExitCode: 1}, nil)
	rule := loadExecRule(t, execRuleTOML("cache_ttl_ms = 0"))

	violations := evalRule(runner, rule, map[string]any{
		"tool_input": map[string]any{"command": "ls -la"},
	})

	if len(violations) != 0 {
		t.Fatalf("expected allow when regex does not match, got %d violations", len(violations))
	}
	if runner.Calls() != 0 {
		t.Fatalf("validator must not fork when a cheaper condition fails, forked %d times", runner.Calls())
	}
}

func TestExecGateForksOncePerEvent(t *testing.T) {
	runner := newCountingRunner(execconcern.RunResult{ExitCode: 1}, nil)
	rule := loadExecRule(t, `
[[rules]]
name = "exec-rule"
events = ["PreToolUse"]
action = "block"
violation_message = "static message"

[[rules.conditions]]
kind = "regex"
field_paths = ["tool_input.command"]
pattern = "grepcode"

[[rules.conditions]]
kind = "regex"
field_paths = ["tool_input.command"]
pattern = "src"

[[rules.conditions]]
kind = "exec"
command = ["/bin/true"]
cache_ttl_ms = 0
`)

	violations := evalRule(runner, rule, map[string]any{
		"tool_input": map[string]any{"command": "grepcode src"},
	})

	if len(violations) == 0 {
		t.Fatalf("expected block when exec returns nonzero")
	}
	if runner.Calls() != 1 {
		t.Fatalf("expected exactly one fork per event, got %d", runner.Calls())
	}
}

func TestExecExitCodeMapsToBlockAllow(t *testing.T) {
	rule := loadExecRule(t, execRuleTOML("cache_ttl_ms = 0"))
	payload := map[string]any{"tool_input": map[string]any{"command": "grepcode here"}}

	blocking := evalRule(newCountingRunner(execconcern.RunResult{ExitCode: 1}, nil), rule, payload)
	if len(blocking) == 0 {
		t.Fatalf("exit 1 should block under default block_on")
	}

	allowing := evalRule(newCountingRunner(execconcern.RunResult{ExitCode: 0}, nil), rule, payload)
	if len(allowing) != 0 {
		t.Fatalf("exit 0 should allow under default block_on, got %d", len(allowing))
	}
}

func TestExecBlockOnZeroInverts(t *testing.T) {
	rule := loadExecRule(t, execRuleTOML("block_on = \"zero\"\ncache_ttl_ms = 0"))
	payload := map[string]any{"tool_input": map[string]any{"command": "grepcode here"}}

	if len(evalRule(newCountingRunner(execconcern.RunResult{ExitCode: 0}, nil), rule, payload)) == 0 {
		t.Fatalf("exit 0 should block under block_on=zero")
	}
	if len(evalRule(newCountingRunner(execconcern.RunResult{ExitCode: 1}, nil), rule, payload)) != 0 {
		t.Fatalf("exit 1 should allow under block_on=zero")
	}
}

func TestExecOnErrorPolicies(t *testing.T) {
	payload := map[string]any{"tool_input": map[string]any{"command": "grepcode here"}}

	open := loadExecRule(t, execRuleTOML("on_error = \"open\"\ncache_ttl_ms = 0"))
	if len(evalRule(newCountingRunner(execconcern.RunResult{}, execconcern.ErrTimeout), open, payload)) != 0 {
		t.Fatalf("on_error=open should allow on timeout")
	}

	closed := loadExecRule(t, execRuleTOML("on_error = \"closed\"\ncache_ttl_ms = 0"))
	if len(evalRule(newCountingRunner(execconcern.RunResult{}, execconcern.ErrTimeout), closed, payload)) == 0 {
		t.Fatalf("on_error=closed should block on timeout")
	}
}

func TestExecNonTemporalMessageOverride(t *testing.T) {
	rule := loadExecRule(t, execRuleTOML("cache_ttl_ms = 0"))
	runner := newCountingRunner(execconcern.RunResult{ExitCode: 1, Stdout: "codebase X not approved\nmore detail\n"}, nil)

	violations := evalRule(runner, rule, map[string]any{
		"tool_input": map[string]any{"command": "grepcode here"},
	})

	if len(violations) == 0 {
		t.Fatalf("expected block")
	}
	for _, v := range violations {
		if v.Message != "codebase X not approved" {
			t.Fatalf("expected stdout message override, got %q", v.Message)
		}
	}
}

func TestExecTemporalEnforcementVerdictKeepsConfiguredMessage(t *testing.T) {
	const sentinel = "TEMPORAL_VIOLATION_SENTINEL"
	for _, action := range []string{config.ActionBlock, config.ActionAudit} {
		t.Run(action, func(t *testing.T) {
			rule := loadExecRule(t, fmt.Sprintf(`
[[rules]]
name = "temporal-enforcement"
cursor_events = ["stop"]
action = %q
violation_message = "static temporal message"

[[rules.conditions]]
kind = "exec"
command = ["/bin/validator"]
field_paths = ["last_user_message"]
cache_ttl_ms = 0
`, action))
			runner := &temporalEchoRunner{mu: sync.Mutex{}, calls: 0, exitCode: 1}
			runtime := rules.NewExecRuntime(runner, nil)
			fields := rules.FieldSet{ConversationID: "conversation-1"}
			if !runtime.ObserveUserPrompt("cursor", fields, 1, sentinel) {
				t.Fatal("ObserveUserPrompt did not store the temporal sentinel")
			}

			ctx := rules.WithExecRuntime(context.Background(), runtime)
			violations := rules.EvaluateAll(
				ctx,
				"cursor",
				"stop",
				fields,
				[]config.Rule{rule},
				nil,
			)
			if len(violations) != 1 {
				t.Fatalf("violations = %d, want one", len(violations))
			}
			if violations[0].Message != "static temporal message" {
				t.Fatalf("violation message = %q, want configured static message", violations[0].Message)
			}
			if strings.Contains(violations[0].Message, sentinel) {
				t.Fatalf("violation message contains temporal validator stdout")
			}
			wantAuditOnly := action == config.ActionAudit
			if violations[0].AuditOnly != wantAuditOnly {
				t.Fatalf("AuditOnly = %v, want %v", violations[0].AuditOnly, wantAuditOnly)
			}
		})
	}
}

func TestExecCrossEventCacheReuse(t *testing.T) {
	dir := t.TempDir()
	rule := loadExecRule(t, execRuleTOML("cache_ttl_ms = 60000"))
	runner := newCountingRunner(execconcern.RunResult{ExitCode: 1}, nil)
	runtime := rules.NewExecRuntime(runner, nil)
	ctx := rules.WithExecRuntime(context.Background(), runtime)
	payload := map[string]any{
		"effective_cwd": dir,
		"tool_input":    map[string]any{"command": "grepcode here"},
	}

	first := rules.EvaluateAll(ctx, "claude", "PreToolUse", testFields(payload), []config.Rule{rule}, nil)
	second := rules.EvaluateAll(ctx, "claude", "PreToolUse", testFields(payload), []config.Rule{rule}, nil)

	if len(first) == 0 || len(second) == 0 {
		t.Fatalf("expected both events to block")
	}
	if runner.Calls() != 1 {
		t.Fatalf("expected one fork served from cache, got %d", runner.Calls())
	}

	// A fresh runtime (as built on config reload) resets the cache and forks again.
	reloaded := rules.NewExecRuntime(runner, nil)
	reloadCtx := rules.WithExecRuntime(context.Background(), reloaded)
	rules.EvaluateAll(reloadCtx, "claude", "PreToolUse", testFields(payload), []config.Rule{rule}, nil)
	if runner.Calls() != 2 {
		t.Fatalf("expected a fresh runtime to fork again, got %d", runner.Calls())
	}
}

func TestExecCachedTemporalVerdictUsesInternalNamespace(t *testing.T) {
	const sentinel = "TEMPORAL_CACHE_SENTINEL"
	rule := loadExecRule(t, `
[[rules]]
name = "temporal-cache"
cursor_events = ["stop"]
action = "block"
violation_message = "static message"

[[rules.conditions]]
kind = "exec"
command = ["/bin/validator"]
field_paths = ["last_user_message"]
cache_ttl_ms = 60000
`)
	store := hotkv.New(hotkv.Options{PruneInterval: 0})
	defer store.Close()
	runner := &temporalEchoRunner{mu: sync.Mutex{}, calls: 0, exitCode: 1}
	runtime := rules.NewExecRuntimeWithCache(runner, nil, store)
	fields := rules.FieldSet{ConversationID: "conversation-1"}
	if !runtime.ObserveUserPrompt("cursor", fields, 1, sentinel) {
		t.Fatal("ObserveUserPrompt did not store the temporal sentinel")
	}
	ctx := rules.WithExecRuntime(context.Background(), runtime)

	first := rules.EvaluateAll(ctx, "cursor", "stop", fields, []config.Rule{rule}, nil)
	second := rules.EvaluateAll(ctx, "cursor", "stop", fields, []config.Rule{rule}, nil)
	if len(first) == 0 || len(second) == 0 {
		t.Fatalf("cached temporal validator verdicts = (%d, %d) violations, want both blocking", len(first), len(second))
	}
	if runner.Calls() != 1 {
		t.Fatalf("temporal validator calls = %d, want one cached run", runner.Calls())
	}

	publicEntries, err := store.List("exec-validator", "", 0, true)
	if err != nil {
		t.Fatalf("List public exec-validator: %v", err)
	}
	if len(publicEntries) != 0 {
		t.Errorf("public exec-validator entries = %d, want none", len(publicEntries))
	}
	for _, entry := range publicEntries {
		if bytes.Contains(entry.Value, []byte(sentinel)) {
			t.Errorf("public exec-validator entry contains temporal sentinel")
		}
	}

	internalNamespace := hotkv.InternalNamespacePrefix + "exec-validator"
	if !hotkv.IsInternalNamespace(internalNamespace) {
		t.Fatalf("temporal cache namespace %q is not protected as internal", internalNamespace)
	}
	internalEntries, err := store.List(internalNamespace, "", 0, true)
	if err != nil {
		t.Fatalf("List internal exec-validator: %v", err)
	}
	if len(internalEntries) != 1 {
		t.Fatalf("internal exec-validator entries = %d, want one", len(internalEntries))
	}
	if bytes.Contains(internalEntries[0].Value, []byte(sentinel)) {
		t.Fatalf("internal enforcement cache entry contains temporal validator stdout")
	}
}

func TestExecTemporalResponseActionRetainsValidatorOutput(t *testing.T) {
	const sentinel = "TEMPORAL_RESPONSE_SENTINEL"
	rule := loadExecRule(t, `
[[rules]]
name = "temporal-response-cache"
cursor_events = ["stop"]
action = "inject"
output = "configured fallback"

[[rules.conditions]]
kind = "exec"
command = ["/bin/validator"]
field_paths = ["last_user_message"]
cache_ttl_ms = 60000
`)
	store := hotkv.New(hotkv.Options{PruneInterval: 0})
	defer store.Close()
	runner := &temporalEchoRunner{mu: sync.Mutex{}, calls: 0, exitCode: 0}
	runtime := rules.NewExecRuntimeWithCache(runner, nil, store)
	fields := rules.FieldSet{ConversationID: "conversation-1"}
	if !runtime.ObserveUserPrompt("cursor", fields, 1, sentinel) {
		t.Fatal("ObserveUserPrompt did not store the temporal sentinel")
	}
	ctx := rules.WithExecRuntime(context.Background(), runtime)
	ctx = withExecResponseTarget(ctx, "context")

	for evaluationIndex := 0; evaluationIndex < 2; evaluationIndex++ {
		detailed := rules.EvaluateAllDetailed(
			ctx,
			"cursor",
			"stop",
			fields,
			[]config.Rule{rule},
			nil,
			nil,
			"test",
		)
		if len(detailed.Violations) != 0 {
			t.Fatalf("response evaluation %d violations = %#v, want none", evaluationIndex, detailed.Violations)
		}
		if len(detailed.Effects) != 1 {
			t.Fatalf("response evaluation %d effects = %#v, want one", evaluationIndex, detailed.Effects)
		}
		if detailed.Effects[0].Output != sentinel {
			t.Fatalf(
				"response evaluation %d output = %q, want temporal validator stdout",
				evaluationIndex,
				detailed.Effects[0].Output,
			)
		}
	}
	if runner.Calls() != 1 {
		t.Fatalf("temporal response validator calls = %d, want one cached run", runner.Calls())
	}
}

func TestExecCachedNonTemporalVerdictUsesPublicNamespace(t *testing.T) {
	rule := loadExecRule(t, execRuleTOML("cache_ttl_ms = 60000"))
	store := hotkv.New(hotkv.Options{PruneInterval: 0})
	defer store.Close()
	runner := newCountingRunner(
		execconcern.RunResult{ExitCode: 1, Stdout: "non-temporal output"},
		nil,
	)
	runtime := rules.NewExecRuntimeWithCache(runner, nil, store)
	ctx := rules.WithExecRuntime(context.Background(), runtime)
	payload := testFields(map[string]any{
		"effective_cwd": t.TempDir(),
		"tool_input":    map[string]any{"command": "grepcode here"},
	})

	rules.EvaluateAll(ctx, "claude", "PreToolUse", payload, []config.Rule{rule}, nil)
	rules.EvaluateAll(ctx, "claude", "PreToolUse", payload, []config.Rule{rule}, nil)
	if runner.Calls() != 1 {
		t.Fatalf("non-temporal validator calls = %d, want one cached run", runner.Calls())
	}
	publicEntries, err := store.List("exec-validator", "", 0, true)
	if err != nil {
		t.Fatalf("List public exec-validator: %v", err)
	}
	if len(publicEntries) != 1 {
		t.Fatalf("public exec-validator entries = %d, want one", len(publicEntries))
	}
	internalEntries, err := store.List(
		hotkv.InternalNamespacePrefix+"exec-validator",
		"",
		0,
		true,
	)
	if err != nil {
		t.Fatalf("List internal exec-validator: %v", err)
	}
	if len(internalEntries) != 0 {
		t.Fatalf("internal exec-validator entries = %d, want none", len(internalEntries))
	}
}

func TestExecCacheTTLOneSecondReusesHotMemory(t *testing.T) {
	dir := t.TempDir()
	rule := loadExecRule(t, execRuleTOML("cache_ttl_ms = 1000"))
	runner := newCountingRunner(execconcern.RunResult{ExitCode: 1}, nil)
	runtime := rules.NewExecRuntime(runner, nil)
	ctx := rules.WithExecRuntime(context.Background(), runtime)
	payload := map[string]any{
		"effective_cwd": dir,
		"tool_input":    map[string]any{"command": "grepcode here"},
	}

	first := rules.EvaluateAll(ctx, "claude", "PreToolUse", testFields(payload), []config.Rule{rule}, nil)
	second := rules.EvaluateAll(ctx, "claude", "PreToolUse", testFields(payload), []config.Rule{rule}, nil)
	if len(first) == 0 || len(second) == 0 {
		t.Fatalf("expected both one-second TTL evaluations to block")
	}
	if runner.Calls() != 1 {
		t.Fatalf("expected one-second hot cache TTL to reuse the validator result, got %d calls", runner.Calls())
	}
}

func TestExecErrorOutcomeNotCached(t *testing.T) {
	dir := t.TempDir()
	rule := loadExecRule(t, execRuleTOML("on_error = \"open\"\ncache_ttl_ms = 60000"))
	runner := newCountingRunner(execconcern.RunResult{}, nil)
	runner.responses = []runnerResponse{
		{res: execconcern.RunResult{}, err: execconcern.ErrTimeout},
		{res: execconcern.RunResult{ExitCode: 0}, err: nil},
	}
	runtime := rules.NewExecRuntime(runner, nil)
	ctx := rules.WithExecRuntime(context.Background(), runtime)
	payload := map[string]any{
		"effective_cwd": dir,
		"tool_input":    map[string]any{"command": "grepcode here"},
	}

	rules.EvaluateAll(ctx, "claude", "PreToolUse", testFields(payload), []config.Rule{rule}, nil)
	rules.EvaluateAll(ctx, "claude", "PreToolUse", testFields(payload), []config.Rule{rule}, nil)

	if runner.Calls() != 2 {
		t.Fatalf("error outcome must not be cached, expected 2 forks, got %d", runner.Calls())
	}
}

func TestExecRetryCountRetriesErroredAttemptWithinBudget(t *testing.T) {
	rule := loadExecRule(t, execRuleTOML("retry_count = 1\ncache_ttl_ms = 0"))
	runner := newCountingRunner(execconcern.RunResult{}, nil)
	runner.responses = []runnerResponse{
		{res: execconcern.RunResult{}, err: execconcern.ErrTimeout},
		{res: execconcern.RunResult{ExitCode: 1}, err: nil},
	}
	payload := map[string]any{"tool_input": map[string]any{"command": "grepcode here"}}

	violations := evalRule(runner, rule, payload)
	if len(violations) == 0 {
		t.Fatalf("retry should recover the block from the second attempt")
	}
	if runner.Calls() != 2 {
		t.Fatalf("retry_count=1 should fork twice on a first errored attempt, got %d", runner.Calls())
	}
}

func TestExecRetryCountExhaustsThenAppliesOnError(t *testing.T) {
	openRule := loadExecRule(t, execRuleTOML("retry_count = 1\non_error = \"open\"\ncache_ttl_ms = 0"))
	payload := map[string]any{"tool_input": map[string]any{"command": "grepcode here"}}

	runner := newCountingRunner(execconcern.RunResult{}, execconcern.ErrTimeout)
	if len(evalRule(runner, openRule, payload)) != 0 {
		t.Fatalf("two errored attempts under on_error=open should allow")
	}
	if runner.Calls() != 2 {
		t.Fatalf("retry_count=1 should exhaust both attempts before failing open, got %d", runner.Calls())
	}
}

func TestExecRetryCountZeroDoesNotRetry(t *testing.T) {
	rule := loadExecRule(t, execRuleTOML("on_error = \"open\"\ncache_ttl_ms = 0"))
	runner := newCountingRunner(execconcern.RunResult{}, execconcern.ErrTimeout)
	payload := map[string]any{"tool_input": map[string]any{"command": "grepcode here"}}

	evalRule(runner, rule, payload)
	if runner.Calls() != 1 {
		t.Fatalf("default retry_count=0 must fork once, got %d", runner.Calls())
	}
}

func TestExecCanonicalCacheKeySharedAcrossSymlinkAliases(t *testing.T) {
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(real, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	rule := loadExecRule(t, execRuleTOML("cache_ttl_ms = 60000"))
	runner := newCountingRunner(execconcern.RunResult{ExitCode: 1}, nil)
	runtime := rules.NewExecRuntime(runner, nil)
	ctx := rules.WithExecRuntime(context.Background(), runtime)

	viaLink := rules.EvaluateAll(ctx, "claude", "PreToolUse", testFields(map[string]any{
		"effective_cwd": link,
		"tool_input":    map[string]any{"command": "grepcode here"},
	}), []config.Rule{rule}, nil)
	viaReal := rules.EvaluateAll(ctx, "claude", "PreToolUse", testFields(map[string]any{
		"effective_cwd": real,
		"tool_input":    map[string]any{"command": "grepcode here"},
	}), []config.Rule{rule}, nil)

	if len(viaLink) == 0 || len(viaReal) == 0 {
		t.Fatalf("expected both aliases to block")
	}
	if runner.Calls() != 1 {
		t.Fatalf("symlink aliases should share one canonical cache entry, forked %d times", runner.Calls())
	}
}

func TestExecExpiredCacheRecomputesSynchronously(t *testing.T) {
	dir := t.TempDir()
	rule := loadExecRule(t, execRuleTOML("cache_ttl_ms = 1"))
	runner := &countingRunner{responses: []runnerResponse{
		{res: execconcern.RunResult{ExitCode: 1}}, // cold: block
		{res: execconcern.RunResult{ExitCode: 0}}, // expired: recompute and allow
	}}
	runtime := rules.NewExecRuntime(runner, nil)
	ctx := rules.WithExecRuntime(context.Background(), runtime)
	payload := map[string]any{
		"effective_cwd": dir,
		"tool_input":    map[string]any{"command": "grepcode here"},
	}

	cold := rules.EvaluateAll(ctx, "claude", "PreToolUse", testFields(payload), []config.Rule{rule}, nil)
	if len(cold) == 0 {
		t.Fatalf("cold event should block on the synchronous verdict")
	}
	if runner.Calls() != 1 {
		t.Fatalf("cold event should fork once synchronously, got %d", runner.Calls())
	}

	time.Sleep(20 * time.Millisecond) // entry is now stale

	expired := rules.EvaluateAll(ctx, "claude", "PreToolUse", testFields(payload), []config.Rule{rule}, nil)
	if len(expired) != 0 {
		t.Fatalf("expired cache should recompute synchronously and allow, got %d violations", len(expired))
	}
	if runner.Calls() != 2 {
		t.Fatalf("expired event should fork once synchronously, got %d calls", runner.Calls())
	}
}

func TestExecCacheCoalescesConcurrentMisses(t *testing.T) {
	dir := t.TempDir()
	rule := loadExecRule(t, execRuleTOML("cache_ttl_ms = 60000"))
	runner := &slowRunner{
		delay: 100 * time.Millisecond,
		res:   execconcern.RunResult{ExitCode: 1, Stdout: "not indexed\n"},
	}
	runtime := rules.NewExecRuntime(runner, nil)
	ctx := rules.WithExecRuntime(context.Background(), runtime)
	payload := map[string]any{
		"effective_cwd": dir,
		"tool_input":    map[string]any{"command": "grepcode here"},
	}

	const requestCount = 16
	var waitGroup sync.WaitGroup
	errs := make(chan error, requestCount)
	for i := 0; i < requestCount; i++ {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			violations := rules.EvaluateAll(ctx, "claude", "PreToolUse", testFields(payload), []config.Rule{rule}, nil)
			if len(violations) == 0 {
				errs <- fmt.Errorf("expected blocking verdict")
			}
		}()
	}
	waitGroup.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if runner.Calls() != 1 {
		t.Fatalf("concurrent miss should fork once, got %d calls", runner.Calls())
	}
}

func TestExecStableCacheKeySurvivesEquivalentRuntimeWithSharedCache(t *testing.T) {
	dir := t.TempDir()
	rule := loadExecRule(t, execRuleTOML("cache_ttl_ms = 60000"))
	runner := newCountingRunner(execconcern.RunResult{ExitCode: 1}, nil)
	store := hotkv.New(hotkv.Options{PruneInterval: 0})
	defer store.Close()

	firstRuntime := rules.NewExecRuntimeWithCache(runner, nil, store)
	firstCtx := rules.WithExecRuntime(context.Background(), firstRuntime)
	payload := map[string]any{
		"effective_cwd": dir,
		"tool_input":    map[string]any{"command": "grepcode here"},
	}
	first := rules.EvaluateAll(firstCtx, "claude", "PreToolUse", testFields(payload), []config.Rule{rule}, nil)
	if len(first) == 0 {
		t.Fatalf("first runtime should block")
	}

	reloadedRule := loadExecRule(t, execRuleTOML("cache_ttl_ms = 60000"))
	secondRuntime := rules.NewExecRuntimeWithCache(runner, nil, store)
	secondCtx := rules.WithExecRuntime(context.Background(), secondRuntime)
	second := rules.EvaluateAll(secondCtx, "claude", "PreToolUse", testFields(payload), []config.Rule{reloadedRule}, nil)
	if len(second) == 0 {
		t.Fatalf("second runtime should block from shared cache")
	}
	if runner.Calls() != 1 {
		t.Fatalf("equivalent runtime should reuse shared hot cache, got %d calls", runner.Calls())
	}
}

func TestExecTemporalCacheSeparatesConfiguredResponseOutput(t *testing.T) {
	ruleTOML := func(output string) string {
		return fmt.Sprintf(`
[[rules]]
name = "temporal-configured-output-cache"
cursor_events = ["stop"]
action = "inject"
output = %q

[[rules.conditions]]
kind = "exec"
command = ["/bin/validator"]
field_paths = ["response_output"]
cache_ttl_ms = 60000
`, output)
	}
	store := hotkv.New(hotkv.Options{PruneInterval: 0})
	defer store.Close()
	runner := newCountingRunner(execconcern.RunResult{ExitCode: 1}, nil)
	fields := rules.FieldSet{ConversationID: "conversation-1"}

	evaluate := func(rule config.Rule) {
		t.Helper()
		runtime := rules.NewExecRuntimeWithCache(runner, nil, store)
		ctx := rules.WithExecRuntime(context.Background(), runtime)
		ctx = withExecResponseTarget(ctx, "context")
		rules.EvaluateAllDetailed(
			ctx,
			"cursor",
			"stop",
			fields,
			[]config.Rule{rule},
			nil,
			nil,
			"test",
		)
	}

	evaluate(loadExecRule(t, ruleTOML("first output")))
	evaluate(loadExecRule(t, ruleTOML("second output")))

	if runner.Calls() != 2 {
		t.Fatalf("configured output change should fork again, got %d calls", runner.Calls())
	}
}

func TestExecTemporalCacheScopesResponseOutput(t *testing.T) {
	rule := loadExecRule(t, `
[[rules]]
name = "temporal-scope-cache"
events = ["stop", "postToolUse"]
action = "inject"

[[rules.conditions]]
kind = "exec"
command = ["/bin/validator"]
field_paths = ["last_user_message"]
block_on = "zero"
cache_ttl_ms = 60000
`)
	store := hotkv.New(hotkv.Options{PruneInterval: 0})
	defer store.Close()
	runner := &temporalEchoRunner{mu: sync.Mutex{}, calls: 0, exitCode: 0}
	runtime := rules.NewExecRuntimeWithCache(runner, nil, store)
	sharedCWD := t.TempDir()
	cursorA := rules.FieldSet{
		ConversationID: "cursor-conversation-a",
		EffectiveCWD:   sharedCWD,
	}
	cursorB := rules.FieldSet{
		ConversationID: "cursor-conversation-b",
		EffectiveCWD:   sharedCWD,
	}
	claudeA := rules.FieldSet{
		ConversationID: "claude-conversation-a",
		EffectiveCWD:   sharedCWD,
	}
	if !runtime.ObserveUserPrompt("cursor", cursorA, 1, "CURSOR_A_OUTPUT") {
		t.Fatal("ObserveUserPrompt did not store cursor conversation A")
	}
	if !runtime.ObserveUserPrompt("cursor", cursorB, 2, "CURSOR_B_OUTPUT") {
		t.Fatal("ObserveUserPrompt did not store cursor conversation B")
	}
	if !runtime.ObserveUserPrompt("claude", claudeA, 3, "CLAUDE_A_OUTPUT") {
		t.Fatal("ObserveUserPrompt did not store Claude conversation A")
	}

	testCases := []struct {
		name       string
		system     string
		eventName  string
		fields     rules.FieldSet
		target     string
		wantOutput string
		wantCalls  int
	}{
		{
			name:       "first scope",
			system:     "cursor",
			eventName:  "stop",
			fields:     cursorA,
			target:     "context",
			wantOutput: "CURSOR_A_OUTPUT",
			wantCalls:  1,
		},
		{
			name:       "same scope reuses cache",
			system:     "cursor",
			eventName:  "stop",
			fields:     cursorA,
			target:     "context",
			wantOutput: "CURSOR_A_OUTPUT",
			wantCalls:  1,
		},
		{
			name:       "conversation scope",
			system:     "cursor",
			eventName:  "stop",
			fields:     cursorB,
			target:     "context",
			wantOutput: "CURSOR_B_OUTPUT",
			wantCalls:  2,
		},
		{
			name:       "event scope",
			system:     "cursor",
			eventName:  "postToolUse",
			fields:     cursorA,
			target:     "context",
			wantOutput: "CURSOR_A_OUTPUT",
			wantCalls:  3,
		},
		{
			name:       "response target scope",
			system:     "cursor",
			eventName:  "stop",
			fields:     cursorA,
			target:     "tool_output",
			wantOutput: "CURSOR_A_OUTPUT",
			wantCalls:  4,
		},
		{
			name:       "provider scope",
			system:     "claude",
			eventName:  "stop",
			fields:     claudeA,
			target:     "context",
			wantOutput: "CLAUDE_A_OUTPUT",
			wantCalls:  5,
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			ctx := rules.WithExecRuntime(context.Background(), runtime)
			ctx = withExecResponseTarget(ctx, testCase.target)
			result := rules.EvaluateAllDetailed(
				ctx,
				testCase.system,
				testCase.eventName,
				testCase.fields,
				[]config.Rule{rule},
				nil,
				nil,
				"test",
			)
			if len(result.Violations) != 0 {
				t.Fatalf("violations = %#v, want none", result.Violations)
			}
			if len(result.Effects) != 1 {
				t.Fatalf("effects = %#v, want one", result.Effects)
			}
			if result.Effects[0].Output != testCase.wantOutput {
				t.Errorf(
					"response output = %q, want %q",
					result.Effects[0].Output,
					testCase.wantOutput,
				)
			}
			if runner.Calls() != testCase.wantCalls {
				t.Errorf("validator calls = %d, want %d", runner.Calls(), testCase.wantCalls)
			}
		})
	}
}

func TestExecTemporalCacheBypassesReuseWithoutCompleteScope(t *testing.T) {
	rule := loadExecRule(t, `
[[rules]]
name = "temporal-incomplete-scope-cache"
cursor_events = ["stop"]
action = "inject"

[[rules.conditions]]
kind = "exec"
command = ["/bin/validator"]
field_paths = ["last_user_message", "status"]
block_on = "zero"
cache_ttl_ms = 60000
`)

	testCases := []struct {
		name           string
		fields         func(string) rules.FieldSet
		responseTarget string
	}{
		{
			name: "missing conversation identity",
			fields: func(status string) rules.FieldSet {
				return rules.FieldSet{Status: status}
			},
			responseTarget: "context",
		},
		{
			name: "missing response target",
			fields: func(status string) rules.FieldSet {
				return rules.FieldSet{
					ConversationID: "conversation-1",
					Status:         status,
				}
			},
			responseTarget: "",
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			store := hotkv.New(hotkv.Options{PruneInterval: 0})
			defer store.Close()
			runner := &blockingFieldEchoRunner{
				mu:      sync.Mutex{},
				calls:   0,
				field:   "status",
				started: make(chan struct{}, 3),
				release: make(chan struct{}),
			}
			runtime := rules.NewExecRuntimeWithCache(runner, nil, store)

			evaluate := func(status string) string {
				ctx := rules.WithExecRuntime(context.Background(), runtime)
				if testCase.responseTarget != "" {
					ctx = withExecResponseTarget(ctx, testCase.responseTarget)
				}
				result := rules.EvaluateAllDetailed(
					ctx,
					"cursor",
					"stop",
					testCase.fields(status),
					[]config.Rule{rule},
					nil,
					nil,
					"test",
				)
				if len(result.Violations) != 0 {
					t.Errorf("violations = %#v, want none", result.Violations)
					return ""
				}
				if len(result.Effects) != 1 {
					t.Errorf("effects = %#v, want one", result.Effects)
					return ""
				}
				return result.Effects[0].Output
			}

			outputs := make(chan string, 2)
			start := make(chan struct{})
			for _, status := range []string{"FIRST_OUTPUT", "SECOND_OUTPUT"} {
				go func() {
					<-start
					outputs <- evaluate(status)
				}()
			}
			close(start)

			for i := 0; i < 2; i++ {
				select {
				case <-runner.started:
				case <-time.After(time.Second):
					close(runner.release)
					<-outputs
					<-outputs
					t.Fatalf(
						"validator starts = %d, want two incomplete-scope events without singleflight",
						runner.Calls(),
					)
				}
			}
			close(runner.release)

			concurrentOutputs := map[string]bool{
				<-outputs: true,
				<-outputs: true,
			}
			for _, status := range []string{"FIRST_OUTPUT", "SECOND_OUTPUT"} {
				if !concurrentOutputs[status] {
					t.Errorf("concurrent outputs = %#v, want %q", concurrentOutputs, status)
				}
			}

			if output := evaluate("THIRD_OUTPUT"); output != "THIRD_OUTPUT" {
				t.Errorf("later output = %q, want fresh incomplete-scope output", output)
			}
			if runner.Calls() != 3 {
				t.Errorf(
					"validator calls = %d, want three without temporal cache reuse",
					runner.Calls(),
				)
			}
		})
	}
}

func TestExecStableCacheKeySeparatesDifferentRuleActions(t *testing.T) {
	const sentinel = "TEMPORAL_ACTION_CACHE_SENTINEL"
	ruleTOML := func(action string) string {
		return fmt.Sprintf(`
[[rules]]
name = "temporal-action-cache"
cursor_events = ["stop"]
action = %q
violation_message = "static message"

[[rules.conditions]]
kind = "exec"
command = ["/bin/validator"]
field_paths = ["last_user_message"]
block_on = "zero"
cache_ttl_ms = 60000
`, action)
	}
	blockRule := loadExecRule(t, ruleTOML(config.ActionBlock))
	responseRule := loadExecRule(t, ruleTOML(config.ActionInject))
	store := hotkv.New(hotkv.Options{PruneInterval: 0})
	defer store.Close()
	runner := &temporalEchoRunner{mu: sync.Mutex{}, calls: 0, exitCode: 0}
	fields := rules.FieldSet{ConversationID: "conversation-1"}

	blockRuntime := rules.NewExecRuntimeWithCache(runner, nil, store)
	if !blockRuntime.ObserveUserPrompt("cursor", fields, 1, sentinel) {
		t.Fatal("ObserveUserPrompt did not store the temporal sentinel")
	}
	blockCtx := rules.WithExecRuntime(context.Background(), blockRuntime)
	blocking := rules.EvaluateAll(
		blockCtx,
		"cursor",
		"stop",
		fields,
		[]config.Rule{blockRule},
		nil,
	)
	if len(blocking) != 1 {
		t.Fatalf("blocking evaluation violations = %d, want one", len(blocking))
	}

	responseRuntime := rules.NewExecRuntimeWithCache(runner, nil, store)
	responseCtx := rules.WithExecRuntime(context.Background(), responseRuntime)
	response := rules.EvaluateAllDetailed(
		responseCtx,
		"cursor",
		"stop",
		fields,
		[]config.Rule{responseRule},
		nil,
		nil,
		"test",
	)
	if len(response.Violations) != 0 {
		t.Fatalf("response evaluation violations = %#v, want none", response.Violations)
	}
	if len(response.Effects) != 1 {
		t.Fatalf("response evaluation effects = %#v, want one", response.Effects)
	}
	if response.Effects[0].Output != sentinel {
		t.Errorf(
			"response output = %q, want fresh temporal validator stdout",
			response.Effects[0].Output,
		)
	}
	if runner.Calls() != 2 {
		t.Errorf("validator calls = %d, want two after rule action change", runner.Calls())
	}
}

func TestExecStableCacheKeySeparatesDifferentFieldPaths(t *testing.T) {
	dir := t.TempDir()
	runner := newCountingRunner(execconcern.RunResult{ExitCode: 1}, nil)
	store := hotkv.New(hotkv.Options{PruneInterval: 0})
	defer store.Close()

	rulePathOne := loadExecRule(t, `
[[rules]]
name = "exec-rule"
events = ["PreToolUse"]
action = "block"
violation_message = "static message"

[[rules.conditions]]
kind = "regex"
field_paths = ["tool_input.command"]
pattern = "grepcode"

[[rules.conditions]]
kind = "exec"
field_paths = ["tool_input.command"]
command = ["/bin/true"]
cache_ttl_ms = 60000
`)
	rulePathTwo := loadExecRule(t, `
[[rules]]
name = "exec-rule"
events = ["PreToolUse"]
action = "block"
violation_message = "static message"

[[rules.conditions]]
kind = "regex"
field_paths = ["tool_input.command"]
pattern = "grepcode"

[[rules.conditions]]
kind = "exec"
field_paths = ["command"]
command = ["/bin/true"]
cache_ttl_ms = 60000
`)
	runtime := rules.NewExecRuntimeWithCache(runner, nil, store)
	ctx := rules.WithExecRuntime(context.Background(), runtime)
	payload := map[string]any{
		"effective_cwd": dir,
		"command":       "grepcode top-level",
		"tool_input":    map[string]any{"command": "grepcode here"},
	}

	first := rules.EvaluateAll(ctx, "claude", "PreToolUse", testFields(payload), []config.Rule{rulePathOne}, nil)
	second := rules.EvaluateAll(ctx, "claude", "PreToolUse", testFields(payload), []config.Rule{rulePathTwo}, nil)
	if len(first) == 0 || len(second) == 0 {
		t.Fatalf("expected both selector variants to block")
	}
	if runner.Calls() != 2 {
		t.Fatalf("different exec field_paths should not share a cache entry, got %d calls", runner.Calls())
	}
}

func TestExecStableCacheKeySeparatesDifferentCacheTTLs(t *testing.T) {
	dir := t.TempDir()
	runner := newCountingRunner(execconcern.RunResult{ExitCode: 1}, nil)
	store := hotkv.New(hotkv.Options{PruneInterval: 0})
	defer store.Close()

	ruleTTLMinute := loadExecRule(t, execRuleTOML("cache_ttl_ms = 60000"))
	ruleTTLSecond := loadExecRule(t, execRuleTOML("cache_ttl_ms = 1000"))
	runtime := rules.NewExecRuntimeWithCache(runner, nil, store)
	ctx := rules.WithExecRuntime(context.Background(), runtime)
	payload := map[string]any{
		"effective_cwd": dir,
		"tool_input":    map[string]any{"command": "grepcode here"},
	}

	first := rules.EvaluateAll(ctx, "claude", "PreToolUse", testFields(payload), []config.Rule{ruleTTLMinute}, nil)
	second := rules.EvaluateAll(ctx, "claude", "PreToolUse", testFields(payload), []config.Rule{ruleTTLSecond}, nil)
	if len(first) == 0 || len(second) == 0 {
		t.Fatalf("expected both TTL variants to block")
	}
	if runner.Calls() != 2 {
		t.Fatalf("different cache_ttl_ms values should not share a cache entry, got %d calls", runner.Calls())
	}
}

func TestExecForEachCmdReadTargetsExpandsItemAndBlocksOnAnyJSONMatch(t *testing.T) {
	firstTarget := t.TempDir()
	secondTarget := t.TempDir()
	wantFirst, err := filepath.EvalSymlinks(firstTarget)
	if err != nil {
		t.Fatalf("EvalSymlinks firstTarget: %v", err)
	}
	wantSecond, err := filepath.EvalSymlinks(secondTarget)
	if err != nil {
		t.Fatalf("EvalSymlinks secondTarget: %v", err)
	}

	rule := loadExecRule(t, `
[[rules]]
name = "exec-rule"
events = ["PreToolUse"]
action = "block"
violation_message = "static message"

[[rules.conditions]]
kind = "regex"
field_paths = ["tool_input.command"]
pattern = "grep"

[[rules.conditions]]
kind = "exec"
command = ["/bin/check-target", "{{item}}"]
for_each = "cmd_read_targets"
match_mode = "any"
stdout_json_field = "searchable"
stdout_json_equals = true
cache_key = "cmd_read_targets"
cache_ttl_ms = 0
search_tools = ["grep"]
`)
	runner := &recordingCommandRunner{
		run: func(command []string) (execconcern.RunResult, error) {
			if len(command) < 2 {
				return execconcern.RunResult{ExitCode: 0, Stdout: `{"searchable":false}`}, nil
			}
			if command[1] == wantSecond {
				return execconcern.RunResult{ExitCode: 0, Stdout: `{"searchable":true}`}, nil
			}
			return execconcern.RunResult{ExitCode: 0, Stdout: `{"searchable":false}`}, nil
		},
	}

	violations := evalRule(runner, rule, map[string]any{
		"cwd":        t.TempDir(),
		"tool_input": map[string]any{"command": "grep -rn x " + firstTarget + " " + secondTarget},
	})
	if len(violations) == 0 {
		t.Fatalf("expected any matching target to block")
	}
	commands := runner.Commands()
	if len(commands) != 2 {
		t.Fatalf("expected two expanded validator commands, got %d", len(commands))
	}
	if commands[0][1] != wantFirst {
		t.Fatalf("first expanded item = %q, want %q", commands[0][1], wantFirst)
	}
	if commands[1][1] != wantSecond {
		t.Fatalf("second expanded item = %q, want %q", commands[1][1], wantSecond)
	}
}

func TestExecForEachCmdWriteTargetsUsesConditionSpecs(t *testing.T) {
	rule := loadExecRule(t, `
[[rules]]
name = "exec-write-targets"
events = ["PreToolUse"]
action = "block"
violation_message = "static message"

[[rules.conditions]]
kind = "exec"
command = ["/bin/check-target", "{{item}}"]
for_each = "cmd_write_targets"
match_mode = "any"
cache_key = "cmd_write_targets"
cache_ttl_ms = 0

[[rules.conditions.write_specs]]
argv0 = ["writer-all"]
target_mode = "all_operands"
`)
	runner := &recordingCommandRunner{
		run: func(command []string) (execconcern.RunResult, error) {
			if filepath.Base(command[1]) == "second.txt" {
				return execconcern.RunResult{ExitCode: 1}, nil
			}
			return execconcern.RunResult{ExitCode: 0}, nil
		},
	}
	cwd := t.TempDir()
	violations := evalRule(runner, rule, map[string]any{
		"cwd":        cwd,
		"tool_input": map[string]any{"command": "writer-all first.txt second.txt"},
	})
	if len(violations) == 0 {
		t.Fatal("declared write targets did not reach exec for_each")
	}
	commands := runner.Commands()
	if len(commands) != 2 {
		t.Fatalf("expanded validator commands = %v, want two", commands)
	}
	wantFirst := filepath.Join(cwd, "first.txt")
	wantSecond := filepath.Join(cwd, "second.txt")
	if commands[0][1] != wantFirst || commands[1][1] != wantSecond {
		t.Fatalf("expanded validator commands = %v, want targets %q and %q", commands, wantFirst, wantSecond)
	}
}

func TestExecCmdWriteTargetsCacheKeyUsesConditionSpecs(t *testing.T) {
	rule := loadExecRule(t, `
[[rules]]
name = "exec-write-target-cache"
events = ["PreToolUse"]
action = "block"
violation_message = "static message"

[[rules.conditions]]
kind = "exec"
command = ["/bin/true"]
cache_key = "cmd_write_targets"
cache_ttl_ms = 60000

[[rules.conditions.write_specs]]
argv0 = ["writer-all"]
target_mode = "all_operands"
`)
	runner := newCountingRunner(execconcern.RunResult{ExitCode: 1}, nil)
	runtime := rules.NewExecRuntime(runner, nil)
	ctx := rules.WithExecRuntime(context.Background(), runtime)
	cwd := t.TempDir()

	for _, target := range []string{"first.txt", "second.txt"} {
		payload := testFields(map[string]any{
			"cwd":        cwd,
			"tool_input": map[string]any{"command": "writer-all " + target},
		})
		violations := rules.EvaluateAll(ctx, "claude", "PreToolUse", payload, []config.Rule{rule}, nil)
		if len(violations) == 0 {
			t.Fatalf("target %q did not block", target)
		}
	}
	if runner.Calls() != 2 {
		t.Fatalf("distinct declared write targets shared a cache entry, got %d calls", runner.Calls())
	}
}

func TestExecForEachExecTargetsFallsBackToFilePath(t *testing.T) {
	target := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	wantTarget, err := filepath.EvalSymlinks(target)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}

	rule := loadExecRule(t, `
[[rules]]
name = "exec-rule"
events = ["PreToolUse"]
action = "block"
violation_message = "static message"

[[rules.conditions]]
kind = "regex"
field_paths = ["tool_name"]
pattern = "^Grep$"

[[rules.conditions]]
kind = "exec"
command = ["/bin/check-target", "{{item}}"]
for_each = "exec_targets"
match_mode = "any"
stdout_json_field = "searchable"
stdout_json_equals = true
cache_key = "exec_targets"
cache_ttl_ms = 0
`)
	runner := &recordingCommandRunner{
		run: func(command []string) (execconcern.RunResult, error) {
			return execconcern.RunResult{ExitCode: 0, Stdout: `{"searchable":false}`}, nil
		},
	}

	evalRule(runner, rule, map[string]any{
		"cwd":       t.TempDir(),
		"tool_name": "Grep",
		"tool_input": map[string]any{
			"path": target,
		},
	})

	commands := runner.Commands()
	if len(commands) != 1 {
		t.Fatalf("expected one expanded validator command, got %d", len(commands))
	}
	if commands[0][1] != wantTarget {
		t.Fatalf("expanded exec_targets item = %q, want %q", commands[0][1], wantTarget)
	}
}

func TestExecJSONMatchErrorOutcomeNotCached(t *testing.T) {
	target := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	rule := loadExecRule(t, `
[[rules]]
name = "exec-rule"
events = ["PreToolUse"]
action = "block"
violation_message = "static message"

[[rules.conditions]]
kind = "regex"
field_paths = ["tool_name"]
pattern = "^Grep$"

[[rules.conditions]]
kind = "exec"
command = ["/bin/check-target", "{{item}}"]
for_each = "exec_targets"
match_mode = "any"
stdout_json_field = "searchable"
stdout_json_equals = true
cache_key = "exec_targets"
cache_ttl_ms = 60000
on_error = "open"
`)
	runner := newCountingRunner(execconcern.RunResult{}, nil)
	runner.responses = []runnerResponse{
		{res: execconcern.RunResult{ExitCode: 0, Stdout: `{"searchable":`}, err: nil},
		{res: execconcern.RunResult{ExitCode: 0, Stdout: `{"searchable":true}`}, err: nil},
	}
	runtime := rules.NewExecRuntime(runner, nil)
	ctx := rules.WithExecRuntime(context.Background(), runtime)
	payload := map[string]any{
		"cwd":       t.TempDir(),
		"tool_name": "Grep",
		"tool_input": map[string]any{
			"path": target,
		},
	}

	first := rules.EvaluateAll(ctx, "claude", "PreToolUse", testFields(payload), []config.Rule{rule}, nil)
	second := rules.EvaluateAll(ctx, "claude", "PreToolUse", testFields(payload), []config.Rule{rule}, nil)
	if len(first) != 0 {
		t.Fatalf("invalid JSON predicate output should fail open")
	}
	if len(second) == 0 {
		t.Fatalf("successful follow-up JSON match should block")
	}
	if runner.Calls() != 2 {
		t.Fatalf("errored JSON predicate outcome must not be cached, got %d calls", runner.Calls())
	}
}

func TestExecTemporalInvalidJSONWarningOmitsValidatorStdout(t *testing.T) {
	const sentinel = "TEMPORAL_LOG_SENTINEL"
	rule := loadExecRule(t, `
[[rules]]
name = "temporal-json"
cursor_events = ["stop"]
action = "block"
violation_message = "static message"

[[rules.conditions]]
kind = "exec"
command = ["/bin/validator"]
field_paths = ["last_user_message"]
block_on = "match"
stdout_json_field = "matched"
stdout_json_equals = true
cache_ttl_ms = 0
on_error = "open"
`)
	var logOutput bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logOutput, nil))
	runner := &temporalEchoRunner{mu: sync.Mutex{}, calls: 0, exitCode: 0}
	runtime := rules.NewExecRuntime(runner, logger)
	fields := rules.FieldSet{ConversationID: "conversation-1"}
	if !runtime.ObserveUserPrompt("cursor", fields, 1, sentinel) {
		t.Fatal("ObserveUserPrompt did not store the temporal sentinel")
	}

	ctx := rules.WithExecRuntime(context.Background(), runtime)
	violations := rules.EvaluateAll(
		ctx,
		"cursor",
		"stop",
		fields,
		[]config.Rule{rule},
		nil,
	)
	if len(violations) != 0 {
		t.Fatalf("invalid JSON validator output should fail open, got %d violations", len(violations))
	}
	logs := logOutput.String()
	if !strings.Contains(
		logs,
		"exec validator expanded command returned invalid JSON predicate output",
	) {
		t.Fatalf("logs do not contain the generic invalid JSON warning: %q", logs)
	}
	if strings.Contains(logs, sentinel) {
		t.Fatalf("operational logs contain temporal validator stdout")
	}
	if strings.Contains(logs, `"stdout`) {
		t.Fatalf("operational logs contain a validator stdout attribute: %q", logs)
	}
}

func TestExecJSONMatchConcurrentMissesSingleflight(t *testing.T) {
	target := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	rule := loadExecRule(t, `
[[rules]]
name = "exec-rule"
events = ["PreToolUse"]
action = "block"
violation_message = "static message"

[[rules.conditions]]
kind = "regex"
field_paths = ["tool_name"]
pattern = "^Grep$"

[[rules.conditions]]
kind = "exec"
command = ["/bin/check-target", "{{item}}"]
for_each = "exec_targets"
match_mode = "any"
stdout_json_field = "searchable"
stdout_json_equals = true
cache_key = "exec_targets"
cache_ttl_ms = 60000
`)
	runner := &slowRunner{
		delay: 100 * time.Millisecond,
		res:   execconcern.RunResult{ExitCode: 0, Stdout: `{"searchable":true}`},
	}
	runtime := rules.NewExecRuntime(runner, nil)
	ctx := rules.WithExecRuntime(context.Background(), runtime)
	payload := map[string]any{
		"cwd":       t.TempDir(),
		"tool_name": "Grep",
		"tool_input": map[string]any{
			"path": target,
		},
	}

	const requestCount = 12
	var waitGroup sync.WaitGroup
	errs := make(chan error, requestCount)
	for i := 0; i < requestCount; i++ {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			violations := rules.EvaluateAll(ctx, "claude", "PreToolUse", testFields(payload), []config.Rule{rule}, nil)
			if len(violations) == 0 {
				errs <- fmt.Errorf("expected blocking verdict")
			}
		}()
	}
	waitGroup.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if runner.Calls() != 1 {
		t.Fatalf("concurrent JSON predicate miss should fork once, got %d calls", runner.Calls())
	}
}

func TestExecJSONMatchTimeoutFailsOpen(t *testing.T) {
	target := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	rule := loadExecRule(t, `
[[rules]]
name = "exec-rule"
events = ["PreToolUse"]
action = "block"
violation_message = "static message"

[[rules.conditions]]
kind = "regex"
field_paths = ["tool_name"]
pattern = "^Grep$"

[[rules.conditions]]
kind = "exec"
command = ["/bin/check-target", "{{item}}"]
for_each = "exec_targets"
match_mode = "any"
stdout_json_field = "searchable"
stdout_json_equals = true
cache_key = "exec_targets"
cache_ttl_ms = 60000
timeout_ms = 50
on_error = "open"
`)
	runner := &slowRunner{
		delay: 150 * time.Millisecond,
		res:   execconcern.RunResult{ExitCode: 0, Stdout: `{"searchable":true}`},
	}
	runtime := rules.NewExecRuntime(runner, nil)
	ctx := rules.WithExecRuntime(context.Background(), runtime)
	payload := map[string]any{
		"cwd":       t.TempDir(),
		"tool_name": "Grep",
		"tool_input": map[string]any{
			"path": target,
		},
	}

	first := rules.EvaluateAll(ctx, "claude", "PreToolUse", testFields(payload), []config.Rule{rule}, nil)
	if len(first) != 0 {
		t.Fatalf("timed-out JSON predicate event should fail open")
	}
}

func TestExecForEachAllRequiresEveryMatch(t *testing.T) {
	targetOne := filepath.Join(t.TempDir(), "repo-one")
	targetTwo := filepath.Join(t.TempDir(), "repo-two")
	if err := os.MkdirAll(targetOne, 0o755); err != nil {
		t.Fatalf("MkdirAll targetOne: %v", err)
	}
	if err := os.MkdirAll(targetTwo, 0o755); err != nil {
		t.Fatalf("MkdirAll targetTwo: %v", err)
	}
	wantOne, err := filepath.EvalSymlinks(targetOne)
	if err != nil {
		t.Fatalf("EvalSymlinks targetOne: %v", err)
	}
	wantTwo, err := filepath.EvalSymlinks(targetTwo)
	if err != nil {
		t.Fatalf("EvalSymlinks targetTwo: %v", err)
	}

	rule := loadExecRule(t, `
[[rules]]
name = "exec-rule"
events = ["PreToolUse"]
action = "block"
violation_message = "static message"

[[rules.conditions]]
kind = "regex"
field_paths = ["tool_input.command"]
pattern = "grep"

[[rules.conditions]]
kind = "exec"
command = ["/bin/check-target", "{{item}}"]
for_each = "cmd_read_targets"
match_mode = "all"
stdout_json_field = "searchable"
stdout_json_equals = true
cache_key = "cmd_read_targets"
cache_ttl_ms = 0
search_tools = ["grep"]
`)
	runner := &recordingCommandRunner{
		run: func(command []string) (execconcern.RunResult, error) {
			if command[1] == wantOne {
				return execconcern.RunResult{ExitCode: 0, Stdout: `{"searchable":true}`}, nil
			}
			if command[1] == wantTwo {
				return execconcern.RunResult{ExitCode: 0, Stdout: `{"searchable":false}`}, nil
			}
			return execconcern.RunResult{ExitCode: 0, Stdout: `{"searchable":true}`}, nil
		},
	}

	violations := evalRule(runner, rule, map[string]any{
		"cwd":        t.TempDir(),
		"tool_input": map[string]any{"command": "grep -rn x " + targetOne + " " + targetTwo},
	})
	if len(violations) != 0 {
		t.Fatalf("match_mode=all should allow when any target mismatches, got %d violations", len(violations))
	}
	commands := runner.Commands()
	if len(commands) != 2 {
		t.Fatalf("expected two expanded commands under match_mode=all, got %d", len(commands))
	}
}

func TestExecStableCacheKeySeparatesDifferentJSONScalarKinds(t *testing.T) {
	dir := t.TempDir()
	runner := newCountingRunner(execconcern.RunResult{ExitCode: 0, Stdout: `{"searchable":true}`}, nil)
	store := hotkv.New(hotkv.Options{PruneInterval: 0})
	defer store.Close()

	ruleInt := loadExecRule(t, `
[[rules]]
name = "exec-rule"
events = ["PreToolUse"]
action = "block"
violation_message = "static message"

[[rules.conditions]]
kind = "regex"
field_paths = ["tool_name"]
pattern = "^Grep$"

[[rules.conditions]]
kind = "exec"
command = ["/bin/check-target", "{{item}}"]
for_each = "exec_targets"
match_mode = "any"
stdout_json_field = "searchable"
stdout_json_equals = 1
cache_key = "exec_targets"
cache_ttl_ms = 60000
`)
	ruleFloat := loadExecRule(t, `
[[rules]]
name = "exec-rule"
events = ["PreToolUse"]
action = "block"
violation_message = "static message"

[[rules.conditions]]
kind = "regex"
field_paths = ["tool_name"]
pattern = "^Grep$"

[[rules.conditions]]
kind = "exec"
command = ["/bin/check-target", "{{item}}"]
for_each = "exec_targets"
match_mode = "any"
stdout_json_field = "searchable"
stdout_json_equals = 1.0
cache_key = "exec_targets"
cache_ttl_ms = 60000
`)
	runtime := rules.NewExecRuntimeWithCache(runner, nil, store)
	ctx := rules.WithExecRuntime(context.Background(), runtime)
	payload := map[string]any{
		"effective_cwd": dir,
		"tool_name":     "Grep",
		"tool_input":    map[string]any{"path": dir},
	}

	rules.EvaluateAll(ctx, "claude", "PreToolUse", testFields(payload), []config.Rule{ruleInt}, nil)
	rules.EvaluateAll(ctx, "claude", "PreToolUse", testFields(payload), []config.Rule{ruleFloat}, nil)

	if runner.Calls() != 2 {
		t.Fatalf("different stdout_json_equals scalar kinds should not share a cache entry, got %d calls", runner.Calls())
	}
}

// A validator that outlives the synchronous budget fails the current event
// open, finishes in the background, and caches its verdict so the next event
// for the same target blocks.
func TestExecGateSlowValidatorFinishesInBackgroundAndCachesBlock(t *testing.T) {
	runner := &slowRunner{delay: 150 * time.Millisecond, res: execconcern.RunResult{ExitCode: 1, Stdout: "indexed\n"}}
	rule := loadExecRule(t, `
[[rules]]
name = "exec-rule"
events = ["PreToolUse"]
action = "block"
violation_message = "static message"

[[rules.conditions]]
kind = "regex"
field_paths = ["tool_input.command"]
pattern = "grepcode"

[[rules.conditions]]
kind = "exec"
command = ["/bin/true"]
timeout_ms = 50
cache_ttl_ms = 60000
`)

	runtime := rules.NewExecRuntime(runner, nil)
	ctx := rules.WithExecRuntime(context.Background(), runtime)
	payload := map[string]any{
		"cwd":        t.TempDir(),
		"tool_input": map[string]any{"command": "grepcode -rn x ."},
	}

	first := rules.EvaluateAll(ctx, "claude", "PreToolUse", testFields(payload), []config.Rule{rule}, nil)
	if len(first) != 0 {
		t.Fatalf("event whose validator exceeds the budget should fail open, got %d violations", len(first))
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		violations := rules.EvaluateAll(ctx, "claude", "PreToolUse", testFields(payload), []config.Rule{rule}, nil)
		if len(violations) > 0 {
			break // background verdict landed in the cache and now blocks
		}
		if time.Now().After(deadline) {
			t.Fatalf("background verdict never reached the cache")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestExecGateTimeoutKeepsSingleflightUntilBackgroundCompletes(t *testing.T) {
	runner := &slowRunner{delay: 150 * time.Millisecond, res: execconcern.RunResult{ExitCode: 1, Stdout: "indexed\n"}}
	rule := loadExecRule(t, `
[[rules]]
name = "exec-rule"
events = ["PreToolUse"]
action = "block"
violation_message = "static message"

[[rules.conditions]]
kind = "regex"
field_paths = ["tool_input.command"]
pattern = "grepcode"

[[rules.conditions]]
kind = "exec"
command = ["/bin/true"]
timeout_ms = 50
cache_ttl_ms = 60000
`)

	runtime := rules.NewExecRuntime(runner, nil)
	ctx := rules.WithExecRuntime(context.Background(), runtime)
	payload := map[string]any{
		"cwd":        t.TempDir(),
		"tool_input": map[string]any{"command": "grepcode -rn x ."},
	}

	first := rules.EvaluateAll(ctx, "claude", "PreToolUse", testFields(payload), []config.Rule{rule}, nil)
	if len(first) != 0 {
		t.Fatalf("timed-out first event should fail open, got %d violations", len(first))
	}
	second := rules.EvaluateAll(ctx, "claude", "PreToolUse", testFields(payload), []config.Rule{rule}, nil)
	if runner.Calls() != 1 {
		t.Fatalf("background window should still coalesce to one validator run, got %d calls", runner.Calls())
	}
	if len(second) == 0 {
		t.Fatalf("concurrent follow-up event should reuse the in-flight validator result once it lands")
	}
}

// capturingRunner records the env passed to the validator so a test can assert
// the read targets the gate computed.
type capturingRunner struct {
	mu  sync.Mutex
	env []string
	res execconcern.RunResult
}

func (r *capturingRunner) Run(_ context.Context, _ []string, _ time.Duration, _ []byte, env []string) (execconcern.RunResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.env = env
	return r.res, nil
}

func (r *capturingRunner) readTargets() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, kv := range r.env {
		if after, ok := strings.CutPrefix(kv, "AGENT_GATE_READ_TARGETS="); ok {
			return after
		}
	}
	return ""
}

func (r *capturingRunner) envValue(key string) (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, kv := range r.env {
		if after, ok := strings.CutPrefix(kv, key+"="); ok {
			return after, true
		}
	}
	return "", false
}

// TestExecReadTargetsResolveAgainstEffectiveCwd guards the cd-chain fix: a search
// run after `cd /other` must be attributed to /other, not the session cwd, so the
// validator checks the directory the search actually reads.
func TestExecReadTargetsResolveAgainstEffectiveCwd(t *testing.T) {
	sessionDir := t.TempDir()
	otherDir := t.TempDir()
	wantTarget, err := filepath.EvalSymlinks(otherDir)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}

	runner := &capturingRunner{res: execconcern.RunResult{ExitCode: 0}}
	rule := loadExecRule(t, `
[[rules]]
name = "exec-rule"
events = ["PreToolUse"]
action = "block"
violation_message = "static message"

[[rules.conditions]]
kind = "regex"
field_paths = ["tool_input.command"]
pattern = "grep"

[[rules.conditions]]
kind = "exec"
command = ["/bin/true"]
cache_ttl_ms = 0
search_tools = ["grep"]
`)

	evalRule(runner, rule, map[string]any{
		"cwd":        sessionDir,
		"tool_input": map[string]any{"command": "cd " + otherDir + " && grep -rn x ."},
	})

	got := runner.readTargets()
	if got != wantTarget {
		t.Fatalf("read targets = %q, want the cd dir %q (not session %q)", got, wantTarget, sessionDir)
	}
}

// A grep behind an unresolvable cd must still reach the validator (the spawn
// must not die on the marker's NUL byte) and the validator must see the
// unknown directory as an empty AGENT_GATE_CWD per the validator contract.
func TestExecGateUnresolvableCwdRunsValidatorWithEmptyCwd(t *testing.T) {
	target := t.TempDir()
	runner := &capturingRunner{res: execconcern.RunResult{ExitCode: 1, Stdout: "indexed\n"}}
	rule := loadExecRule(t, `
[[rules]]
name = "exec-rule"
events = ["PreToolUse"]
action = "block"
violation_message = "static message"

[[rules.conditions]]
kind = "regex"
field_paths = ["tool_input.command"]
pattern = "grep"

[[rules.conditions]]
kind = "exec"
command = ["/bin/true"]
cache_ttl_ms = 0
`)

	violations := evalRule(runner, rule, map[string]any{
		"cwd":        t.TempDir(),
		"tool_input": map[string]any{"command": `cd "$(echo /tmp)" && grep -rn x ` + target},
	})

	if len(violations) == 0 {
		t.Fatalf("validator exit 1 behind an unresolvable cd should block; the spawn must not fail open")
	}
	cwd, found := runner.envValue("AGENT_GATE_CWD")
	if !found {
		t.Fatalf("validator env missing AGENT_GATE_CWD")
	}
	if cwd != "" {
		t.Fatalf("AGENT_GATE_CWD = %q, want empty for an unresolvable cwd", cwd)
	}
}
