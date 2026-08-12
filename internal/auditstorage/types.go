// Package auditstorage owns the ordered SQLite schema shared by audit writers.
package auditstorage

import (
	"context"
	"database/sql"
	"slices"
)

// Migration applies one ordered schema version inside its caller's transaction.
type Migration struct {
	Version             int
	ForeignKeysDisabled bool
	Apply               func(context.Context, *sql.Tx) error
	AfterCommit         func(context.Context, *sql.Conn)
}

// DetailState describes whether stored detail can be returned safely.
type DetailState string

// DetailClass identifies one independently retained audit content class.
type DetailClass string

// DetailProjection describes the stored detail available to a query.
type DetailProjection struct {
	State            DetailState   `json:"state"`
	RecordedClasses  []DetailClass `json:"recorded_classes"`
	AvailableClasses []DetailClass `json:"available_classes"`
	ExpiredAt        string        `json:"expired_at,omitempty"`
}

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
	// DetailClassDeferredAuditPayload identifies deferred audit delivery content.
	DetailClassDeferredAuditPayload DetailClass = "deferred_audit_payload"
)

// ProjectDetail resolves stored class metadata for the classes requested by a query.
func ProjectDetail(
	recordedClasses []DetailClass,
	availableClasses []DetailClass,
	requestedClasses []DetailClass,
	storedState DetailState,
	stateChangedAt string,
	protectedClasses []DetailClass,
) DetailProjection {
	if recordedClasses == nil {
		recordedClasses = make([]DetailClass, 0)
	}
	if availableClasses == nil {
		availableClasses = make([]DetailClass, 0)
	}
	projection := DetailProjection{
		State:            DetailStateAvailable,
		RecordedClasses:  recordedClasses,
		AvailableClasses: availableClasses,
		ExpiredAt:        "",
	}
	for _, requestedClass := range requestedClasses {
		if containsDetailClass(protectedClasses, requestedClass) {
			projection.State = DetailStateProtected
			return projection
		}
	}
	if storedState == DetailStateExpired {
		projection.State = DetailStateExpired
		projection.ExpiredAt = stateChangedAt
		return projection
	}
	if storedState == DetailStateNotRecorded {
		projection.State = DetailStateNotRecorded
		return projection
	}
	hasExpired := false
	hasNotRecorded := false
	for _, requestedClass := range requestedClasses {
		recorded := containsDetailClass(recordedClasses, requestedClass)
		available := containsDetailClass(availableClasses, requestedClass)
		if recorded && !available {
			hasExpired = true
		}
		if !recorded {
			hasNotRecorded = true
		}
	}
	if hasExpired {
		projection.State = DetailStateExpired
		projection.ExpiredAt = stateChangedAt
		return projection
	}
	if hasNotRecorded {
		projection.State = DetailStateNotRecorded
	}
	return projection
}

func containsDetailClass(classes []DetailClass, target DetailClass) bool {
	return slices.Contains(classes, target)
}
