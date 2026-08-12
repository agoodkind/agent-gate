// Package auditmaintenance previews and reports bounded audit retention work.
package auditmaintenance

import (
	"time"

	"goodkind.io/agent-gate/internal/config"
)

// SizeState reports how the configured size target relates to current storage.
type SizeState string

const (
	// SizeStateDisabled means no size target is configured.
	SizeStateDisabled SizeState = "disabled"
	// SizeStateWithinTarget means current storage fits the target.
	SizeStateWithinTarget SizeState = "within_target"
	// SizeStateOverTarget means eligible data can be pruned to approach the target.
	SizeStateOverTarget SizeState = "over_target"
	// SizeStateConstrained means protected data prevents meeting the target.
	SizeStateConstrained SizeState = "constrained"
	// SizeStateReclaimPending means free pages still need reclamation.
	SizeStateReclaimPending SizeState = "reclaim_pending"
)

// Plan describes one immutable maintenance preview.
type Plan struct {
	PlannedAt              time.Time  `json:"planned_at"`
	PolicyHash             string     `json:"policy_hash"`
	DetailCutoff           *time.Time `json:"detail_cutoff,omitempty"`
	SummaryCutoff          time.Time  `json:"summary_cutoff"`
	DetailCandidateGraphs  int64      `json:"detail_candidate_graphs"`
	SummaryCandidateGraphs int64      `json:"summary_candidate_graphs"`
	ProtectedGraphs        int64      `json:"protected_graphs"`
	ProtectedBytes         int64      `json:"protected_bytes"`
	EstimatedDeleteBytes   int64      `json:"estimated_delete_bytes"`
}

// RunSummary describes the newest recorded maintenance run when one exists.
type RunSummary struct {
	RunID          string     `json:"run_id"`
	PlannedAt      time.Time  `json:"planned_at"`
	StartedAt      time.Time  `json:"started_at"`
	CompletedAt    *time.Time `json:"completed_at,omitempty"`
	PolicyHash     string     `json:"policy_hash"`
	DetailGraphs   int64      `json:"detail_graphs"`
	SummaryGraphs  int64      `json:"summary_graphs"`
	ReclaimedBytes int64      `json:"reclaimed_bytes"`
	Result         string     `json:"result"`
	ErrorClass     string     `json:"error_class,omitempty"`
	NextDueAt      *time.Time `json:"next_due_at,omitempty"`
}

// Status describes audit storage without changing it.
type Status struct {
	Policy            config.AuditStoragePolicy `json:"policy"`
	DatabaseBytes     int64                     `json:"database_bytes"`
	WALBytes          int64                     `json:"wal_bytes"`
	OldestDetailAt    *time.Time                `json:"oldest_detail_at,omitempty"`
	OldestSummaryAt   *time.Time                `json:"oldest_summary_at,omitempty"`
	ProtectedGraphs   int64                     `json:"protected_graphs"`
	ReclaimablePages  int64                     `json:"reclaimable_pages"`
	FullCompactNeeded bool                      `json:"full_compact_needed"`
	IntegrityOK       bool                      `json:"integrity_ok"`
	IntegrityError    string                    `json:"integrity_error,omitempty"`
	LastRun           *RunSummary               `json:"last_run,omitempty"`
	MaintenanceDueAt  *time.Time                `json:"maintenance_due_at,omitempty"`
	NextAttemptAt     *time.Time                `json:"next_attempt_at,omitempty"`
	Overdue           bool                      `json:"overdue"`
	SizeState         SizeState                 `json:"size_state"`
}
