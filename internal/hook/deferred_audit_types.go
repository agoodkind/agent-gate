package hook

import (
	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/rules"
)

// HotEvaluation is the synchronous hook decision plus deferred audit payload.
type HotEvaluation struct {
	Stdout   []byte
	Stderr   []byte
	ExitCode int
	Deferred DeferredAuditEvent
	Trace    rules.DecisionTrace
}

// DeferredAuditEvent is the durable audit input rebuilt from stored intake.
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
	InferenceTraces     []rules.InferenceTrace
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
