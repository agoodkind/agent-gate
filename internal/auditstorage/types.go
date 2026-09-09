// Package auditstorage owns the SQLite audit schema.
package auditstorage

// DetailState describes whether stored detail can be returned safely.
type DetailState string

// DetailClass identifies one independently retained audit content class.
type DetailClass string

// DetailProjection describes the stored detail available to a query.
type DetailProjection struct {
	State            DetailState   `json:"state"`
	RecordedClasses  []DetailClass `json:"recorded_classes"`
	AvailableClasses []DetailClass `json:"available_classes"`
}

const (
	// DetailStateAvailable means every requested class is present.
	DetailStateAvailable DetailState = "available"
	// DetailStateNotRecorded means policy omitted detail after work became terminal.
	DetailStateNotRecorded DetailState = "not_recorded"

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

// DetailMask records the independently retained content classes.
type DetailMask uint32

const (
	// DetailWireInput retains exact hook input bytes.
	DetailWireInput DetailMask = 1
	// DetailNormalizedInput retains normalized hook input.
	DetailNormalizedInput DetailMask = 2
	// DetailProviderEvidence retains provider classification evidence.
	DetailProviderEvidence DetailMask = 4
	// DetailEnvironmentEvidence retains captured environment evidence.
	DetailEnvironmentEvidence DetailMask = 8
	// DetailEvaluationContent retains evaluation input and output content.
	DetailEvaluationContent DetailMask = 16
)

// Classes returns the retained classes in stable bit order.
func (mask DetailMask) Classes() []DetailClass {
	classes := make([]DetailClass, 0, 5)
	for _, entry := range []struct {
		bit   DetailMask
		class DetailClass
	}{
		{DetailWireInput, DetailClassWireInput},
		{DetailNormalizedInput, DetailClassNormalizedInput},
		{DetailProviderEvidence, DetailClassProviderEvidence},
		{DetailEnvironmentEvidence, DetailClassEnvironmentEvidence},
		{DetailEvaluationContent, DetailClassEvaluationContent},
	} {
		if mask&entry.bit != 0 {
			classes = append(classes, entry.class)
		}
	}
	return classes
}

// ProjectDetail exposes only retained content that remains present.
func ProjectDetail(recorded, available, requested DetailMask) DetailProjection {
	available &= recorded
	state := DetailStateAvailable
	if available&requested != requested {
		state = DetailStateNotRecorded
	}
	return DetailProjection{State: state, RecordedClasses: recorded.Classes(), AvailableClasses: available.Classes()}
}
