// Package pipeline is the generic Concern-Orchestrator framework that internal
// rule evaluation will adopt in later landings. This landing supplies only the
// types, a fixed scheduler, an event-scoped memo, and a no-op sentinel.
package pipeline

import (
	"context"
	"time"
)

// CostClass categorizes a Concern by execution cost so the scheduler can tier work.
type CostClass int

// CostClass values run from cheapest to most expensive in declaration order.
const (
	// CostCheap is pure CPU work that completes in well under a millisecond.
	CostCheap CostClass = iota
	// CostMedium is typed regex over large fields and similar work.
	CostMedium
	// CostExpensive covers entropy, fingerprint, and other in-process heavy paths.
	CostExpensive
	// CostExternal covers shellouts and network calls.
	CostExternal
)

// MemoLifetime selects the cache scope for memoized Outcomes.
type MemoLifetime string

// MemoLifetime constants name the supported cache scopes.
const (
	// MemoEvent caches results only for the duration of a single Run.
	MemoEvent MemoLifetime = "event"
	// MemoSession caches results across Runs that share a session token.
	MemoSession MemoLifetime = "session"
)

// Profile describes a Concern's execution and caching behavior.
type Profile struct {
	Name         string
	Cost         CostClass
	Idempotent   bool
	DependsOn    []string
	Timeout      time.Duration
	MemoLifetime MemoLifetime
	MemoTTL      time.Duration
}

// Input is the per-Run payload supplied to every Concern. The framework stays
// neutral about caller payload shape; later landings narrow this at the call
// sites that build Orchestrators.
type Input = any

// Outcome is whatever a Concern produces. Callers narrow with a type assertion
// right after Execute.
type Outcome = any

// Concern is the unit of work scheduled by the Orchestrator.
type Concern interface {
	Profile() Profile
	Execute(ctx context.Context, in Input) (Outcome, error)
}
