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
		if isDeferredRule(rule) {
			deferredRules = append(deferredRules, rule)
			continue
		}
		syncRules = append(syncRules, rule)
	}
	return syncRules, deferredRules
}

func isDeferredRule(rule config.Rule) bool {
	if rule.Class == config.RuleClassDeferred {
		return true
	}
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

// DeferredConfig returns a shallow config copy that keeps only deferred rules.
func DeferredConfig(cfg *config.Config) *config.Config {
	_, deferredRules := PartitionRules(cfg)
	return cloneConfigWithRules(cfg, deferredRules)
}
