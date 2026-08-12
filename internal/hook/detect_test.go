package hook_test

import (
	"encoding/json"
	"path/filepath"
	"slices"
	"testing"

	"goodkind.io/agent-gate/internal/hook"
)

func TestClassifyUsesPayloadEvidenceOverInheritedMarkers(t *testing.T) {
	rawPayload := []byte(`{"hook_event_name":"preToolUse","conversation_id":"cursor-1","cursor_version":"1.0"}`)
	classification := hook.Classify(
		rawPayload,
		hook.SystemUnknown,
		[]string{"agent-gate"},
		map[string]string{
			"CLAUDE_CODE_ENTRYPOINT": "cli",
			"CODEX_THREAD_ID":        "inherited",
		},
	)

	if classification.ResolvedProvider != hook.SystemCursor.String() {
		t.Fatalf(
			"resolved provider = %q, want %q",
			classification.ResolvedProvider,
			hook.SystemCursor.String(),
		)
	}
	if classification.Confidence != hook.ClassificationConfidenceHigh {
		t.Fatalf(
			"confidence = %q, want %q",
			classification.Confidence,
			hook.ClassificationConfidenceHigh,
		)
	}
	if classification.Result != hook.ClassificationResultResolved {
		t.Fatalf(
			"result = %q, want %q",
			classification.Result,
			hook.ClassificationResultResolved,
		)
	}
	if classification.Input.RawPayloadBytes != len(rawPayload) {
		t.Fatalf(
			"raw payload bytes = %d, want %d",
			classification.Input.RawPayloadBytes,
			len(rawPayload),
		)
	}
	if classification.Input.RawPayloadHash == "" {
		t.Fatal("raw payload hash is empty")
	}
	if !slices.Equal(classification.Input.Argv, []string{"agent-gate"}) {
		t.Fatalf("argv = %q, want agent-gate", classification.Input.Argv)
	}
	assertClassificationEvidence(
		t,
		classification,
		"payload_shape",
		"cursor_fields",
		hook.SystemCursor.String(),
		"match",
	)
	assertClassificationEvidence(
		t,
		classification,
		"environment",
		"CODEX_THREAD_ID",
		hook.SystemCodex.String(),
		"conflict",
	)
	assertClassificationEvidence(
		t,
		classification,
		"environment",
		"CLAUDE_CODE_ENTRYPOINT",
		hook.SystemClaude.String(),
		"conflict",
	)
}

func TestClassifyRecordsManagedCopilotRoute(t *testing.T) {
	rawPayload := []byte(`{"sessionId":"copilot-1","toolName":"run_in_terminal","toolInput":{"command":"true"}}`)
	classification := hook.Classify(
		rawPayload,
		hook.SystemCopilot,
		[]string{"agent-gate", "copilot-hook", "preToolUse"},
		map[string]string{"CLAUDE_CODE_ENTRYPOINT": "cli"},
	)

	if classification.ResolvedSystem() != hook.SystemCopilot {
		t.Fatalf(
			"resolved system = %q, want %q",
			classification.ResolvedSystem(),
			hook.SystemCopilot,
		)
	}
	if classification.Input.ProviderHint != hook.SystemCopilot.String() {
		t.Fatalf(
			"provider hint = %q, want %q",
			classification.Input.ProviderHint,
			hook.SystemCopilot.String(),
		)
	}
	assertClassificationEvidence(
		t,
		classification,
		"provider_hint",
		hook.SystemCopilot.String(),
		hook.SystemCopilot.String(),
		"match",
	)
	assertClassificationEvidence(
		t,
		classification,
		"environment",
		"CLAUDE_CODE_ENTRYPOINT",
		hook.SystemClaude.String(),
		"conflict",
	)
}

func TestClassifyReportsAmbiguousSharedEvent(t *testing.T) {
	classification := hook.Classify(
		[]byte(`{"hook_event_name":"preToolUse"}`),
		hook.SystemUnknown,
		[]string{"agent-gate"},
		map[string]string{},
	)

	if classification.ResolvedSystem() != hook.SystemUnknown {
		t.Fatalf("resolved system = %q, want unknown", classification.ResolvedSystem())
	}
	if classification.Result != hook.ClassificationResultAmbiguous {
		t.Fatalf(
			"result = %q, want %q",
			classification.Result,
			hook.ClassificationResultAmbiguous,
		)
	}
}

func TestClassifyReportsEmptyInputAsInvalid(t *testing.T) {
	classification := hook.Classify(
		[]byte{},
		hook.SystemUnknown,
		[]string{"agent-gate"},
		map[string]string{},
	)

	if classification.Result != hook.ClassificationResultInvalid {
		t.Fatalf(
			"result = %q, want %q",
			classification.Result,
			hook.ClassificationResultInvalid,
		)
	}
	if classification.Confidence != hook.ClassificationConfidenceNone {
		t.Fatalf(
			"confidence = %q, want %q",
			classification.Confidence,
			hook.ClassificationConfidenceNone,
		)
	}
	if classification.Input.RawPayloadBytes != 0 {
		t.Fatalf("raw payload bytes = %d, want 0", classification.Input.RawPayloadBytes)
	}
	if classification.Input.RawPayloadHash == "" {
		t.Fatal("raw payload hash is empty")
	}
}

func TestClassifyReportsNonObjectAndMalformedInputAsInvalid(t *testing.T) {
	for _, rawPayload := range []string{"null", "[]", `"value"`, "{"} {
		classification := hook.Classify(
			[]byte(rawPayload), hook.SystemUnknown, []string{"agent-gate"}, nil,
		)

		if classification.Result != hook.ClassificationResultInvalid {
			t.Fatalf("payload %q result = %q, want invalid", rawPayload, classification.Result)
		}
		if classification.Confidence != hook.ClassificationConfidenceNone {
			t.Fatalf("payload %q confidence = %q, want none", rawPayload, classification.Confidence)
		}
		if classification.Input.RawPayloadBytes != len(rawPayload) {
			t.Fatalf(
				"payload %q byte count = %d, want %d",
				rawPayload,
				classification.Input.RawPayloadBytes,
				len(rawPayload),
			)
		}
		if classification.Input.Payload.Status != hook.SignalStatusUnreadable {
			t.Fatalf(
				"payload %q status = %q, want unreadable",
				rawPayload,
				classification.Input.Payload.Status,
			)
		}
	}
}

func TestClassifyInvalidInputDoesNotResolveProvider(t *testing.T) {
	classification := hook.Classify(
		[]byte("{"),
		hook.SystemClaude,
		[]string{"agent-gate", "managed-hook", "claude"},
		map[string]string{"CODEX_THREAD_ID": "inherited"},
	)

	if classification.ResolvedSystem() != hook.SystemUnknown {
		t.Fatalf("resolved system = %q, want unknown", classification.ResolvedSystem())
	}
	if classification.Result != hook.ClassificationResultInvalid {
		t.Fatalf("result = %q, want invalid", classification.Result)
	}
	if classification.Confidence != hook.ClassificationConfidenceNone {
		t.Fatalf("confidence = %q, want none", classification.Confidence)
	}
	if len(classification.Conflicts) != 0 {
		t.Fatalf("conflicts = %#v, want none", classification.Conflicts)
	}
	if len(classification.Evidence) == 0 {
		t.Fatal("classification evidence is empty")
	}
	for _, evidence := range classification.Evidence {
		if evidence.Result != "candidate" {
			t.Fatalf("evidence = %#v, want candidate result", evidence)
		}
	}
}

func TestClassifyRecordsPayloadFieldsCasingAndProviderIdentifier(t *testing.T) {
	classification := hook.ClassifyWithContext(
		[]byte(`{"providerId":"gemini-cli","hook_event_name":"SessionStart","sessionId":"s1"}`),
		"",
		[]string{"agent-gate"},
		nil,
		hook.InvocationContext{},
	)

	if classification.ResolvedSystem() != hook.SystemGemini {
		t.Fatalf("resolved system = %q, want gemini", classification.ResolvedSystem())
	}
	if classification.Confidence != hook.ClassificationConfidenceMedium {
		t.Fatalf("confidence = %q, want medium", classification.Confidence)
	}
	if len(classification.Input.Payload.ProviderIdentifiers) != 1 {
		t.Fatalf("provider identifiers = %#v", classification.Input.Payload.ProviderIdentifiers)
	}
	assertPayloadField(t, classification, "providerId", "lower_camel")
	assertPayloadField(t, classification, "hook_event_name", "snake_case")
	assertClassificationEvidence(
		t, classification, "payload_identifier", "gemini-cli",
		hook.SystemGemini.String(), "match",
	)
	assertClassificationEvidence(
		t, classification, "payload_shape", "copilot_fields",
		hook.SystemCopilot.String(), "conflict",
	)
}

func TestClassifyPayloadOverridesInheritedManagedAndProcessEvidence(t *testing.T) {
	invocation := hook.InvocationContext{
		ManagedRegistration: hook.ObservedValue{
			Value: "claude", Source: "managed_registration", Provenance: "hook_tag",
			Status: hook.SignalStatusObserved,
		},
		ParentProcess: hook.ProcessEvidence{
			Name: "codex", ExecutablePath: filepath.Join(t.TempDir(), "codex"),
			Source:     "parent_process",
			Provenance: "operating_system", Status: hook.SignalStatusObserved,
		},
		Environment: []hook.EnvironmentEvidence{{
			Name: "CLAUDE_CODE_ENTRYPOINT", Value: "cli", Category: "provider_environment",
			Source: "environment", Provenance: "inherited_environment",
			Status: hook.SignalStatusObserved,
		}},
	}
	classification := hook.ClassifyWithContext(
		[]byte(`{"hook_event_name":"preToolUse","cursor_version":"1.0","conversation_id":"c1"}`),
		"",
		[]string{"agent-gate", "managed-hook", "claude"},
		nil,
		invocation,
	)

	if classification.ResolvedSystem() != hook.SystemCursor {
		t.Fatalf("resolved system = %q, want cursor", classification.ResolvedSystem())
	}
	if len(classification.Conflicts) < 3 {
		t.Fatalf("conflicts = %#v, want inherited registration, process, and environment", classification.Conflicts)
	}
	assertClassificationEvidence(
		t, classification, "managed_registration", "claude",
		hook.SystemClaude.String(), "conflict",
	)
	assertClassificationEvidence(
		t, classification, "parent_process", "codex",
		hook.SystemCodex.String(), "conflict",
	)
}

func TestClassifyManagedRegistrationOverridesInheritedContext(t *testing.T) {
	invocation := hook.InvocationContext{
		ManagedRegistration: hook.ObservedValue{
			Value: "claude", Source: "managed_registration", Provenance: "hook_tag",
			Status: hook.SignalStatusObserved,
		},
		ParentProcess: hook.ProcessEvidence{
			Name: "codex", ExecutablePath: filepath.Join(t.TempDir(), "codex"),
			Source:     "parent_process",
			Provenance: "operating_system", Status: hook.SignalStatusObserved,
		},
		Environment: []hook.EnvironmentEvidence{{
			Name: "CODEX_THREAD_ID", Value: "inherited", Category: "provider_environment",
			Source: "environment", Provenance: "inherited_environment",
			Status: hook.SignalStatusObserved,
		}},
	}
	classification := hook.ClassifyWithContext(
		[]byte(`{"hook_event_name":"SessionStart","session_id":"s1","transcript_path":"/tmp/t"}`),
		"",
		[]string{"agent-gate", "managed-hook", "claude"},
		nil,
		invocation,
	)

	if classification.ResolvedSystem() != hook.SystemClaude {
		t.Fatalf("resolved system = %q, want claude", classification.ResolvedSystem())
	}
	if classification.Confidence != hook.ClassificationConfidenceLow {
		t.Fatalf("confidence = %q, want low", classification.Confidence)
	}
	assertClassificationEvidence(
		t, classification, "managed_registration", "claude",
		hook.SystemClaude.String(), "match",
	)
	assertClassificationEvidence(
		t, classification, "parent_process", "codex",
		hook.SystemCodex.String(), "conflict",
	)
	assertClassificationEvidence(
		t, classification, "environment", "CODEX_THREAD_ID",
		hook.SystemCodex.String(), "conflict",
	)
}

func TestClassifyRecordsHookInjectedEnvironmentProvenance(t *testing.T) {
	invocation := hook.InvocationContext{
		Environment: []hook.EnvironmentEvidence{{
			Name: "AGENT_GATE_HOOK_PROVIDER", Value: "copilot", Category: "hook_environment",
			Source: "environment", Provenance: "hook_injected",
			Status: hook.SignalStatusObserved,
		}},
	}
	classification := hook.ClassifyWithContext(
		[]byte(`{"session_id":"s1"}`), "", []string{"agent-gate"}, nil, invocation,
	)

	if classification.ResolvedSystem() != hook.SystemCopilot {
		t.Fatalf("resolved system = %q, want copilot", classification.ResolvedSystem())
	}
	assertClassificationEvidenceWithProvenance(
		t, classification, "environment", "hook_injected", "AGENT_GATE_HOOK_PROVIDER",
		hook.SystemCopilot.String(), "match",
	)
}

func TestClassifyRecordsMissingAndUnreadableEvidence(t *testing.T) {
	invocation := hook.InvocationContext{
		WorkingDirectory: hook.ObservedValue{
			Source: "working_directory", Provenance: "operating_system",
			Status: hook.SignalStatusUnreadable,
		},
		CollectionIssues: []hook.CollectionIssue{{
			Source: "working_directory", Status: hook.SignalStatusUnreadable,
			Detail: "working directory unavailable",
		}},
	}
	classification := hook.ClassifyWithContext(
		[]byte(`{"field":"value"}`), "", []string{"agent-gate"}, nil, invocation,
	)

	if classification.Result != hook.ClassificationResultUnknown {
		t.Fatalf("result = %q, want unknown", classification.Result)
	}
	if classification.Input.Invocation.WorkingDirectory.Status != hook.SignalStatusUnreadable {
		t.Fatalf("working directory = %#v", classification.Input.Invocation.WorkingDirectory)
	}
	if len(classification.Input.Invocation.CollectionIssues) != 1 {
		t.Fatalf("collection issues = %#v", classification.Input.Invocation.CollectionIssues)
	}
}

func assertPayloadField(
	t *testing.T,
	classification hook.Classification,
	name string,
	casing string,
) {
	t.Helper()
	for _, field := range classification.Input.Payload.Fields {
		if field.Name == name && field.Casing == casing {
			return
		}
	}
	t.Fatalf("payload field %q with casing %q missing: %#v", name, casing, classification.Input.Payload.Fields)
}

func assertClassificationEvidenceWithProvenance(
	t *testing.T,
	classification hook.Classification,
	source string,
	provenance string,
	signal string,
	provider string,
	result string,
) {
	t.Helper()
	for _, evidence := range classification.Evidence {
		if evidence.Source == source && evidence.Provenance == provenance &&
			evidence.Signal == signal && evidence.Provider == provider &&
			evidence.Result == result {
			return
		}
	}
	t.Fatalf("classification evidence missing provenance: %#v", classification.Evidence)
}

func assertClassificationEvidence(
	t *testing.T,
	classification hook.Classification,
	source string,
	signal string,
	provider string,
	result string,
) {
	t.Helper()
	for _, evidence := range classification.Evidence {
		if evidence.Source == source &&
			evidence.Signal == signal &&
			evidence.Provider == provider &&
			evidence.Result == result {
			return
		}
	}
	t.Fatalf(
		"classification evidence missing source=%q signal=%q provider=%q result=%q: %#v",
		source,
		signal,
		provider,
		result,
		classification.Evidence,
	)
}

var allTrackedEnvVars = []string{
	"CODEX_THREAD_ID",
	"CODEX_CI",
	"COPILOT_OTEL_FILE_EXPORTER_PATH",
	"COPILOT_OTEL_ENABLED",
	"COPILOT_OTEL_EXPORTER_TYPE",
	"CURSOR_VERSION",
	"CURSOR_WORKSPACE_NAME",
	"CURSOR_MODE",
	"GEMINI_CLI",
	"CLAUDE_CODE_ENTRYPOINT",
	"AI_AGENT",
	"VSCODE_PID",
	"VSCODE_IPC_HOOK",
	"TERM_PROGRAM",
}

func clearTrackedEnv(t *testing.T) {
	t.Helper()
	for _, variable := range allTrackedEnvVars {
		t.Setenv(variable, "")
	}
}

func TestDetectEvidenceResolution(t *testing.T) {
	cases := []struct {
		name    string
		env     map[string]string
		payload hook.DetectionPayload
		hint    hook.System
		want    hook.System
	}{
		{name: "conflicting provider environments are ambiguous", env: map[string]string{"CODEX_THREAD_ID": "abc", "CLAUDE_CODE_ENTRYPOINT": "cli"}, payload: hook.DetectionPayload{HookEventName: "PreToolUse"}, want: hook.SystemUnknown},
		{name: "codex CI flag alone", env: map[string]string{"CODEX_CI": "1"}, payload: hook.DetectionPayload{HookEventName: "PreToolUse"}, want: hook.SystemCodex},
		{name: "cursor env beats claude payload", env: map[string]string{"CURSOR_VERSION": "0.42"}, payload: hook.DetectionPayload{HookEventName: "PreToolUse", PermissionMode: "default"}, want: hook.SystemCursor},
		{name: "cursor payload markers", payload: hook.DetectionPayload{HookEventName: "PreToolUse", CursorVersion: "0.42"}, want: hook.SystemCursor},
		{name: "shared lower camel event is ambiguous", payload: hook.DetectionPayload{HookEventName: "beforeShellExecution"}, want: hook.SystemUnknown},
		{name: "gemini env", env: map[string]string{"GEMINI_CLI": "1", "CLAUDECODE": "1"}, payload: hook.DetectionPayload{HookEventName: "PreToolUse"}, want: hook.SystemGemini},
		{name: "gemini event name", payload: hook.DetectionPayload{HookEventName: "BeforeTool"}, want: hook.SystemGemini},
		{name: "claude env CLAUDE_CODE_ENTRYPOINT", env: map[string]string{"CLAUDE_CODE_ENTRYPOINT": "cli"}, payload: hook.DetectionPayload{HookEventName: "PreToolUse"}, want: hook.SystemClaude},
		{name: "claude env AI_AGENT", env: map[string]string{"AI_AGENT": "claude-code/2.1.121/agent"}, payload: hook.DetectionPayload{HookEventName: "PreToolUse"}, want: hook.SystemClaude},
		{name: "claude payload transcript_path", payload: hook.DetectionPayload{HookEventName: "PreToolUse", TranscriptPath: "/tmp/x.jsonl"}, want: hook.SystemClaude},
		{name: "claude payload permission_mode", payload: hook.DetectionPayload{HookEventName: "Stop", PermissionMode: "default"}, want: hook.SystemClaude},
		{name: "copilot env beats claude payload", env: map[string]string{"COPILOT_OTEL_FILE_EXPORTER_PATH": "/dev/null", "VSCODE_PID": "62178"}, payload: hook.DetectionPayload{HookEventName: "PreToolUse", TranscriptPath: "/tmp/x.jsonl"}, want: hook.SystemCopilot},
		{name: "copilot OTEL_ENABLED alone", env: map[string]string{"COPILOT_OTEL_ENABLED": "true"}, payload: hook.DetectionPayload{HookEventName: "UserPromptSubmit"}, want: hook.SystemCopilot},
		{name: "vscode env without other markers", env: map[string]string{"VSCODE_PID": "12345"}, payload: hook.DetectionPayload{HookEventName: "PreToolUse"}, want: hook.SystemVSCode},
		{name: "vscode env loses to claude env", env: map[string]string{"VSCODE_PID": "12345", "CLAUDE_CODE_ENTRYPOINT": "cli"}, payload: hook.DetectionPayload{HookEventName: "PreToolUse"}, want: hook.SystemClaude},
		{name: "codex subcommand hint beats shared transcript path", payload: hook.DetectionPayload{HookEventName: "PostToolUse", TranscriptPath: "/tmp/codex.jsonl"}, hint: hook.SystemCodex, want: hook.SystemCodex},
		{name: "codex subcommand hint beats leaked claude env", env: map[string]string{"CLAUDE_CODE_ENTRYPOINT": "cli"}, payload: hook.DetectionPayload{HookEventName: "PostToolUse", TranscriptPath: "/tmp/codex.jsonl"}, hint: hook.SystemCodex, want: hook.SystemCodex},
		{name: "subcommand hint reached when nothing else matches", payload: hook.DetectionPayload{HookEventName: "PreToolUse"}, hint: hook.SystemCodex, want: hook.SystemCodex},
		{name: "managed route records conflicting cursor payload", payload: hook.DetectionPayload{HookEventName: "PreToolUse", CursorVersion: "0.42"}, hint: hook.SystemCodex, want: hook.SystemCodex},
		{name: "pure pascal case with no markers returns unknown", payload: hook.DetectionPayload{HookEventName: "PreToolUse"}, want: hook.SystemUnknown},
		{name: "empty event name returns unknown", payload: hook.DetectionPayload{}, want: hook.SystemUnknown},
		{name: "term_program ghostty does not trigger vscode", env: map[string]string{"TERM_PROGRAM": "ghostty"}, payload: hook.DetectionPayload{HookEventName: "PreToolUse"}, want: hook.SystemUnknown},
		{name: "term_program vscode alone is not a vscode signal", env: map[string]string{"TERM_PROGRAM": "vscode"}, payload: hook.DetectionPayload{HookEventName: "PreToolUse"}, want: hook.SystemUnknown},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			rawPayload, err := json.Marshal(testCase.payload)
			if err != nil {
				t.Fatalf("Marshal payload: %v", err)
			}
			got := hook.Classify(
				rawPayload,
				testCase.hint,
				nil,
				testCase.env,
			).ResolvedSystem()
			if got != testCase.want {
				t.Errorf("Classify() = %v, want %v", got, testCase.want)
			}
		})
	}
}

func TestHookPayloadAccessors(t *testing.T) {
	payload, err := hook.ParseHookPayload(hook.SystemClaude, []byte(`{"hook_event_name":"PreToolUse","session_id":"abc123","cwd":"/tmp/project"}`))
	if err != nil {
		t.Fatalf("ParseHookPayload: %v", err)
	}
	if got := payload.EventName(); got != "PreToolUse" {
		t.Errorf("EventName() = %q, want %q", got, "PreToolUse")
	}
	if got := payload.SessionID(); got != "abc123" {
		t.Errorf("SessionID() = %q, want %q", got, "abc123")
	}
	if got := payload.CWD(); got != "/tmp/project" {
		t.Errorf("CWD() = %q, want %q", got, "/tmp/project")
	}
}

func TestUnknownPayloadSessionIDFallsBackToConversationID(t *testing.T) {
	payload, err := hook.ParseHookPayload(hook.SystemUnknown, []byte(`{"conversation_id":"cursor-conv-999"}`))
	if err != nil {
		t.Fatalf("ParseHookPayload: %v", err)
	}
	if got := payload.SessionID(); got != "cursor-conv-999" {
		t.Errorf("SessionID() fallback = %q, want %q", got, "cursor-conv-999")
	}
}
