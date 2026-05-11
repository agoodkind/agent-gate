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
	System   HookSystem
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
		if rule.Action != config.ActionBlock {
			continue
		}
		for _, pair := range ruleSubscriptions(rule) {
			c := LookupCapability(pair.system, pair.event)
			if c == CapabilityBlock || c == CapabilitySubstitute {
				continue
			}
			d := CapabilityDowngrade{
				Rule:     rule.Name,
				System:   pair.system,
				Event:    pair.event,
				Declared: rule.Action,
				Effect:   "audit",
			}
			log.WarnContext(ctx, "rule subscribes to non-blockable event; effective behavior is audit",
				slog.String("rule", d.Rule),
				slog.String("provider", d.System.String()),
				slog.String("event", d.Event),
				slog.String("declared", d.Declared),
				slog.String("effect", d.Effect),
				slog.String("capability", c.String()),
			)
			downgrades = append(downgrades, d)
		}
	}
	return downgrades
}

type ruleSubscription struct {
	system HookSystem
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
		for _, sys := range []HookSystem{SystemClaude, SystemCodex, SystemCursor, SystemGemini} {
			if _, ok := capabilityTable[capabilityKey{sys, e}]; ok {
				out = append(out, ruleSubscription{sys, e})
			}
		}
	}
	return out
}
