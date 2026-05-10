package pipeline

import (
	"context"
	"time"
)

// pipelineNow is the package clock; tests can swap it to inject deterministic times.
var pipelineNow = time.Now

// Orchestrator runs a fixed slice of Concerns serially in declaration order.
type Orchestrator struct {
	Concerns  []Concern
	Scheduler Scheduler
	Memo      Cache
	Sentinel  Sentinel
}

// Result records a single Concern's execution outcome.
type Result struct {
	ConcernName string
	Outcome     Outcome
	Err         error
	Slot        int
	Elapsed     time.Duration
}

// Run executes each Concern in order, collecting one Result per Concern.
func (o *Orchestrator) Run(ctx context.Context, in Input) ([]Result, error) {
	results := make([]Result, len(o.Concerns))
	for slot, concern := range o.Concerns {
		profile := concern.Profile()
		start := pipelineNow()
		outcome, err := concern.Execute(ctx, in)
		results[slot] = Result{
			ConcernName: profile.Name,
			Outcome:     outcome,
			Err:         err,
			Slot:        slot,
			Elapsed:     pipelineNow().Sub(start),
		}
		if o.Scheduler != nil {
			o.Scheduler.Observe(profile.Name, results[slot].Elapsed, err)
		}
	}
	return results, nil
}
