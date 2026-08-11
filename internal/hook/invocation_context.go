package hook

// SignalStatus states whether an invocation signal was available.
type SignalStatus string

const (
	// SignalStatusObserved means the collector supplied a value.
	SignalStatusObserved SignalStatus = "observed"
	// SignalStatusMissing means the source did not supply a value.
	SignalStatusMissing SignalStatus = "missing"
	// SignalStatusUnreadable means collection failed before a value was available.
	SignalStatusUnreadable SignalStatus = "unreadable"
)

// ObservedValue records a scalar classification signal and its provenance.
type ObservedValue struct {
	Value      string       `json:"value"`
	Source     string       `json:"source"`
	Provenance string       `json:"provenance"`
	Status     SignalStatus `json:"status"`
}

// ProcessEvidence records one observed process without persisting its process ID.
type ProcessEvidence struct {
	Name           string       `json:"name"`
	ExecutablePath string       `json:"executable_path"`
	Source         string       `json:"source"`
	Provenance     string       `json:"provenance"`
	Status         SignalStatus `json:"status"`
}

// EnvironmentEvidence records one classification-relevant environment signal.
type EnvironmentEvidence struct {
	Name       string       `json:"name"`
	Value      string       `json:"value"`
	Category   string       `json:"category"`
	Source     string       `json:"source"`
	Provenance string       `json:"provenance"`
	Status     SignalStatus `json:"status"`
}

// CollectionIssue records unavailable evidence without inventing a value.
type CollectionIssue struct {
	Source string       `json:"source"`
	Status SignalStatus `json:"status"`
	Detail string       `json:"detail"`
}

// InvocationContext contains evidence observed by the hook transport.
type InvocationContext struct {
	HookSubcommand      ObservedValue         `json:"hook_subcommand"`
	HookTags            []ObservedValue       `json:"hook_tags"`
	WorkingDirectory    ObservedValue         `json:"working_directory"`
	Executable          ProcessEvidence       `json:"executable"`
	ParentProcess       ProcessEvidence       `json:"parent_process"`
	Ancestors           []ProcessEvidence     `json:"ancestors"`
	Environment         []EnvironmentEvidence `json:"environment"`
	ManagedRegistration ObservedValue         `json:"managed_registration"`
	CollectionIssues    []CollectionIssue     `json:"collection_issues"`
}
