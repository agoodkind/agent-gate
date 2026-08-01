package rules

import (
	"maps"
	"sync"

	"goodkind.io/agent-gate/internal/config"
	execconcern "goodkind.io/agent-gate/internal/rules/concerns/exec"
)

// execEventMemo guarantees one validator fork per event per condition and
// carries the per-rule message overrides emitted by blocking validators. It is
// created once per EvaluateAll call. Conditions run sequentially within one
// event (single scheduler slot), but the mutex keeps it safe regardless.
type execEventMemo struct {
	system    string
	eventName string

	mu        sync.Mutex
	verdicts  map[*config.Condition]execconcern.Verdict
	overrides map[string]string
	outputs   map[string]string
	errors    map[string]bool
	partial   map[string]bool
}

func newExecEventMemo(system string, eventName string) *execEventMemo {
	return &execEventMemo{
		system:    system,
		eventName: eventName,
		mu:        sync.Mutex{},
		verdicts:  make(map[*config.Condition]execconcern.Verdict),
		overrides: make(map[string]string),
		outputs:   make(map[string]string),
		errors:    make(map[string]bool),
		partial:   make(map[string]bool),
	}
}

func (m *execEventMemo) lookup(c *config.Condition) (execconcern.Verdict, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.verdicts[c]
	return v, ok
}

func (m *execEventMemo) record(c *config.Condition, ruleName string, v execconcern.Verdict) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.verdicts[c] = v
	if v.Block && v.Message != "" {
		m.overrides[ruleName] = v.Message
	}
	if v.Block {
		m.outputs[ruleName] = v.Output
	}
	if v.Errored {
		m.errors[ruleName] = true
	}
	// Tracked apart from errors, which drives the errored no-op response. A
	// partial failure decided nothing and must not change enforcement; it only
	// says the expansion behind this verdict was not fully probed.
	if v.PartialError {
		m.partial[ruleName] = true
	}
}

func (m *execEventMemo) outputFor(ruleName string) (string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	output, ok := m.outputs[ruleName]
	return output, ok
}

func (m *execEventMemo) erroredFor(ruleName string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.errors[ruleName]
}

// partialRuleNames returns the rules whose verdict was decided while some
// expanded target went unclassified, so the audit record can tell a fully
// probed expansion from one that merely reached a decision first. It returns
// nil when every expansion was fully probed, which is the common case.
func (m *execEventMemo) partialRuleNames() map[string]bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.partial) == 0 {
		return nil
	}
	names := make(map[string]bool, len(m.partial))
	maps.Copy(names, m.partial)
	return names
}

func (m *execEventMemo) overrideFor(ruleName string) (string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	msg, ok := m.overrides[ruleName]
	return msg, ok
}
