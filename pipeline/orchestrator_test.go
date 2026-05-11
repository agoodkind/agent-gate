package pipeline

import (
	"context"
	"errors"
	"testing"
)

type countingCondition struct {
	name    string
	calls   *int
	order   *[]string
	outcome Outcome
	err     error
}

func (c *countingCondition) Profile() Profile {
	return Profile{Name: c.name}
}

func (c *countingCondition) Execute(_ context.Context, _ Input) (Outcome, error) {
	*c.calls++
	*c.order = append(*c.order, c.name)
	return c.outcome, c.err
}

func TestOrchestratorRunSingleCondition(t *testing.T) {
	t.Parallel()
	calls := 0
	order := []string{}
	condition := &countingCondition{
		name:    "alpha",
		calls:   &calls,
		order:   &order,
		outcome: "ok",
		err:     nil,
	}
	orch := &Orchestrator{Conditions: []Condition{condition}}
	results, err := orch.Run(context.Background(), nil)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected 1 call, got %d", calls)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	got := results[0]
	if got.ConditionName != "alpha" {
		t.Fatalf("ConditionName mismatch: %q", got.ConditionName)
	}
	if got.Outcome != "ok" {
		t.Fatalf("Outcome mismatch: %v", got.Outcome)
	}
	if got.Err != nil {
		t.Fatalf("Err should be nil, got %v", got.Err)
	}
	if got.Slot != 0 {
		t.Fatalf("Slot mismatch: %d", got.Slot)
	}
	if got.Elapsed < 0 {
		t.Fatalf("Elapsed should be non-negative, got %s", got.Elapsed)
	}
}

func TestOrchestratorRunPreservesOrder(t *testing.T) {
	t.Parallel()
	calls := 0
	order := []string{}
	names := []string{"first", "second", "third"}
	conditions := make([]Condition, 0, len(names))
	for _, name := range names {
		conditions = append(conditions, &countingCondition{
			name:  name,
			calls: &calls,
			order: &order,
		})
	}
	orch := &Orchestrator{Conditions: conditions}
	results, err := orch.Run(context.Background(), nil)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if calls != len(names) {
		t.Fatalf("expected %d calls, got %d", len(names), calls)
	}
	if len(results) != len(names) {
		t.Fatalf("expected %d results, got %d", len(names), len(results))
	}
	for index, name := range names {
		if order[index] != name {
			t.Fatalf("execution order at %d: want %q, got %q", index, name, order[index])
		}
		if results[index].ConditionName != name {
			t.Fatalf("Result[%d].ConditionName: want %q, got %q", index, name, results[index].ConditionName)
		}
		if results[index].Slot != index {
			t.Fatalf("Result[%d].Slot: want %d, got %d", index, index, results[index].Slot)
		}
	}
}

func TestOrchestratorRunCapturesError(t *testing.T) {
	t.Parallel()
	calls := 0
	order := []string{}
	boom := errors.New("boom")
	condition := &countingCondition{
		name:  "explodes",
		calls: &calls,
		order: &order,
		err:   boom,
	}
	orch := &Orchestrator{Conditions: []Condition{condition}}
	results, err := orch.Run(context.Background(), nil)
	if err != nil {
		t.Fatalf("Run itself should not return aggregated error in Landing 1, got %v", err)
	}
	if !errors.Is(results[0].Err, boom) {
		t.Fatalf("expected boom in Result.Err, got %v", results[0].Err)
	}
}
