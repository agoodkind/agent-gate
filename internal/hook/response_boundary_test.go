package hook_test

import (
	"strings"
	"testing"

	"goodkind.io/agent-gate/internal/hook"
)

func TestFailOpenResponseRendering(t *testing.T) {
	tests := []struct {
		name       string
		system     hook.System
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

func TestRenderResponseBlockIncludesEventID(t *testing.T) {
	tests := []struct {
		name       string
		system     hook.System
		eventName  string
		wantStdout bool
	}{
		{name: "claude", system: hook.SystemClaude, eventName: "PreToolUse", wantStdout: false},
		{name: "cursor", system: hook.SystemCursor, eventName: "preToolUse", wantStdout: true},
		{name: "codex", system: hook.SystemCodex, eventName: "PreToolUse", wantStdout: true},
		{name: "gemini", system: hook.SystemGemini, eventName: "BeforeTool", wantStdout: true},
		{name: "unknown", system: hook.SystemUnknown, eventName: "PreToolUse", wantStdout: false},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			request := blockRequest(testCase.system, testCase.eventName)
			request.EventID = "intake_test"
			response := hook.RenderResponse(request)
			haystack := string(response.Stderr)
			if testCase.wantStdout {
				haystack = string(response.Stdout)
			}
			if !strings.Contains(haystack, "agent-gate event_id: intake_test") {
				t.Fatalf("response missing event_id: stdout=%q stderr=%q", string(response.Stdout), string(response.Stderr))
			}
		})
	}
}

func TestRenderResponseInjectsLifecycleContext(t *testing.T) {
	tests := []struct {
		name      string
		system    hook.System
		eventName string
		wantJSON  string
	}{
		{
			name:      "claude session start",
			system:    hook.SystemClaude,
			eventName: "SessionStart",
			wantJSON:  `"additionalContext":"start context"`,
		},
		{
			name:      "codex prompt submit",
			system:    hook.SystemCodex,
			eventName: "UserPromptSubmit",
			wantJSON:  `"additionalContext":"start context"`,
		},
		{
			name:      "cursor session start",
			system:    hook.SystemCursor,
			eventName: "sessionStart",
			wantJSON:  `"additional_context":"start context"`,
		},
		{
			name:      "cursor stop follow-up",
			system:    hook.SystemCursor,
			eventName: "stop",
			wantJSON:  `"followup_message":"start context"`,
		},
		{
			name:      "copilot session start",
			system:    hook.SystemCopilot,
			eventName: "SessionStart",
			wantJSON:  `"additionalContext":"start context"`,
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			response := hook.RenderResponse(hook.ResponseRequest{
				System:      testCase.system,
				EventName:   testCase.eventName,
				Decision:    hook.ResponseDecisionAllow,
				ContextText: "start context",
			})
			if response.ExitCode != 0 {
				t.Fatalf("ExitCode = %d, want 0", response.ExitCode)
			}
			if !strings.Contains(string(response.Stdout), testCase.wantJSON) {
				t.Fatalf("Stdout = %q, want substring %q", string(response.Stdout), testCase.wantJSON)
			}
		})
	}
}

func TestRenderResponseMutatesEverySupportedTarget(t *testing.T) {
	tests := []struct {
		name      string
		system    hook.System
		eventName string
		mutation  string
		prompt    string
		wantJSON  []string
	}{
		{
			name: "claude tool input", system: hook.SystemClaude, eventName: "PreToolUse",
			mutation: `{"command":"make test"}`,
			wantJSON: []string{
				`"permissionDecision":"allow"`,
				`"updatedInput":{"command":"make test"}`,
			},
		},
		{
			name: "claude tool output", system: hook.SystemClaude, eventName: "PostToolUse",
			mutation: `{"content":"safe"}`, wantJSON: []string{`"updatedToolOutput":{"content":"safe"}`},
		},
		{
			name: "codex tool input", system: hook.SystemCodex, eventName: "PreToolUse",
			mutation: `{"command":"make test"}`,
			wantJSON: []string{
				`"permissionDecision":"allow"`,
				`"updatedInput":{"command":"make test"}`,
			},
		},
		{
			name: "cursor MCP output", system: hook.SystemCursor, eventName: "postToolUse",
			mutation: `{"content":"safe"}`, wantJSON: []string{`"updated_mcp_tool_output":{"content":"safe"}`},
		},
		{
			name: "copilot tool input", system: hook.SystemCopilot, eventName: "preToolUse",
			mutation: `{"command":"make test"}`, wantJSON: []string{`"modifiedArgs":{"command":"make test"}`},
		},
		{
			name: "copilot tool output", system: hook.SystemCopilot, eventName: "postToolUse",
			mutation: `{"resultType":"success","textResultForLlm":"safe"}`,
			wantJSON: []string{`"modifiedResult":{"resultType":"success","textResultForLlm":"safe"}`},
		},
		{
			name: "copilot prompt", system: hook.SystemCopilot, eventName: "userPromptTransformed",
			mutation: "replacement prompt", prompt: "original prompt", wantJSON: []string{`"modifiedTransformedPrompt":"replacement prompt"`},
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			response := hook.RenderResponse(hook.ResponseRequest{
				System:       testCase.system,
				EventName:    testCase.eventName,
				Decision:     hook.ResponseDecisionAllow,
				MutationText: testCase.mutation,
				PromptText:   testCase.prompt,
			})
			if response.ExitCode != 0 {
				t.Fatalf("ExitCode = %d, want 0", response.ExitCode)
			}
			for _, wantJSON := range testCase.wantJSON {
				if !strings.Contains(string(response.Stdout), wantJSON) {
					t.Fatalf("Stdout = %q, want substring %q", response.Stdout, wantJSON)
				}
			}
		})
	}
}

func TestRenderResponseRejectsInvalidCopilotToolOutputMutation(t *testing.T) {
	response := hook.RenderResponse(hook.ResponseRequest{
		System:       hook.SystemCopilot,
		EventName:    "postToolUse",
		Decision:     hook.ResponseDecisionAllow,
		MutationText: `{"content":"safe"}`,
	})
	if strings.Contains(string(response.Stdout), "modifiedResult") {
		t.Fatalf("invalid Copilot tool output mutation rendered: %q", response.Stdout)
	}
}

func TestRenderResponseComposesCopilotPromptInjection(t *testing.T) {
	response := hook.RenderResponse(hook.ResponseRequest{
		System:      hook.SystemCopilot,
		EventName:   "userPromptTransformed",
		Decision:    hook.ResponseDecisionAllow,
		ContextText: "turn context",
		PromptText:  "transformed prompt",
	})
	if !strings.Contains(string(response.Stdout), `"modifiedTransformedPrompt":"turn context\n\ntransformed prompt"`) {
		t.Fatalf("Stdout = %q", response.Stdout)
	}
}

func TestRenderResponseNoOpsUnsupportedOrInvalidEffects(t *testing.T) {
	tests := []hook.ResponseRequest{
		{
			System: hook.SystemCursor, EventName: "beforeSubmitPrompt",
			Decision: hook.ResponseDecisionAllow, ContextText: "not supported",
		},
		{
			System: hook.SystemCodex, EventName: "PreToolUse",
			Decision: hook.ResponseDecisionAllow, MutationText: "not JSON",
		},
	}
	for _, request := range tests {
		response := hook.RenderResponse(request)
		if string(response.Stdout) == "" || strings.Contains(string(response.Stdout), "not supported") {
			t.Fatalf("unsupported response = %q", response.Stdout)
		}
	}
}

func blockRequest(system hook.System, eventName string) hook.ResponseRequest {
	return hook.ResponseRequest{
		System:         system,
		EventName:      eventName,
		Decision:       hook.ResponseDecisionBlock,
		DiagnosticText: "blocked",
		EventID:        "",
		FailOpenReason: "",
	}
}
