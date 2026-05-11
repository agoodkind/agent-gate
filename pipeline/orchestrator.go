package pipeline

import (
	"context"
	"time"
)

// pipelineNow is the package clock; tests can swap it to inject deterministic times.
var pipelineNow = time.Now

// Orchestrator runs a fixed slice of Conditions serially in declaration order.
type Orchestrator struct {
	Conditions []Condition
	Scheduler  Scheduler
	Memo       Cache
	Sentinel   Sentinel
}

// Result records a single Condition's execution outcome.
type Result struct {
	ConditionName string
	Outcome       Outcome
	Err           error
	Slot          int
	Elapsed       time.Duration
}

// Run executes each Condition in order, collecting one Result per Condition.
func (o *Orchestrator) Run(ctx context.Context, in Input) ([]Result, error) {
	results := make([]Result, len(o.Conditions))
	for slot, condition := range o.Conditions {
		profile := condition.Profile()
		start := pipelineNow()
		outcome, err := condition.Execute(ctx, in)
		results[slot] = Result{
			ConditionName: profile.Name,
			Outcome:       outcome,
			Err:           err,
			Slot:          slot,
			Elapsed:       pipelineNow().Sub(start),
		}
		if o.Scheduler != nil {
			o.Scheduler.Observe(profile.Name, results[slot].Elapsed, err)
		}
	}
	return results, nil
}
