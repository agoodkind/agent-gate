# Codex Hook Trust Guidance Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Print exact Codex Desktop and CLI trust instructions after every successful Codex hook installation.

**Architecture:** Keep Codex hook registration in `~/.codex/config.toml` and leave Codex trust decisions to Codex. Extend the existing hook installation output after a successful Codex write. Verify the user-visible output through the real prepare and apply path.

**Tech Stack:** Go, standard library, existing installer package tests.

## Global Constraints

- Do not read, write, migrate, remove, or test legacy `~/.codex/hooks.json` behavior.
- Do not write Codex trust hashes.
- Print guidance after every successful Codex installation, including idempotent runs.
- Cover Codex Desktop and Codex CLI separately.
- Link `https://developers.openai.com/codex/hooks/`.
- Run repository checks through `make check`.

---

### Task 1: Print Codex trust guidance

**Files:**
- Modify: `internal/install/install_test.go:467`
- Modify: `internal/install/install.go:20`
- Modify: `internal/install/install.go:244`

**Interfaces:**
- Consumes: `HooksOptions.Stdout`, `PrepareHookInstallation(HooksOptions)`, and `ApplyHookInstallation(*HookInstallationPlan)`.
- Produces: `codexHookTrustGuidance` and a Codex-only output branch in `ApplyHookInstallation`.

- [ ] **Step 1: Write the failing behavior test**

Add this test beside the existing Codex installation test. The production break
it catches is a successful Codex installation that omits the review steps, or
prints them only on the first run.

```go
func TestInstallHooksPrintsCodexTrustInstructionsOnEveryRun(t *testing.T) {
	binPath := writeExecutable(t, filepath.Join(t.TempDir(), "agent-gate"))
	homeDir := t.TempDir()
	configPath := filepath.Join(homeDir, ".codex", "config.toml")
	var output strings.Builder

	options := DefaultHooksOptions(binPath)
	options.HomeDir = homeDir
	options.Stdout = &output
	options.InstallClaude = false
	options.InstallCursor = false
	options.InstallGemini = false
	options.InstallCopilot = false

	want := "agent-gate install: updated " + configPath + " (codex hooks)\n" + `agent-gate install: Codex hooks require review before they can run.
Codex Desktop:
  1. Open Settings > Hooks.
  2. Click Reload hooks.
  3. Under From Config, open User config.
  4. Click Trust for each agent-gate hook marked New hook or Hook changed since last trusted.
Codex CLI:
  1. Restart Codex CLI.
  2. Run /hooks.
  3. Select each event containing an agent-gate hook and press Enter.
  4. Select each agent-gate hook and press t to trust it.
OpenAI docs: https://developers.openai.com/codex/hooks/
`

	for runNumber := 1; runNumber <= 2; runNumber++ {
		output.Reset()
		if err := installHooks(options); err != nil {
			t.Fatalf("InstallHooks run %d: %v", runNumber, err)
		}
		if got := output.String(); got != want {
			t.Fatalf("run %d output = %q, want %q", runNumber, got, want)
		}
	}
}
```

- [ ] **Step 2: Run the test and confirm the missing behavior**

Run:

```bash
go test ./internal/install -run TestInstallHooksPrintsCodexTrustInstructionsOnEveryRun -count=1
```

Expected: FAIL because output ends after the existing Codex update line.

- [ ] **Step 3: Add the exact guidance**

Add this package constant near the installer constants:

```go
const codexHookTrustGuidance = `agent-gate install: Codex hooks require review before they can run.
Codex Desktop:
  1. Open Settings > Hooks.
  2. Click Reload hooks.
  3. Under From Config, open User config.
  4. Click Trust for each agent-gate hook marked New hook or Hook changed since last trusted.
Codex CLI:
  1. Restart Codex CLI.
  2. Run /hooks.
  3. Select each event containing an agent-gate hook and press Enter.
  4. Select each agent-gate hook and press t to trust it.
OpenAI docs: https://developers.openai.com/codex/hooks/
`
```

After the existing successful update message in `ApplyHookInstallation`, add:

```go
		if write.provider == "codex" {
			_, _ = fmt.Fprint(plan.writer, codexHookTrustGuidance)
		}
```

- [ ] **Step 4: Run the targeted test**

Run:

```bash
go test ./internal/install -run TestInstallHooksPrintsCodexTrustInstructionsOnEveryRun -count=1
```

Expected: PASS.

- [ ] **Step 5: Run all repository checks**

Run:

```bash
make check
```

Expected: PASS.

- [ ] **Step 6: Commit the implementation**

```bash
git add internal/install/install.go internal/install/install_test.go
git commit -S -m "Print Codex hook trust instructions" \
  -m "Co-authored-by: Codex <noreply@openai.com>"
```
