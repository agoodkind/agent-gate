package hook

import (
	"context"
	"log/slog"

	"goodkind.io/agent-gate/internal/config"
)

// CapabilityDowngrade describes a (rule, provider, event) tuple where the
// rule's declared Action exceeds what the protocol can deliver on that
// (provider, event) pair. The daemon emits one WARN per downgrade at
// config-load time so the operator sees which rules silently degrade to
// audit.
type CapabilityDowngrade struct {
	Rule     string
	System   System
	Event    string
	Declared string
	Effect   string
}

// WarnCapabilityDowngrades walks each rule with action="block" and logs a
// WARN for any (provider, event) subscription that LookupCapability reports
// as CapabilityObserve. A rule whose subscriptions mix blockable and
// non-blockable events keeps block behavior where the protocol allows it;
// only the non-blockable pairs are logged.
//
// The returned slice is the set of downgrades emitted, mainly for tests.
func WarnCapabilityDowngrades(ctx context.Context, log *slog.Logger, cfg *config.Config) []CapabilityDowngrade {
	if cfg == nil || log == nil {
		return nil
	}
	var downgrades []CapabilityDowngrade
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
			log.WarnContext(
				ctx, "rule subscribes to an unsupported hook response capability",
				slog.String("rule", d.Rule),
				slog.String("provider", d.System.String()),
				slog.String("event", d.Event),
				slog.String("declared", d.Declared),
				slog.String("effect", d.Effect),
				slog.String("capability", capability),
			)
			downgrades = append(downgrades, d)
		}
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
// system prefix apply to every supported system that recognises the event
// name in its canonical form.
func ruleSubscriptions(rule *config.Rule) []ruleSubscription {
	var out []ruleSubscription
	for _, e := range rule.ClaudeEvents {
		out = append(out, ruleSubscription{SystemClaude, e})
	}
	for _, e := range rule.CodexEvents {
		out = append(out, ruleSubscription{SystemCodex, e})
	}
	for _, e := range rule.CopilotEvents {
		out = append(out, ruleSubscription{SystemCopilot, e})
	}
	for _, e := range rule.CursorEvents {
		out = append(out, ruleSubscription{SystemCursor, e})
	}
	for _, e := range rule.GeminiEvents {
		out = append(out, ruleSubscription{SystemGemini, e})
	}
	for _, e := range rule.Events {
		// Events without a provider prefix: the daemon routes the same event
		// name to whichever provider sent the hook payload. To warn
		// accurately, look the event up under every system that has it.
		for _, sys := range []System{SystemClaude, SystemCodex, SystemCopilot, SystemCursor, SystemGemini} {
			if _, ok := capabilityTable[capabilityKey{sys, e}]; ok {
				out = append(out, ruleSubscription{sys, e})
			}
		}
	}
	return out
}
