package hook

import (
	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/rules"
)

// HotEvaluation contains a rendered hook response and its audit evidence.
type HotEvaluation struct {
	Stdout                  []byte
	Stderr                  []byte
	ExitCode                int
	Deferred                DeferredAuditEvent
	Trace                   rules.DecisionTrace
	TemporalResponseOutputs []TemporalResponseOutput
}

// EvaluationInput separates immutable hook bytes from derived evaluation data.
type EvaluationInput struct {
	WireBytes      []byte
	NormalizedJSON []byte
	Classification Classification
}

// TemporalResponseOutput is an in-memory model-facing value eligible for
// process-local prior-response tracking after durable evaluation persistence.
type TemporalResponseOutput struct {
	Action string
	Target string
	Output string
}

// DeferredAuditEvent contains the audit evidence from one evaluated phase.
type DeferredAuditEvent struct {
	Valid               bool
	RawBytes            []byte
	System              System
	SystemString        string
	EventName           string
	SessionID           string
	EventID             string
	CWD                 string
	Fields              rules.FieldSet
	Rules               []config.Rule
	BlockingViolations  []rules.Violation
	AuditOnlyViolations []rules.Violation
	ResponseEffects     []ResponseEffectRecord
	Trace               rules.DecisionTrace
	Decision            ResponseDecision
	DiagnosticText      string
}

// ResponseEffectRecord is the non-sensitive audit view of a matched inject or
// mutate rule. It intentionally contains no configured, file, or command
// output content.
type ResponseEffectRecord struct {
	RuleName    string
	EffectType  string
	Target      string
	ByteCount   int
	Disposition string
}
