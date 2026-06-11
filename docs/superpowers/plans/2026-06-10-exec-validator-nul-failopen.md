# Exec Validator NUL Fail-Open Fix Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Stop the exec validator from failing open when the working directory is the shelldecomp `"\x00UNRESOLVABLE"` marker or the command text carries a NUL byte, stop the marker from reaching the intake database, and let a validator that outlives its synchronous budget finish in the background so its verdict catches the next event.

**Architecture:** The marker keeps its value and meaning inside the rule engine. The fix sanitizes it at three process boundaries: the validator input builder (`internal/rules/exec_gate.go`) maps it to an empty cwd, the env builder (`internal/rules/concerns/exec/concern.go`) strips NUL bytes from every env value as a backstop, and the intake record builder (`internal/daemon/server.go`) maps it to an empty string before storage. Separately, `runValidator` forks the validator under a detached 30-second context and waits only `timeout_ms` synchronously; on expiry the event fails open as today, but the run continues and its verdict lands in the per-target cache.

**Tech Stack:** Go, `make test` / `make lint` / `make check`, targeted `go test ./internal/... -run Name` for narrow loops per CLAUDE.md.

**Spec:** `docs/superpowers/specs/2026-06-10-exec-validator-nul-failopen-design.md`

---

## File Structure

- `internal/rules/concerns/exec/concern.go` assembles the validator env and gains `sanitizeEnv`, the NUL-stripping backstop.
- `internal/rules/concerns/exec/concern_test.go` holds the existing `BuildRequest` and `OSRunner` tests and gains the NUL invariant tests.
- `internal/rules/exec_gate.go` orchestrates the validator; `buildInput` gains the marker-to-empty mapping and `runValidator` gains the background-completion structure.
- `internal/rules/exec_gate_test.go` is the external (`rules_test`) harness with `loadExecRule`, `evalRule`, `testFields`, and `capturingRunner`; it gains the unresolvable-cwd test, a `slowRunner`, and the background-completion test.
- `internal/daemon/server.go` holds `buildIntakeRecord`, which gains the marker-to-empty mapping for `effective_cwd`.
- `internal/daemon/server_test.go` is in-package (`package daemon`) and gains the intake sanitization test.

No config change: the synchronous budget stays at the `timeout_ms` default (1500 ms).

---

### Task 1: NUL backstop in the validator env builder

**Files:**
- Modify: `internal/rules/concerns/exec/concern.go` (function `BuildRequest`, near line 85)
- Test: `internal/rules/concerns/exec/concern_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `internal/rules/concerns/exec/concern_test.go`. The file already imports `context`, `encoding/json`, `errors`, `slices`, `testing`, `time`, and `goodkind.io/agent-gate/internal/config`; add `strings` to the import list.

```go
// The shelldecomp unresolvable-cwd marker begins with a NUL byte, and os/exec
// refuses to start a process whose environment contains one. BuildRequest must
// therefore never emit NUL in any env value, whatever the input carries.
func TestBuildRequestStripsNULFromEnv(t *testing.T) {
	in := Input{
		Event:        "PreToolUse",
		System:       "claude",
		ToolName:     "Bash",
		Rule:         "grep-code-use-semantic-search",
		Command:      "grep -rn \x00marker .",
		EffectiveCWD: PathView{Raw: "\x00UNRESOLVABLE", Canonical: "\x00UNRESOLVABLE", IsCanonical: false},
		CacheKey:     PathView{Raw: "\x00UNRESOLVABLE", Canonical: "\x00UNRESOLVABLE", IsCanonical: false},
	}

	_, env, err := BuildRequest(in)
	if err != nil {
		t.Fatalf("BuildRequest: %v", err)
	}
	for _, kv := range env {
		if strings.Contains(kv, "\x00") {
			t.Fatalf("env entry %q contains a NUL byte", kv)
		}
	}
}

// End-to-end proof on the real spawn path: a sanitized env from marker-bearing
// input must start a process without error.
func TestOSRunnerStartsWithSanitizedMarkerEnv(t *testing.T) {
	in := Input{
		Command:      "grep -rn \x00marker .",
		EffectiveCWD: PathView{Raw: "\x00UNRESOLVABLE", Canonical: "\x00UNRESOLVABLE", IsCanonical: false},
	}
	_, env, err := BuildRequest(in)
	if err != nil {
		t.Fatalf("BuildRequest: %v", err)
	}

	var runner OSRunner
	res, err := runner.Run(context.Background(), []string{"/usr/bin/true"}, time.Second, nil, env)
	if err != nil {
		t.Fatalf("spawn with sanitized env failed: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("expected exit 0, got %d", res.ExitCode)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/rules/concerns/exec -run 'TestBuildRequestStripsNULFromEnv|TestOSRunnerStartsWithSanitizedMarkerEnv' -v`
Expected: both FAIL. The first fails on `env entry ... contains a NUL byte` and the second fails with `environment variable contains NUL`.

- [ ] **Step 3: Implement the backstop**

In `internal/rules/concerns/exec/concern.go`, change the tail of `BuildRequest` and add the helper. The current code returns the `env` literal directly; wrap it:

```go
	env = sanitizeEnv([]string{
		"AGENT_GATE_EVENT=" + in.Event,
		"AGENT_GATE_SYSTEM=" + in.System,
		"AGENT_GATE_TOOL=" + in.ToolName,
		"AGENT_GATE_RULE=" + in.Rule,
		"AGENT_GATE_COMMAND=" + in.Command,
		"AGENT_GATE_CWD=" + in.EffectiveCWD.Canonical,
		"AGENT_GATE_FILE_PATH=" + in.FilePath.Canonical,
		"AGENT_GATE_CACHE_KEY=" + in.CacheKey.Canonical,
		"AGENT_GATE_READ_TARGETS=" + readTargetsEnv(in.ReadTargets),
	})
	return stdin, env, nil
}

// sanitizeEnv strips NUL bytes from every env value. os/exec refuses to start
// a process whose environment contains a NUL, which silently fails the gate
// open under on_error=open; the shelldecomp unresolvable-cwd marker and
// NUL-bearing command text are both real inputs here.
func sanitizeEnv(env []string) []string {
	for i, kv := range env {
		if strings.Contains(kv, "\x00") {
			env[i] = strings.ReplaceAll(kv, "\x00", "")
		}
	}
	return env
}
```

`strings` is already imported in `concern.go`.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/rules/concerns/exec -v`
Expected: all tests in the package PASS, including the two new ones.

- [ ] **Step 5: Commit**

```bash
git add internal/rules/concerns/exec/concern.go internal/rules/concerns/exec/concern_test.go
git commit -m "Strip NUL bytes from exec validator env in BuildRequest"
```

---

### Task 2: map the unresolvable marker to an empty cwd in the validator input

**Files:**
- Modify: `internal/rules/exec_gate.go` (function `buildInput`, near line 282)
- Test: `internal/rules/exec_gate_test.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/rules/exec_gate_test.go`. First add an env accessor to the existing `capturingRunner` (defined near line 332), next to its `readTargets` method:

```go
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
```

Then add the test. The `cd "$(echo /tmp)"` shape is the live-verified reproduction: shelldecomp cannot resolve a command-substitution `cd`, so the effective cwd becomes the marker.

```go
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
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/rules -run TestExecGateUnresolvableCwdRunsValidatorWithEmptyCwd -v`
Expected: FAIL on the `AGENT_GATE_CWD` assertion, which reports the marker value (its leading NUL may render invisibly). The block assertion passes already because the fake runner does not enforce the NUL spawn rule; the env assertion is the load-bearing check.

- [ ] **Step 3: Implement the mapping**

In `internal/rules/exec_gate.go`, add the import `"goodkind.io/gksyntax/shelldecomp"` to the import block, then change `buildInput`. The current line reads:

```go
		EffectiveCWD: canonicalizePathField(r.canon, cwd, fields.String(config.FieldEffectiveCWD)),
```

Add this before the returned struct:

```go
	effectiveCwd := fields.String(config.FieldEffectiveCWD)
	if effectiveCwd == shelldecomp.Unresolvable {
		// The marker means "directory unknown". The validator contract
		// expresses that as an empty cwd, and the marker's NUL byte must
		// never reach the process environment.
		effectiveCwd = ""
	}
```

and use it in the returned struct:

```go
		EffectiveCWD: canonicalizePathField(r.canon, cwd, effectiveCwd),
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/rules -run TestExecGateUnresolvableCwdRunsValidatorWithEmptyCwd -v`
Expected: PASS.

- [ ] **Step 5: Run the package to catch regressions**

Run: `go test ./internal/rules`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/rules/exec_gate.go internal/rules/exec_gate_test.go
git commit -m "Map shelldecomp unresolvable marker to empty cwd in exec validator input"
```

---

### Task 3: keep the marker out of the intake database

**Files:**
- Modify: `internal/daemon/server.go` (function `buildIntakeRecord`, near line 440)
- Test: `internal/daemon/server_test.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/daemon/server_test.go` (in-package, so `buildIntakeRecord` is reachable):

```go
// An unresolvable cd makes the effective-cwd field the shelldecomp marker,
// which begins with a NUL byte. The intake record must store the unknown
// directory as an empty string, not leak the marker into SQLite.
func TestBuildIntakeRecordMapsUnresolvableCwdToEmpty(t *testing.T) {
	raw := []byte(`{
		"hook_event_name": "PreToolUse",
		"session_id": "test-session",
		"cwd": "/tmp",
		"tool_name": "Bash",
		"tool_input": {"command": "cd \"$(echo /tmp)\" && grep -rn x ."}
	}`)

	record, err := buildIntakeRecord(raw, "claude", map[string]string{})
	if err != nil {
		t.Fatalf("buildIntakeRecord: %v", err)
	}
	if record.Operation.EffectiveCWD != "" {
		t.Fatalf("EffectiveCWD = %q, want empty for an unresolvable cwd", record.Operation.EffectiveCWD)
	}
}
```

If `hook.ParseHookPayload` requires more claude payload fields, copy the minimal payload shape from the existing tests in this file (for example the payload used by `TestEvaluateHook_DaemonOwnsEnforcement` near line 105) and keep the `cwd` and `tool_input.command` values above.

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/daemon -run TestBuildIntakeRecordMapsUnresolvableCwdToEmpty -v`
Expected: FAIL because `EffectiveCWD` holds the marker (its leading NUL may render invisibly; the assertion fires either way).

- [ ] **Step 3: Implement the mapping**

In `internal/daemon/server.go`, add the import `"goodkind.io/gksyntax/shelldecomp"`, then change the line in `buildIntakeRecord`:

```go
	record.Operation.EffectiveCWD = fields.String(config.FieldEffectiveCWD)
```

to:

```go
	effectiveCwd := fields.String(config.FieldEffectiveCWD)
	if effectiveCwd == shelldecomp.Unresolvable {
		// Store the unknown directory as empty; the marker's NUL byte must
		// not leak into the intake database.
		effectiveCwd = ""
	}
	record.Operation.EffectiveCWD = effectiveCwd
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/daemon -run TestBuildIntakeRecordMapsUnresolvableCwdToEmpty -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/daemon/server.go internal/daemon/server_test.go
git commit -m "Store empty effective_cwd for unresolvable cwd in intake records"
```

---

### Task 4: finish slow validator runs in the background

**Files:**
- Modify: `internal/rules/exec_gate.go` (function `runValidator`, near line 256)
- Test: `internal/rules/exec_gate_test.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/rules/exec_gate_test.go`. First the slow fake runner, next to `countingRunner`:

```go
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
```

Then the test. It must reuse one `ExecRuntime` across evaluations, because the `evalRule` helper builds a fresh runtime per call and a fresh runtime has an empty cache:

```go
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
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/rules -run TestExecGateSlowValidatorFinishesInBackgroundAndCachesBlock -v`
Expected: FAIL. The current code passes the 50 ms budget straight to the runner, the fake runner ignores it and answers after 150 ms with a block, so the first assertion fails with `got 1 violations`.

- [ ] **Step 3: Implement background completion**

In `internal/rules/exec_gate.go`, add the constant near `canonCacheTTL`:

```go
// backgroundValidatorTimeout bounds a validator run that outlived its event's
// synchronous budget. The run continues detached so its verdict can land in
// the cache and decide the next event for the same target; 30s rides out a
// busy lm-semantic-search daemon while still reclaiming the process.
const backgroundValidatorTimeout = 30 * time.Second
```

Replace the body of `runValidator` after the `BuildRequest` error check with:

```go
	done := make(chan execconcern.Verdict, 1)
	bgCtx := context.WithoutCancel(ctx)
	go func() {
		res, runErr := r.runner.Run(bgCtx, c.Command, backgroundValidatorTimeout, stdin, env)
		verdict := execconcern.Interpret(c, res, runErr)
		if verdict.Errored {
			r.log.WarnContext(bgCtx, "exec validator errored",
				"rule", rule.Name, "on_error", c.OnError, "block", verdict.Block, "err", runErr)
		}
		done <- verdict
	}()

	syncBudget := time.Duration(c.TimeoutMs) * time.Millisecond
	if syncBudget <= 0 {
		return <-done
	}
	timer := time.NewTimer(syncBudget)
	defer timer.Stop()
	select {
	case verdict := <-done:
		return verdict
	case <-timer.C:
		// The event fails open now, but the run continues so its verdict can
		// decide the next event for this target. Registration in refreshing
		// keeps a stale-entry refresh from forking a duplicate meanwhile.
		key := cacheEntryKey(c, keyView.Canonical)
		r.mu.Lock()
		_, busy := r.refreshing[key]
		if !busy {
			r.refreshing[key] = struct{}{}
		}
		r.mu.Unlock()
		go func() {
			verdict := <-done
			if !busy {
				r.mu.Lock()
				delete(r.refreshing, key)
				r.mu.Unlock()
			}
			if !verdict.Errored && c.CacheTTLMs > 0 {
				r.cacheStore(c, keyView.Canonical, verdict)
			}
		}()
		r.log.WarnContext(ctx, "exec validator exceeded synchronous budget; continuing in background",
			"rule", rule.Name, "on_error", c.OnError, "budget_ms", c.TimeoutMs)
		return execconcern.Verdict{Block: c.OnError == config.OnErrorClosed, Message: "", Errored: true}
	}
}
```

The `timeout` parameter the runner receives is the background deadline. `OSRunner` itself is unchanged, so `ErrTimeout` from the runner means the 30-second background deadline was exceeded.

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/rules -run TestExecGateSlowValidatorFinishesInBackgroundAndCachesBlock -v`
Expected: PASS.

- [ ] **Step 5: Run the rules packages with the race detector**

Run: `go test -race ./internal/rules/...`
Expected: PASS. The stale-while-revalidate tests near `exec_gate_test.go:300` exercise the same maps concurrently and must stay green.

- [ ] **Step 6: Commit**

```bash
git add internal/rules/exec_gate.go internal/rules/exec_gate_test.go
git commit -m "Continue slow exec validators in background and cache their verdicts"
```

---

### Task 5: full check, deploy, live verification

**Files:** none beyond the previous tasks.

- [ ] **Step 1: Run the full repo checks**

Run: `make check`
Expected: tests and lint pass.

- [ ] **Step 2: Deploy the daemon**

Run: `make deploy`
Expected: build, sign, and restart succeed. Then `make daemon-status` reports the service running.

- [ ] **Step 3: Verify the NUL fix live**

Note the current NUL failure count, then rerun the reproduction against an indexed codebase whose verdict is not already cached (any indexed repo not searched in the last minutes; `/Users/agoodkind/Sites/macos-fan-curve` was indexed at last check):

```bash
grep -c "environment variable contains NUL" ~/.local/state/agent-gate/agent-gate.jsonl
cd "$(echo /tmp)" && grep -rln "func main" /Users/agoodkind/Sites/macos-fan-curve --include="*.go"
grep -c "environment variable contains NUL" ~/.local/state/agent-gate/agent-gate.jsonl
```

Expected: the grep is blocked with the semantic-search message, and the NUL count is unchanged by the new event.

- [ ] **Step 4: Verify the intake column live**

```bash
sqlite3 ~/.local/state/agent-gate/sqlite/audit.db \
  "select hex(effective_cwd) from intake_events order by seq desc limit 3;"
```

Expected: the row for the reproduction command shows an empty `effective_cwd` (no `00554E...` marker hex), while older rows keep their historical bytes.

- [ ] **Step 5: Confirm a clean tree**

```bash
git status --short
```

Expected: clean tree; all work was committed per task.

---

## Self-Review

- Spec coverage: Part 1 maps to Tasks 1 and 2, Part 2 maps to Task 3, Part 3 maps to Task 4, and the spec's post-deploy check maps to Task 5. The spec's test list maps one-to-one onto the task tests.
- Placeholder scan: every code step carries complete code; the one conditional instruction (the Task 3 payload shape) names the exact fallback source file and line.
- Type consistency: `sanitizeEnv` (Task 1) is package-local to `exec`; `envValue`, `slowRunner.Calls`, and the `capturingRunner` extension match the existing harness types; `cacheEntryKey`, `r.refreshing`, `r.cacheStore`, and `keyView.Canonical` all exist in `exec_gate.go` today.
