package hook_test

import (
	"strings"
	"testing"

	"goodkind.io/agent-gate/internal/hook"
)

func TestFailOpenResponseRendering(t *testing.T) {
	tests := []struct {
		name       string
		system     hook.HookSystem
		wantStdout string
	}{
		{name: "claude", system: hook.SystemClaude, wantStdout: "{}\n"},
		{name: "cursor", system: hook.SystemCursor, wantStdout: "{\"permission\":\"allow\"}\n"},
		{name: "codex", system: hook.SystemCodex, wantStdout: "{}\n"},
		{name: "gemini", system: hook.SystemGemini, wantStdout: "{}\n"},
		{name: "copilot", system: hook.SystemCopilot, wantStdout: "{}\n"},
		{name: "vscode", system: hook.SystemVSCode, wantStdout: "{}\n"},
		{name: "unknown", system: hook.SystemUnknown, wantStdout: ""},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			response := hook.FailOpenResponse(
				testCase.system,
				"PreToolUse",
				"daemon unavailable",
				hook.FailOpenReasonDaemonUnavailable,
			)
			if response.ExitCode != 0 {
				t.Fatalf("ExitCode = %d, want 0", response.ExitCode)
			}
			if string(response.Stdout) != testCase.wantStdout {
				t.Fatalf("Stdout = %q, want %q", string(response.Stdout), testCase.wantStdout)
			}
			if len(response.Stderr) != 0 {
				t.Fatalf("Stderr = %q, want empty", string(response.Stderr))
			}
		})
	}
}

func TestRenderResponseBlocksRemainProviderSpecific(t *testing.T) {
	tests := []struct {
		name         string
		request      hook.ResponseRequest
		wantExitCode int
		wantStdout   string
		wantStderr   string
	}{
		{
			name:         "claude",
			request:      blockRequest(hook.SystemClaude, "PreToolUse"),
			wantExitCode: 2,
			wantStdout:   "{}\n",
			wantStderr:   "blocked\n",
		},
		{
			name:         "cursor",
			request:      blockRequest(hook.SystemCursor, "preToolUse"),
			wantExitCode: 0,
			wantStdout:   `"permission":"deny"`,
		},
		{
			name:         "codex",
			request:      blockRequest(hook.SystemCodex, "PreToolUse"),
			wantExitCode: 0,
			wantStdout:   `"permissionDecision":"deny"`,
		},
		{
			name:         "gemini",
			request:      blockRequest(hook.SystemGemini, "BeforeTool"),
			wantExitCode: 0,
			wantStdout:   `"decision":"deny"`,
		},
		{
			name:         "copilot",
			request:      blockRequest(hook.SystemCopilot, "PreToolUse"),
			wantExitCode: 2,
			wantStdout:   "{}\n",
			wantStderr:   "blocked\n",
		},
		{
			name:         "unknown",
			request:      blockRequest(hook.SystemUnknown, "PreToolUse"),
			wantExitCode: 2,
			wantStderr:   "blocked\n",
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			response := hook.RenderResponse(testCase.request)
			if response.ExitCode != testCase.wantExitCode {
				t.Fatalf("ExitCode = %d, want %d", response.ExitCode, testCase.wantExitCode)
			}
			if !strings.Contains(string(response.Stdout), testCase.wantStdout) {
				t.Fatalf("Stdout = %q, want substring %q", string(response.Stdout), testCase.wantStdout)
			}
			if !strings.Contains(string(response.Stderr), testCase.wantStderr) {
				t.Fatalf("Stderr = %q, want substring %q", string(response.Stderr), testCase.wantStderr)
			}
		})
	}
}

func blockRequest(system hook.HookSystem, eventName string) hook.ResponseRequest {
	return hook.ResponseRequest{
		System:         system,
		EventName:      eventName,
		Decision:       hook.ResponseDecisionBlock,
		DiagnosticText: "blocked",
	}
}
