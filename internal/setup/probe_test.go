package setup

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/evaluation"
	installer "goodkind.io/agent-gate/internal/install"
	"goodkind.io/agent-gate/internal/intake"
)

func TestLifecycleProbePayloadsContainOnlySessionIdentity(t *testing.T) {
	for _, test := range []struct {
		provider   installer.Provider
		eventName  string
		sessionKey string
	}{
		{installer.ProviderClaude, "SessionStart", "session_id"},
		{installer.ProviderCodex, "SessionStart", "session_id"},
		{installer.ProviderCursor, "sessionStart", "session_id"},
		{installer.ProviderGemini, "SessionStart", "session_id"},
		{installer.ProviderCopilot, "", "sessionId"},
	} {
		t.Run(string(test.provider), func(t *testing.T) {
			payload, err := marshalLifecycleProbePayload(test.provider, "setup-123")
			if err != nil {
				t.Fatalf("marshalLifecycleProbePayload: %v", err)
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(payload, &fields); err != nil {
				t.Fatalf("Unmarshal payload: %v", err)
			}
			if len(fields) != 2 && test.provider != installer.ProviderCopilot {
				t.Fatalf("fields = %#v, want session and event only", fields)
			}
			if len(fields) != 1 && test.provider == installer.ProviderCopilot {
				t.Fatalf("fields = %#v, want session only", fields)
			}
			var sessionID string
			if err := json.Unmarshal(fields[test.sessionKey], &sessionID); err != nil {
				t.Fatalf("Unmarshal session ID: %v", err)
			}
			if sessionID != "setup-123" {
				t.Fatalf("session ID = %q, want setup-123", sessionID)
			}
			if test.eventName != "" {
				var eventName string
				if err := json.Unmarshal(fields["hook_event_name"], &eventName); err != nil {
					t.Fatalf("Unmarshal event name: %v", err)
				}
				if eventName != test.eventName {
					t.Fatalf("event name = %q, want %q", eventName, test.eventName)
				}
			}
			for _, forbidden := range []string{"command", "prompt", "tool_input", "tool_name", "user_content"} {
				if _, ok := fields[forbidden]; ok {
					t.Fatalf("payload contains %q: %s", forbidden, payload)
				}
			}
		})
	}
}

func TestVerifyInstalledHooksReportsMissingDurableIntake(t *testing.T) {
	homeDir := t.TempDir()
	binPath := "/usr/bin/true"
	options := installer.DefaultHooksOptions(binPath)
	options.HomeDir = homeDir
	options.Providers = []installer.Provider{installer.ProviderClaude}
	plan, err := installer.PrepareHookInstallation(options)
	if err != nil {
		t.Fatalf("PrepareHookInstallation: %v", err)
	}
	if err := installer.ApplyHookInstallation(plan); err != nil {
		t.Fatalf("ApplyHookInstallation: %v", err)
	}
	databasePath := filepath.Join(t.TempDir(), "audit.db")
	cfg := &config.Config{Audit: config.Audit{Outputs: config.AuditOutput{
		SQLite: config.AuditSQLiteOutput{Path: databasePath},
	}}}

	_, err = VerifyInstalledHooks(t.Context(), ProbeRequest{
		SetupID:   "missing-intake",
		Providers: []installer.Provider{installer.ProviderClaude},
		HomeDir:   homeDir,
		BinPath:   binPath,
		Config:    cfg,
		Timeout:   50 * time.Millisecond,
	})
	if err == nil || !strings.Contains(err.Error(), "claude: durable intake was not recorded before 50ms") {
		t.Fatalf("error = %v, want durable intake stage", err)
	}
}

func TestVerifyInstalledHooksRejectsUnexpectedInstalledExecutable(t *testing.T) {
	homeDir := t.TempDir()
	options := installer.DefaultHooksOptions("/usr/bin/true")
	options.HomeDir = homeDir
	options.Providers = []installer.Provider{installer.ProviderCursor}
	plan, err := installer.PrepareHookInstallation(options)
	if err != nil {
		t.Fatalf("PrepareHookInstallation: %v", err)
	}
	if err := installer.ApplyHookInstallation(plan); err != nil {
		t.Fatalf("ApplyHookInstallation: %v", err)
	}
	cfg := &config.Config{Audit: config.Audit{Outputs: config.AuditOutput{
		SQLite: config.AuditSQLiteOutput{Path: filepath.Join(t.TempDir(), "audit.db")},
	}}}

	_, err = VerifyInstalledHooks(t.Context(), ProbeRequest{
		SetupID: "unexpected-executable", Providers: []installer.Provider{installer.ProviderCursor},
		HomeDir: homeDir, BinPath: "/usr/bin/false", Config: cfg, Timeout: time.Second,
	})
	if err == nil || !strings.Contains(err.Error(), "conflicting lifecycle commands") {
		t.Fatalf("error = %v, want executable mismatch", err)
	}
}

func TestVerifyInstalledHooksRequiresExpectedExecutable(t *testing.T) {
	cfg := &config.Config{Audit: config.Audit{Outputs: config.AuditOutput{
		SQLite: config.AuditSQLiteOutput{Path: filepath.Join(t.TempDir(), "audit.db")},
	}}}
	_, err := VerifyInstalledHooks(t.Context(), ProbeRequest{
		SetupID: "missing-executable", Providers: []installer.Provider{installer.ProviderClaude},
		HomeDir: t.TempDir(), BinPath: "", Config: cfg, Timeout: time.Second,
	})
	if err == nil || !strings.Contains(err.Error(), "expected binary path is required") {
		t.Fatalf("error = %v, want expected binary requirement", err)
	}
}

func TestReadDurableEvaluationAcceptsCompletedDeferredAllow(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "audit.db")
	store, err := intake.OpenSQLite(t.Context(), databasePath, nil)
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	})
	receipt, err := store.Append(t.Context(), intake.Record{
		EventID: "setup-deferred-event", RecordedAt: time.Now(), System: "codex",
		SessionID: "setup-deferred", EventName: "SessionStart",
		RawPayload: []byte(`{}`), NormalizedJSON: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	now := time.Now()
	if err := store.Evaluations().RecordCompleted(t.Context(), evaluation.Record{
		Evaluation: evaluation.Evaluation{
			EvaluationID: "setup-deferred-evaluation", ReceiptID: receipt.ReceiptID,
			EventID: receipt.EventID, Attempt: 1, Mode: "deferred",
			StartedAt: now, CompletedAt: now, FinalVerdict: "allow",
		},
	}); err != nil {
		t.Fatalf("RecordCompleted: %v", err)
	}
	cfg := &config.Config{Audit: config.Audit{Outputs: config.AuditOutput{
		SQLite: config.AuditSQLiteOutput{Path: databasePath},
	}}}
	result := ProbeResult{IntakeEventID: receipt.EventID}
	complete, err := readDurableEvaluation(
		t.Context(),
		ProbeRequest{SetupID: "setup-deferred", Config: cfg},
		installer.ManagedHookCommand{Provider: installer.ProviderCodex, EventName: "SessionStart"},
		&result,
	)
	if err != nil {
		t.Fatalf("readDurableEvaluation: %v", err)
	}
	if complete {
		t.Fatal("evaluation completed the full durable probe before audit")
	}
	if result.EvaluationID != "setup-deferred-evaluation" || result.ReceiptID != receipt.ReceiptID {
		t.Fatalf("evaluation result = %#v", result)
	}
}
