package rules

import (
	"testing"

	"goodkind.io/agent-gate/internal/config"
)

func pointForTest() config.InferencePoint {
	return config.InferencePoint{Endpoint: "endpoint", Model: "model"}
}

// TestParseBatchDecisions confirms the array reply parses into a rule_id map, that
// an entry with an unknown decision is dropped, and that a non-JSON reply reports
// failure so the caller errors every rule.
func TestParseBatchDecisions(t *testing.T) {
	parsed, ok := parseBatchDecisions(`{"decisions":[{"rule_id":"a","decision":"block"},{"rule_id":"b","decision":"allow"},{"rule_id":"c","decision":"maybe"}]}`)
	if !ok {
		t.Fatal("expected ok for well-formed array")
	}
	if parsed["a"] != "block" || parsed["b"] != "allow" {
		t.Fatalf("parsed = %+v, want a=block b=allow", parsed)
	}
	if _, present := parsed["c"]; present {
		t.Fatalf("invalid decision should be dropped, got %+v", parsed)
	}
	if _, ok := parseBatchDecisions("not json"); ok {
		t.Fatal("expected failure for non-JSON reply")
	}
}

// TestBatchDecisionsForParticipants confirms a participant the model omitted is
// marked errored with missing_decision, so the read site applies its on_error
// rather than silently allowing.
func TestBatchDecisionsForParticipants(t *testing.T) {
	participants := []batchParticipant{{ruleName: "a"}, {ruleName: "b"}}
	parsed := map[string]string{"a": "block"}
	decisions := batchDecisionsForParticipants(participants, parsed)
	if decisions["a"].errored || !decisions["a"].block {
		t.Fatalf("rule a = %+v, want block, not errored", decisions["a"])
	}
	if !decisions["b"].errored || decisions["b"].errorCode != "missing_decision" {
		t.Fatalf("rule b = %+v, want errored missing_decision", decisions["b"])
	}
}

// TestBatchVerdictForFallsBackWhenAbsent confirms verdictFor reports no verdict
// when the memo is nil or the point or rule is missing, so the read site falls back
// to an individual call.
func TestBatchVerdictForFallsBackWhenAbsent(t *testing.T) {
	var nilMemo *batchInferenceMemo
	if _, found := nilMemo.verdictFor(pointForTest(), "a"); found {
		t.Fatal("nil memo should report no verdict")
	}
	empty := &batchInferenceMemo{groups: map[string]*batchGroupResult{}}
	if _, found := empty.verdictFor(pointForTest(), "a"); found {
		t.Fatal("empty memo should report no verdict")
	}
}
