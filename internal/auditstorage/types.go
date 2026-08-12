// Package auditstorage owns the ordered SQLite schema shared by audit writers.
package auditstorage

import (
	"context"
	"database/sql"
)

// Migration applies one ordered schema version inside its caller's transaction.
type Migration struct {
	Version             int
	ForeignKeysDisabled bool
	Apply               func(context.Context, *sql.Tx) error
}

// DetailState describes whether stored detail can be returned safely.
type DetailState string

// DetailClass identifies one independently retained audit content class.
type DetailClass string

const (
	// DetailStateAvailable means every requested class is present.
	DetailStateAvailable DetailState = "available"
	// DetailStateExpired means retention removed previously recorded detail.
	DetailStateExpired DetailState = "expired"
	// DetailStateNotRecorded means policy omitted detail after work became terminal.
	DetailStateNotRecorded DetailState = "not_recorded"
	// DetailStateProtected means live work retains detail beyond terminal policy.
	DetailStateProtected DetailState = "protected"

	// DetailClassWireInput identifies exact hook input bytes.
	DetailClassWireInput DetailClass = "wire_input"
	// DetailClassNormalizedInput identifies normalized hook input.
	DetailClassNormalizedInput DetailClass = "normalized_input"
	// DetailClassProviderEvidence identifies complete provider classification evidence.
	DetailClassProviderEvidence DetailClass = "provider_evidence"
	// DetailClassEnvironmentEvidence identifies captured environment evidence.
	DetailClassEnvironmentEvidence DetailClass = "environment_evidence"
	// DetailClassEvaluationContent identifies evaluation input and output content.
	DetailClassEvaluationContent DetailClass = "evaluation_content"
)
