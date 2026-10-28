package hook

import (
	"context"
	"log/slog"

	"goodkind.io/agent-gate/internal/config"
)

// CapabilityDowngrade records a rule action that its provider event cannot apply.
type CapabilityDowngrade struct {
	Rule     string
	System   System
	Event    string
	Declared string
	Effect   string
}

// WarnCapabilityDowngrades logs one warning summary and debug details for unsupported actions.
// It returns every affected rule subscription.
func WarnCapabilityDowngrades(ctx context.Context, log *slog.Logger, cfg *config.Config) []CapabilityDowngrade {
	if cfg == nil || log == nil {
		return nil
	}
	var downgrades []CapabilityDowngrade
	affectedRules := make(map[string]struct{})
	for i := range cfg.Rules {
		rule := &cfg.Rules[i]
		for _, pair := range ruleSubscriptions(rule) {
			effect, capability, unsupported := unsupportedRuleEffect(rule, pair)
			if !unsupported {
				continue
			}
			d := CapabilityDowngrade{
				Rule:     rule.Name,
				System:   pair.system,
				Event:    pair.event,
				Declared: rule.Action,
				Effect:   effect,
			}
			log.DebugContext(
				ctx, "rule subscribes to an unsupported hook response capability",
				slog.String("rule", d.Rule),
				slog.String("provider", d.System.String()),
				slog.String("event", d.Event),
				slog.String("declared", d.Declared),
				slog.String("effect", d.Effect),
				slog.String("capability", capability),
			)
			downgrades = append(downgrades, d)
			affectedRules[d.Rule] = struct{}{}
		}
	}
	if len(downgrades) > 0 {
		log.WarnContext(ctx, "rules have unsupported hook response capabilities",
			"rules", len(affectedRules), "subscriptions", len(downgrades))
	}
	return downgrades
}

func unsupportedRuleEffect(rule *config.Rule, pair ruleSubscription) (string, string, bool) {
	switch rule.Action {
	case config.ActionBlock:
		capability := LookupCapability(pair.system, pair.event)
		if capability == CapabilityBlock || capability == CapabilitySubstitute {
			return "", capability.String(), false
		}
		return "audit", capability.String(), true
	case config.ActionInject:
		capability := LookupResponseCapability(pair.system, pair.event)
		if capability.Supports(ResponseCapabilityInject) {
			return "", "inject", false
		}
		return "noop", "none", true
	case config.ActionMutate:
		capability := LookupResponseCapability(pair.system, pair.event)
		if responseTarget, _ := responseMutationTarget(capability); responseTarget != "" {
			return "", responseTarget, false
		}
		return "noop", "none", true
	default:
		return "", "", false
	}
}

type ruleSubscription struct {
	system System
	event  string
}

// ruleSubscriptions expands a rule's Events / ClaudeEvents / CursorEvents /
// CodexEvents / GeminiEvents into (system, event) pairs. Events without a
// system prefix apply to every supported system that recognizes the event
// name in its canonical form.
func ruleSubscriptions(rule *config.Rule) []ruleSubscription {
	if rule.AllEvents {
		var out []ruleSubscription
		for key := range capabilityTable {
			if rule.ProviderDisabled(key.system.String()) {
				continue
			}
			out = append(out, ruleSubscription(key))
		}
		return out
	}

	var out []ruleSubscription
	if !rule.ProviderDisabled("claude") {
		for _, e := range rule.ClaudeEvents {
			out = append(out, ruleSubscription{SystemClaude, e})
		}
	}
	if !rule.ProviderDisabled("codex") {
		for _, e := range rule.CodexEvents {
			out = append(out, ruleSubscription{SystemCodex, e})
		}
	}
	if !rule.ProviderDisabled("copilot") {
		for _, e := range rule.CopilotEvents {
			out = append(out, ruleSubscription{SystemCopilot, e})
		}
	}
	if !rule.ProviderDisabled("cursor") {
		for _, e := range rule.CursorEvents {
			out = append(out, ruleSubscription{SystemCursor, e})
		}
	}
	if !rule.ProviderDisabled("gemini") {
		for _, e := range rule.GeminiEvents {
			out = append(out, ruleSubscription{SystemGemini, e})
		}
	}
	for _, e := range rule.Events {
		// Events without a provider prefix: the daemon routes the same event
		// name to whichever provider sent the hook payload. To warn
		// accurately, look the event up under every system that has it.
		for _, sys := range []System{SystemClaude, SystemCodex, SystemCopilot, SystemCursor, SystemGemini, SystemResponse} {
			if rule.ProviderDisabled(sys.String()) {
				continue
			}
			if _, ok := capabilityTable[capabilityKey{sys, e}]; ok {
				out = append(out, ruleSubscription{sys, e})
			}
		}
	}
	return out
}
