// Package evaluation persists complete, ordered evaluation records for later
// query and training export.
package evaluation

import (
	"encoding/json"
	"time"
)

// Evaluation records one completed evaluation and its final enforcement result.
type Evaluation struct {
	EvaluationID      string
	ReceiptID         int64
	EventID           string
	Attempt           int
	Mode              string
	ConfigHash        string
	EngineVersion     string
	EngineCommit      string
	EngineBuildHash   string
	InputHash         string
	StartedAt         time.Time
	CompletedAt       time.Time
	FinalVerdict      string
	FinalSource       string
	EnforcementAction string
	Enforced          bool
	TotalLatencyUS    int64
	ErrorJSON         json.RawMessage
}

// Layer records one ordered evaluation step and its exact inputs and outputs.
type Layer struct {
	LayerIndex        int
	ParentLayerIndex  *int
	Kind              string
	Name              string
	Status            string
	Outcome           string
	Verdict           string
	InputReference    string
	InputJSON         json.RawMessage
	InputHash         string
	OutputHash        string
	OutputJSON        json.RawMessage
	MetadataJSON      json.RawMessage
	StartedAt         time.Time
	CompletedAt       time.Time
	LatencyUS         int64
	ServiceName       string
	ServiceVersion    string
	ModelName         string
	ModelVersion      string
	PromptHash        string
	SchemaHash        string
	CacheStatus       string
	CacheKeyHash      string
	CacheEntryVersion *int64
	CacheExpiresAt    *time.Time
	ErrorCode         string
	ErrorMessage      string
	RetryCount        int
}

// Label records one versioned adjudication attached to an evaluation.
type Label struct {
	Namespace    string
	LabelVersion int
	Verdict      string
	Source       string
	Confidence   *float64
	Rationale    string
	CreatedAt    time.Time
}

// Record contains one evaluation and all of its ordered layers and labels.
type Record struct {
	Evaluation Evaluation
	Layers     []Layer
	Labels     []Label
}
