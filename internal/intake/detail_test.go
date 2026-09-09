package intake_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"goodkind.io/agent-gate/internal/auditstorage"
	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/intake"
)

func TestCanonicalInputRetainedUntilEveryReceiptCompletes(t *testing.T) {
	for _, policy := range []config.AuditStoragePolicy{fullDetailPolicy(), minimalDetailPolicy()} {
		t.Run(string(policy.Profile), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "audit.db")
			store := openDetailStore(t, path, policy)
			input := populatedDetailRecord("repeated")
			first, err := store.Append(t.Context(), input)
			if err != nil {
				t.Fatal(err)
			}
			second, err := store.Append(t.Context(), input)
			if err != nil {
				t.Fatal(err)
			}
			if !first.Inserted || second.Inserted || first.ReceiptID == second.ReceiptID {
				t.Fatalf("receipts: %+v %+v", first, second)
			}
			if err := store.CommitHotEvaluation(t.Context(), first.EventID, first.ReceiptID, false, atomicEvaluationRecord(first, "hot-first", "hot", 1), nil); err != nil {
				t.Fatal(err)
			}
			if err := store.CommitHotEvaluation(t.Context(), second.EventID, second.ReceiptID, true, atomicEvaluationRecord(second, "hot-second", "hot", 1), nil); err != nil {
				t.Fatal(err)
			}
			if err := store.Handle().Close(); err != nil {
				t.Fatal(err)
			}
			store = openDetailStore(t, path, policy)
			replay, claim, err := store.ClaimDeferred(t.Context(), second.ReceiptID, "owner", time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			assertRecordDetailEqual(t, replay, input)
			if err := store.CommitDeferredEvaluation(t.Context(), claim, atomicEvaluationRecord(second, "deferred", "deferred", claim.Attempt), nil); err != nil {
				t.Fatal(err)
			}
			got, err := store.GetReceipt(t.Context(), second.ReceiptID)
			if err != nil {
				t.Fatal(err)
			}
			if policy.Detail.WireInput {
				assertRecordDetailEqual(t, got, input)
			} else if len(got.RawPayload) != 0 || got.NormalizedJSON != nil || got.ClassificationJSON != nil || len(got.EnvFingerprint) != 0 {
				t.Fatalf("terminal input retained: %+v", got)
			}
			third, err := store.Append(t.Context(), input)
			if err != nil {
				t.Fatal(err)
			}
			assertDetailRecord(t, store, third.ReceiptID, input)
		})
	}
}

func TestCanonicalEmptyWireInputSurvivesPendingRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.db")
	store := openDetailStore(t, path, minimalDetailPolicy())
	receipt, err := store.Append(t.Context(), intake.Record{EventID: "empty", RawPayload: []byte{}})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CommitHotEvaluation(t.Context(), receipt.EventID, receipt.ReceiptID, true, atomicEvaluationRecord(receipt, "empty-hot", "hot", 1), nil); err != nil {
		t.Fatal(err)
	}
	if err := store.Handle().Close(); err != nil {
		t.Fatal(err)
	}
	store = openDetailStore(t, path, minimalDetailPolicy())
	input, _, err := store.ClaimDeferred(t.Context(), receipt.ReceiptID, "owner", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if input.RawPayload == nil || !bytes.Equal(input.RawPayload, []byte{}) {
		t.Fatalf("empty wire = %#v", input.RawPayload)
	}
}

func openDetailStore(
	t *testing.T,
	path string,
	policy config.AuditStoragePolicy,
) *intake.Store {
	t.Helper()
	database, err := auditstorage.OpenWriter(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	store, err := intake.NewStore(t.Context(), database, policy, nil)
	if err != nil {
		t.Fatalf("OpenSQLiteWithOptions: %v", err)
	}
	t.Cleanup(func() { _ = store.Handle().Close() })
	return store
}

func populatedDetailRecord(eventID string) intake.Record {
	return intake.Record{
		EventID: eventID, System: "codex", SessionID: "session-detail",
		TurnID: "turn-detail", EventName: "PreToolUse", ToolName: "Shell",
		ToolUseID: "tool-detail", Operation: intake.Operation{
			CWD: "/repo", EffectiveCWD: "/repo", Command: "echo detail", FilePath: "",
		},
		RawPayload:         []byte(`{"wire":true}`),
		NormalizedJSON:     json.RawMessage(`{"normalized":true}`),
		ClassificationJSON: json.RawMessage(`{"provider":"codex"}`),
		EnvFingerprint:     map[string]string{"CODEX_THREAD_ID": "thread-detail"},
	}
}

func fullDetailPolicy() config.AuditStoragePolicy {
	return config.AuditStoragePolicy{
		Profile: config.AuditStorageProfileFull,
		Detail: config.AuditStorageDetailPolicy{
			WireInput: true, NormalizedInput: true, ProviderEvidence: true,
			EnvironmentEvidence: true, EvaluationContent: true,
		},
	}
}

func minimalDetailPolicy() config.AuditStoragePolicy {
	return config.AuditStoragePolicy{Profile: config.AuditStorageProfileMinimal}
}

func TestRecordedInputBitsAccumulateAcrossPolicySnapshots(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.db")
	input := populatedDetailRecord("policy-change")
	for index, policy := range []config.AuditStoragePolicy{minimalDetailPolicy(), fullDetailPolicy(), minimalDetailPolicy()} {
		store := openDetailStore(t, path, policy)
		receipt, err := store.Append(t.Context(), input)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.CommitHotEvaluation(t.Context(), receipt.EventID, receipt.ReceiptID, false, atomicEvaluationRecord(receipt, fmt.Sprintf("policy-%d", index), "hot", 1), nil); err != nil {
			t.Fatal(err)
		}
		if index > 0 {
			assertDetailRecord(t, store, receipt.ReceiptID, input)
		}
		if err := store.Handle().Close(); err != nil {
			t.Fatal(err)
		}
	}
	result, err := intake.Query(t.Context(), queryConfig(path), intake.QueryFilter{EventID: input.EventID, IncludeNormalized: true, IncludeEnv: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Records) != 1 || len(result.Records[0].Detail.RecordedClasses) != 4 {
		t.Fatalf("retained classes: %+v", result.Records)
	}
}

func TestReplayOnlyInputDoesNotAppearAsRecordedContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.db")
	store := openDetailStore(t, path, minimalDetailPolicy())
	receipt, err := store.Append(t.Context(), populatedDetailRecord("replay-only"))
	if err != nil {
		t.Fatal(err)
	}
	result, err := intake.Query(t.Context(), queryConfig(path), intake.QueryFilter{EventID: receipt.EventID, IncludeNormalized: true, IncludeEnv: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Records) != 1 {
		t.Fatalf("records: %+v", result.Records)
	}
	record := result.Records[0]
	if len(record.Detail.RecordedClasses) != 0 || len(record.Detail.AvailableClasses) != 0 || record.NormalizedJSON != nil || record.Classification != nil || record.EnvFingerprint != nil {
		t.Fatalf("replay-only input exposed: %+v", record)
	}
}

func assertDetailRecord(
	t *testing.T,
	store *intake.Store,
	receiptID int64,
	want intake.Record,
) {
	t.Helper()
	got, err := store.GetReceipt(t.Context(), receiptID)
	if err != nil {
		t.Fatalf("GetReceipt: %v", err)
	}
	assertRecordDetailEqual(t, got, want)
}

func assertRecordDetailEqual(t *testing.T, got intake.Record, want intake.Record) {
	t.Helper()
	if !reflect.DeepEqual(got.RawPayload, want.RawPayload) ||
		!reflect.DeepEqual(got.NormalizedJSON, want.NormalizedJSON) ||
		!reflect.DeepEqual(got.ClassificationJSON, want.ClassificationJSON) ||
		!reflect.DeepEqual(got.EnvFingerprint, want.EnvFingerprint) {
		t.Fatalf("record detail = %#v, want %#v", got, want)
	}
}
