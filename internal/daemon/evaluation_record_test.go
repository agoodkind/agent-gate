package daemon

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"goodkind.io/agent-gate/internal/hook"
	"goodkind.io/agent-gate/internal/intake"
	"goodkind.io/agent-gate/internal/rules"
)

func TestBuildHotEvaluationRecordPersistsOrderedExactLayers(t *testing.T) {
	startedAt := time.Date(2026, 7, 11, 1, 2, 3, 0, time.UTC)
	completedAt := startedAt.Add(25 * time.Millisecond)
	parent := 0
	cacheVersion := int64(9)
	cacheExpiry := completedAt.Add(time.Minute)
	trace := rules.DecisionTrace{
		Deterministic: rules.DeterministicTrace{
			StartedAt: startedAt, CompletedAt: startedAt.Add(5 * time.Millisecond),
			InputJSON:  json.RawMessage(`{"tool_input":{"command":"blocked"}}`),
			OutputJSON: json.RawMessage(`{"result":"allow","rules":[]}`),
			InputHash:  "sha256:deterministic-input", OutputHash: "sha256:deterministic-output",
			ServiceName: "agent-gate", ServiceVersion: "trace-version",
			CheckedRules: []rules.RuleDecision{},
		},
		Layers: []rules.LayerTrace{
			{
				RuleName: "infer-rule", ConditionIndex: 0, LayerName: "v4", Kind: "inference",
				Status: "complete", SkipReason: "", ParentTraceIndex: &parent,
				StartedAt: startedAt.Add(5 * time.Millisecond), CompletedAt: startedAt.Add(20 * time.Millisecond),
				InputReference: "intake.normalized_json",
				InputJSON:      json.RawMessage(`{"prompt":"classify","input":"blocked"}`),
				OutputJSON:     json.RawMessage(`{"decision":"block"}`),
				InputHash:      "sha256:infer-input", OutputHash: "sha256:infer-output",
				ServiceName: "inference", ServiceVersion: "",
				VerifiedProvenance: rules.VerifiedProvenance{
					RequestedModel: "v4", PromptSHA256: "sha256:prompt",
					SchemaSHA256: "sha256:schema", ReportedPromptHashStatus: "mismatch",
					ReportedSchemaHashStatus: "mismatch",
				},
				CacheStatus: "hit", CacheKeyHash: "sha256:cache",
				CacheEntryVersion: &cacheVersion, CacheExpiresAt: &cacheExpiry,
				UpstreamMetadata: rules.UpstreamMetadata{
					Source: "inference_reply", Trust: "untrusted", Status: "present",
					Raw: json.RawMessage(`{"request_id":"request-1","service_version":"service-v1","actual_model":"v4-actual","backend_version":"backend-v2","prompt_tokens":"0"}`),
				},
				ErrorCode: "", ErrorMessage: "", RetryCount: 0,
			},
		},
	}
	result := hook.HotEvaluation{
		Stdout: []byte(`{"decision":"block"}`), Stderr: []byte("blocked\n"), ExitCode: 2,
		Deferred: hook.DeferredAuditEvent{
			Valid: true, RawBytes: nil, System: hook.SystemCodex, SystemString: "codex",
			EventName: "PreToolUse", SessionID: "session", EventID: "event-1", CWD: "/repo",
			Fields: rules.FieldSet{}, Rules: nil,
			BlockingViolations:  []rules.Violation{{RuleName: "infer-rule"}},
			AuditOnlyViolations: nil, InferenceTraces: nil,
			Decision: hook.ResponseDecisionBlock, DiagnosticText: "blocked",
		},
		Trace: trace,
	}

	record := buildHotEvaluationRecord(hotEvaluationRecordInput{
		ReceiptID: 42, EventID: "event-1",
		Intake:     intake.Record{RawPayload: []byte(`{"raw":true}`), NormalizedJSON: trace.Deterministic.InputJSON},
		ConfigHash: "sha256:config", EngineVersion: "engine-v1",
		EngineCommit: "commit-1", EngineBuildHash: "build-1",
		StartedAt: startedAt, CompletedAt: completedAt,
		Result: result, SystemError: "",
	})

	if record.Evaluation.EvaluationID != hotEvaluationID(42) || record.Evaluation.ReceiptID != 42 ||
		record.Evaluation.FinalVerdict != "block" || record.Evaluation.FinalSource != "inference" ||
		record.Evaluation.EnforcementAction != "deny" || !record.Evaluation.Enforced {
		t.Fatalf("evaluation = %+v", record.Evaluation)
	}
	if record.Evaluation.EngineVersion != "engine-v1" || record.Evaluation.EngineCommit != "commit-1" ||
		record.Evaluation.EngineBuildHash != "build-1" || record.Evaluation.ConfigHash != "sha256:config" {
		t.Fatalf("identity = %+v", record.Evaluation)
	}
	if len(record.Layers) != 3 || record.Layers[0].Kind != "deterministic" ||
		record.Layers[1].Kind != "inference" || record.Layers[2].Kind != "final" {
		t.Fatalf("layers = %+v", record.Layers)
	}
	if string(record.Layers[1].InputJSON) != string(trace.Layers[0].InputJSON) ||
		string(record.Layers[1].OutputJSON) != string(trace.Layers[0].OutputJSON) ||
		record.Layers[1].ModelName != "v4" || record.Layers[1].ServiceVersion != "" ||
		record.Layers[1].ModelVersion != "" || record.Layers[1].CacheEntryVersion == nil ||
		*record.Layers[1].CacheEntryVersion != 9 {
		t.Fatalf("inference layer = %+v", record.Layers[1])
	}
	var metadata layerMetadataV2
	if err := json.Unmarshal(record.Layers[1].MetadataJSON, &metadata); err != nil {
		t.Fatalf("metadata JSON: %v", err)
	}
	if metadata.SchemaVersion != 2 || metadata.RuleName != "infer-rule" ||
		metadata.ConditionIndex != 0 || metadata.UpstreamMetadata.Source != "inference_reply" ||
		metadata.UpstreamMetadata.Trust != "untrusted" ||
		!strings.Contains(string(metadata.UpstreamMetadata.Raw), `"prompt_tokens":"0"`) {
		t.Fatalf("metadata = %+v", metadata)
	}
	if metadata.VerifiedProvenance.RequestedModel != "v4" ||
		metadata.VerifiedProvenance.PromptSHA256 != "sha256:prompt" {
		t.Fatalf("verified metadata = %+v", metadata.VerifiedProvenance)
	}
	if record.Layers[2].ParentLayerIndex == nil || *record.Layers[2].ParentLayerIndex != 1 {
		t.Fatalf("final parent = %+v", record.Layers[2].ParentLayerIndex)
	}
}

func TestBuildHotEvaluationRecordUsesActualFailOpenResult(t *testing.T) {
	startedAt := time.Date(2026, 7, 11, 2, 0, 0, 0, time.UTC)
	record := buildHotEvaluationRecord(hotEvaluationRecordInput{
		ReceiptID: 7, EventID: "event-fail-open",
		Intake:     intake.Record{RawPayload: []byte(`{}`), NormalizedJSON: json.RawMessage(`{}`)},
		ConfigHash: "sha256:config", EngineVersion: "version", EngineCommit: "commit",
		EngineBuildHash: "build", StartedAt: startedAt, CompletedAt: startedAt.Add(time.Millisecond),
		Result: hook.HotEvaluation{
			Stdout: nil, Stderr: nil, ExitCode: 0,
			Deferred: hook.DeferredAuditEvent{
				Valid: true, RawBytes: nil, System: hook.SystemCodex, SystemString: "codex",
				EventName: "PreToolUse", SessionID: "session", EventID: "event-fail-open", CWD: "",
				Fields: rules.FieldSet{}, Rules: nil,
				BlockingViolations:  []rules.Violation{{RuleName: "would-block"}},
				AuditOnlyViolations: nil, InferenceTraces: nil,
				Decision: hook.ResponseDecisionBlock, DiagnosticText: "blocked",
			},
			Trace: rules.DecisionTrace{
				Deterministic: rules.DeterministicTrace{
					StartedAt: startedAt, CompletedAt: startedAt,
					InputJSON: json.RawMessage(`{}`), OutputJSON: json.RawMessage(`{}`),
					InputHash: "sha256:input", OutputHash: "sha256:output",
					ServiceName: "agent-gate", ServiceVersion: "version", CheckedRules: nil,
				},
				Layers: nil,
			},
		},
		SystemError: "deferred_pending_failed",
	})

	if record.Evaluation.FinalVerdict != "error" || record.Evaluation.FinalSource != "system_error" ||
		record.Evaluation.EnforcementAction != "fail_open" || record.Evaluation.Enforced {
		t.Fatalf("fail-open evaluation = %+v", record.Evaluation)
	}
	final := record.Layers[len(record.Layers)-1]
	if final.Status != "error" || final.Name != "hook-response" {
		t.Fatalf("final layer = %+v", final)
	}
}

func TestBuildHotEvaluationRecordAttributesInferenceAllowAndError(t *testing.T) {
	for _, status := range []string{"complete", "error"} {
		t.Run(status, func(t *testing.T) {
			startedAt := time.Date(2026, 7, 11, 2, 30, 0, 0, time.UTC)
			parent := 0
			result := hook.HotEvaluation{
				Deferred: hook.DeferredAuditEvent{
					Valid: true, System: hook.SystemCodex, SystemString: "codex",
					EventName: "PreToolUse", SessionID: "session", EventID: "event-inference-allow",
					Decision: hook.ResponseDecisionAllow,
				},
				Trace: rules.DecisionTrace{
					Deterministic: rules.DeterministicTrace{
						StartedAt: startedAt, CompletedAt: startedAt,
						InputJSON: json.RawMessage(`{}`), OutputJSON: json.RawMessage(`{}`),
						InputHash: "sha256:input", OutputHash: "sha256:output",
						ServiceName: "agent-gate", ServiceVersion: "version",
					},
					Layers: []rules.LayerTrace{{
						RuleName: "infer-rule", LayerName: "v4", Kind: "inference", Status: status,
						ParentTraceIndex: &parent, StartedAt: startedAt, CompletedAt: startedAt,
						InputJSON: json.RawMessage(`{}`), OutputJSON: json.RawMessage(`{}`),
					}},
				},
			}
			record := buildHotEvaluationRecord(hotEvaluationRecordInput{
				ReceiptID: 8, EventID: "event-inference-allow",
				Intake:     intake.Record{RawPayload: []byte(`{}`), NormalizedJSON: json.RawMessage(`{}`)},
				ConfigHash: "sha256:config", EngineVersion: "version", EngineCommit: "commit",
				EngineBuildHash: "build", StartedAt: startedAt, CompletedAt: startedAt,
				Result: result,
			})
			if record.Evaluation.FinalSource != "inference" {
				t.Fatalf("final source = %q, want inference", record.Evaluation.FinalSource)
			}
			var output finalLayerOutput
			if err := json.Unmarshal(record.Layers[len(record.Layers)-1].OutputJSON, &output); err != nil {
				t.Fatalf("decode final layer: %v", err)
			}
			if output.Source != "inference" {
				t.Fatalf("final layer source = %q, want inference", output.Source)
			}
		})
	}
}

func TestHotEvaluationIDSeparatesDuplicateReceipts(t *testing.T) {
	if hotEvaluationID(100) == hotEvaluationID(101) {
		t.Fatal("distinct receipts produced the same hot evaluation id")
	}
}

func TestHotFinalDispositionRecordsProviderSubstitution(t *testing.T) {
	disposition := hotFinalDisposition(hook.HotEvaluation{
		Deferred: hook.DeferredAuditEvent{
			Valid: true, RawBytes: nil, System: hook.SystemCodex, SystemString: "codex",
			EventName: "PostToolUse", SessionID: "session", EventID: "event", CWD: "",
			Fields: rules.FieldSet{}, Rules: nil,
			BlockingViolations:  []rules.Violation{{RuleName: "post-rule"}},
			AuditOnlyViolations: nil, InferenceTraces: nil,
			Decision: hook.ResponseDecisionBlock, DiagnosticText: "substitute result",
		},
	}, "")

	if disposition.verdict != "block" || disposition.enforcementAction != "substitute" ||
		!disposition.enforced {
		t.Fatalf("substitution disposition = %+v", disposition)
	}
}
