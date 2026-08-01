package hook_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"goodkind.io/agent-gate/internal/hook"
)

// TestFailOpenNoticeIsNotDoublePrefixed keeps the stderr warning readable. The
// notice already names agent-gate, so prefixing it again produced
// "agent-gate: agent-gate did not evaluate ...".
func TestFailOpenNoticeIsNotDoublePrefixed(t *testing.T) {
	response := hook.FailOpenResponse(
		hook.SystemUnknown, "PreToolUse", "no socket",
		hook.FailOpenReasonDaemonUnavailable,
	)
	stderr := string(response.Stderr)
	if strings.Count(stderr, "agent-gate") != 1 {
		t.Fatalf("stderr = %q, want agent-gate named exactly once", stderr)
	}
	if strings.Contains(stderr, "agent-gate: agent-gate") {
		t.Fatalf("stderr = %q, want no duplicated prefix", stderr)
	}
}

// TestEveryProviderSeesTheFailOpenNotice is the regression for a warning that
// reached only some providers. A fail-open must never be silent: an allow that
// nobody evaluated is otherwise indistinguishable from one that passed every
// rule, which is what let a ten hour outage read as a quiet period.
func TestEveryProviderSeesTheFailOpenNotice(t *testing.T) {
	systems := []hook.System{
		hook.SystemClaude,
		hook.SystemVSCode,
		hook.SystemCursor,
		hook.SystemCodex,
		hook.SystemGemini,
		hook.SystemCopilot,
		hook.SystemUnknown,
	}
	for _, system := range systems {
		t.Run(system.String(), func(t *testing.T) {
			response := hook.FailOpenResponse(
				system, "PreToolUse", "no socket",
				hook.FailOpenReasonDaemonUnavailable,
			)
			if response.ExitCode != 0 {
				t.Fatalf("exit = %d, want 0; a fail-open must not block the call",
					response.ExitCode)
			}
			combined := string(response.Stdout) + string(response.Stderr)
			if !strings.Contains(combined, "no rule was enforced") {
				t.Fatalf("output = %q, want the fail-open notice", combined)
			}
			if !strings.Contains(combined, string(hook.FailOpenReasonDaemonUnavailable)) {
				t.Fatalf("output = %q, want it to name the reason", combined)
			}
		})
	}
}

// TestEvaluatedAllowStaysSilent keeps the notice from leaking onto a call the
// daemon really did evaluate, which would make every allow look like an outage.
func TestEvaluatedAllowStaysSilent(t *testing.T) {
	systems := []hook.System{
		hook.SystemClaude, hook.SystemCursor, hook.SystemCodex,
		hook.SystemGemini, hook.SystemCopilot, hook.SystemUnknown,
	}
	for _, system := range systems {
		t.Run(system.String(), func(t *testing.T) {
			response := hook.RenderResponse(hook.ResponseRequest{
				System: system, EventName: "PreToolUse",
				Decision: hook.ResponseDecisionAllow,
			})
			if len(response.Stderr) != 0 {
				t.Fatalf("stderr = %q, want empty for an evaluated allow", response.Stderr)
			}
		})
	}
}

// TestFailOpenRecordReportsItsOwnTruncation is the regression for a capped
// history presented as a complete one. Past the size cap later calls go
// unrecorded, so a count reported without that qualifier understates the very
// outage the record exists to prove.
func TestFailOpenRecordReportsItsOwnTruncation(t *testing.T) {
	stateHome := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateHome)

	// Fill the record past its cap. Each call carries a long detail so the file
	// reaches the limit without needing a very large number of records.
	detail := strings.Repeat("x", 4096)
	for range 3000 {
		hook.RecordFailOpen(
			hook.FailOpenReasonDaemonUnavailable, hook.SystemClaude,
			"PreToolUse", "Bash", "/repo", detail,
		)
	}

	summary, err := hook.FailOpenRecordSummary()
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if !summary.Truncated {
		t.Fatalf("summary reports %d records as a complete history after the cap",
			summary.Count)
	}
	if summary.Count == 0 {
		t.Fatal("the cap dropped everything, so no outage window survives at all")
	}

	// The file itself must stay under the cap, or the guard bought nothing.
	info, err := os.Stat(filepath.Join(stateHome, "agent-gate", "fail-open.jsonl"))
	if err != nil {
		t.Fatalf("stat record: %v", err)
	}
	if info.Size() > 8<<20 {
		t.Fatalf("record is %d bytes, above its 8 MiB cap", info.Size())
	}
}
