package hook_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/hook"
)

type credentialCopilotReadInput struct {
	FilePath string `json:"filePath"`
}

type credentialCopilotReadPayload struct {
	Session string                     `json:"sessionId"`
	Tool    string                     `json:"toolName"`
	CWD     string                     `json:"cwd"`
	Input   credentialCopilotReadInput `json:"toolInput"`
}

func TestCredentialReadRuleDeniesEachProvider(t *testing.T) {
	cfg, err := config.LoadExisting(filepath.Join("..", "..", "examples", "rules", "no-credential-file-reads.toml"))
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	providers := []struct {
		system hook.System
		event  string
	}{
		{hook.SystemClaude, "PreToolUse"},
		{hook.SystemCodex, "PreToolUse"},
		{hook.SystemCursor, "preToolUse"},
		{hook.SystemGemini, "BeforeTool"},
		{hook.SystemCopilot, "PreToolUse"},
		{hook.SystemCopilot, "preToolUse"},
	}
	for _, provider := range providers {
		t.Run(provider.system.String()+"/"+provider.event, func(t *testing.T) {
			raw, err := json.Marshal(credentialReadPayload{
				Event: provider.event, Session: "credential-provider", Tool: "Read", CWD: root,
				Input: credentialReadInput{FilePath: filepath.Join(root, ".env")},
			})
			if err != nil {
				t.Fatal(err)
			}
			normalized := raw
			if provider.system == hook.SystemCopilot {
				raw, err = json.Marshal(credentialCopilotReadPayload{
					Session: "credential-provider", Tool: "Read", CWD: root,
					Input: credentialCopilotReadInput{FilePath: filepath.Join(root, ".env")},
				})
				if err != nil {
					t.Fatal(err)
				}
				normalized, err = hook.NormalizeCopilotPayload(raw, provider.event)
				if err != nil {
					t.Fatal(err)
				}
			}
			classification := hook.Classify(normalized, provider.system, nil, nil)
			result := hook.EvaluateClassifiedHotWithEventID(context.Background(), hook.EvaluationInput{
				WireBytes: raw, NormalizedJSON: normalized, Classification: classification,
			}, cfg, os.Getenv, "credential-provider-event")
			if result.Deferred.Decision != hook.ResponseDecisionBlock {
				t.Fatalf("provider decision = %s, want block", result.Deferred.Decision)
			}
			if !strings.Contains(string(result.Stdout)+string(result.Stderr), "Credential file reads") {
				t.Fatal("the provider response omitted the denial reason")
			}
		})
	}
}
