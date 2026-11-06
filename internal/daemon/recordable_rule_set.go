package daemon

import (
	"fmt"

	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/evaluation"
	"goodkind.io/agent-gate/internal/rules"
)

// ValidateRecordableRuleSet checks the largest metadata record for all rules.
// The daemon allows a call if the evaluation store rejects its record.
func ValidateRecordableRuleSet(configRules []config.Rule) error {
	decisions := make([]rules.RuleDecision, len(configRules))
	for index := range configRules {
		decisions[index] = rules.LargestRuleDecision(configRules[index].Name)
	}
	metadata := marshalDeterministicMetadata(deterministicLayerMetadata{
		SchemaVersion: 1, CheckedRules: decisions,
	})
	if _, err := evaluation.UnmarshalLayerMetadata(metadata); err != nil {
		return fmt.Errorf(
			"%d rules produce %d bytes of rule-engine metadata: %s",
			len(configRules), len(metadata), err.Error(),
		)
	}
	return nil
}
