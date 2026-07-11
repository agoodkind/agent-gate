package hook

import "goodkind.io/agent-gate/internal/config"

// PartitionRules splits rules into the hot-path sync set and the deferred set.
func PartitionRules(cfg *config.Config) ([]config.Rule, []config.Rule) {
	if cfg == nil || len(cfg.Rules) == 0 {
		return nil, nil
	}

	syncRules := make([]config.Rule, 0, len(cfg.Rules))
	deferredRules := make([]config.Rule, 0, len(cfg.Rules))
	for _, rule := range cfg.Rules {
		if isAuditRule(rule) {
			deferredRules = append(deferredRules, rule)
			continue
		}
		syncRules = append(syncRules, rule)
	}
	return syncRules, deferredRules
}

// isAuditRule reports whether rule runs in the audit phase (post-event, no
// blocking) rather than the gate phase. The check reads AuditOnly, which is
// derived from Action during compileRule.
func isAuditRule(rule config.Rule) bool {
	return rule.AuditOnly
}

func cloneConfigWithRules(cfg *config.Config, rules []config.Rule) *config.Config {
	if cfg == nil {
		if len(rules) == 0 {
			return nil
		}
		var cloned config.Config
		cloned.Rules = append([]config.Rule(nil), rules...)
		return &cloned
	}

	cloned := *cfg
	cloned.Rules = append([]config.Rule(nil), rules...)
	return &cloned
}

// SyncConfig returns a shallow config copy that keeps only hot-path rules.
func SyncConfig(cfg *config.Config) *config.Config {
	syncRules, _ := PartitionRules(cfg)
	return cloneConfigWithRules(cfg, syncRules)
}

// ReplaySyncConfig returns hot-path rules that can be reconstructed without
// repeating external inference attempted before the hook response.
func ReplaySyncConfig(cfg *config.Config) *config.Config {
	syncRules, _ := PartitionRules(cfg)
	replayRules := make([]config.Rule, 0, len(syncRules))
	for _, rule := range syncRules {
		if ruleHasInference(rule) {
			continue
		}
		replayRules = append(replayRules, rule)
	}
	return cloneConfigWithRules(cfg, replayRules)
}

func ruleHasInference(rule config.Rule) bool {
	for _, condition := range rule.Conditions {
		if config.ConditionKind(condition.Kind) == config.ConditionKindInfer {
			return true
		}
	}
	return false
}

func partitionInferenceRules(ruleSet []config.Rule) ([]config.Rule, []config.Rule) {
	deterministicRules := make([]config.Rule, 0, len(ruleSet))
	inferenceRules := make([]config.Rule, 0, len(ruleSet))
	for _, rule := range ruleSet {
		if ruleHasInference(rule) {
			inferenceRules = append(inferenceRules, rule)
			continue
		}
		deterministicRules = append(deterministicRules, rule)
	}
	return deterministicRules, inferenceRules
}

// DeferredConfig returns a shallow config copy that keeps only deferred rules.
func DeferredConfig(cfg *config.Config) *config.Config {
	_, deferredRules := PartitionRules(cfg)
	return cloneConfigWithRules(cfg, deferredRules)
}

// ReplayDeferredConfig returns deferred rules that can be reconstructed
// without repeating external inference after process-local hot state is lost.
func ReplayDeferredConfig(cfg *config.Config) *config.Config {
	_, deferredRules := PartitionRules(cfg)
	replayRules := make([]config.Rule, 0, len(deferredRules))
	for _, rule := range deferredRules {
		if ruleHasInference(rule) {
			continue
		}
		replayRules = append(replayRules, rule)
	}
	return cloneConfigWithRules(cfg, replayRules)
}
